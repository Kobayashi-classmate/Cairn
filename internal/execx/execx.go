// SPDX-License-Identifier: AGPL-3.0-only

// Package execx is the only place in Cairn allowed to start external
// processes. Commands are always an executable plus an argv list; shells are
// refused outright, so no request parameter can ever be interpreted as shell
// syntax. The environment is reset to a fixed, minimal set.
package execx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// SafePath is the only PATH used to resolve bare command names and the only
// PATH passed to child processes.
const SafePath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

const (
	defaultTimeout   = 60 * time.Second
	defaultMaxOutput = 1 << 20 // per stream
	waitDelay        = 5 * time.Second
)

var (
	// ErrShell is returned when the command would start a shell interpreter.
	ErrShell = errors.New("execx: starting a shell is not allowed")
	// ErrNotFound is returned when a bare command name is not on SafePath.
	ErrNotFound = errors.New("execx: executable not found on safe path")
)

// shells lists interpreters that would evaluate their arguments as code.
var shells = map[string]bool{
	"sh": true, "bash": true, "dash": true, "zsh": true, "ksh": true,
	"mksh": true, "ash": true, "csh": true, "tcsh": true, "fish": true, "busybox": true,
}

// Cmd describes a command to run.
type Cmd struct {
	// Name is an absolute path or a bare name resolved against SafePath.
	Name string
	// Args are passed verbatim as argv[1:]; they are never parsed by a shell.
	Args []string
	// Env holds extra KEY=VALUE entries appended to the fixed base environment.
	Env []string
	// Dir is the working directory; empty means "/".
	Dir string
	// Stdin is optional input.
	Stdin io.Reader
	// Timeout bounds the run; zero means 60 seconds.
	Timeout time.Duration
	// MaxOutput caps stdout and stderr each, in bytes; zero means 1 MiB.
	MaxOutput int
	// Output, when set, also receives stdout and stderr as they are produced,
	// interleaved (live logs of long operations). Its errors are ignored and
	// it does not count against MaxOutput.
	Output io.Writer
}

// Result is the outcome of a finished command.
type Result struct {
	Stdout    []byte
	Stderr    []byte
	ExitCode  int
	Truncated bool
	Duration  time.Duration
}

// ExitError reports a command that ran but exited with a non-zero status.
type ExitError struct {
	Name     string
	ExitCode int
	Stderr   string // trailing part of stderr, for diagnostics
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("execx: %s exited with status %d: %s", e.Name, e.ExitCode, e.Stderr)
}

// Run executes c and waits for it. A non-zero exit status yields an
// *ExitError together with the populated Result.
func Run(ctx context.Context, c Cmd) (Result, error) {
	path, err := resolve(c.Name)
	if err != nil {
		return Result{}, err
	}
	for _, a := range c.Args {
		if strings.IndexByte(a, 0) >= 0 {
			return Result{}, errors.New("execx: argument contains a NUL byte")
		}
	}
	env, err := buildEnv(c.Env)
	if err != nil {
		return Result{}, err
	}

	timeout := c.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	limit := c.MaxOutput
	if limit <= 0 {
		limit = defaultMaxOutput
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stdout := &cappedBuffer{limit: limit}
	stderr := &cappedBuffer{limit: limit}
	cmd := exec.CommandContext(ctx, path, c.Args...)
	cmd.Env = env
	cmd.Dir = c.Dir
	if cmd.Dir == "" {
		cmd.Dir = "/"
	}
	cmd.Stdin = c.Stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if c.Output != nil {
		live := &lockedWriter{w: c.Output}
		cmd.Stdout = io.MultiWriter(stdout, live)
		cmd.Stderr = io.MultiWriter(stderr, live)
	}
	// Run in its own process group so a timeout kills every descendant.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = waitDelay

	start := time.Now()
	runErr := cmd.Run()
	res := Result{
		Stdout:    stdout.Bytes(),
		Stderr:    stderr.Bytes(),
		Truncated: stdout.truncated || stderr.truncated,
		Duration:  time.Since(start),
	}
	if ctx.Err() != nil {
		res.ExitCode = -1
		return res, fmt.Errorf("execx: %s: %w", c.Name, ctx.Err())
	}
	var ee *exec.ExitError
	if errors.As(runErr, &ee) {
		res.ExitCode = ee.ExitCode()
		return res, &ExitError{Name: c.Name, ExitCode: res.ExitCode, Stderr: tail(res.Stderr, 512)}
	}
	if runErr != nil {
		return res, fmt.Errorf("execx: %s: %w", c.Name, runErr)
	}
	return res, nil
}

// resolve maps a command name to an absolute executable path and refuses
// shells, whether named directly or reached through a symlink.
func resolve(name string) (string, error) {
	if name == "" {
		return "", errors.New("execx: empty command name")
	}
	var path string
	if filepath.IsAbs(name) {
		path = filepath.Clean(name)
	} else {
		if strings.ContainsRune(name, '/') {
			return "", errors.New("execx: relative paths are not allowed")
		}
		for _, dir := range filepath.SplitList(SafePath) {
			candidate := filepath.Join(dir, name)
			if isExecutable(candidate) {
				path = candidate
				break
			}
		}
		if path == "" {
			return "", fmt.Errorf("%w: %s", ErrNotFound, name)
		}
	}
	if shells[filepath.Base(path)] {
		return "", ErrShell
	}
	if real, err := filepath.EvalSymlinks(path); err == nil && shells[filepath.Base(real)] {
		return "", ErrShell
	}
	return path, nil
}

func isExecutable(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0
}

// buildEnv returns the fixed base environment plus validated extra entries.
func buildEnv(extra []string) ([]string, error) {
	env := []string{"PATH=" + SafePath, "LANG=C.UTF-8", "LC_ALL=C.UTF-8"}
	for _, kv := range extra {
		k, _, ok := strings.Cut(kv, "=")
		if !ok || k == "" || strings.ContainsAny(k, " \t\n\x00") || strings.IndexByte(kv, 0) >= 0 {
			return nil, fmt.Errorf("execx: invalid environment entry %q", k)
		}
		if k == "PATH" || strings.HasPrefix(k, "LD_") {
			return nil, fmt.Errorf("execx: environment variable %s may not be overridden", k)
		}
		env = append(env, kv)
	}
	return env, nil
}

func tail(b []byte, n int) string {
	if len(b) > n {
		b = b[len(b)-n:]
	}
	return strings.TrimSpace(string(b))
}

// cappedBuffer keeps at most limit bytes and silently discards the rest, so a
// chatty child can neither exhaust memory nor block on a full pipe.
type cappedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := c.limit - c.buf.Len(); room > 0 {
		if len(p) > room {
			c.buf.Write(p[:room])
			c.truncated = true
		} else {
			c.buf.Write(p)
		}
	} else if len(p) > 0 {
		c.truncated = true
	}
	return len(p), nil
}

func (c *cappedBuffer) Bytes() []byte { return c.buf.Bytes() }

// lockedWriter serializes the stdout and stderr copies into one writer and
// swallows its errors, so a failing log never stops the command.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.w.Write(p)
	return len(p), nil
}
