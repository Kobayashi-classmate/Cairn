// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"bytes"
	"context"
	"fmt"
	"io"
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

	probing sync.Mutex
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
		if _, o, err := s.offer(req); err == nil && o.Method == catalog.MethodStandalone {
			if len(o.Packages) > 0 { // system libraries come first
				return []string{LockPkg, LockRuntimes}
			}
			return []string{LockRuntimes}
		}
		return []string{LockPkg}
	}
	RegisterOp(r, o, helperapi.ActionPkgInstall, OpSpec[helperapi.SoftwareRequest]{Locks: locks, Run: s.install})
	RegisterOp(r, o, helperapi.ActionPkgInstallSet, OpSpec[helperapi.InstallSetRequest]{Locks: s.setLocks, Run: s.installSet})
	RegisterOp(r, o, helperapi.ActionPkgRemove, OpSpec[helperapi.SoftwareRequest]{Locks: locks, Run: s.remove})
}

func pkgErr(code string, params map[string]any) error {
	return &helperapi.Error{Code: code, Params: params}
}

func (s *Software) system() catalog.System {
	return catalog.System{Chain: s.Chain, Arch: s.Arch, Now: time.Now()}
}

// offer resolves a request to an installable line of this system.
func (s *Software) offer(req helperapi.SoftwareRequest) (*catalog.Software, catalog.Offer, error) {
	notAvailable := pkgErr(helperapi.CodePkgNotAvailable, map[string]any{"software": req.Software, "line": req.Line})
	sw, ok := s.Catalog.Find(req.Software)
	if !ok || (s.Manager != "apt" && s.Manager != "dnf") {
		return nil, catalog.Offer{}, notAvailable
	}
	for _, o := range sw.Offers(s.system()) {
		if o.Line == req.Line && s.handles(o.Method) {
			return sw, o, nil
		}
	}
	return nil, catalog.Offer{}, notAvailable
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
// depends on the line (PGDG on EL) get one instance per line.
func instance(r catalog.Repo, line string) string {
	usesLine := r.DNF != nil && strings.Contains(r.DNF.BaseURL, "{line}")
	if r.APT != nil {
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
	sw, o, err := s.offer(req)
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
	if req.Source != "" && req.Source != sources.IDOfficial {
		// Repositories are used as configured for now; only runtime
		// downloads have a choice of source.
		return nil, pkgErr(helperapi.CodePkgSourceUnknown, map[string]any{"source": req.Source})
	}
	logf("Installing %s %s (%s%s).\n", sw.Name, o.Line, o.Method, map[bool]string{true: ", repository " + o.Repo}[o.Repo != ""])
	out, _ := s.installPackages(ctx, log, []lineItem{{req: req, sw: sw, o: o}}, nil)
	if out[0].err != nil {
		return nil, out[0].err
	}
	return out[0].res, nil
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
func (s *Software) installPackages(ctx context.Context, log io.Writer, items []lineItem, extra []string) ([]pkgOutcome, error) {
	logf := func(format string, a ...any) { _, _ = fmt.Fprintf(log, format, a...) }
	out := make([]pkgOutcome, len(items))
	seen := map[string]bool{}
	var live []int
	for i, it := range items {
		if it.o.Method == catalog.MethodVendor {
			if err := s.ensureRepo(ctx, log, it.o.Repo, it.o.Line, seen); err != nil {
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
	var unreachable []string // hosts whose indexes apt could not fetch
	if s.Manager == "apt" {
		var upd strings.Builder
		if err := runCommand(ctx, io.MultiWriter(log, &limitedBuilder{b: &upd, max: 1 << 20}), "apt-get", "update"); err != nil {
			for _, i := range live {
				out[i].err = err
			}
			return out, err
		}
		unreachable = aptFailedHosts(upd.String())
	}
	versions := make([]map[string]string, len(items))
	pinned := map[string]string{}
	var ready []int
	for _, i := range live {
		o := items[i].o
		if o.Stream != "" {
			if err := s.enableStream(ctx, log, o.Stream); err != nil {
				out[i].err = err
				continue
			}
		}
		v, err := s.candidates(ctx, o)
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
	if err := s.pkgInstall(ctx, log, all); err != nil {
		if len(ready) > 1 || (len(ready) > 0 && len(missing) > 0) {
			logf("The combined install failed; installing each line on its own.\n")
			for _, i := range ready {
				if err := s.pkgInstall(ctx, log, args([]int{i})); err != nil {
					out[i].err = err
				}
			}
			if len(missing) > 0 {
				extraErr = s.pkgInstall(ctx, log, missing)
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
		res := helperapi.InstallResult{Software: sw.ID, Line: o.Line, Method: o.Method, Repo: o.Repo, Services: o.Services}
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

// pkgInstall runs the package manager's install with exact arguments.
func (s *Software) pkgInstall(ctx context.Context, log io.Writer, args []string) error {
	if len(args) == 0 {
		return nil
	}
	if s.Manager == "apt" {
		return runCommand(ctx, log, "apt-get", append([]string{"install", "-y", "--no-install-recommends",
			"-o", "Dpkg::Options::=--force-confold"}, args...)...)
	}
	return runCommand(ctx, log, "dnf", append([]string{"install", "-y"}, args...)...)
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
	_, err := s.installPackages(ctx, log, nil, pkgs)
	return err
}

func (s *Software) remove(ctx context.Context, _ helperapi.Caller, req helperapi.SoftwareRequest, log io.Writer) (any, error) {
	sw, o, err := s.offer(req)
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
	installed := s.installed(ctx, o.Packages)
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
	_, _ = fmt.Fprintf(log, "Removing %s %s; configuration and data are kept.\n", sw.Name, o.Line)
	if s.Manager == "apt" {
		err = runCommand(ctx, log, "apt-get", append([]string{"remove", "-y"}, present...)...)
	} else {
		err = runCommand(ctx, log, "dnf", append([]string{"remove", "-y"}, present...)...)
	}
	if err != nil {
		return nil, err
	}
	s.Log.Info("software removed", "software", sw.ID, "line", o.Line)
	return helperapi.InstallResult{Software: sw.ID, Line: o.Line, Method: o.Method, Repo: o.Repo, Packages: res}, nil
}

// Status reports the installed lines of every catalog entry.
func (s *Software) Status(ctx context.Context) (helperapi.SoftwareStatus, error) {
	st, err := s.installedLines(ctx)
	if err != nil {
		return st, err
	}
	st.Services = s.serviceStates(ctx, st.Installed)
	return st, nil
}

// serviceStates asks systemd for the state of the services the installed
// lines start; nil without systemd.
func (s *Software) serviceStates(ctx context.Context, installed map[string]map[string][]helperapi.InstalledPackage) map[string]string {
	if !systemdRunning() {
		return nil
	}
	var units []string
	for i := range s.Catalog.Software {
		sw := &s.Catalog.Software[i]
		lines := installed[sw.ID]
		if len(lines) == 0 {
			continue
		}
		for _, o := range sw.Offers(s.system()) {
			if _, ok := lines[o.Line]; !ok {
				continue
			}
			for _, u := range o.Services {
				if !slices.Contains(units, u) {
					units = append(units, u)
				}
			}
		}
	}
	if len(units) == 0 {
		return nil
	}
	out, err := queryCommand(ctx, "systemctl", append([]string{"show", "--property=LoadState,ActiveState", "--"}, units...)...)
	if err != nil {
		s.Log.Warn("service states unavailable", "err", err)
		return nil
	}
	return parseUnitStates(out, units)
}

// parseUnitStates reads "systemctl show" output for units, which prints one
// block per unit in the order asked, blank lines between them. Units systemd
// does not know are left out.
func parseUnitStates(out string, units []string) map[string]string {
	states := map[string]string{}
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
		}
	}
	return states
}

// installedLines finds the installed lines of every software.
func (s *Software) installedLines(ctx context.Context) (helperapi.SoftwareStatus, error) {
	st := helperapi.SoftwareStatus{Installed: map[string]map[string][]helperapi.InstalledPackage{}}
	if s.Runtimes != nil {
		for i := range s.Catalog.Software {
			sw := &s.Catalog.Software[i]
			var lines []string
			for _, o := range sw.Offers(s.system()) {
				if o.Method == catalog.MethodStandalone {
					lines = append(lines, o.Line)
				}
			}
			if found := s.Runtimes.status(sw.ID, lines); len(found) > 0 {
				st.Installed[sw.ID] = found
			}
		}
	}
	if s.Manager != "apt" && s.Manager != "dnf" {
		return st, nil
	}
	type pending struct {
		sw string
		o  catalog.Offer
	}
	var offers []pending
	var names []string
	for i := range s.Catalog.Software {
		sw := &s.Catalog.Software[i]
		for _, o := range sw.Offers(s.system()) {
			if o.Method == catalog.MethodSystem || o.Method == catalog.MethodVendor {
				offers = append(offers, pending{sw.ID, o})
				for _, p := range o.Packages {
					if !slices.Contains(names, p) {
						names = append(names, p)
					}
				}
			}
		}
	}
	installed := s.installed(ctx, names)
	for _, p := range offers {
		var pkgs []helperapi.InstalledPackage
		for _, name := range p.o.Packages {
			if v := installed[name]; v != "" {
				pkgs = append(pkgs, helperapi.InstalledPackage{Name: name, Version: v})
			}
		}
		// Several lines can share package names (EL module streams), so the
		// installed version must belong to this line.
		if len(pkgs) == len(p.o.Packages) && lineMatches(pkgs[0].Version, p.o.Line) {
			if st.Installed[p.sw] == nil {
				st.Installed[p.sw] = map[string][]helperapi.InstalledPackage{}
			}
			st.Installed[p.sw][p.o.Line] = pkgs
		}
	}
	return st, nil
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
// installed.
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
		if len(f) == 3 && strings.HasPrefix(f[2], "ii") && f[1] != "" {
			out[f[0]] = f[1]
		}
	}
	return out
}

// candidates picks, per package, the newest version of the line that the
// offer's source provides, so the installed version is exactly the one
// chosen here whatever apt's own preferences say.
func (s *Software) candidates(ctx context.Context, o catalog.Offer) (map[string]string, error) {
	out := map[string]string{}
	if s.Manager == "apt" {
		text, err := queryCommand(ctx, "apt-cache", append([]string{"madison"}, o.Packages...)...)
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
		args := []string{"repoquery", "--latest-limit", "1", "--qf", "%{name} %{evr}\n"}
		if o.Method == catalog.MethodVendor {
			args = append(args, "--repo", instance(s.Catalog.Repos[o.Repo], o.Line))
		}
		text, err := queryCommand(ctx, "dnf", append(args, o.Packages...)...)
		if err != nil {
			return nil, err
		}
		f := strings.Fields(text)
		for i := 0; i+1 < len(f); i += 2 {
			if slices.Contains(o.Packages, f[i]) && out[f[i]] == "" && lineMatches(f[i+1], o.Line) {
				out[f[i]] = f[i+1]
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

func (s *Software) enableStream(ctx context.Context, log io.Writer, stream string) error {
	module, want, _ := strings.Cut(stream, ":")
	text, _ := queryCommand(ctx, "dnf", "module", "list", "--enabled", module)
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == module && f[1] != want {
			return pkgErr(helperapi.CodePkgStreamConflict, map[string]any{"module": module, "enabled": f[1], "wanted": want})
		}
	}
	return runCommand(ctx, log, "dnf", "module", "enable", "-y", stream)
}

// ensureRepo adds a vendor repository (and the ones it requires) unless it
// is already configured with a verified key.
func (s *Software) ensureRepo(ctx context.Context, log io.Writer, id, line string, seen map[string]bool) error {
	if seen[id] {
		return nil
	}
	seen[id] = true
	r, ok := s.Catalog.Repos[id]
	if !ok {
		return pkgErr(helperapi.CodePkgNotAvailable, map[string]any{"repo": id})
	}
	for _, req := range r.Requires {
		if err := s.ensureRepo(ctx, log, req, line, seen); err != nil {
			return err
		}
	}
	name := instance(r, line)
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
		if err := writeIfChanged(log, s.path("/etc/apt/sources.list.d/"+name+".sources"), sources); err != nil {
			return err
		}
		return writeIfChanged(log, s.path("/etc/apt/preferences.d/"+name), prefs)
	case s.Manager == "dnf" && r.DNF != nil && r.DNF.ReleasePackage != "":
		if s.installed(ctx, []string{r.DNF.ReleasePackage})[r.DNF.ReleasePackage] != "" {
			return nil
		}
		return runCommand(ctx, log, "dnf", "install", "-y", r.DNF.ReleasePackage)
	case s.Manager == "dnf" && r.DNF != nil:
		d := r.DNF
		keyPath := s.path("/etc/pki/rpm-gpg/RPM-GPG-KEY-" + name)
		if err := s.ensureKey(ctx, log, s.expand(d.Key.URL, line), d.Key.Fingerprints, keyPath, true); err != nil {
			return err
		}
		repo := fmt.Sprintf("%s\n[%s]\nname=%s (Cairn)\nbaseurl=%s\nenabled=1\ngpgcheck=1\ngpgkey=file:///etc/pki/rpm-gpg/RPM-GPG-KEY-%s\n",
			managedNote, name, r.Name, s.expand(d.BaseURL, line), name)
		if err := writeIfChanged(log, s.path("/etc/yum.repos.d/"+name+".repo"), repo); err != nil {
			return err
		}
		return runCommand(ctx, log, "dnf", "makecache", "--assumeyes", "--repo", name)
	}
	return pkgErr(helperapi.CodePkgNotAvailable, map[string]any{"repo": id})
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
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // G301: system configuration directories.
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".cairn-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no-op after the rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil { //nolint:gosec // G302: apt and dnf read these as any user.
		return err
	}
	return os.Rename(tmp.Name(), path)
}
