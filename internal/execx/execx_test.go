// SPDX-License-Identifier: AGPL-3.0-only

package execx

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRunCapturesOutput(t *testing.T) {
	res, err := Run(context.Background(), Cmd{Name: "echo", Args: []string{"hello", "$(id)", "; rm -rf /"}})
	if err != nil {
		t.Fatal(err)
	}
	// Arguments reach the program verbatim: no shell expansion happened.
	if got := strings.TrimSpace(string(res.Stdout)); got != "hello $(id) ; rm -rf /" {
		t.Fatalf("unexpected stdout %q", got)
	}
}

func TestShellsAreRefused(t *testing.T) {
	for _, name := range []string{"sh", "bash", "/bin/sh", "/usr/bin/bash", "dash"} {
		if _, err := Run(context.Background(), Cmd{Name: name, Args: []string{"-c", "id"}}); !errors.Is(err, ErrShell) && !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: want ErrShell, got %v", name, err)
		}
	}
	if _, err := Run(context.Background(), Cmd{Name: "./echo"}); err == nil {
		t.Error("relative path should be refused")
	}
}

func TestNonZeroExit(t *testing.T) {
	res, err := Run(context.Background(), Cmd{Name: "false"})
	var ee *ExitError
	if !errors.As(err, &ee) || ee.ExitCode != 1 || res.ExitCode != 1 {
		t.Fatalf("want ExitError with code 1, got %v (res %+v)", err, res)
	}
}

func TestTimeoutKillsProcess(t *testing.T) {
	start := time.Now()
	_, err := Run(context.Background(), Cmd{Name: "sleep", Args: []string{"10"}, Timeout: 200 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline exceeded, got %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("timeout did not stop the process promptly")
	}
}

func TestOutputIsCapped(t *testing.T) {
	res, err := Run(context.Background(), Cmd{Name: "head", Args: []string{"-c", "300000", "/dev/zero"}, MaxOutput: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Stdout) != 1000 || !res.Truncated {
		t.Fatalf("want 1000 bytes and Truncated, got %d bytes truncated=%v", len(res.Stdout), res.Truncated)
	}
}

func TestEnvironmentIsReset(t *testing.T) {
	t.Setenv("CAIRN_SECRET_SHOULD_NOT_LEAK", "x")
	res, err := Run(context.Background(), Cmd{Name: "env", Env: []string{"FOO=bar"}})
	if err != nil {
		t.Fatal(err)
	}
	out := string(res.Stdout)
	if strings.Contains(out, "CAIRN_SECRET_SHOULD_NOT_LEAK") || !strings.Contains(out, "FOO=bar") || !strings.Contains(out, "PATH="+SafePath) {
		t.Fatalf("unexpected environment:\n%s", out)
	}
	for _, bad := range []string{"PATH=/tmp", "LD_PRELOAD=/x.so", "NOEQUALS"} {
		if _, err := Run(context.Background(), Cmd{Name: "env", Env: []string{bad}}); err == nil {
			t.Errorf("env entry %q should be refused", bad)
		}
	}
}

func TestOutputIsStreamed(t *testing.T) {
	var live strings.Builder
	res, err := Run(context.Background(), Cmd{Name: "printf", Args: []string{"one\ntwo\n"}, Output: &live, MaxOutput: 4})
	if err != nil {
		t.Fatal(err)
	}
	// The live copy is complete even when the captured copy is capped.
	if live.String() != "one\ntwo\n" || string(res.Stdout) != "one\n" || !res.Truncated {
		t.Fatalf("live %q, captured %q, truncated %v", live.String(), res.Stdout, res.Truncated)
	}
}

func TestStdoutIsRedirected(t *testing.T) {
	var out, live strings.Builder
	res, err := Run(context.Background(), Cmd{Name: "printf", Args: []string{"0123456789"}, Stdout: &out, Output: &live, MaxOutput: 4})
	if err != nil {
		t.Fatal(err)
	}
	// Redirected output is complete, not captured and not in the live log.
	if out.String() != "0123456789" || len(res.Stdout) != 0 || res.Truncated || live.Len() != 0 {
		t.Fatalf("out %q, captured %q, truncated %v, live %q", out.String(), res.Stdout, res.Truncated, live.String())
	}
}

func TestStartTerminal(t *testing.T) {
	if _, err := os.Stat("/dev/ptmx"); err != nil {
		t.Skip("no /dev/ptmx")
	}
	sh := "/bin/sh"
	if !listedShell(sh) {
		t.Skip("/bin/sh is not in /etc/shells")
	}
	if _, _, err := StartTerminal(Terminal{Shell: "/usr/bin/env", Uid: uint32(os.Getuid()), Gid: uint32(os.Getgid()), Home: "/"}); err == nil {
		t.Fatal("a program not in /etc/shells must be refused")
	}
	master, cmd, err := StartTerminal(Terminal{Shell: sh, Uid: uint32(os.Getuid()), Gid: uint32(os.Getgid()), Home: os.TempDir(),
		Env: []string{"TERM=dumb"}, Cols: 100, Rows: 30, TTYGroup: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	if _, err := master.WriteString("stty size; echo \"marker $((40+2))\"; exit\n"); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	buf := make([]byte, 4096)
	for !strings.Contains(out.String(), "marker 42") {
		n, err := master.Read(buf)
		out.Write(buf[:n])
		if err != nil {
			break
		}
	}
	_ = cmd.Wait()
	if !strings.Contains(out.String(), "30 100") || !strings.Contains(out.String(), "marker 42") {
		t.Fatalf("terminal output %q", out.String())
	}
}

func TestTerminalUnitArgs(t *testing.T) {
	args := strings.Join(TerminalUnitArgs(Terminal{Shell: "/bin/bash", Uid: 1000, Gid: 1001, Home: "/home/a", Env: []string{"TERM=xterm-256color"}},
		"cairn-term-x", "/dev/pts/7"), " ")
	for _, want := range []string{"--unit=cairn-term-x", "--uid=1000 --gid=1001", "--property=TTYPath=/dev/pts/7", "--property=StandardInput=tty",
		"--setenv=TERM=xterm-256color", "-- /bin/bash -l"} {
		if !strings.Contains(args, want) {
			t.Errorf("args lack %q: %s", want, args)
		}
	}
}
