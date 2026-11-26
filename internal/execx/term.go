// SPDX-License-Identifier: AGPL-3.0-only

package execx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Terminal describes an interactive login shell on a new pseudo-terminal:
// the one place where Cairn starts a shell. It takes no command: the
// person at the terminal types the commands, as the user it runs as.
type Terminal struct {
	// Shell is an absolute path; it must be listed in /etc/shells.
	Shell string
	Uid   uint32
	Gid   uint32
	// Groups are the user's supplementary groups.
	Groups []uint32
	// Home is the working directory and $HOME.
	Home string
	// Env holds extra KEY=VALUE entries (USER, LOGNAME, TERM...).
	Env        []string
	Cols, Rows uint16
	// TTYGroup owns the terminal device (the "tty" group), -1 to keep it.
	TTYGroup int
}

// StartTerminal opens a pseudo-terminal and starts the shell on it as a
// session leader with the terminal as its controlling terminal. The caller
// gets the master side and must wait for the process. The shell shares
// the caller's namespaces and control group; StartTerminalUnit does not.
func StartTerminal(t Terminal) (*os.File, *exec.Cmd, error) {
	env, err := buildEnv(t.Env)
	if err != nil {
		return nil, nil, err
	}
	master, slave, err := preparePTY(t)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = slave.Close() }()
	cmd := &exec.Cmd{
		Path:   t.Shell,
		Args:   []string{"-" + filepath.Base(t.Shell)}, // a login shell
		Env:    env,
		Dir:    t.Home,
		Stdin:  slave,
		Stdout: slave,
		Stderr: slave,
		SysProcAttr: &syscall.SysProcAttr{
			Setsid:     true,
			Setctty:    true,
			Ctty:       0, // stdin
			Credential: &syscall.Credential{Uid: t.Uid, Gid: t.Gid, Groups: t.Groups},
		},
	}
	if err := cmd.Start(); err != nil {
		_ = master.Close()
		return nil, nil, err
	}
	return master, cmd, nil
}

// preparePTY checks the shell and opens a terminal of the right size,
// owned like login(1) does: by the user, writable by the tty group.
func preparePTY(t Terminal) (*os.File, *os.File, error) {
	if !filepath.IsAbs(t.Shell) {
		return nil, nil, errors.New("execx: terminal shell must be an absolute path")
	}
	if !listedShell(t.Shell) {
		return nil, nil, fmt.Errorf("execx: %s is not in /etc/shells", t.Shell)
	}
	master, slave, err := openPTY()
	if err != nil {
		return nil, nil, err
	}
	fail := func(err error) (*os.File, *os.File, error) {
		_ = master.Close()
		_ = slave.Close()
		return nil, nil, err
	}
	ws := unix.Winsize{Col: t.Cols, Row: t.Rows}
	if err := unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, &ws); err != nil {
		return fail(err)
	}
	if err := slave.Chown(int(t.Uid), t.TTYGroup); err != nil {
		return fail(err)
	}
	if err := slave.Chmod(0o620); err != nil {
		return fail(err)
	}
	return master, slave, nil
}

// TerminalMaxRuntime is how long a terminal's unit may run at most. The
// panel ends a terminal with the browser session that opened it, and a
// session lasts 12 hours at most: no terminal in use runs this long. It
// ends a shell that outlived its terminal (one that ignores the hang-up
// when the terminal is closed, while the panel could not stop its unit).
const TerminalMaxRuntime = 13 * time.Hour

// TerminalUnitArgs is the systemd-run command line that starts the shell
// as a transient service of its own on tty: its own control group, the
// host's /tmp, nothing inherited from the caller's unit. With
// Type=exec, systemd-run returns once the shell runs and holds the
// terminal, so the caller may close its own copy of the terminal: the
// master would see a hang-up only when the last holder is gone. Stopping
// the unit ends everything the shell started, with a hang-up besides the
// SIGTERM (SendSIGHUP), as closing a terminal would.
func TerminalUnitArgs(t Terminal, unit, tty string) []string {
	args := []string{"--unit=" + unit, "--description=Cairn terminal", "--collect", "--quiet",
		"--uid=" + strconv.FormatUint(uint64(t.Uid), 10), "--gid=" + strconv.FormatUint(uint64(t.Gid), 10),
		"--working-directory=" + t.Home,
		"--service-type=exec", "--property=TTYPath=" + tty, "--property=StandardInput=tty", "--property=StandardOutput=tty", "--property=StandardError=tty",
		"--property=SendSIGHUP=yes", "--property=RuntimeMaxSec=" + strconv.FormatInt(int64(TerminalMaxRuntime/time.Second), 10)}
	for _, e := range t.Env {
		args = append(args, "--setenv="+e)
	}
	return append(args, "--", t.Shell, "-l")
}

// StartTerminalUnit starts the shell through systemd (see
// TerminalUnitArgs) and returns the terminal's master side. systemd opens
// the terminal itself and makes it the shell's controlling terminal.
func StartTerminalUnit(ctx context.Context, t Terminal, unit string) (*os.File, error) {
	if _, err := buildEnv(t.Env); err != nil {
		return nil, err
	}
	master, slave, err := preparePTY(t)
	if err != nil {
		return nil, err
	}
	defer func() { _ = slave.Close() }()
	if _, err := Run(ctx, Cmd{Name: "systemd-run", Args: TerminalUnitArgs(t, unit, slave.Name()), Timeout: 30 * time.Second}); err != nil {
		_ = master.Close()
		return nil, err
	}
	return master, nil
}

func listedShell(shell string) bool {
	data, err := os.ReadFile("/etc/shells")
	if err != nil {
		return false
	}
	for _, line := range splitLines(string(data)) {
		if line == shell {
			return true
		}
	}
	return false
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '\n' {
			line := s[start:i]
			for len(line) > 0 && (line[len(line)-1] == '\r' || line[len(line)-1] == ' ') {
				line = line[:len(line)-1]
			}
			if line != "" && line[0] != '#' {
				out = append(out, line)
			}
			start = i + 1
		}
	}
	return out
}

// openPTY opens a new pseudo-terminal pair (Linux devpts).
func openPTY() (*os.File, *os.File, error) {
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	master := os.NewFile(uintptr(fd), "/dev/ptmx")
	var unlock int32
	if err := ioctl(fd, unix.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); err != nil {
		_ = master.Close()
		return nil, nil, err
	}
	n, err := unix.IoctlGetUint32(fd, unix.TIOCGPTN)
	if err != nil {
		_ = master.Close()
		return nil, nil, err
	}
	name := fmt.Sprintf("/dev/pts/%d", n)
	sfd, err := unix.Open(name, unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		_ = master.Close()
		return nil, nil, err
	}
	return master, os.NewFile(uintptr(sfd), name), nil
}

func ioctl(fd int, req uint, arg uintptr) error {
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(req), arg); e != 0 {
		return e
	}
	return nil
}
