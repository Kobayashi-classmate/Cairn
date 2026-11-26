// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/Kobayashi-classmate/Cairn/catalog"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// StartRefresh takes the web lock and, in the background, brings every
// applied site's files to what this version writes (see Refresh), after
// repairing the SELinux port labels of an earlier version (seMigrate). It is
// called once when the helper starts, before it serves requests, so that
// the lock is free and site changes queue behind the refresh.
func (st *Sites) StartRefresh(ctx context.Context, o *Ops, logger *slog.Logger) {
	release, ok := o.TryLock(LockWeb)
	if !ok {
		logger.Warn("site refresh skipped: the web configuration is being changed")
		return
	}
	go func() {
		defer release()
		st.seMigrate(ctx, logger)
		st.Refresh(ctx, logger)
	}()
}

// Refresh rewrites the nginx server block and PHP-FPM pool of every applied
// site from its recorded spec, so that what an upgrade changed in them (the
// isolation between sites above all) reaches existing sites without anyone
// saving them again. Files already up to date are left alone and nothing is
// reloaded for them. The sites are changed together; if a check or reload
// fails, each site is tried on its own, so that one broken site does not
// hold back the others. A site that still fails keeps its previous files
// (put back, as in any change) and is reported in the helper's log; its next
// change, or the next start of the helper, tries again.
func (st *Sites) Refresh(ctx context.Context, logger *slog.Logger) {
	if st.Unsupported != "" {
		return
	}
	if _, err := os.Stat(st.path(nginxBinary)); err != nil {
		return
	}
	st.refreshStreamTop(ctx, logger)
	states, err := st.states()
	if err != nil {
		logger.Warn("site refresh: reading the applied sites failed", "err", err)
		return
	}
	codes := make([]string, 0, len(states))
	for c := range states {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	var sites []siteFiles
	regrouped := false
	for _, code := range codes {
		var out bytes.Buffer
		sf, err := st.refreshFiles(ctx, &out, states[code].Spec)
		regrouped = regrouped || sf.regrouped
		logLines(logger, slog.LevelInfo, "site refresh", code, out.String())
		if err != nil {
			logger.Warn("site refresh: site left as it was", "site", code, "err", err)
			continue
		}
		sites = append(sites, sf)
	}
	var out bytes.Buffer
	var reloaded []string
	// nginx's workers keep the groups they started with: when its groups
	// changed and no file did, it is reloaded all the same, or it would
	// go on reading the sites it was taken out of.
	defer func() {
		if !regrouped || contains(reloaded, "nginx") {
			return
		}
		out.Reset()
		if _, err := st.reload(ctx, &out, st.nginxReload()); err != nil {
			logger.Warn("site refresh: reloading nginx after its groups changed failed", "err", err)
		}
		logLines(logger, slog.LevelInfo, "site refresh", "", out.String())
	}()
	if len(sites) == 0 {
		return
	}
	changes, checks, reloads := mergeSiteFiles(sites...)
	done, err := st.transact(ctx, &out, changes, checks, reloads)
	if err == nil {
		reloaded = done
		logLines(logger, slog.LevelInfo, "site refresh", "", out.String())
		return
	}
	logLines(logger, slog.LevelWarn, "site refresh", "", out.String())
	for _, sf := range sites {
		out.Reset()
		changes, checks, reloads := mergeSiteFiles(sf)
		done, err := st.transact(ctx, &out, changes, checks, reloads)
		reloaded = append(reloaded, done...)
		level := slog.LevelInfo
		if err != nil {
			level = slog.LevelWarn
		}
		logLines(logger, level, "site refresh", sf.code, out.String())
		if err != nil {
			logger.Warn("site refresh: the site keeps its previous configuration", "site", sf.code, "err", err)
		}
	}
}

// siteFiles is one site's part of a refresh.
type siteFiles struct {
	code string
	// regrouped: nginx's membership of the site's group changed.
	regrouped bool
	changes   []fileChange
	checks    []configCheck
	reloads   []reloader
}

// refreshFiles renders a site's files as apply would, after giving nginx
// the access the site's type calls for and aligning the site's links when
// it allows links between its users.
func (st *Sites) refreshFiles(ctx context.Context, log io.Writer, spec helperapi.SiteSpec) (siteFiles, error) {
	sf := siteFiles{code: spec.Code}
	regrouped, err := st.nginxAccess(ctx, log, spec.Code, servesFiles(spec.Type))
	sf.regrouped = regrouped
	if err != nil {
		return sf, err
	}
	if spec.AllowSymlinks && servesFiles(spec.Type) {
		if err := st.alignLinks(ctx, log, spec.Code); err != nil {
			return sf, err
		}
	}
	var fpm *catalog.FPM
	if spec.Type == helperapi.SitePHP {
		f, installed := st.phpFPM(spec.PHP)
		if !installed {
			return sf, siteErr(helperapi.CodeSitePHPMissing, map[string]any{"line": spec.PHP})
		}
		fpm = f
	}
	conf, pool, err := st.render(spec, fpm)
	if err != nil {
		return sf, err
	}
	// The panel-wide file comes along as in apply: a site's block may use
	// what a newer version defines there.
	sf.changes = []fileChange{
		{path: path.Join(nginxConfD, "cairn.conf"), data: []byte(globalNginx)},
		{path: siteConfPath(spec.Code), data: conf},
	}
	if fpm != nil {
		sf.changes = append(sf.changes, fileChange{path: poolPath(fpm, spec.Code), data: pool})
		sf.checks = append(sf.checks, fpmCheck(fpm))
		sf.reloads = append(sf.reloads, fpmReload(fpm))
	}
	sf.checks = append(sf.checks, st.nginxCheck())
	sf.reloads = append(sf.reloads, st.nginxReload())
	return sf, nil
}

// mergeSiteFiles joins sites' changes into one transaction: every service
// checked and reloaded once, the PHP-FPM masters before nginx.
func mergeSiteFiles(sites ...siteFiles) ([]fileChange, []configCheck, []reloader) {
	var changes []fileChange
	var checks []configCheck
	var fpms, nginx []reloader
	seenCheck, seenReload, seenFile := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, sf := range sites {
		for _, c := range sf.changes {
			// The panel-wide file is every site's.
			if !seenFile[c.path] {
				seenFile[c.path] = true
				changes = append(changes, c)
			}
		}
		for _, c := range sf.checks {
			if !seenCheck[c.name] {
				seenCheck[c.name] = true
				checks = append(checks, c)
			}
		}
		for _, r := range sf.reloads {
			if seenReload[r.name] {
				continue
			}
			seenReload[r.name] = true
			if r.name == "nginx" {
				nginx = append(nginx, r)
			} else {
				fpms = append(fpms, r)
			}
		}
	}
	// nginx is checked last too, as apply does.
	sort.SliceStable(checks, func(i, j int) bool { return checks[i].name != "nginx" && checks[j].name == "nginx" })
	return changes, checks, append(fpms, nginx...)
}

// logLines writes a step log to the helper's log, line by line.
func logLines(logger *slog.Logger, level slog.Level, msg, site, text string) {
	for _, l := range strings.Split(strings.TrimSpace(text), "\n") {
		if l == "" {
			continue
		}
		if site != "" {
			logger.Log(context.Background(), level, msg, "site", site, "step", l)
		} else {
			logger.Log(context.Background(), level, msg, "step", l)
		}
	}
}

// refreshStreamTop moves the stream block to where the installed nginx
// reads it, when there are four-layer proxies and it is not there: after
// nginx was replaced by one with another layout (nginx.org's for the
// distribution's, or back), the proxies would otherwise stop, or nginx
// would read them twice and refuse to start.
func (st *Sites) refreshStreamTop(ctx context.Context, logger *slog.Logger) {
	streams, err := st.streamStates()
	if err != nil || len(streams) == 0 {
		return
	}
	var pending []fileChange
	for _, c := range st.streamTopChanges() {
		if c.data != nil {
			if have, err := os.ReadFile(st.path(c.path)); err == nil && bytes.Equal(have, c.data) {
				continue
			}
		}
		pending = append(pending, c)
	}
	if len(pending) == 0 {
		return
	}
	var out bytes.Buffer
	_, err = st.transact(ctx, &out, pending, []configCheck{st.nginxCheck()}, []reloader{st.nginxReload()})
	level := slog.LevelInfo
	if err != nil {
		level = slog.LevelWarn
	}
	logLines(logger, level, "stream refresh", "", out.String())
	if err != nil {
		logger.Warn("stream refresh: the four-layer proxies keep their previous place", "err", err)
	}
}
