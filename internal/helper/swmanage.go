// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/Kobayashi-classmate/Cairn/catalog"
	"github.com/Kobayashi-classmate/Cairn/internal/execx"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// DefaultConfigVersionsDir keeps the earlier versions of configuration
// files saved from the panel.
const DefaultConfigVersionsDir = "/var/lib/cairn-helper/configs"

const (
	configCheckLimit = 2 * time.Minute
	serviceWait      = 20 * time.Second
	selinuxXattr     = "security.selinux"
)

// Replaceable in tests.
var (
	// checkCommand runs a configuration check and returns its combined
	// output.
	checkCommand = func(ctx context.Context, argv []string) (string, error) {
		res, err := execx.Run(ctx, execx.Cmd{Name: argv[0], Args: argv[1:], Timeout: configCheckLimit, MaxOutput: 256 << 10})
		return string(res.Stdout) + string(res.Stderr), err
	}
	// pause waits between service state checks.
	pause = func(d time.Duration) { time.Sleep(d) }
)

// checkFailed matches the complaints of a check that still exits 0: PHP
// reports php.ini syntax errors and carries on.
var checkFailed = regexp.MustCompile(`(?i)syntax error|parse error`)

// registerLineActions adds the actions on installed lines.
func registerLineActions(r *Registry, o *Ops, s *Software) {
	Register(r, helperapi.ActionPkgExtStatus, s.extStatus)
	Register(r, helperapi.ActionPkgConfigRead, s.configRead)
	RegisterOp(r, o, helperapi.ActionPkgService, OpSpec[helperapi.ServiceRequest]{
		Locks: func(q helperapi.ServiceRequest) []string { return lineLocks(q.Software, q.Line) }, Run: s.service})
	RegisterOp(r, o, helperapi.ActionPkgExtApply, OpSpec[helperapi.ExtApplyRequest]{
		Locks: func(helperapi.ExtApplyRequest) []string { return []string{LockPkg, LockWeb} }, Run: s.extApply})
	RegisterOp(r, o, helperapi.ActionPkgConfigWrite, OpSpec[helperapi.ConfigWriteRequest]{
		Locks: func(q helperapi.ConfigWriteRequest) []string { return lineLocks(q.Software, q.Line) }, Run: s.configWrite})
}

// lineLocks are the locks of the other operations that touch the same
// services: sites reload nginx and PHP-FPM, databases restart their
// servers.
func lineLocks(software, line string) []string {
	switch software {
	case "nginx", "php":
		return []string{LockWeb}
	case "mariadb", "mysql":
		return []string{"db-" + helperapi.DBMySQL}
	case "postgresql":
		return []string{"db-" + helperapi.DBPostgreSQL + "-" + line}
	case "redis", "valkey":
		return []string{"db-" + helperapi.DBRedis}
	}
	return []string{"line-" + software + "-" + line}
}

// installedOffer resolves a request to a line that is installed here.
func (s *Software) installedOffer(ctx context.Context, software, line string) (*catalog.Software, catalog.Offer, error) {
	sw, o, err := s.knownOffer(helperapi.SoftwareRequest{Software: software, Line: line})
	if err != nil {
		return nil, o, err
	}
	notInstalled := pkgErr(helperapi.CodePkgNotInstalled, map[string]any{"software": software, "line": line})
	if o.Method == catalog.MethodStandalone {
		if s.Runtimes == nil || s.Runtimes.Installed(sw.ID, o.Line) == "" {
			return nil, o, notInstalled
		}
		return sw, o, nil
	}
	if len(o.Packages) == 0 {
		return nil, o, notInstalled
	}
	if v := s.installed(ctx, o.Packages[:1])[o.Packages[0]]; v == "" || !lineMatches(v, o.Line) {
		return nil, o, notInstalled
	}
	return sw, o, nil
}

func unitActive(ctx context.Context, unit string) string {
	out, _ := queryCommand(ctx, "systemctl", "is-active", unit)
	return strings.TrimSpace(out)
}

// waitActive waits until unit is active; it fails when the unit stops
// trying (failed or inactive) or does not settle in time.
func waitActive(ctx context.Context, unit string) bool {
	deadline := time.Now().Add(serviceWait)
	for {
		switch unitActive(ctx, unit) {
		case "active":
			return true
		case "failed", "inactive":
			return false
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return false
		}
		pause(time.Second)
	}
}

// journalTail is the end of what a unit logged since a moment, for an
// error message.
func journalTail(ctx context.Context, unit string, since time.Time) string {
	out, _ := queryCommand(ctx, "journalctl", "--no-pager", "--quiet", "--output=cat", "-n", "15",
		"--since=@"+strconv.FormatInt(since.Unix()-1, 10), "-u", unit)
	return tailString(strings.TrimSpace(out), 1200)
}

// Services ------------------------------------------------------------------

func (s *Software) service(ctx context.Context, _ helperapi.Caller, req helperapi.ServiceRequest, log io.Writer) (any, error) {
	sw, o, err := s.installedOffer(ctx, req.Software, req.Line)
	if err != nil {
		return nil, err
	}
	if len(o.Services) == 0 {
		return nil, pkgErr(helperapi.CodePkgServiceNone, map[string]any{"software": req.Software, "line": req.Line})
	}
	if !systemdRunning() {
		return nil, pkgErr(helperapi.CodePkgServiceNone, map[string]any{"software": req.Software, "line": req.Line, "reason": "no_systemd"})
	}
	if req.Action == helperapi.ServiceReload {
		_, reloadable := s.unitStates(ctx, o.Services)
		for _, u := range o.Services {
			if !slices.Contains(reloadable, u) {
				return nil, pkgErr(helperapi.CodePkgServiceNoReload, map[string]any{"service": u})
			}
		}
	}
	for _, u := range o.Services {
		_, _ = fmt.Fprintf(log, "%s %s (%s %s)\n", serviceVerb(req.Action), u, sw.Name, o.Line)
		started := time.Now()
		if err := runCommand(ctx, log, "systemctl", req.Action, u); err != nil {
			return nil, pkgErr(helperapi.CodePkgServiceFailed, map[string]any{"service": u, "output": journalTail(ctx, u, started)})
		}
		if req.Action == helperapi.ServiceStart || req.Action == helperapi.ServiceRestart {
			if !waitActive(ctx, u) {
				return nil, pkgErr(helperapi.CodePkgServiceFailed, map[string]any{"service": u, "output": journalTail(ctx, u, started)})
			}
		}
	}
	s.Log.Info("service action", "software", sw.ID, "line", o.Line, "action", req.Action)
	return nil, nil
}

func serviceVerb(action string) string {
	switch action {
	case helperapi.ServiceStart:
		return "Starting"
	case helperapi.ServiceStop:
		return "Stopping"
	case helperapi.ServiceRestart:
		return "Restarting"
	}
	return "Reloading"
}

// unitStates asks systemd for the ActiveState of units and which of them
// can reload; units systemd does not know are left out.
func (s *Software) unitStates(ctx context.Context, units []string) (map[string]string, []string) {
	if len(units) == 0 || !systemdRunning() {
		return nil, nil
	}
	out, err := queryCommand(ctx, "systemctl", append([]string{"show", "--property=LoadState,ActiveState,CanReload", "--"}, units...)...)
	if err != nil {
		s.Log.Warn("service states unavailable", "err", err)
		return nil, nil
	}
	return parseUnitStates(out, units)
}

// PHP extensions -------------------------------------------------------------

// extPackages resolves the catalog extensions of a line to its packages
// ("" where the line's source does not build one).
func extPackages(sw *catalog.Software, o catalog.Offer) map[string]string {
	out := map[string]string{}
	for _, e := range sw.Extensions {
		out[e.ID] = e.ExtensionPackage(o)
	}
	return out
}

// phpModules lists the modules the line's PHP loads, lowercase.
func phpModules(ctx context.Context, root, cli string) ([]string, error) {
	out, err := queryCommand(ctx, filepath.Join(root, cli), "-m")
	if err != nil {
		return nil, err
	}
	var mods []string
	for _, l := range strings.Split(out, "\n") {
		l = strings.ToLower(strings.TrimSpace(l))
		if l == "" || strings.HasPrefix(l, "[") || slices.Contains(mods, l) {
			continue
		}
		mods = append(mods, l)
	}
	sort.Strings(mods)
	return mods, nil
}

func (s *Software) extStatus(ctx context.Context, _ helperapi.Caller, req helperapi.LineRef) (helperapi.ExtStatus, error) {
	sw, o, err := s.installedOffer(ctx, req.Software, req.Line)
	if err != nil {
		return helperapi.ExtStatus{}, err
	}
	if !o.Extensions {
		return helperapi.ExtStatus{}, pkgErr(helperapi.CodePkgExtUnsupported, map[string]any{"software": req.Software, "line": req.Line})
	}
	pkgs := extPackages(sw, o)
	var names []string
	for _, p := range pkgs {
		if p != "" && !slices.Contains(names, p) {
			names = append(names, p)
		}
	}
	installed := s.installed(ctx, names)
	mods, err := phpModules(ctx, s.Root, o.CLI)
	if err != nil {
		s.Log.Warn("php modules unavailable", "line", o.Line, "err", err)
	}
	st := helperapi.ExtStatus{Extensions: []helperapi.ExtensionInfo{}, Modules: mods}
	for _, e := range sw.Extensions {
		info := helperapi.ExtensionInfo{ID: e.ID, Name: e.Name, Modules: e.Modules}
		p := pkgs[e.ID]
		info.Installed = p != "" && installed[p] != ""
		info.Loaded = mods != nil
		for _, m := range e.Modules {
			if !slices.Contains(mods, m) {
				info.Loaded = false
			}
		}
		info.Builtin = info.Loaded && !info.Installed
		info.Available = p != "" || info.Builtin
		st.Extensions = append(st.Extensions, info)
	}
	return st, nil
}

func (s *Software) extApply(ctx context.Context, _ helperapi.Caller, req helperapi.ExtApplyRequest, log io.Writer) (any, error) {
	sw, o, err := s.installedOffer(ctx, req.Software, req.Line)
	if err != nil {
		return nil, err
	}
	if !o.Extensions || o.FPM == nil {
		return nil, pkgErr(helperapi.CodePkgExtUnsupported, map[string]any{"software": req.Software, "line": req.Line})
	}
	pkgs := extPackages(sw, o)
	resolve := func(ids []string) ([]string, error) {
		var out []string
		for _, id := range ids {
			p, ok := pkgs[id]
			if !ok {
				return nil, pkgErr(helperapi.CodePkgExtUnknown, map[string]any{"extension": id})
			}
			if p == "" {
				return nil, pkgErr(helperapi.CodePkgExtUnavailable, map[string]any{"extension": id})
			}
			out = append(out, p)
		}
		return out, nil
	}
	add, err := resolve(req.Install)
	if err != nil {
		return nil, err
	}
	drop, err := resolve(req.Remove)
	if err != nil {
		return nil, err
	}
	res := helperapi.ExtApplyResult{}
	if len(drop) > 0 {
		have := s.installed(ctx, drop)
		var present []string
		for _, p := range drop {
			if have[p] != "" {
				present = append(present, p)
			}
		}
		if len(present) > 0 {
			if by := s.removalTakes(ctx, present); len(by) > 0 {
				return nil, pkgErr(helperapi.CodePkgExtNeeded, map[string]any{"packages": strings.Join(present, ", "), "by": strings.Join(by, ", ")})
			}
			_, _ = fmt.Fprintf(log, "Removing %s from PHP %s.\n", strings.Join(present, ", "), o.Line)
			if s.Manager == "apt" {
				err = runPackageManager(ctx, log, "apt-get", append([]string{"remove", "-y"}, present...)...)
			} else {
				err = runPackageManager(ctx, log, "dnf", append([]string{"remove", "-y", "--noautoremove"}, present...)...)
			}
			if err != nil {
				return nil, err
			}
			res.Removed = present
		}
	}
	if len(add) > 0 {
		_, _ = fmt.Fprintf(log, "Installing %s for PHP %s.\n", strings.Join(add, ", "), o.Line)
		if s.Manager == "apt" {
			err = runPackageManager(ctx, log, "apt-get", append([]string{"install", "-y", "--no-install-recommends"}, add...)...)
		} else {
			err = runPackageManager(ctx, log, "dnf", append([]string{"install", "-y"}, add...)...)
		}
		if err != nil {
			return nil, err
		}
		have := s.installed(ctx, add)
		for _, p := range add {
			res.Installed = append(res.Installed, helperapi.InstalledPackage{Name: p, Version: have[p]})
		}
	}
	started := time.Now()
	did, err := s.reloadService(ctx, log, o.FPM.Service, o.FPM.PidFile, syscall.SIGUSR2)
	if err != nil {
		return nil, pkgErr(helperapi.CodePkgServiceFailed, map[string]any{"service": o.FPM.Service, "output": journalTail(ctx, o.FPM.Service, started)})
	}
	if did {
		res.Reloaded = o.FPM.Service
	}
	s.Log.Info("php extensions changed", "line", o.Line, "installed", req.Install, "removed", req.Remove)
	return res, nil
}

// removalTakes lists the other installed packages that removing pkgs
// would take with them (the package manager removes what depends on
// them); removing an extension must never remove more than it.
func (s *Software) removalTakes(ctx context.Context, pkgs []string) []string {
	var others []string
	if s.Manager == "apt" {
		out, _ := queryCommand(ctx, "apt-get", append([]string{"remove", "-s"}, pkgs...)...)
		for _, l := range strings.Split(out, "\n") {
			f := strings.Fields(l)
			if len(f) >= 2 && f[0] == "Remv" && !slices.Contains(pkgs, f[1]) {
				others = append(others, f[1])
			}
		}
		return others
	}
	out, err := checkCommand(ctx, append([]string{"rpm", "-e", "--test"}, pkgs...))
	if err == nil {
		return nil
	}
	for _, l := range strings.Split(out, "\n") {
		if _, by, ok := strings.Cut(l, "is needed by (installed) "); ok {
			if name := strings.Fields(by); len(name) > 0 && !slices.Contains(others, name[0]) {
				others = append(others, name[0])
			}
		}
	}
	if len(others) == 0 {
		others = append(others, "?")
	}
	return others
}

// reloadService reloads a running service through systemd, or by signal
// to the process in pidFile without systemd (containers). A service that
// is not running is left alone.
func (s *Software) reloadService(ctx context.Context, log io.Writer, unit, pidFile string, sig syscall.Signal) (bool, error) {
	if systemdRunning() {
		if unitActive(ctx, unit) != "active" {
			_, _ = fmt.Fprintf(log, "%s is not running; the change applies when it starts.\n", unit)
			return false, nil
		}
		return true, runCommand(ctx, log, "systemctl", "reload", unit)
	}
	pid, ok := int(0), false
	if pidFile != "" {
		pid, ok = processAlive(s.path(pidFile))
	}
	if !ok {
		_, _ = fmt.Fprintf(log, "%s is not running; the change applies when it starts.\n", unit)
		return false, nil
	}
	_, _ = fmt.Fprintf(log, "Reloading %s (signal to %d).\n", unit, pid)
	if err := signalProcess(pid, sig); err != nil {
		return false, err
	}
	pause(300 * time.Millisecond)
	return true, nil
}

// Configuration files ----------------------------------------------------------

func findConfig(o catalog.Offer, id string) (catalog.ConfigFile, bool) {
	for _, c := range o.Configs {
		if c.ID == id {
			return c, true
		}
	}
	return catalog.ConfigFile{}, false
}

func (s *Software) versionsDir(software, line, id string) string {
	base := s.ConfigVersionsDir
	if base == "" {
		base = DefaultConfigVersionsDir
	}
	return filepath.Join(base, software, line, id)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func textOK(b []byte) bool { return utf8.Valid(b) && !bytes.ContainsRune(b, 0) }

// readConfigFile reads a configuration file for showing or editing.
func (s *Software) readConfigFile(p string) ([]byte, os.FileInfo, error) {
	data, info, err := readConfFile(s.Root, p, helperapi.ConfigMax)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil, pkgErr(helperapi.CodePkgConfigMissing, map[string]any{"path": p})
	case errors.Is(err, errConfTooLarge):
		return nil, nil, pkgErr(helperapi.CodePkgConfigTooLarge, map[string]any{"path": p, "max": helperapi.ConfigMax})
	case err != nil:
		return nil, nil, confPkgErr(err)
	}
	if !textOK(data) {
		return nil, nil, pkgErr(helperapi.CodePkgConfigNotText, map[string]any{"path": p})
	}
	return data, info, nil
}

func (s *Software) configVersions(dir string) []helperapi.ConfigVersion {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []helperapi.ConfigVersion{}
	}
	out := []helperapi.ConfigVersion{}
	for _, e := range entries {
		n, err := strconv.ParseInt(e.Name(), 10, 64)
		if err != nil || !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, helperapi.ConfigVersion{ID: e.Name(), SavedAt: time.Unix(0, n).Unix(), Size: info.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

func (s *Software) configRead(ctx context.Context, _ helperapi.Caller, req helperapi.ConfigRequest) (helperapi.ConfigFile, error) {
	_, o, err := s.installedOffer(ctx, req.Software, req.Line)
	if err != nil {
		return helperapi.ConfigFile{}, err
	}
	cf, ok := findConfig(o, req.Config)
	if !ok {
		return helperapi.ConfigFile{}, pkgErr(helperapi.CodePkgConfigUnknown, map[string]any{"config": req.Config})
	}
	editable := s.RootAccess != "never"
	dir := s.versionsDir(req.Software, o.Line, cf.ID)
	if req.Version != "" {
		p := filepath.Join(dir, req.Version)
		data, err := os.ReadFile(p) //nolint:gosec // G304: the version ID is digits only.
		if errors.Is(err, fs.ErrNotExist) {
			return helperapi.ConfigFile{}, pkgErr(helperapi.CodePkgConfigVersion, map[string]any{"version": req.Version})
		}
		if err != nil {
			return helperapi.ConfigFile{}, err
		}
		n, _ := strconv.ParseInt(req.Version, 10, 64)
		return helperapi.ConfigFile{ID: cf.ID, Path: cf.Path, Content: string(data), SHA256: sha256Hex(data), Size: int64(len(data)),
			Editable: editable, Versions: []helperapi.ConfigVersion{}, SavedAt: time.Unix(0, n).Unix()}, nil
	}
	data, info, err := s.readConfigFile(cf.Path)
	if err != nil {
		return helperapi.ConfigFile{}, err
	}
	return helperapi.ConfigFile{ID: cf.ID, Path: cf.Path, Content: string(data), SHA256: sha256Hex(data), Size: info.Size(),
		ModTime: info.ModTime().Unix(), Editable: editable, Versions: s.configVersions(dir)}, nil
}

// keepVersion saves the current content as a version and drops the oldest
// beyond the limit.
func (s *Software) keepVersion(dir string, data []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	name := strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := writePrivate(filepath.Join(dir, name), data); err != nil {
		return err
	}
	versions := s.configVersions(dir)
	for _, v := range versions[min(len(versions), helperapi.ConfigVersionsKept):] {
		_ = os.Remove(filepath.Join(dir, v.ID))
	}
	return nil
}

// confPkgErr turns the refusal of a configuration path into its error
// code; other errors pass unchanged.
func confPkgErr(err error) error {
	if ue, ok := asConfUnsafe(err); ok {
		return pkgErr(helperapi.CodePkgConfigUnsafe, map[string]any{"path": ue.Path, "reason": ue.Reason})
	}
	return err
}

// replaceFile writes data over the existing configuration file p
// (beneath root) atomically, keeping its mode, owner and SELinux label.
func replaceFile(root, p string, data []byte) error {
	return confPkgErr(writeConfFile(root, p, data, confWrite{}))
}

func (s *Software) configWrite(ctx context.Context, _ helperapi.Caller, req helperapi.ConfigWriteRequest, log io.Writer) (any, error) {
	sw, o, err := s.installedOffer(ctx, req.Software, req.Line)
	if err != nil {
		return nil, err
	}
	cf, ok := findConfig(o, req.Config)
	if !ok {
		return nil, pkgErr(helperapi.CodePkgConfigUnknown, map[string]any{"config": req.Config})
	}
	if s.RootAccess == "never" {
		return nil, pkgErr(helperapi.CodePkgConfigLocked, nil)
	}
	next := []byte(req.Content)
	if !textOK(next) {
		return nil, pkgErr(helperapi.CodePkgConfigNotText, map[string]any{"path": cf.Path})
	}
	cur, _, err := s.readConfigFile(cf.Path)
	if err != nil {
		return nil, err
	}
	if sha256Hex(cur) != req.Base {
		return nil, pkgErr(helperapi.CodePkgConfigChanged, map[string]any{"path": cf.Path})
	}
	res := helperapi.ConfigWriteResult{SHA256: sha256Hex(next)}
	if bytes.Equal(cur, next) {
		_, _ = fmt.Fprintf(log, "%s is unchanged.\n", cf.Path)
		return res, nil
	}
	if err := s.keepVersion(s.versionsDir(sw.ID, o.Line, cf.ID), cur); err != nil {
		return nil, err
	}
	_, _ = fmt.Fprintf(log, "Kept the current %s as a version; writing the new one.\n", cf.Path)
	// Whether the service runs before the save decides whether it must
	// run again should the new file fail it.
	wasActive := cf.Service != "" && systemdRunning() && unitActive(ctx, cf.Service) == "active"
	if err := replaceFile(s.Root, cf.Path, next); err != nil {
		return nil, err
	}
	res.Changed = true
	restore := func() error {
		err := replaceFile(s.Root, cf.Path, cur)
		if err != nil {
			s.Log.Error("restoring a configuration file failed", "path", cf.Path, "err", err)
			_, _ = fmt.Fprintf(log, "Putting the previous %s back failed: %v\n", cf.Path, err)
		}
		return err
	}
	restoreFailed := func(err error, output string) error {
		return pkgErr(helperapi.CodePkgConfigRestoreFailed, map[string]any{"path": cf.Path, "service": cf.Service,
			"output": tailString(strings.TrimSpace(output+"\n"+err.Error()), 1200)})
	}
	if len(cf.Check) > 0 {
		argv := slices.Clone(cf.Check)
		argv[0] = filepath.Join(s.Root, argv[0])
		_, _ = fmt.Fprintf(log, "$ %s\n", strings.Join(cf.Check, " "))
		out, err := checkCommand(ctx, argv)
		if err != nil && strings.TrimSpace(out) == "" {
			out = err.Error() // it did not run, or said nothing
		}
		_, _ = io.WriteString(log, tailLines(out, 20))
		if err != nil || checkFailed.MatchString(out) {
			if rerr := restore(); rerr != nil {
				return nil, restoreFailed(rerr, out)
			}
			_, _ = fmt.Fprintf(log, "The check failed; the previous file was put back. Nothing was changed.\n")
			return nil, pkgErr(helperapi.CodePkgConfigInvalid, map[string]any{"check": filepath.Base(cf.Check[0]), "output": tailString(strings.TrimSpace(out), 1200)})
		}
	}
	started := time.Now()
	applied, err := s.applyConfig(ctx, log, o, cf)
	if err != nil {
		// The journal says why before the service comes back.
		why := journalTail(ctx, cf.Service, started)
		if rerr := restore(); rerr != nil {
			return nil, restoreFailed(rerr, why)
		}
		_, _ = fmt.Fprintf(log, "%s failed with the new file; the previous file was put back.\n%s\n", cf.Service, why)
		if !wasActive {
			// Without systemd, or a service that was not running: as it
			// was, reloaded with the previous file where it runs.
			_, _ = s.applyConfig(ctx, io.Discard, o, cf)
			return nil, pkgErr(helperapi.CodePkgConfigApplyFailed, map[string]any{"service": cf.Service, "output": why})
		}
		back := time.Now()
		if err := s.serviceBack(ctx, log, o, cf); err != nil {
			_, _ = fmt.Fprintf(log, "%s did not start again with the previous file: %v\n", cf.Service, err)
			return nil, pkgErr(helperapi.CodePkgConfigNotRunning, map[string]any{"service": cf.Service,
				"output": tailString(strings.TrimSpace(why+"\n"+journalTail(ctx, cf.Service, back)), 1200)})
		}
		return nil, pkgErr(helperapi.CodePkgConfigApplyFailed, map[string]any{"service": cf.Service, "output": why})
	}
	res.Applied = applied
	s.Log.Info("configuration file saved", "software", sw.ID, "line", o.Line, "config", cf.ID, "applied", applied)
	return res, nil
}

// serviceBack brings a service that ran before a failed save back with
// the previous file. One still running reloads or restarts as the file
// asks; one the new file stopped is started explicitly, since applying a
// file leaves a stopped service alone and not every unit restarts itself
// (Redis on EL, MariaDB after a configuration error).
func (s *Software) serviceBack(ctx context.Context, log io.Writer, o catalog.Offer, cf catalog.ConfigFile) error {
	if waitActive(ctx, cf.Service) {
		if _, err := s.applyConfig(ctx, log, o, cf); err == nil || unitActive(ctx, cf.Service) == "active" {
			return nil
		}
	}
	_, _ = fmt.Fprintf(log, "Starting %s with the previous file.\n", cf.Service)
	// A unit that failed too often in a row refuses to start until told.
	_ = runCommand(ctx, log, "systemctl", "reset-failed", cf.Service)
	if err := runCommand(ctx, log, "systemctl", "start", cf.Service); err != nil {
		return err
	}
	if !waitActive(ctx, cf.Service) {
		return fmt.Errorf("%s did not start", cf.Service)
	}
	return nil
}

// applyConfig reloads or restarts the service that reads the file; a
// service that is not running reads it when it starts.
func (s *Software) applyConfig(ctx context.Context, log io.Writer, o catalog.Offer, cf catalog.ConfigFile) (string, error) {
	switch cf.Apply {
	case catalog.ApplyReload:
		pidFile, sig := "", syscall.SIGHUP
		switch {
		case o.FPM != nil && cf.Service == o.FPM.Service:
			pidFile, sig = o.FPM.PidFile, syscall.SIGUSR2
		case cf.Service == "nginx":
			pidFile = nginxPidFile
		}
		did, err := s.reloadService(ctx, log, cf.Service, pidFile, sig)
		if err != nil || !did {
			return "", err
		}
		return cf.Service, nil
	case catalog.ApplyRestart:
		if !systemdRunning() || unitActive(ctx, cf.Service) != "active" {
			_, _ = fmt.Fprintf(log, "%s is not running; the change applies when it starts.\n", cf.Service)
			return "", nil
		}
		if err := runCommand(ctx, log, "systemctl", "restart", cf.Service); err != nil {
			return "", err
		}
		if !waitActive(ctx, cf.Service) {
			return "", fmt.Errorf("%s did not start", cf.Service)
		}
		return cf.Service, nil
	}
	return "", nil
}

// tailLines returns the last n lines of s, ending in a newline.
func tailLines(s string, n int) string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	return strings.Join(lines[max(0, len(lines)-n):], "\n") + "\n"
}
