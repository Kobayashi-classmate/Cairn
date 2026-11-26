// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Kobayashi-classmate/Cairn/catalog"
	"github.com/Kobayashi-classmate/Cairn/internal/execx"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/pgpkey"
	"github.com/Kobayashi-classmate/Cairn/internal/platform"
	"github.com/Kobayashi-classmate/Cairn/internal/sources"
)

const (
	maxKeySize  = 1 << 20
	keyTimeout  = time.Minute
	vendorPin   = 100 // below the system repositories (500)
	preferPin   = 600 // above them, for the vendor's shared packages
	managedNote = "# Managed by Cairn. Remove the software in the panel instead of editing this file."
)

// Replaceable in tests, which must never run a package manager or touch
// the real system.
var (
	queryCommand = func(ctx context.Context, name string, args ...string) (string, error) {
		res, err := execx.Run(ctx, execx.Cmd{Name: name, Args: args, Timeout: 5 * time.Minute, MaxOutput: 8 << 20,
			Env: []string{"DEBIAN_FRONTEND=noninteractive"}})
		return string(res.Stdout), err
	}
	systemdRunning = func() bool { _, err := os.Stat("/run/systemd/system"); return err == nil }
)

// Software installs and removes catalog software with the system package
// manager. Everything it does is derived from its own catalog and the
// platform; requests only name an entry and a line.
type Software struct {
	Catalog *catalog.Catalog
	Manager string // apt | dnf
	OS      platform.OSRelease
	Chain   []string
	Arch    string
	// Root prefixes the paths of repository files ("" means /; tests).
	Root string
	HTTP *http.Client
	Log  *slog.Logger
	// Runtimes installs the "standalone" lines; nil disables them.
	Runtimes *Runtimes
	// Sources probes download sources (nil: defaults).
	Sources *sources.Prober
	// RootAccess is the server's [security] root_access: "never" keeps the
	// configuration files of installed lines read-only.
	RootAccess string
	// ConfigVersionsDir keeps earlier versions of the configuration files
	// saved from the panel ("" means DefaultConfigVersionsDir).
	ConfigVersionsDir string
	// WebServerChanged is called, with the web lock held, after a web
	// server line was installed (nil: nothing to do).
	WebServerChanged func(ctx context.Context)

	probing sync.Mutex
	// originsMu guards the record of where installed lines came from.
	originsMu sync.Mutex
	// now is the clock the lines' end-of-life dates are compared with (nil
	// means time.Now; tests).
	now func() time.Time
}

// RegisterSoftware adds pkg.status, pkg.probe, pkg.install and pkg.remove.
func RegisterSoftware(r *Registry, o *Ops, s *Software) {
	Register(r, helperapi.ActionPkgStatus, func(ctx context.Context, _ helperapi.Caller, _ helperapi.Empty) (helperapi.SoftwareStatus, error) {
		return s.Status(ctx)
	})
	Register(r, helperapi.ActionPkgProbe, s.probe)
	// Prebuilt runtimes do not use the package manager: their downloads
	// must not hold up package operations.
	locks := func(req helperapi.SoftwareRequest) []string {
		if _, o, err := s.knownOffer(req); err == nil && o.Method == catalog.MethodStandalone {
			if len(o.Packages) > 0 { // system libraries come first
				return []string{LockPkg, LockRuntimes}
			}
			return []string{LockRuntimes}
		}
		return []string{LockPkg}
	}
	// Installing a web server changes what sites run on: sites follow it
	// afterwards (WebServerChanged), and no site change may run meanwhile.
	installLocks := func(req helperapi.SoftwareRequest) []string {
		l := locks(req)
		if sw, ok := s.Catalog.Find(req.Software); ok && sw.ProvidesCap(catalog.CapWebServer) {
			l = append(l, LockWeb)
		}
		return l
	}
	RegisterOp(r, o, helperapi.ActionPkgInstall, OpSpec[helperapi.SoftwareRequest]{Locks: installLocks, Run: s.install})
	RegisterOp(r, o, helperapi.ActionPkgInstallSet, OpSpec[helperapi.InstallSetRequest]{Locks: s.setLocks, Run: s.installSet})
	// Removing the web server or a PHP line changes what sites run on: no
	// site change may run meanwhile (and see a line that is going away).
	removeLocks := func(req helperapi.SoftwareRequest) []string {
		l := locks(req)
		if sw, ok := s.Catalog.Find(req.Software); ok && servesSites(sw) {
			l = append(l, LockWeb)
		}
		return l
	}
	RegisterOp(r, o, helperapi.ActionPkgRemove, OpSpec[helperapi.SoftwareRequest]{Locks: removeLocks, Run: s.remove})
	registerLineActions(r, o, s)
}

func pkgErr(code string, params map[string]any) error {
	return &helperapi.Error{Code: code, Params: params}
}

func (s *Software) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *Software) system() catalog.System {
	return catalog.System{Chain: s.Chain, Arch: s.Arch, Now: s.clock()}
}

// knownOffer resolves a request to a line the catalog describes for this
// system, whether or not it can still be installed: from the origin the
// request names, else from the one the line was installed from, else from
// its default source. Everything that acts on an installed line (removing
// it, its services, files and extensions) goes by it, through
// installedOffer: a line does not stop being here when its upstream
// support ends.
func (s *Software) knownOffer(req helperapi.SoftwareRequest) (*catalog.Software, catalog.Offer, error) {
	notAvailable := pkgErr(helperapi.CodePkgNotAvailable, map[string]any{"software": req.Software, "line": req.Line})
	sw, ok := s.Catalog.Find(req.Software)
	if !ok || (s.Manager != "apt" && s.Manager != "dnf") {
		return nil, catalog.Offer{}, notAvailable
	}
	if req.Origin != "" {
		if o, ok := s.lineOffer(sw, req.Line, req.Origin); ok {
			return sw, o, nil
		}
		if _, ok := s.lineOffer(sw, req.Line, ""); ok {
			return nil, catalog.Offer{}, pkgErr(helperapi.CodePkgOriginUnknown, map[string]any{"software": req.Software, "line": req.Line, "origin": req.Origin})
		}
		return nil, catalog.Offer{}, notAvailable
	}
	if o, ok := s.currentOffer(sw, req.Line); ok {
		return sw, o, nil
	}
	return nil, catalog.Offer{}, notAvailable
}

// installableOffer resolves a request to a line that can be installed here
// now. Only what installs asks it (install, install set, probe): a line
// past its upstream support is refused here and nowhere else.
func (s *Software) installableOffer(req helperapi.SoftwareRequest) (*catalog.Software, catalog.Offer, error) {
	sw, o, err := s.knownOffer(req)
	if err != nil {
		return nil, catalog.Offer{}, err
	}
	if !o.Installable {
		return nil, catalog.Offer{}, pkgErr(helperapi.CodePkgNotAvailable,
			map[string]any{"software": req.Software, "line": req.Line, "reason": o.Reason, "eol": o.EOL})
	}
	return sw, o, nil
}

func (s *Software) handles(method string) bool {
	switch method {
	case catalog.MethodSystem, catalog.MethodVendor:
		return true
	case catalog.MethodStandalone:
		return s.Runtimes != nil
	}
	return false
}

func (s *Software) path(p string) string { return filepath.Join(s.Root, p) }

func (s *Software) expand(tmpl, line string) string {
	major, _, _ := strings.Cut(s.OS.VersionID, ".")
	return strings.NewReplacer("{distro}", s.OS.ID, "{codename}", s.OS.Codename, "{major}", major, "{line}", line).Replace(tmpl)
}

// instance names the files of a repository; repositories whose definition
// depends on the line (PGDG on EL, MariaDB) and those whose packages are
// pinned to a line get one instance per line.
func instance(r catalog.Repo, line string) string {
	usesLine := r.PinLine || r.DNF != nil && strings.Contains(r.DNF.BaseURL, "{line}")
	if r.APT != nil {
		usesLine = usesLine || strings.Contains(r.APT.URI, "{line}") || strings.Contains(r.APT.Suite, "{line}")
		for _, c := range r.APT.Components {
			usesLine = usesLine || strings.Contains(c, "{line}")
		}
	}
	if usesLine {
		return "cairn-" + r.ID + "-" + strings.ReplaceAll(line, ".", "")
	}
	return "cairn-" + r.ID
}

func (s *Software) install(ctx context.Context, _ helperapi.Caller, req helperapi.SoftwareRequest, log io.Writer) (any, error) {
	sw, o, err := s.installableOffer(req)
	if err != nil {
		return nil, err
	}
	logf := func(format string, a ...any) { _, _ = fmt.Fprintf(log, format, a...) }
	if o.Method == catalog.MethodStandalone {
		logf("Installing %s %s (prebuilt build from %s).\n", sw.Name, o.Line, o.Provider)
		if err := s.ensureLibraries(ctx, log, o.Packages); err != nil {
			return nil, err
		}
		v, err := s.Runtimes.Install(ctx, log, sw.ID, o.Provider, o.Line, req.Source, req.Policy)
		if err != nil {
			return nil, err
		}
		s.Log.Info("runtime installed", "software", sw.ID, "line", o.Line, "version", v)
		return runtimeResult(sw, o, v), nil
	}
	src, err := s.lineSource(ctx, log, sw, o, req)
	if err != nil {
		return nil, err
	}
	items := []lineItem{{req: req, sw: sw, o: o}}
	if err := s.checkInstall(ctx, items); err != nil {
		return nil, err
	}
	logf("Installing %s %s (%s%s).\n", sw.Name, o.Line, o.Method, map[bool]string{true: ", repository " + o.Repo}[o.Repo != ""])
	out, _ := s.installPackages(ctx, log, items, nil, src)
	if out[0].err != nil {
		return nil, out[0].err
	}
	s.webServerChanged(ctx, log, items, out)
	return out[0].res, nil
}

// webServerChanged lets the sites follow a web server line that was just
// installed.
func (s *Software) webServerChanged(ctx context.Context, log io.Writer, items []lineItem, out []pkgOutcome) {
	if s.WebServerChanged == nil {
		return
	}
	for i, it := range items {
		if out[i].err == nil && it.sw.ProvidesCap(catalog.CapWebServer) {
			_, _ = fmt.Fprintf(log, "Bringing the sites to %s %s.\n", it.sw.Name, it.o.Line)
			s.WebServerChanged(ctx)
			return
		}
	}
}

// lineSource checks the source a request names for a line from the package
// manager: a candidate of the system's main repositories for a system line
// (nil: the system's own configuration), none for a vendor line, whose
// repository has only its official address.
func (s *Software) lineSource(ctx context.Context, log io.Writer, sw *catalog.Software, o catalog.Offer, req helperapi.SoftwareRequest) (*chosenSource, error) {
	switch {
	case req.Source == "":
		return nil, nil
	case o.Method != catalog.MethodSystem:
		return nil, pkgErr(helperapi.CodePkgSourceUnknown, map[string]any{"source": req.Source, "software": sw.ID})
	}
	return s.chooseSystemSource(ctx, log, req.Source, req.Policy)
}

func runtimeResult(sw *catalog.Software, o catalog.Offer, version string) helperapi.InstallResult {
	return helperapi.InstallResult{Software: sw.ID, Line: o.Line, Method: o.Method,
		Packages: []helperapi.InstalledPackage{{Name: sw.ID, Version: version}}}
}

// lineItem is a request resolved to its catalog entry and offer.
type lineItem struct {
	req helperapi.SoftwareRequest
	sw  *catalog.Software
	o   catalog.Offer
}

// pkgOutcome is how one line of a package transaction ended.
type pkgOutcome struct {
	res helperapi.InstallResult
	err error
}

// installPackages installs lines from the system and vendor repositories,
// and extra packages (the system libraries runtimes need, unpinned), in one
// package manager transaction: the vendor repositories are added first, the
// indexes refreshed once, and every package of a line pinned to the exact
// version chosen for the line. When the combined transaction fails, each
// line is installed on its own so that one broken line does not fail the
// others. The error is that of the extra packages.
//
// src, when set, is the system source chosen for this transaction: every
// package manager command of it then runs with a private copy of the
// source configuration pointing the system repositories at it, removed
// afterwards (installsource.go).
func (s *Software) installPackages(ctx context.Context, log io.Writer, items []lineItem, extra []string, src *chosenSource) ([]pkgOutcome, error) {
	logf := func(format string, a ...any) { _, _ = fmt.Fprintf(log, format, a...) }
	out := make([]pkgOutcome, len(items))
	seen := map[string]bool{}
	var live []int
	for i, it := range items {
		if it.o.Method == catalog.MethodVendor {
			if err := s.ensureRepo(ctx, log, it.o.Repo, it.o, seen); err != nil {
				out[i].err = err
				continue
			}
		}
		live = append(live, i)
	}
	missing := s.missing(ctx, extra)
	if len(live) == 0 && len(missing) == 0 {
		return out, nil
	}
	if len(missing) > 0 {
		logf("Installing the system libraries it needs: %s\n", strings.Join(missing, " "))
	}
	failAll := func(err error) ([]pkgOutcome, error) {
		for _, i := range live {
			out[i].err = err
		}
		return out, err
	}
	var pm *pkgConfig // nil: the system's own configuration
	if src != nil {
		var err error
		if pm, err = s.privateConfig(src); err != nil {
			return failAll(err)
		}
		defer pm.remove()
		logf("Package source: %s (%s), for this installation only.\n", src.cand.Name, src.cand.URL)
	}
	var unreachable []string // hosts whose indexes apt could not fetch
	if s.Manager == "apt" {
		var upd strings.Builder
		if err := runPackageManager(ctx, io.MultiWriter(log, &limitedBuilder{b: &upd, max: 1 << 20}), "apt-get", pm.args("update")...); err != nil {
			return failAll(err)
		}
		unreachable = aptFailedHosts(upd.String())
		// apt goes on without a source it cannot read: the chosen one
		// must not silently be left out.
		if src != nil && slices.Contains(unreachable, hostOf(src.cand.URL)) {
			return failAll(sourceUnusable(src.cand.ID, unusableUpdate, map[string]any{"host": hostOf(src.cand.URL)}))
		}
	} else if pm != nil {
		// The private cache: the moved repositories' metadata, from the
		// chosen source.
		if err := runPackageManager(ctx, log, "dnf", pm.args("makecache", "--assumeyes")...); err != nil {
			return failAll(err)
		}
	}
	versions := make([]map[string]string, len(items))
	pinned := map[string]string{}
	var ready []int
	for _, i := range live {
		o := items[i].o
		if o.Stream != "" {
			if err := s.enableStream(ctx, log, o.Stream, pm); err != nil {
				out[i].err = err
				continue
			}
		}
		v, err := s.candidates(ctx, o, false, pm)
		if err == nil && pm != nil {
			err = s.notOlder(ctx, pm, o, v)
		}
		if err != nil {
			// apt keeps going when a repository does not answer; say so
			// instead of "version unknown".
			if o.Method == catalog.MethodVendor && s.Manager == "apt" {
				if host := hostOf(s.expand(s.Catalog.Repos[o.Repo].APT.URI, o.Line)); slices.Contains(unreachable, host) {
					err = pkgErr(helperapi.CodePkgRepoUnreachable, map[string]any{"repo": o.Repo, "host": host})
				}
			}
			out[i].err = err
			continue
		}
		conflict := ""
		for _, p := range o.Packages {
			if have, ok := pinned[p]; ok && have != v[p] {
				conflict = p
			}
		}
		if conflict != "" {
			out[i].err = pkgErr(helperapi.CodePkgConflict, map[string]any{"package": conflict, "software": items[i].sw.ID, "line": o.Line})
			continue
		}
		for _, p := range o.Packages {
			pinned[p] = v[p]
		}
		versions[i] = v
		ready = append(ready, i)
	}
	args := func(idx []int) []string {
		var a []string
		for _, i := range idx {
			for _, p := range items[i].o.Packages {
				if !slices.Contains(a, p+"="+versions[i][p]) && !slices.Contains(a, p+"-"+versions[i][p]) {
					if s.Manager == "apt" {
						a = append(a, p+"="+versions[i][p])
					} else {
						a = append(a, p+"-"+versions[i][p])
					}
				}
			}
		}
		return a
	}
	all := append(args(ready), missing...)
	if len(ready) > 0 {
		logf("Exact versions: %s\n", strings.Join(args(ready), " "))
	}
	var extraErr error
	if err := s.pkgInstall(ctx, log, pm, all); err != nil {
		if len(ready) > 1 || (len(ready) > 0 && len(missing) > 0) {
			logf("The combined install failed; installing each line on its own.\n")
			for _, i := range ready {
				if err := s.pkgInstall(ctx, log, pm, args([]int{i})); err != nil {
					out[i].err = err
				}
			}
			if len(missing) > 0 {
				extraErr = s.pkgInstall(ctx, log, pm, missing)
			}
		} else {
			for _, i := range ready {
				out[i].err = err
			}
			extraErr = err
		}
	}

	var names []string
	for _, i := range ready {
		names = append(names, items[i].o.Packages...)
	}
	installed := s.installed(ctx, names)
	for _, i := range ready {
		if out[i].err != nil {
			continue
		}
		sw, o := items[i].sw, items[i].o
		res := helperapi.InstallResult{Software: sw.ID, Line: o.Line, Method: o.Method, Repo: o.Repo, Origin: o.Origin, Services: o.Services}
		for _, p := range o.Packages {
			if installed[p] != versions[i][p] {
				out[i].err = pkgErr(helperapi.CodePkgVersionMismatch, map[string]any{"package": p, "want": versions[i][p], "got": installed[p]})
				break
			}
			res.Packages = append(res.Packages, helperapi.InstalledPackage{Name: p, Version: installed[p]})
		}
		if out[i].err != nil {
			continue
		}
		if err := s.setOrigin(sw.ID, o.Line, o.Origin); err != nil {
			// The line is installed; without the record it is found by
			// trying its sources in order.
			s.Log.Warn("software origin not recorded", "software", sw.ID, "line", o.Line, "err", err)
		}
		if len(o.Services) > 0 {
			if !systemdRunning() {
				logf("systemd is not running; services not started: %s\n", strings.Join(o.Services, " "))
			} else if err := runCommand(ctx, log, "systemctl", append([]string{"enable", "--now"}, o.Services...)...); err != nil {
				out[i].err = pkgErr(helperapi.CodePkgServiceFailed, map[string]any{"units": strings.Join(o.Services, " ")})
				continue
			} else {
				res.ServicesStarted = true
			}
		}
		out[i].res = res
		s.Log.Info("software installed", "software", sw.ID, "line", o.Line, "method", o.Method, "repo", o.Repo)
	}
	return out, extraErr
}

// aptFailedHosts returns the hosts of the indexes "apt-get update" could
// not fetch ("W: Failed to fetch https://host/...", "Err:6 https://host ...").
func aptFailedHosts(out string) []string {
	var hosts []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		var rest string
		switch {
		case strings.HasPrefix(line, "W: Failed to fetch "):
			rest = strings.TrimPrefix(line, "W: Failed to fetch ")
		case strings.HasPrefix(line, "E: Failed to fetch "):
			rest = strings.TrimPrefix(line, "E: Failed to fetch ")
		case strings.HasPrefix(line, "Err:"):
			f := strings.Fields(line)
			if len(f) >= 2 {
				rest = f[1]
			}
		default:
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			continue
		}
		if h := hostOf(f[0]); h != "" && !slices.Contains(hosts, h) {
			hosts = append(hosts, h)
		}
	}
	return hosts
}

// limitedBuilder keeps up to max bytes of what is written to it.
type limitedBuilder struct {
	b   *strings.Builder
	max int
}

func (l *limitedBuilder) Write(p []byte) (int, error) {
	if room := l.max - l.b.Len(); room > 0 {
		l.b.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// pkgInstall runs the package manager's install with exact arguments, with
// the transaction's configuration.
func (s *Software) pkgInstall(ctx context.Context, log io.Writer, pm *pkgConfig, args []string) error {
	if len(args) == 0 {
		return nil
	}
	if s.Manager == "apt" {
		return runPackageManager(ctx, log, "apt-get", pm.args(append([]string{"install", "-y", "--no-install-recommends",
			"-o", "Dpkg::Options::=--force-confold"}, args...)...)...)
	}
	return runPackageManager(ctx, log, "dnf", pm.args(append([]string{"install", "-y"}, args...)...)...)
}

// missing returns the packages of pkgs that are not installed.
func (s *Software) missing(ctx context.Context, pkgs []string) []string {
	installed := s.installed(ctx, pkgs)
	var out []string
	for _, p := range pkgs {
		if installed[p] == "" && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// ensureLibraries installs the system packages a prebuilt runtime needs
// (from the system repositories) unless they are present already.
func (s *Software) ensureLibraries(ctx context.Context, log io.Writer, pkgs []string) error {
	_, err := s.installPackages(ctx, log, nil, pkgs, nil)
	return err
}

func (s *Software) remove(ctx context.Context, _ helperapi.Caller, req helperapi.SoftwareRequest, log io.Writer) (any, error) {
	sw, o, err := s.installedOffer(ctx, req.Software, req.Line)
	if err != nil {
		return nil, err
	}
	if o.Method == catalog.MethodStandalone {
		v, err := s.Runtimes.Remove(sw.ID, o.Line)
		if err != nil {
			return nil, err
		}
		_, _ = fmt.Fprintf(log, "Removed %s %s (%s).\n", sw.Name, o.Line, v)
		s.Log.Info("runtime removed", "software", sw.ID, "line", o.Line, "version", v)
		return helperapi.InstallResult{Software: sw.ID, Line: o.Line, Method: o.Method,
			Packages: []helperapi.InstalledPackage{{Name: sw.ID, Version: v}}}, nil
	}
	installed := s.installed(ctx, append(slices.Clone(o.Packages), o.Companions...))
	var present []string
	var res []helperapi.InstalledPackage
	for _, p := range o.Packages {
		if v := installed[p]; v != "" {
			present = append(present, p)
			res = append(res, helperapi.InstalledPackage{Name: p, Version: v})
		}
	}
	if len(present) == 0 || !lineMatches(installed[o.Packages[0]], o.Line) {
		return nil, pkgErr(helperapi.CodePkgNotInstalled, map[string]any{"software": req.Software, "line": req.Line})
	}
	// Companions depend on the line's packages: they go with them.
	for _, p := range o.Companions {
		if installed[p] != "" && !slices.Contains(present, p) {
			present = append(present, p)
		}
	}
	if sw.ProvidesCap(catalog.CapPHPFPM) {
		states, err := siteStatesAt(s.Root)
		if err != nil {
			return nil, err
		}
		var sites []string
		for code, st := range states {
			if st.Spec.Type == helperapi.SitePHP && st.Spec.PHP == o.Line {
				sites = append(sites, code)
			}
		}
		if len(sites) > 0 {
			slices.Sort(sites)
			return nil, pkgErr(helperapi.CodePkgInUse, map[string]any{"software": sw.ID, "line": o.Line, "sites": strings.Join(sites, " ")})
		}
	}
	// The package manager also removes what depends on the packages: only
	// the line's own extensions may go with it.
	if by := s.removalTakes(ctx, present); len(by) > 0 {
		own := map[string]bool{}
		for _, p := range extPackages(sw, o) {
			own[p] = p != ""
		}
		var others []string
		for _, p := range by {
			// rpm names what needs them with version and architecture.
			if !own[p] && (s.Manager == "apt" || !own[nevraName(p)]) {
				others = append(others, p)
			}
		}
		if len(others) > 0 {
			return nil, pkgErr(helperapi.CodePkgRemoveTakes, map[string]any{"packages": strings.Join(present, ", "), "by": strings.Join(others, ", ")})
		}
	}
	_, _ = fmt.Fprintf(log, "Removing %s %s; configuration and data are kept.\n", sw.Name, o.Line)
	if s.Manager == "apt" {
		err = runPackageManager(ctx, log, "apt-get", append([]string{"remove", "-y"}, present...)...)
	} else {
		err = runPackageManager(ctx, log, "dnf", append([]string{"remove", "-y"}, present...)...)
	}
	if err != nil {
		return nil, err
	}
	if err := s.setOrigin(sw.ID, o.Line, ""); err != nil {
		s.Log.Warn("software origin not dropped", "software", sw.ID, "line", o.Line, "err", err)
	}
	if o.Method == catalog.MethodVendor {
		s.dropRepoIfUnused(ctx, log, o)
	}
	s.Log.Info("software removed", "software", sw.ID, "line", o.Line, "origin", o.Origin)
	return helperapi.InstallResult{Software: sw.ID, Line: o.Line, Method: o.Method, Repo: o.Repo, Origin: o.Origin, Packages: res}, nil
}

// Status reports the installed lines of every catalog entry.
func (s *Software) Status(ctx context.Context) (helperapi.SoftwareStatus, error) {
	st, err := s.installedLines(ctx)
	if err != nil {
		return st, err
	}
	st.Services, st.Reloadable = s.serviceStates(ctx, st.Origins)
	return st, nil
}

// serviceStates asks systemd for the state of the services the installed
// lines start (software -> line -> origin), and which of them can reload;
// nil without systemd.
func (s *Software) serviceStates(ctx context.Context, installed map[string]map[string]string) (map[string]string, []string) {
	var units []string
	for i := range s.Catalog.Software {
		sw := &s.Catalog.Software[i]
		for line, origin := range installed[sw.ID] {
			o, ok := s.lineOffer(sw, line, origin)
			if !ok {
				continue
			}
			for _, u := range o.Services {
				if !slices.Contains(units, u) {
					units = append(units, u)
				}
			}
		}
	}
	slices.Sort(units)
	return s.unitStates(ctx, units)
}

// parseUnitStates reads "systemctl show" output for units, which prints one
// block per unit in the order asked, separated by blank lines: the
// ActiveState of each loaded unit, and the units that can reload.
func parseUnitStates(out string, units []string) (map[string]string, []string) {
	states := map[string]string{}
	var reloadable []string
	blocks := strings.Split(strings.TrimSpace(out), "\n\n")
	for _, unit := range units {
		if len(blocks) == 0 {
			break
		}
		props := map[string]string{}
		for _, line := range strings.Split(blocks[0], "\n") {
			if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
				props[k] = v
			}
		}
		blocks = blocks[1:]
		if props["LoadState"] == "loaded" && props["ActiveState"] != "" {
			states[unit] = props["ActiveState"]
			if props["CanReload"] == "yes" {
				reloadable = append(reloadable, unit)
			}
		}
	}
	return states, reloadable
}

// installedLines finds the installed lines of every software and the
// origin each came from: the recorded one when its packages are there,
// else the first of the line's sources, in the default order, whose
// packages are.
func (s *Software) installedLines(ctx context.Context) (helperapi.SoftwareStatus, error) {
	st := helperapi.SoftwareStatus{Installed: map[string]map[string][]helperapi.InstalledPackage{}, Origins: map[string]map[string]string{}}
	found := func(sw, line, origin string, pkgs []helperapi.InstalledPackage) {
		if st.Installed[sw] == nil {
			st.Installed[sw] = map[string][]helperapi.InstalledPackage{}
			st.Origins[sw] = map[string]string{}
		}
		st.Installed[sw][line], st.Origins[sw][line] = pkgs, origin
	}
	if s.Runtimes != nil {
		for i := range s.Catalog.Software {
			sw := &s.Catalog.Software[i]
			providers := map[string]string{}
			var lines []string
			for _, o := range sw.Offers(s.system()) {
				if o.Method == catalog.MethodStandalone {
					lines = append(lines, o.Line)
					providers[o.Line] = o.Origin
				}
			}
			for line, pkgs := range s.Runtimes.status(sw.ID, lines) {
				found(sw.ID, line, providers[line], pkgs)
			}
		}
	}
	if s.Manager != "apt" && s.Manager != "dnf" {
		return st, nil
	}
	type pending struct {
		sw     string
		offers []catalog.Offer // the line from each of its sources, recorded first
	}
	recorded := s.readOrigins()
	var lines []pending
	var names []string
	for i := range s.Catalog.Software {
		sw := &s.Catalog.Software[i]
		for _, def := range sw.Offers(s.system()) {
			var offers []catalog.Offer
			for _, src := range def.Sources {
				if src.Method != catalog.MethodSystem && src.Method != catalog.MethodVendor {
					continue
				}
				o, ok := s.lineOffer(sw, def.Line, src.Origin)
				if !ok {
					continue
				}
				if src.Origin == recorded[sw.ID][def.Line] {
					offers = append([]catalog.Offer{o}, offers...)
				} else {
					offers = append(offers, o)
				}
				for _, p := range o.Packages {
					if !slices.Contains(names, p) {
						names = append(names, p)
					}
				}
			}
			if len(offers) > 0 {
				lines = append(lines, pending{sw.ID, offers})
			}
		}
	}
	installed := s.installed(ctx, names)
	for _, l := range lines {
		for _, o := range l.offers {
			var pkgs []helperapi.InstalledPackage
			for _, name := range o.Packages {
				if v := installed[name]; v != "" {
					pkgs = append(pkgs, helperapi.InstalledPackage{Name: name, Version: v})
				}
			}
			// Several lines can share package names (EL module streams, the
			// lines of one vendor), so the installed version must belong to
			// this line.
			if len(pkgs) == len(o.Packages) && lineMatches(pkgs[0].Version, o.Line) {
				found(l.sw, o.Line, o.Origin, pkgs)
				break
			}
		}
	}
	return st, nil
}

// checkInstall refuses what cannot be installed next to what is installed
// already, or next to the other items of the same install: another line of
// software that has one line at a time, the same line from another origin
// (it has to be removed first; a package manager would replace one build by
// the other without migrating anything), and software that conflicts.
func (s *Software) checkInstall(ctx context.Context, items []lineItem) error {
	st, err := s.installedLines(ctx)
	if err != nil {
		return err
	}
	for i, it := range items {
		sw, o := it.sw, it.o
		params := map[string]any{"software": sw.ID, "line": o.Line}
		if origin, ok := st.Origins[sw.ID][o.Line]; ok && origin != o.Origin {
			params["installed"] = origin
			return pkgErr(helperapi.CodePkgOtherOrigin, params)
		}
		if sw.Single {
			for line := range st.Installed[sw.ID] {
				if line != o.Line {
					params["installed"] = line
					return pkgErr(helperapi.CodePkgOtherLine, params)
				}
			}
		}
		for j, other := range items {
			switch {
			case j == i:
			case other.sw.ID == sw.ID && sw.Single && other.o.Line != o.Line:
				params["installed"] = other.o.Line
				return pkgErr(helperapi.CodePkgOtherLine, params)
			case slices.Contains(sw.Conflicts, other.sw.ID):
				params["other"] = other.sw.ID
				return pkgErr(helperapi.CodePkgConflictsWith, params)
			}
		}
		for _, c := range sw.Conflicts {
			if len(st.Installed[c]) > 0 {
				params["other"] = c
				return pkgErr(helperapi.CodePkgConflictsWith, params)
			}
		}
	}
	return nil
}

// lineMatches reports whether a package version belongs to a line: "8.4"
// matches "8.4.16-1~deb13u1" and "2:8.4.1-1", not "8.40.1".
func lineMatches(version, line string) bool {
	if epoch, rest, ok := strings.Cut(version, ":"); ok && epoch != "" && strings.Trim(epoch, "0123456789") == "" {
		version = rest
	}
	return strings.HasPrefix(version, line+".")
}

// installed returns the installed version of each of names that is fully
// installed. A configured package whose triggers are still pending ("it",
// "iW": another package's installation is running them) counts.
func (s *Software) installed(ctx context.Context, names []string) map[string]string {
	out := map[string]string{}
	if len(names) == 0 {
		return out
	}
	var text string
	if s.Manager == "apt" {
		// dpkg-query exits non-zero when some names are unknown; the known
		// ones are still printed.
		text, _ = queryCommand(ctx, "dpkg-query", append([]string{"-W", "-f", "${Package}\t${Version}\t${db:Status-Abbrev}\n"}, names...)...)
	} else {
		text, _ = queryCommand(ctx, "rpm", append([]string{"-q", "--qf", "%{NAME}\t%{EVR}\tii \n"}, names...)...)
	}
	for _, line := range strings.Split(text, "\n") {
		f := strings.Split(line, "\t")
		if len(f) == 3 && len(f[2]) >= 2 && f[2][0] == 'i' && strings.ContainsRune("itW", rune(f[2][1])) && f[1] != "" {
			out[f[0]] = f[1]
		}
	}
	return out
}

// candidates picks, per package, the newest version of the line that the
// offer's source provides, so the installed version is exactly the one
// chosen here whatever apt's own preferences say. cacheOnly keeps dnf from
// refreshing its metadata (apt never does here); pm is the transaction's
// configuration (nil: the system's own).
func (s *Software) candidates(ctx context.Context, o catalog.Offer, cacheOnly bool, pm *pkgConfig) (map[string]string, error) {
	out := map[string]string{}
	if s.Manager == "apt" {
		text, err := queryCommand(ctx, "apt-cache", pm.args(append([]string{"madison"}, o.Packages...)...)...)
		if err != nil {
			return nil, err
		}
		vendorHosts := s.vendorHosts()
		wantHost := ""
		if o.Method == catalog.MethodVendor {
			wantHost = hostOf(s.expand(s.Catalog.Repos[o.Repo].APT.URI, o.Line))
		}
		// madison lists each package's versions newest first.
		for _, line := range strings.Split(text, "\n") {
			f := strings.Split(line, "|")
			if len(f) < 3 {
				continue
			}
			name, version := strings.TrimSpace(f[0]), strings.TrimSpace(f[1])
			src := strings.Fields(strings.TrimSpace(f[2]))
			if len(src) == 0 || out[name] != "" || !slices.Contains(o.Packages, name) {
				continue
			}
			host := hostOf(src[0])
			fromVendor := slices.Contains(vendorHosts, host)
			fromSource := (o.Method == catalog.MethodVendor && host == wantHost) || (o.Method == catalog.MethodSystem && !fromVendor)
			if fromSource && lineMatches(version, o.Line) {
				out[name] = version
			}
		}
	} else {
		// Every version, not only the newest: one repository can serve
		// several lines (Redis). A system line is looked for in the
		// system's repositories only, where a vendor's newer build of the
		// same name would otherwise hide it.
		args := []string{"repoquery", "--qf", "%{name} %{evr}\n"}
		if cacheOnly {
			args = append(args, "-C")
		}
		if o.Method == catalog.MethodVendor {
			args = append(args, "--repo", instance(s.Catalog.Repos[o.Repo], o.Line))
		} else {
			args = append(args, "--disablerepo", "cairn-*")
		}
		text, err := queryCommand(ctx, "dnf", pm.args(append(args, o.Packages...)...)...)
		if err != nil {
			return nil, err
		}
		f := strings.Fields(text)
		for i := 0; i+1 < len(f); i += 2 {
			name, evr := f[i], f[i+1]
			if slices.Contains(o.Packages, name) && lineMatches(evr, o.Line) && (out[name] == "" || rpmEVRCompare(evr, out[name]) > 0) {
				out[name] = evr
			}
		}
	}
	for _, p := range o.Packages {
		if out[p] == "" {
			return nil, pkgErr(helperapi.CodePkgVersionUnknown, map[string]any{"package": p, "line": o.Line})
		}
	}
	return out, nil
}

func hostOf(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return p.Hostname()
}

func (s *Software) vendorHosts() []string {
	var hosts []string
	for _, r := range s.Catalog.Repos {
		if r.APT != nil {
			hosts = append(hosts, hostOf(s.expand(r.APT.URI, "")))
		}
	}
	return hosts
}

func (s *Software) enableStream(ctx context.Context, log io.Writer, stream string, pm *pkgConfig) error {
	module, want, _ := strings.Cut(stream, ":")
	text, _ := queryCommand(ctx, "dnf", pm.args("module", "list", "--enabled", module)...)
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == module && f[1] != want {
			return pkgErr(helperapi.CodePkgStreamConflict, map[string]any{"module": module, "enabled": f[1], "wanted": want})
		}
	}
	return runPackageManager(ctx, log, "dnf", pm.args("module", "enable", "-y", stream)...)
}

// ensureRepo adds the vendor repository of a line (and the ones it
// requires) unless it is already configured with a verified key.
func (s *Software) ensureRepo(ctx context.Context, log io.Writer, id string, o catalog.Offer, seen map[string]bool) error {
	r, ok := s.Catalog.Repos[id]
	if !ok {
		return pkgErr(helperapi.CodePkgNotAvailable, map[string]any{"repo": id})
	}
	line := o.Line
	name := instance(r, line)
	if seen[name] {
		return nil
	}
	seen[name] = true
	for _, req := range r.Requires {
		if err := s.ensureRepo(ctx, log, req, catalog.Offer{Line: line}, seen); err != nil {
			return err
		}
	}
	// The packages a line's updates keep to, in a repository serving every
	// line (only the line's own packages, as the line installs them).
	var pinned []string
	if r.PinLine && o.Repo == id {
		pinned = o.Packages
	}
	switch {
	case s.Manager == "apt" && r.APT != nil:
		a := r.APT
		keyPath := s.path("/etc/apt/keyrings/" + name + ".gpg")
		if err := s.ensureKey(ctx, log, s.expand(a.Key.URL, line), a.Key.Fingerprints, keyPath, false); err != nil {
			return err
		}
		comps := make([]string, len(a.Components))
		for i, c := range a.Components {
			comps[i] = s.expand(c, line)
		}
		uri := s.expand(a.URI, line)
		sources := fmt.Sprintf("%s\n# %s (%s)\nTypes: deb\nURIs: %s\nSuites: %s\nComponents: %s\nSigned-By: /etc/apt/keyrings/%s.gpg\n",
			managedNote, r.Name, r.Homepage, uri, s.expand(a.Suite, line), strings.Join(comps, " "), name)
		host := hostOf(uri)
		prefs := fmt.Sprintf("%s\n# Packages from %s are used only where the system repositories have none.\nPackage: *\nPin: origin %s\nPin-Priority: %d\n",
			managedNote, r.Name, host, vendorPin)
		if len(a.Prefer) > 0 {
			prefs += fmt.Sprintf("\n# Shared packages its builds need in its newer version.\nPackage: %s\nPin: origin %s\nPin-Priority: %d\n",
				strings.Join(a.Prefer, " "), host, preferPin)
		}
		if len(pinned) > 0 {
			prefs += fmt.Sprintf("\n# Updates keep to the installed line, %s.\nPackage: %s\nPin: version %s\nPin-Priority: %d\n",
				line, strings.Join(pinned, " "), aptLinePattern(line), preferPin)
		}
		if err := writeIfChanged(log, s.path("/etc/apt/sources.list.d/"+name+".sources"), sources); err != nil {
			return err
		}
		return writeIfChanged(log, s.path("/etc/apt/preferences.d/"+name), prefs)
	case s.Manager == "dnf" && r.DNF != nil && r.DNF.ReleasePackage != "":
		if s.installed(ctx, []string{r.DNF.ReleasePackage})[r.DNF.ReleasePackage] != "" {
			return nil
		}
		return runPackageManager(ctx, log, "dnf", "install", "-y", r.DNF.ReleasePackage)
	case s.Manager == "dnf" && r.DNF != nil:
		d := r.DNF
		keyPath := s.path("/etc/pki/rpm-gpg/RPM-GPG-KEY-" + name)
		if err := s.ensureKey(ctx, log, s.expand(d.Key.URL, line), d.Key.Fingerprints, keyPath, true); err != nil {
			return err
		}
		repo := fmt.Sprintf("%s\n[%s]\nname=%s (Cairn)\nbaseurl=%s\nenabled=1\ngpgcheck=1\ngpgkey=file:///etc/pki/rpm-gpg/RPM-GPG-KEY-%s\n",
			managedNote, name, r.Name, s.expand(d.BaseURL, line), name)
		if d.ModuleHotfixes {
			repo += "module_hotfixes=1\n"
		}
		if len(pinned) > 0 {
			globs := make([]string, len(pinned))
			for i, p := range pinned {
				globs[i] = p + "-" + line + ".*"
			}
			repo += "# Updates keep to the installed line.\nincludepkgs=" + strings.Join(globs, " ") + "\n"
		}
		if err := writeIfChanged(log, s.path("/etc/yum.repos.d/"+name+".repo"), repo); err != nil {
			return err
		}
		return runPackageManager(ctx, log, "dnf", "makecache", "--assumeyes", "--repo", name)
	}
	return pkgErr(helperapi.CodePkgNotAvailable, map[string]any{"repo": id})
}

// aptLinePattern is an apt preferences version pattern for the versions
// of a line, with or without an epoch: "8.10" -> /^([0-9]+:)?8\.10\./.
func aptLinePattern(line string) string {
	return "/^([0-9]+:)?" + strings.ReplaceAll(line, ".", `\.`) + `\./`
}

// dropRepoIfUnused removes the files of the repository instance a removed
// line came from once no installed line comes from it: a configured
// repository takes part in every system update, and an EL repository whose
// packages obsolete the system's (MariaDB, MySQL) would replace them. The
// repositories it requires (EPEL) stay.
func (s *Software) dropRepoIfUnused(ctx context.Context, log io.Writer, o catalog.Offer) {
	r, ok := s.Catalog.Repos[o.Repo]
	if !ok {
		return
	}
	name := instance(r, o.Line)
	st, err := s.installedLines(ctx)
	if err != nil {
		return
	}
	for _, lines := range st.Origins {
		for line, origin := range lines {
			if origin == r.ID && instance(r, line) == name {
				return
			}
		}
	}
	var files []string
	switch {
	case s.Manager == "apt" && r.APT != nil:
		files = []string{"/etc/apt/sources.list.d/" + name + ".sources", "/etc/apt/preferences.d/" + name, "/etc/apt/keyrings/" + name + ".gpg"}
	case s.Manager == "dnf" && r.DNF != nil && r.DNF.BaseURL != "":
		files = []string{"/etc/yum.repos.d/" + name + ".repo", "/etc/pki/rpm-gpg/RPM-GPG-KEY-" + name}
	}
	for _, f := range files {
		if err := os.Remove(s.path(f)); err == nil {
			_, _ = fmt.Fprintf(log, "Removed %s: no installed line comes from %s any more.\n", f, r.Name)
		} else if !errors.Is(err, fs.ErrNotExist) {
			s.Log.Warn("repository file not removed", "file", f, "err", err)
		}
	}
}

// ensureKey keeps a verified copy of a repository key at path, downloading
// it when the file is missing or no longer matches the pinned fingerprints.
func (s *Software) ensureKey(ctx context.Context, log io.Writer, keyURL string, allowed []string, path string, armored bool) error {
	if data, err := os.ReadFile(path); err == nil && keyAllowed(data, allowed) == nil { //nolint:gosec // G304: path built from the catalog.
		return nil
	}
	_, _ = fmt.Fprintf(log, "Downloading the signing key %s\n", keyURL)
	data, err := s.download(ctx, keyURL)
	if err != nil {
		return pkgErr(helperapi.CodePkgKeyDownload, map[string]any{"url": keyURL, "reason": err.Error()})
	}
	if err := keyAllowed(data, allowed); err != nil {
		return err
	}
	packets, err := pgpkey.Binary(data)
	if err != nil {
		return pkgErr(helperapi.CodePkgKeyMismatch, map[string]any{"url": keyURL, "reason": err.Error()})
	}
	if armored {
		packets = pgpkey.Armor(packets)
	}
	fprs, _ := pgpkey.Fingerprints(data)
	_, _ = fmt.Fprintf(log, "Key verified: %s\n", strings.Join(fprs, " "))
	return writeFile(path, packets)
}

func keyAllowed(data []byte, allowed []string) error {
	fprs, err := pgpkey.Fingerprints(data)
	if err != nil {
		return pkgErr(helperapi.CodePkgKeyMismatch, map[string]any{"reason": err.Error()})
	}
	for _, f := range fprs {
		if !slices.Contains(allowed, f) {
			return pkgErr(helperapi.CodePkgKeyMismatch, map[string]any{"got": f})
		}
	}
	return nil
}

func (s *Software) download(ctx context.Context, u string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, keyTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Cairn")
	hc := s.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: keyTimeout}
	}
	res, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxKeySize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxKeySize {
		return nil, fmt.Errorf("key larger than %d bytes", maxKeySize)
	}
	return data, nil
}

func writeIfChanged(log io.Writer, path, content string) error {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, []byte(content)) { //nolint:gosec // G304: path built from the catalog.
		return nil
	}
	_, _ = fmt.Fprintf(log, "Writing %s\n", path)
	return writeFile(path, []byte(content))
}

// writeFile writes a world-readable system file atomically.
func writeFile(path string, data []byte) error {
	return writeFileAs(path, data, 0o644) // apt and dnf read these as any user
}

// writeFileAs writes a file atomically with its final mode. The temporary
// file gets that mode through its descriptor before any content is in it,
// so a private file (a key) is never readable by others, not even between
// the write and the rename. The content is on the disk before the rename,
// and the rename before it returns (see durable.go): after a crash the
// file is the old one or the new one, never an empty one.
func writeFileAs(path string, data []byte, mode os.FileMode) error {
	return writeFileAtomic(path, data, mode, true)
}

// writeFileAtomic is writeFileAs; without durable nothing is flushed, for a
// scratch copy that is thrown away after use.
func writeFileAtomic(path string, data []byte, mode os.FileMode, durable bool) error {
	tmp, err := stageFile(path, data, mode, durable)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil { //nolint:gosec // G703: tmp is CreateTemp's, beside path, which callers build from fixed or validated system paths.
		_ = os.Remove(tmp) //nolint:gosec // G703: as above.
		return err
	}
	if durable {
		syncDir(filepath.Dir(path))
	}
	return nil
}
