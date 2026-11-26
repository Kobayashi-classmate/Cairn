// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/Kobayashi-classmate/Cairn/catalog"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// App sites: the panel runs the project's command as a systemd unit, as
// the site's runner, behind nginx. The unit is locked down (read-only
// system, writable only the site's tmp/ and declared directories); the
// variables the user sets live in a root-only environment file, not in
// the world-readable unit.

const (
	appEnvDir  = "/etc/cairn/apps"
	appUnitDir = "/etc/systemd/system"
)

func appUnit(code string) string     { return "cairn-app-" + code + ".service" }
func appUnitPath(code string) string { return path.Join(appUnitDir, appUnit(code)) }
func appEnvPath(code string) string  { return path.Join(appEnvDir, code+".env") }

// systemdQuote escapes s for a double-quoted word of a unit file:
// backslashes and quotes, specifiers (%) and variables ($), which the shell
// expands at run time instead.
func systemdQuote(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`, `$`, `$$`).Replace(s)
}

// envFileQuote escapes a value for a double-quoted assignment of an
// environment file, where the escapes of a POSIX shell's double quotes
// apply: backslash, quote, backtick and dollar keep the next character.
func envFileQuote(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", `$`, `\$`).Replace(s)
}

// runtimeEnv returns the PATH directories and variables of an app runtime.
func (st *Sites) runtimeEnv(a *helperapi.AppSpec) ([]string, []string) {
	if a.Runtime == "" {
		return nil, nil
	}
	dir := path.Join(st.runtimesDir(), a.Runtime, a.Line)
	switch a.Runtime {
	case "java":
		return []string{dir + "/bin"}, []string{"JAVA_HOME=" + dir}
	case "dotnet":
		return []string{dir}, []string{"DOTNET_ROOT=" + dir, "DOTNET_CLI_TELEMETRY_OPTOUT=1", "DOTNET_NOLOGO=1"}
	}
	return []string{dir + "/bin"}, nil
}

func (st *Sites) runtimesDir() string {
	if st.Software != nil && st.Software.Runtimes != nil && st.Software.Runtimes.Dir != "" {
		return strings.TrimPrefix(st.Software.Runtimes.Dir, st.Root)
	}
	return "/opt/cairn/runtimes"
}

var unitTemplate = template.Must(template.New("unit").Parse(`# {{.Managed}}
# Site {{.Code}} (app)
[Unit]
Description=Cairn app {{.Code}}
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User={{.Runner}}
Group={{.Group}}
WorkingDirectory={{.WorkDir}}
Environment="HOME={{.SiteDir}}/tmp" "TMPDIR={{.SiteDir}}/tmp" "HOST=127.0.0.1" "PORT={{.Port}}" "PATH={{.Path}}"{{range .RuntimeEnv}} "{{.}}"{{end}}
EnvironmentFile=-{{.EnvFile}}
ExecStart=/bin/sh -c "{{.Command}}"
Restart=on-failure
RestartSec=3s
TimeoutStopSec=20s
UMask=0007
{{- if .MemoryMB}}
MemoryMax={{.MemoryMB}}M
{{- end}}
{{- if .CPUPercent}}
CPUQuota={{.CPUPercent}}%
{{- end}}
TasksMax=1024
# The app listens on its own port and on no other, so it cannot take
# another app's while that one restarts (sites' PHP, terminals and tasks
# are kept off the apps' ports as well). This needs cgroup v2.
SocketBindAllow=tcp:{{.Port}}
SocketBindDeny=any
NoNewPrivileges=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectSystem=strict
ProtectHome=yes
ReadWritePaths={{.SiteDir}}/tmp{{range .Writable}} {{.}}{{end}}
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictSUIDSGID=yes
LockPersonality=yes
RestrictRealtime=yes
SyslogIdentifier=cairn-app-{{.Code}}

[Install]
WantedBy=multi-user.target
`))

// renderApp returns the app's unit and environment file.
func (st *Sites) renderApp(spec helperapi.SiteSpec) ([]byte, []byte, error) {
	a := spec.App
	dir := siteDirOf(spec.Code)
	pathDirs, runtimeEnv := st.runtimeEnv(a)
	pathDirs = append(pathDirs, "/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin", "/sbin", "/bin")
	var writable []string
	for _, w := range spec.Writable {
		writable = append(writable, path.Join(dir, "app", w))
	}
	var unit bytes.Buffer
	if err := unitTemplate.Execute(&unit, map[string]any{
		"Managed": siteManaged, "Code": spec.Code, "Runner": runnerOf(spec.Code), "Group": ownerOf(spec.Code),
		"WorkDir": path.Join(dir, "app", a.WorkDir), "SiteDir": dir, "Port": a.Port, "Path": strings.Join(pathDirs, ":"),
		"RuntimeEnv": runtimeEnv, "EnvFile": appEnvPath(spec.Code), "Command": systemdQuote(a.Command),
		"MemoryMB": a.MemoryMB, "CPUPercent": a.CPUPercent, "Writable": writable,
	}); err != nil {
		return nil, nil, err
	}
	var env strings.Builder
	env.WriteString("# " + siteManaged + "\n")
	for _, e := range a.Env {
		fmt.Fprintf(&env, "%s=\"%s\"\n", e.Name, envFileQuote(e.Value))
	}
	return unit.Bytes(), []byte(env.String()), nil
}

// appChanges adds an app site's files and checks to a change; with
// remove, it takes them away.
func (st *Sites) appChanges(spec helperapi.SiteSpec, remove bool) ([]fileChange, []configCheck, error) {
	if remove {
		return []fileChange{{path: appUnitPath(spec.Code)}, {path: appEnvPath(spec.Code)}}, nil, nil
	}
	unit, env, err := st.renderApp(spec)
	if err != nil {
		return nil, nil, err
	}
	return []fileChange{
			{path: appEnvPath(spec.Code), data: env, mode: 0o600},
			{path: appUnitPath(spec.Code), data: unit},
		}, []configCheck{{name: "systemd-analyze", argv: []string{"systemd-analyze", "verify", appUnitPath(spec.Code)}}},
		nil
}

// checkAppPort refuses a port another site's app or a stream uses.
func (st *Sites) checkAppPort(spec helperapi.SiteSpec) error {
	states, err := st.states()
	if err != nil {
		return err
	}
	for code, s := range states {
		if code != spec.Code && s.Spec.App != nil && s.Spec.App.Port == spec.App.Port {
			return siteErr(helperapi.CodeAppPortTaken, map[string]any{"port": spec.App.Port, "site": code})
		}
	}
	streams, err := st.streamStates()
	if err != nil {
		return err
	}
	for code, s := range streams {
		if s.Port == spec.App.Port {
			return siteErr(helperapi.CodeAppPortTaken, map[string]any{"port": spec.App.Port, "stream": code})
		}
	}
	if st.PanelPort == spec.App.Port {
		return siteErr(helperapi.CodeAppPortTaken, map[string]any{"port": spec.App.Port, "panel": true})
	}
	return nil
}

// siteBindDeny keeps a unit run as a site's owner (a terminal, a shell
// task) from listening on any TCP or UDP port: its code could otherwise
// take an app's port while the app restarts and answer for it. Ports the
// kernel picks (port 0) stay allowed, and so do Unix sockets. This needs
// cgroup v2; without it systemd leaves it out.
const siteBindDeny = "SocketBindDeny=any"

// fpmPortsDropIn is the drop-in of a PHP line's PHP-FPM service that keeps
// it off the apps' ports.
func fpmPortsDropIn(f *catalog.FPM) string {
	return path.Join(appUnitDir, f.Service+".service.d", "cairn-app-ports.conf")
}

// fpmPortsConf renders that drop-in: the range the panel gives apps their
// ports from, every app's port outside it, and the panel's own port.
func (st *Sites) fpmPortsConf(states map[string]helperapi.SiteState) []byte {
	var b strings.Builder
	b.WriteString("# " + siteManaged + "\n")
	b.WriteString("# The pools of every site of this PHP line run in this service: the sites'\n" +
		"# PHP (and what it starts) may not listen on the ports of the panel's apps\n" +
		"# or of the panel. The panel's pools listen on Unix sockets, left alone.\n" +
		"# This needs cgroup v2.\n[Service]\n")
	fmt.Fprintf(&b, "SocketBindDeny=tcp:%d-%d\n", helperapi.AppPortMin, helperapi.AppPortMax)
	seen := map[int]bool{}
	var ports []int
	add := func(p int) {
		if p > 0 && (p < helperapi.AppPortMin || p > helperapi.AppPortMax) && !seen[p] {
			seen[p] = true
			ports = append(ports, p)
		}
	}
	for _, s := range states {
		if s.Spec.Type == helperapi.SiteApp && s.Spec.App != nil {
			add(s.Spec.App.Port)
		}
	}
	add(st.PanelPort)
	sort.Ints(ports)
	for _, p := range ports {
		fmt.Fprintf(&b, "SocketBindDeny=tcp:%d\n", p)
	}
	return []byte(b.String())
}

// applyFPMPorts brings the drop-in of every PHP line's PHP-FPM service to
// the apps applied now: the pools of a line share one master, in one
// control group, so the limit is the line's, not a site's. A line no longer
// installed loses its drop-in. systemd takes up a changed drop-in with a
// daemon-reload.
func (st *Sites) applyFPMPorts(ctx context.Context, log io.Writer) error {
	sw, ok := phpSoftware(st.Software.Catalog)
	if !ok {
		return nil
	}
	states, err := st.states()
	if err != nil {
		return err
	}
	conf := st.fpmPortsConf(states)
	want := map[string]bool{}
	for _, l := range sw.Lines {
		if fpm, installed := st.phpFPM(l.Version); fpm != nil {
			p := fpmPortsDropIn(fpm)
			want[p] = want[p] || installed
		}
	}
	paths := make([]string, 0, len(want))
	for p := range want {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	changed := false
	for _, p := range paths {
		have, err := os.ReadFile(st.path(p)) //nolint:gosec // G304: built from the catalog's service names.
		switch {
		case want[p] && (err != nil || !bytes.Equal(have, conf)):
			_, _ = fmt.Fprintf(log, "Writing %s\n", p)
			if err := writeFile(st.path(p), conf); err != nil {
				return err
			}
			changed = true
		case !want[p] && err == nil:
			_, _ = fmt.Fprintf(log, "Removing %s\n", p)
			if err := os.Remove(st.path(p)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			changed = true
		}
	}
	if changed && systemdRunning() {
		return runCommand(ctx, log, "systemctl", "daemon-reload")
	}
	return nil
}

// startApp brings the unit to the spec's state after its files changed.
func (st *Sites) startApp(ctx context.Context, log io.Writer, spec helperapi.SiteSpec, changed bool) error {
	unit := appUnit(spec.Code)
	if err := runCommand(ctx, log, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	if !spec.Enabled {
		return runCommand(ctx, log, "systemctl", "disable", "--now", unit)
	}
	if err := runCommand(ctx, log, "systemctl", "enable", unit); err != nil {
		return err
	}
	if changed || !st.unitActive(ctx, unit) {
		// A failing app is the project's problem, not the configuration's:
		// its state shows in the panel, the files stay.
		if err := runCommand(ctx, log, "systemctl", "restart", unit); err != nil {
			_, _ = fmt.Fprintf(log, "The app did not start; see its log in the panel.\n")
		}
	}
	return nil
}

// stopApp stops and disables an app's unit before its files go.
func (st *Sites) stopApp(ctx context.Context, log io.Writer, code string) {
	_ = runCommand(ctx, log, "systemctl", "disable", "--now", appUnit(code))
}

// restoreApp starts an app stopped for a change that then failed: its
// files were put back, and it runs as before.
func (st *Sites) restoreApp(ctx context.Context, log io.Writer, prev helperapi.SiteSpec) {
	if !prev.Enabled {
		return
	}
	_, _ = fmt.Fprintf(log, "Starting the app again with its previous configuration.\n")
	if err := st.startApp(ctx, log, prev, true); err != nil {
		_, _ = fmt.Fprintf(log, "Warning: the app could not be started again: %v\n", err)
	}
}

// AppStatus reads an app unit's state from systemd.
func (st *Sites) AppStatus(ctx context.Context, code string) (helperapi.AppStatus, error) {
	s := helperapi.AppStatus{Code: code}
	if !systemdRunning() {
		return s, siteErr(helperapi.CodeAppNoSystemd, nil)
	}
	out, err := queryCommand(ctx, "systemctl", "show", appUnit(code),
		"--property=ActiveState,SubState,MainPID,NRestarts,MemoryCurrent,CPUUsageNSec,ActiveEnterTimestampMonotonic,Result,LoadState")
	if err != nil {
		return s, err
	}
	props := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), "="); ok {
			props[k] = v
		}
	}
	if props["LoadState"] == "not-found" {
		return s, siteErr(helperapi.CodeAppNotFound, map[string]any{"site": code})
	}
	num := func(k string) int64 {
		v, err := strconv.ParseInt(props[k], 10, 64)
		if err != nil || v < 0 {
			return 0
		}
		return v
	}
	// Counts are read at the width they are kept in, not narrowed after.
	count := func(k string) int {
		v, err := strconv.Atoi(props[k])
		if err != nil || v < 0 {
			return 0
		}
		return v
	}
	s.ActiveState, s.SubState, s.Result = props["ActiveState"], props["SubState"], props["Result"]
	s.MainPID, s.Restarts = count("MainPID"), count("NRestarts")
	if v := props["MemoryCurrent"]; v != "[not set]" {
		s.MemoryBytes = num("MemoryCurrent")
	}
	if v := props["CPUUsageNSec"]; v != "[not set]" {
		s.CPUNanos = num("CPUUsageNSec")
	}
	if mono := num("ActiveEnterTimestampMonotonic"); mono > 0 && s.ActiveState == "active" {
		s.Since = time.Now().Add(-(monotonicNow() - time.Duration(mono)*time.Microsecond)).Unix()
	}
	return s, nil
}

// AppLog reads an app's journal: the last 300 lines, or those after cursor.
// With --all, journalctl prints lines with control characters as they are
// instead of "[N blob data]": the panel shows the colours of an app's
// output and drops every other escape sequence.
func (st *Sites) AppLog(ctx context.Context, req helperapi.AppLogRequest) (helperapi.AppLogResponse, error) {
	if !systemdRunning() {
		return helperapi.AppLogResponse{}, siteErr(helperapi.CodeAppNoSystemd, nil)
	}
	args := []string{"--unit", appUnit(req.Code), "--no-pager", "--all", "--output", "short-iso", "--show-cursor"}
	if req.Cursor != "" {
		args = append(args, "--after-cursor", req.Cursor)
	} else {
		args = append(args, "--lines", "300")
	}
	out, _ := queryCommand(ctx, "journalctl", args...)
	res := helperapi.AppLogResponse{Lines: []string{}, Cursor: req.Cursor}
	for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if c, ok := strings.CutPrefix(l, "-- cursor: "); ok {
			res.Cursor = strings.TrimSpace(c)
			continue
		}
		if l == "" || l == "-- No entries --" {
			continue
		}
		res.Lines = append(res.Lines, l)
	}
	return res, nil
}

// restartApp implements app.restart.
func (st *Sites) restartApp(ctx context.Context, _ helperapi.Caller, req helperapi.AppRef, log io.Writer) (any, error) {
	states, err := st.states()
	if err != nil {
		return nil, err
	}
	s, ok := states[req.Code]
	if !ok || s.Spec.Type != helperapi.SiteApp {
		return nil, siteErr(helperapi.CodeAppNotFound, map[string]any{"site": req.Code})
	}
	if !systemdRunning() {
		return nil, siteErr(helperapi.CodeAppNoSystemd, nil)
	}
	if !s.Spec.Enabled {
		return nil, siteErr(helperapi.CodeAppNotFound, map[string]any{"site": req.Code, "stopped": true})
	}
	if err := runCommand(ctx, log, "systemctl", "restart", appUnit(req.Code)); err != nil {
		return nil, err
	}
	return st.AppStatus(ctx, req.Code)
}
