// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/execx"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// The package manager's own unit.
//
// cairn-helper.service is sandboxed, and whatever the helper starts inherits
// the sandbox. apt-get and dnf therefore run in cairn-pkg.service, a
// transient unit with no sandbox, whose command is this binary again
// ("cairn-helper run-pkg <job>", RunPkgUnit).
//
// The unit does not depend on the helper's: a transaction goes on when the
// helper stops or restarts half-way, because a dpkg or rpm killed in the
// middle is the worse outcome. So the two share nothing but root-only files
// in pkgUnitDir, named after the job: the helper writes the command, the
// unit appends the command's output to a file the helper follows into the
// operation's log, and writes the exit status when the command has ended.
// Nothing a request carries reaches systemd-run: its arguments are constants
// and the job's name, which the helper makes up.
//
// The unit's name is fixed, so systemd refuses a second transaction while
// one runs, whichever process asks for it.
const (
	pkgUnit = "cairn-pkg.service"
	// A job's files: the command as the helper wrote it, the same file once
	// the unit has taken it, the command's output and how it ended.
	pkgJobSuffix    = ".job.json"
	pkgTakenSuffix  = ".run.json"
	pkgOutputSuffix = ".log"
	pkgResultSuffix = ".result.json"
	// pkgUnitStopWait is longer than systemd's default 90 seconds between
	// SIGTERM and SIGKILL.
	pkgUnitStopWait = 2 * time.Minute
	// pkgUnitKeep is how long a transaction's files stay, to be read after
	// a helper restart. No transaction runs that long, so pruning older
	// files never takes those of one that another process started.
	pkgUnitKeep = 24 * time.Hour
)

// Replaceable in tests, which must never start a unit or a package manager.
var (
	pkgUnitDir = "/var/lib/cairn-helper/pkg"
	// pkgUnitExec runs systemd-run and systemctl.
	pkgUnitExec = func(ctx context.Context, c execx.Cmd) (execx.Result, error) { return execx.Run(ctx, c) }
	// pkgJobExec runs the package manager, inside the unit.
	pkgJobExec = func(ctx context.Context, c execx.Cmd) (execx.Result, error) { return execx.Run(ctx, c) }
	// pkgUnitBinary is the helper's executable, which the unit runs.
	pkgUnitBinary = func() string {
		if exe, err := os.Executable(); err == nil {
			return exe
		}
		return "/usr/lib/cairn/cairn-helper"
	}
	// pkgUnitPoll is how often the helper reads new output and looks for
	// the result; pkgUnitCheck how often it asks systemd about a unit that
	// has given no result yet, or that it is waiting for.
	pkgUnitPoll  = 300 * time.Millisecond
	pkgUnitCheck = 2 * time.Second
)

// pkgUnitSlot lets this process start one transaction at a time, whether or
// not the caller holds LockPkg.
var pkgUnitSlot = make(chan struct{}, 1)

var pkgJobID = regexp.MustCompile(`^[0-9a-f]{8,32}$`)

// pkgJob is a command for the unit.
type pkgJob struct {
	ID   string   `json:"id"`
	Name string   `json:"name"`
	Args []string `json:"args"`
}

// pkgResult is how a job's command ended. Error is set when it did not run
// to an exit status.
type pkgResult struct {
	ID       string `json:"id"`
	ExitCode int    `json:"exitCode"`
	Stderr   string `json:"stderr,omitempty"` // its tail, as in execx.ExitError
	TimedOut bool   `json:"timedOut,omitempty"`
	Error    string `json:"error,omitempty"`
}

func (j pkgJob) path(suffix string) string { return filepath.Join(pkgUnitDir, j.ID+suffix) }

// result reads the job's result once the unit has written it.
func (j pkgJob) result() (pkgResult, bool) {
	var r pkgResult
	data, err := os.ReadFile(j.path(pkgResultSuffix))
	if err != nil || json.Unmarshal(data, &r) != nil || r.ID != j.ID {
		return r, false
	}
	return r, true
}

// pkgUnitState is the unit's state as systemd names it ("active",
// "inactive", ...), or "" when systemd did not answer.
func pkgUnitState(ctx context.Context) string {
	// is-active exits non-zero for every state but active; the state is on
	// standard output either way. A unit systemd does not know is inactive.
	res, _ := pkgUnitExec(ctx, execx.Cmd{Name: "systemctl", Args: []string{"is-active", pkgUnit}, Timeout: 30 * time.Second, MaxOutput: 4096})
	return strings.TrimSpace(string(res.Stdout))
}

// pkgUnitRunning reports a unit that exists and has not ended yet.
func pkgUnitRunning(state string) bool {
	switch state {
	case "active", "activating", "deactivating", "reloading":
		return true
	}
	return false
}

// pkgUnitEnded reports a unit systemd says is gone. No answer is neither
// running nor ended: systemd may be restarting itself for an update.
func pkgUnitEnded(state string) bool { return state == "inactive" || state == "failed" }

// PkgUnitBusy reports whether the package manager is running in its unit.
// A transaction outlives the helper process that started it, so the next
// process finds it here rather than in its own locks: package operations
// and the panel's own update wait for it (Ops.Busy).
func PkgUnitBusy() bool {
	if len(pkgUnitSlot) > 0 {
		return true
	}
	if !systemdRunning() {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return pkgUnitRunning(pkgUnitState(ctx))
}

// runInPkgUnit runs the package manager in its unit and waits for it. The
// outcome is that of running the command directly: its output in log, nil
// or an *execx.ExitError with its exit status. When ctx ends first (a
// canceled operation) the unit is stopped.
func runInPkgUnit(ctx context.Context, log io.Writer, name string, args []string) error {
	ended := func(cause error) error { return fmt.Errorf("execx: %s: %w", name, cause) }
	select {
	case pkgUnitSlot <- struct{}{}:
		defer func() { <-pkgUnitSlot }()
	case <-ctx.Done():
		return ended(ctx.Err())
	}
	if err := waitPkgUnitFree(ctx, log); err != nil {
		if ctx.Err() != nil {
			return ended(ctx.Err())
		}
		return err
	}

	job := pkgJob{ID: newHexID(""), Name: name, Args: args}
	if err := os.MkdirAll(pkgUnitDir, 0o700); err != nil {
		return err
	}
	prunePkgUnitDir()
	data, err := json.Marshal(job)
	if err != nil {
		return err
	}
	if err := writeFileAs(job.path(pkgJobSuffix), data, 0o600); err != nil {
		return err
	}
	// The output file exists before the unit does, so following it never
	// has to wait for the unit to make it.
	out, err := os.OpenFile(job.path(pkgOutputSuffix), os.O_RDONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	buf := make([]byte, 32<<10)
	follow := func() {
		for {
			n, err := out.Read(buf)
			if n > 0 {
				_, _ = log.Write(buf[:n])
			}
			if err != nil { // io.EOF: nothing more for now
				return
			}
		}
	}

	_, _ = fmt.Fprintf(log, "$ %s %s\n", name, strings.Join(args, " "))
	start := []string{"--quiet", "--collect", "--unit=" + pkgUnit, "--description=Cairn: package manager transaction",
		// A backstop: the command has its own time limit inside the unit.
		fmt.Sprintf("--property=RuntimeMaxSec=%d", int((pkgTimeout+pkgUnitStopWait)/time.Second)),
		"--", pkgUnitBinary(), "run-pkg", job.ID}
	// Not canceled half-way: afterwards the unit either runs this job or
	// was never started, and only a unit known to run this job is stopped.
	if _, err := pkgUnitExec(context.WithoutCancel(ctx), execx.Cmd{Name: "systemd-run", Args: start, Timeout: time.Minute}); err != nil {
		return err
	}

	limit := time.NewTimer(pkgTimeout + pkgUnitStopWait)
	defer limit.Stop()
	tick := time.NewTicker(pkgUnitPoll)
	defer tick.Stop()
	checked := time.Now()
	for {
		select {
		case <-ctx.Done():
			stopPkgUnit(log)
			follow()
			return ended(ctx.Err())
		case <-limit.C:
			stopPkgUnit(log)
			follow()
			return ended(context.DeadlineExceeded)
		case <-tick.C:
		}
		follow()
		res, ok := job.result()
		if !ok && time.Since(checked) >= pkgUnitCheck {
			checked = time.Now()
			if pkgUnitEnded(pkgUnitState(ctx)) {
				// The result is written before the unit ends: look once more.
				if res, ok = job.result(); !ok {
					follow()
					return fmt.Errorf("%s: %s ended without a result (journalctl -u %s has more)", name, pkgUnit, pkgUnit)
				}
			}
		}
		if !ok {
			continue
		}
		// The command's output is complete before its result appears. The
		// unit is gone a moment later; the next command starts after that.
		follow()
		waitPkgUnitEnd()
		switch {
		case res.TimedOut:
			return ended(context.DeadlineExceeded)
		case res.Error != "":
			return errors.New(res.Error)
		case res.ExitCode != 0:
			return &execx.ExitError{Name: name, ExitCode: res.ExitCode, Stderr: res.Stderr}
		}
		return nil
	}
}

// waitPkgUnitFree waits while a transaction runs in the unit that this
// process did not start: one left by an earlier helper process, or one of a
// command line run ("cairn-helper firewall setup").
func waitPkgUnitFree(ctx context.Context, log io.Writer) error {
	deadline := time.Now().Add(pkgTimeout + pkgUnitStopWait)
	for told := false; ; {
		state := pkgUnitState(ctx)
		if state == "failed" {
			// A failed unit that is still loaded would refuse the start.
			_, _ = pkgUnitExec(ctx, execx.Cmd{Name: "systemctl", Args: []string{"reset-failed", pkgUnit}, Timeout: 30 * time.Second})
		}
		if !pkgUnitRunning(state) {
			return nil
		}
		if time.Now().After(deadline) {
			return &helperapi.Error{Code: helperapi.CodePkgBusy}
		}
		if !told {
			told = true
			_, _ = fmt.Fprintf(log, "Waiting for the package manager transaction that is still running (%s).\n", pkgUnit)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pkgUnitCheck):
		}
	}
}

// waitPkgUnitEnd waits for the unit of a command that has ended: systemd
// takes a moment to notice, longer when it has to end what a maintainer
// script left running in the unit.
func waitPkgUnitEnd() {
	ctx, cancel := context.WithTimeout(context.Background(), pkgUnitStopWait)
	defer cancel()
	for !pkgUnitEnded(pkgUnitState(ctx)) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(pkgUnitPoll):
		}
	}
}

// stopPkgUnit stops the transaction this process started; systemd signals
// everything the unit runs.
func stopPkgUnit(log io.Writer) {
	ctx, cancel := context.WithTimeout(context.Background(), pkgUnitStopWait)
	defer cancel()
	_, err := pkgUnitExec(ctx, execx.Cmd{Name: "systemctl", Args: []string{"stop", pkgUnit}, Timeout: pkgUnitStopWait})
	// Stopping a unit that has just ended by itself fails, and is fine.
	if err != nil && !pkgUnitEnded(pkgUnitState(ctx)) {
		_, _ = fmt.Fprintf(log, "Warning: stopping %s failed: %v\n", pkgUnit, err)
	}
}

// prunePkgUnitDir removes the files of transactions long over.
func prunePkgUnitDir() {
	entries, err := os.ReadDir(pkgUnitDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > pkgUnitKeep {
			_ = os.Remove(filepath.Join(pkgUnitDir, e.Name()))
		}
	}
}

// RunPkgUnit is what cairn-pkg.service runs ("cairn-helper run-pkg <job>"):
// the package manager command the helper left as job id, outside the
// helper's sandbox, with the environment and the time limit it has when the
// helper runs it itself. The output goes to the job's file rather than to a
// pipe, so the command never writes to a helper that is gone; the result
// file tells the helper how it ended.
func RunPkgUnit(id string) error {
	if !pkgJobID.MatchString(id) {
		return errors.New("not a job name")
	}
	job := pkgJob{ID: id}
	if _, done := job.result(); done {
		return fmt.Errorf("job %s has ended already", id)
	}
	res := pkgResult{ID: id}
	err := job.run()
	var ee *execx.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		res.ExitCode, res.Stderr = ee.ExitCode, ee.Stderr
	case errors.Is(err, context.DeadlineExceeded):
		res.ExitCode, res.TimedOut = -1, true
	default:
		res.ExitCode, res.Error = -1, err.Error()
	}
	data, merr := json.Marshal(res)
	if merr != nil {
		return merr
	}
	if werr := writeFileAs(job.path(pkgResultSuffix), data, 0o600); werr != nil {
		return werr
	}
	return err
}

// run runs the command the helper wrote for the job.
func (j pkgJob) run() error {
	// A job runs once. Taking its file keeps a restart of the unit (by
	// hand, or by a tool that restarts services after an upgrade) from
	// running the command a second time; the helper is told instead.
	if err := os.Rename(j.path(pkgJobSuffix), j.path(pkgTakenSuffix)); err != nil {
		if errors.Is(err, fs.ErrNotExist) && fileExists(j.path(pkgTakenSuffix)) {
			return fmt.Errorf("%s was restarted while the package manager ran; the command was not run again", pkgUnit)
		}
		return err
	}
	data, err := os.ReadFile(j.path(pkgTakenSuffix))
	if err != nil {
		return err
	}
	var job pkgJob
	if err := json.Unmarshal(data, &job); err != nil {
		return err
	}
	if job.ID != j.ID || !pkgManagers[job.Name] {
		return fmt.Errorf("job %s does not name a package manager", j.ID)
	}
	out, err := os.OpenFile(j.path(pkgOutputSuffix), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	_, err = pkgJobExec(context.Background(), execx.Cmd{
		Name: job.Name, Args: job.Args, Timeout: pkgTimeout, Output: &cappedLog{w: out, limit: maxOpLog}, MaxOutput: 64 << 10,
		Env: pkgEnv,
	})
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}
