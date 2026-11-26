// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/Kobayashi-classmate/Cairn/internal/execx"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// Terminals: a login shell on a new pseudo-terminal, as a site's owner or
// an ordinary login user (root only when switched on), whose master side
// goes to the web process. Closing it hangs the shell up; a shell that
// ignores the hang-up is ended by term.close, which the web process calls
// when it closes a terminal.

const maxTerminals = 16

// startTerminal and startTerminalUnit start a shell. Tests replace them.
var (
	startTerminal     = execx.StartTerminal
	startTerminalUnit = execx.StartTerminalUnit
)

// activeTerminalUnits counts the running terminal units.
func (f *Files) activeTerminalUnits(ctx context.Context) int {
	out, err := queryCommand(ctx, "systemctl", "list-units", "--type=service", "--state=active", "--plain", "--no-legend", "cairn-term-*")
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "cairn-term-") {
			n++
		}
	}
	return n
}

type passwdEntry struct {
	name        string
	uid, gid    int
	home, shell string
}

func (f *Files) passwd() []passwdEntry {
	data, err := os.ReadFile(f.path("/etc/passwd"))
	if err != nil {
		return nil
	}
	var out []passwdEntry
	for _, line := range strings.Split(string(data), "\n") {
		p := strings.Split(line, ":")
		if len(p) < 7 {
			continue
		}
		// A line whose IDs are not IDs names no user (see parseID).
		uid, err1 := parseID(p[2])
		gid, err2 := parseID(p[3])
		if err1 != nil || err2 != nil {
			continue
		}
		out = append(out, passwdEntry{name: p[0], uid: uid, gid: gid, home: p[5], shell: p[6]})
	}
	return out
}

// loginShell reports whether a passwd shell lets its user log in.
func loginShell(shell string) bool {
	return shell != "" && !strings.HasSuffix(shell, "/nologin") && !strings.HasSuffix(shell, "/false")
}

func (f *Files) termUsers() helperapi.TermUsers {
	s := f.access()
	out := helperapi.TermUsers{Users: []helperapi.TermUser{}, RootTerminal: s.RootTerminal, Locked: s.Locked}
	byName := map[string]passwdEntry{}
	for _, e := range f.passwd() {
		byName[e.name] = e
	}
	if entries, err := os.ReadDir(f.path(sitesDir)); err == nil {
		for _, d := range entries {
			if !d.IsDir() || !helperapi.ValidSiteCode(d.Name()) {
				continue
			}
			if e, ok := byName[ownerOf(d.Name())]; ok {
				out.Users = append(out.Users, helperapi.TermUser{Name: e.name, Kind: helperapi.TermUserSite, Home: siteDirOf(d.Name()), Site: d.Name()})
			}
		}
	}
	var users []helperapi.TermUser
	for _, e := range byName {
		if e.uid >= 1000 && e.uid < 60000 && loginShell(e.shell) && !strings.HasPrefix(e.name, "cw-") {
			users = append(users, helperapi.TermUser{Name: e.name, Kind: helperapi.TermUserUser, Home: e.home})
		}
	}
	sort.Slice(users, func(i, j int) bool { return users[i].Name < users[j].Name })
	out.Users = append(out.Users, users...)
	if s.RootTerminal {
		out.Users = append(out.Users, helperapi.TermUser{Name: "root", Kind: helperapi.TermUserRoot, Home: "/root"})
	}
	return out
}

// shellFor picks a user's shell: theirs when it is a login shell, else
// bash (site owners have none).
func (f *Files) shellFor(e passwdEntry) string {
	if loginShell(e.shell) && path.IsAbs(e.shell) {
		if _, err := os.Stat(f.path(e.shell)); err == nil {
			return e.shell
		}
	}
	if _, err := os.Stat(f.path("/bin/bash")); err == nil {
		return "/bin/bash"
	}
	return "/bin/sh"
}

// terminalEnv is the shell's environment on top of execx's base. The web
// terminal is an xterm with true colour; CAIRN_TERM=1 tells
// /etc/profile.d/cairn-term.sh (packaging/profile) that the login is one,
// so it turns on colours there and nowhere else.
func terminalEnv(home, name, shell string) []string {
	return []string{"HOME=" + home, "USER=" + name, "LOGNAME=" + name, "SHELL=" + shell,
		"TERM=xterm-256color", "COLORTERM=truecolor", "CAIRN_TERM=1"}
}

// terminalLocale is LANG for a shell that systemd starts: the system's own
// (systemd hands it to every unit, and so to the helper) when it is a UTF-8
// locale, else C.UTF-8 like a shell started without systemd. The web
// terminal speaks UTF-8; under another locale, or none, programs garble
// or drop every non-ASCII character.
func terminalLocale(system string) string {
	lower := strings.ToLower(system)
	if (strings.Contains(lower, ".utf-8") || strings.Contains(lower, ".utf8")) && localeName.MatchString(system) {
		return system
	}
	return "C.UTF-8"
}

// localeName is a locale name as glibc writes them (zh_CN.UTF-8,
// sr_RS.UTF-8@latin), nothing else.
var localeName = regexp.MustCompile(`^[A-Za-z0-9_.@-]{1,64}$`)

func (f *Files) termOpen(ctx context.Context, c helperapi.Caller, req helperapi.TermOpenRequest) (helperapi.TermSession, *os.File, error) {
	var allowed *helperapi.TermUser
	users := f.termUsers()
	for i := range users.Users {
		if users.Users[i].Name == req.User {
			allowed = &users.Users[i]
		}
	}
	if allowed == nil {
		if req.User == "root" {
			return helperapi.TermSession{}, nil, fileErr(helperapi.CodeTermRootDisabled, nil)
		}
		return helperapi.TermSession{}, nil, fileErr(helperapi.CodeTermUserNotAllowed, map[string]any{"user": req.User})
	}
	var e passwdEntry
	for _, p := range f.passwd() {
		if p.name == req.User {
			e = p
		}
	}
	if e.name == "" {
		return helperapi.TermSession{}, nil, fileErr(helperapi.CodeTermUserNotAllowed, map[string]any{"user": req.User})
	}
	var groups []uint32
	if u, err := user.LookupId(strconv.Itoa(e.uid)); err == nil {
		if ids, err := u.GroupIds(); err == nil {
			for _, id := range ids {
				if g, err := strconv.ParseUint(id, 10, 32); err == nil {
					groups = append(groups, uint32(g))
				}
			}
		}
	}
	tty := -1
	if g, err := lookupGroup("tty"); err == nil {
		tty = g
	}
	f.mu.Lock()
	if f.terms >= maxTerminals {
		f.mu.Unlock()
		return helperapi.TermSession{}, nil, fileErr(helperapi.CodeTermTooMany, map[string]any{"max": maxTerminals})
	}
	f.terms++
	f.mu.Unlock()
	release := func() {
		f.mu.Lock()
		f.terms--
		f.mu.Unlock()
	}
	home, shell := allowed.Home, f.shellFor(e)
	if _, err := os.Stat(f.path(home)); err != nil {
		home = "/"
	}
	term := execx.Terminal{
		Shell: shell, Uid: uint32(e.uid), Gid: uint32(e.gid), Groups: groups, Home: home, //nolint:gosec // G115: 31-bit ids (parseID).
		Env:  terminalEnv(home, e.name, shell),
		Cols: uint16(req.Cols), Rows: uint16(req.Rows), TTYGroup: tty, //nolint:gosec // G115: validated sizes.
	}
	if systemdRunning() {
		// Without systemd execx sets a UTF-8 locale itself.
		term.Env = append(term.Env, "LANG="+terminalLocale(os.Getenv("LANG")))
		// A unit of its own: the helper's sandbox and control group stay
		// out of the shell, and a helper restart does not end it.
		release()
		if n := f.activeTerminalUnits(ctx); n >= maxTerminals {
			return helperapi.TermSession{}, nil, fileErr(helperapi.CodeTermTooMany, map[string]any{"max": maxTerminals})
		}
		unit := randomName("cairn-term-")
		master, err := startTerminalUnit(ctx, term, unit)
		if err != nil {
			return helperapi.TermSession{}, nil, err
		}
		f.Log.Info("terminal opened", "user", e.name, "unit", unit, "by", c.Username, "remote", c.Remote)
		return helperapi.TermSession{User: e.name, Home: home, Shell: shell, Unit: unit}, master, nil
	}
	master, cmd, err := startTerminal(term)
	if err != nil {
		release()
		return helperapi.TermSession{}, nil, err
	}
	pid := cmd.Process.Pid
	f.mu.Lock()
	if f.shells == nil {
		f.shells = map[int]*os.Process{}
	}
	f.shells[pid] = cmd.Process
	f.mu.Unlock()
	f.Log.Info("terminal opened", "user", e.name, "pid", pid, "by", c.Username, "remote", c.Remote)
	go func() {
		_ = cmd.Wait()
		f.mu.Lock()
		delete(f.shells, pid)
		f.mu.Unlock()
		release()
		f.Log.Info("terminal closed", "user", e.name, "pid", pid)
	}()
	return helperapi.TermSession{PID: pid, User: e.name, Home: home, Shell: shell}, master, nil
}

// termClose ends a terminal term.open started: its unit, with everything
// in it (the unit's name says it is a terminal's, and only the helper
// starts units), or a shell started without systemd that has not ended
// yet. One that ended already is no error.
func (f *Files) termClose(ctx context.Context, c helperapi.Caller, req helperapi.TermCloseRequest) (helperapi.Empty, error) {
	if req.Unit != "" {
		// Not waiting for it to stop: it is not counted as running from
		// now on (see activeTerminalUnits).
		_, err := queryCommand(ctx, "systemctl", "stop", "--no-block", req.Unit+".service")
		var ee *execx.ExitError
		if errors.As(err, &ee) && ee.ExitCode == 5 {
			return helperapi.Empty{}, nil // not loaded: ended and collected
		}
		if err != nil {
			return helperapi.Empty{}, fmt.Errorf("stopping %s: %w", req.Unit, err)
		}
		f.Log.Info("terminal ended", "unit", req.Unit, "by", c.Username, "remote", c.Remote)
		return helperapi.Empty{}, nil
	}
	f.mu.Lock()
	p := f.shells[req.PID]
	f.mu.Unlock()
	if p == nil {
		return helperapi.Empty{}, nil
	}
	if err := p.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return helperapi.Empty{}, err
	}
	f.Log.Info("terminal ended", "pid", req.PID, "by", c.Username, "remote", c.Remote)
	return helperapi.Empty{}, nil
}
