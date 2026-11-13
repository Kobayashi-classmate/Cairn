// SPDX-License-Identifier: AGPL-3.0-only

package execx

import (
	"context"
	"errors"
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
