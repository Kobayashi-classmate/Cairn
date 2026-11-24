// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/execx"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

func init() {
	// No test starts the package manager's unit, or a package manager in
	// it, by accident, whatever the host runs on: the tests of the unit
	// bring their own systemd (fakePkgUnit).
	refuse := func(_ context.Context, c execx.Cmd) (execx.Result, error) {
		return execx.Result{}, fmt.Errorf("a test tried to run %s", c.Name)
	}
	pkgUnitExec, pkgJobExec = refuse, refuse
}

// stubPackageManager sends the package manager to the test's stand-in for
// runCommand, whether or not the host runs systemd.
func stubPackageManager(t *testing.T) {
	t.Helper()
	old := runPackageManager
	t.Cleanup(func() { runPackageManager = old })
	runPackageManager = func(ctx context.Context, log io.Writer, name string, args ...string) error {
		return runCommand(ctx, log, name, args...)
	}
}

// fakePkgUnit stands in for systemd and for the command inside the unit:
// its "systemd-run" runs RunPkgUnit the way cairn-pkg.service runs
// "cairn-helper run-pkg".
type fakePkgUnit struct {
	t  *testing.T
	mu sync.Mutex
	// state is what "systemctl is-active" answers.
	state string
	// starts are the systemd-run command lines, ctl the other systemctl
	// calls, jobs the commands run inside the unit.
	starts [][]string
	ctl    []string
	jobs   []execx.Cmd
	// running counts the commands inside the unit right now.
	running, maxRunning int

	// What the command does: its output, how it ends, and what it waits
	// for before ending (a stop of the unit ends it too).
	output string
	exit   error
	hold   chan struct{}
	// startErr fails systemd-run; noResult ends the unit without running
	// the job (a unit killed before it wrote its result).
	startErr error
	noResult bool

	stop  chan struct{} // closed by "systemctl stop"
	ended chan struct{} // closed when the unit is gone
	units sync.WaitGroup
}

func (f *fakePkgUnit) use(t *testing.T) {
	t.Helper()
	f.t = t
	if f.state == "" {
		f.state = "inactive"
	}
	oldDir, oldExec, oldJob, oldBin := pkgUnitDir, pkgUnitExec, pkgJobExec, pkgUnitBinary
	oldPoll, oldCheck, oldSystemd := pkgUnitPoll, pkgUnitCheck, systemdRunning
	t.Cleanup(func() {
		f.units.Wait()
		pkgUnitDir, pkgUnitExec, pkgJobExec, pkgUnitBinary = oldDir, oldExec, oldJob, oldBin
		pkgUnitPoll, pkgUnitCheck, systemdRunning = oldPoll, oldCheck, oldSystemd
	})
	pkgUnitDir = filepath.Join(t.TempDir(), "pkg")
	pkgUnitExec, pkgJobExec = f.systemd, f.command
	pkgUnitBinary = func() string { return "/usr/lib/cairn/cairn-helper" }
	pkgUnitPoll, pkgUnitCheck = 2*time.Millisecond, 10*time.Millisecond
	systemdRunning = func() bool { return true }
}

func (f *fakePkgUnit) set(state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state = state
}

func (f *fakePkgUnit) snapshot() (starts [][]string, ctl []string, jobs []execx.Cmd) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.starts), slices.Clone(f.ctl), slices.Clone(f.jobs)
}

func (f *fakePkgUnit) systemd(_ context.Context, c execx.Cmd) (execx.Result, error) {
	f.mu.Lock()
	switch {
	case c.Name == "systemctl" && len(c.Args) == 2 && c.Args[0] == "is-active" && c.Args[1] == pkgUnit:
		defer f.mu.Unlock()
		if f.state != "active" {
			// As systemctl does: the state on standard output, status 3.
			return execx.Result{Stdout: []byte(f.state + "\n"), ExitCode: 3}, &execx.ExitError{Name: c.Name, ExitCode: 3}
		}
		return execx.Result{Stdout: []byte("active\n")}, nil
	case c.Name == "systemctl" && len(c.Args) == 2 && c.Args[0] == "reset-failed" && c.Args[1] == pkgUnit:
		defer f.mu.Unlock()
		f.ctl = append(f.ctl, strings.Join(c.Args, " "))
		if f.state == "failed" {
			f.state = "inactive"
		}
		return execx.Result{}, nil
	case c.Name == "systemctl" && len(c.Args) == 2 && c.Args[0] == "stop" && c.Args[1] == pkgUnit:
		f.ctl = append(f.ctl, strings.Join(c.Args, " "))
		stop, ended := f.stop, f.ended
		f.stop = nil
		f.mu.Unlock()
		if stop == nil {
			return execx.Result{ExitCode: 5}, &execx.ExitError{Name: c.Name, ExitCode: 5, Stderr: "Unit cairn-pkg.service not loaded."}
		}
		// systemctl stop returns when the unit is gone.
		close(stop)
		<-ended
		return execx.Result{}, nil
	case c.Name == "systemd-run":
		defer f.mu.Unlock()
		f.starts = append(f.starts, slices.Clone(c.Args))
		if f.startErr != nil {
			return execx.Result{ExitCode: 1}, f.startErr
		}
		if f.state != "inactive" {
			// What systemd does for a unit name that is taken. The helper
			// must not get here: it waits for the unit first.
			f.t.Errorf("the unit was started while it was %s", f.state)
			return execx.Result{ExitCode: 1}, &execx.ExitError{Name: c.Name, ExitCode: 1,
				Stderr: "Failed to start transient service unit: Unit cairn-pkg.service already exists."}
		}
		f.state = "active"
		stop, ended := make(chan struct{}), make(chan struct{})
		f.stop, f.ended = stop, ended
		id, noResult := c.Args[len(c.Args)-1], f.noResult
		f.units.Add(1)
		go func() {
			defer f.units.Done()
			if !noResult {
				_ = RunPkgUnit(id)
			}
			f.mu.Lock()
			f.state = "inactive"
			if f.stop == stop {
				f.stop = nil
			}
			f.mu.Unlock()
			close(ended)
		}()
		return execx.Result{}, nil
	}
	f.mu.Unlock()
	f.t.Errorf("unexpected command: %s %s", c.Name, strings.Join(c.Args, " "))
	return execx.Result{}, errors.New("unexpected command")
}

func (f *fakePkgUnit) command(_ context.Context, c execx.Cmd) (execx.Result, error) {
	f.mu.Lock()
	f.jobs = append(f.jobs, c)
	f.running++
	f.maxRunning = max(f.maxRunning, f.running)
	output, exit, hold, stop := f.output, f.exit, f.hold, f.stop
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.running--
		f.mu.Unlock()
	}()
	_, _ = io.WriteString(c.Output, output)
	if hold != nil {
		select {
		case <-hold:
		case <-stop:
			return execx.Result{ExitCode: -1}, &execx.ExitError{Name: c.Name, ExitCode: -1, Stderr: "terminated"}
		}
	}
	return execx.Result{}, exit
}

// lockedBuffer is a log read while the operation still writes to it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestPackageManagerRunsInItsOwnUnit(t *testing.T) {
	f := &fakePkgUnit{output: "Reading package lists...\nSetting up linux-image-6.12.63+deb13-amd64 (6.12.63-1) ...\n"}
	f.use(t)
	oldRun := runCommand
	t.Cleanup(func() { runCommand = oldRun })
	runCommand = func(_ context.Context, _ io.Writer, name string, _ ...string) error {
		t.Errorf("%s ran in the helper's own unit", name)
		return nil
	}
	// Whatever the arguments look like, none of them is systemd-run's.
	args := []string{"install", "-y", "--only-upgrade", "-o", "Dpkg::Options::=--force-confold",
		"linux-image-amd64", "nginx=1.26.3-3+deb13u1", "${HOME}", "--property=ProtectSystem=strict", "--", "a b"}
	var log bytes.Buffer
	if err := runPackageManager(t.Context(), &log, "apt-get", args...); err != nil {
		t.Fatal(err)
	}
	starts, ctl, jobs := f.snapshot()
	if len(starts) != 1 || len(jobs) != 1 {
		t.Fatalf("starts %v, commands %v", starts, jobs)
	}
	// The command line of systemd-run: constants and the job's name. No
	// sandbox, nothing that ties the unit to the helper (--wait, --pipe),
	// nothing a request carries.
	start := starts[0]
	id := start[len(start)-1]
	want := []string{"--quiet", "--collect", "--unit=cairn-pkg.service", "--description=Cairn: package manager transaction",
		"--property=RuntimeMaxSec=" + strconv.Itoa(int((pkgTimeout+pkgUnitStopWait)/time.Second)),
		"--", "/usr/lib/cairn/cairn-helper", "run-pkg", id}
	if !slices.Equal(start, want) || !regexp.MustCompile(`^[0-9a-f]{15}$`).MatchString(id) {
		t.Fatalf("systemd-run %q\nwant        %q", start, want)
	}
	// Inside the unit: the command as given, with the environment and the
	// time limit it has when the helper runs it itself.
	job := jobs[0]
	if job.Name != "apt-get" || !slices.Equal(job.Args, args) || !slices.Equal(job.Env, []string{"DEBIAN_FRONTEND=noninteractive"}) ||
		job.Timeout != pkgTimeout || job.Stdin != nil {
		t.Fatalf("command in the unit: %+v", job)
	}
	if got := log.String(); got != "$ apt-get "+strings.Join(args, " ")+"\n"+f.output {
		t.Fatalf("log:\n%s", got)
	}
	if len(ctl) != 0 {
		t.Fatalf("systemctl %v", ctl)
	}
	// The files the two share are root's alone.
	if info, err := os.Stat(pkgUnitDir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("directory: %v %v", info, err)
	}
	for _, suffix := range []string{pkgTakenSuffix, pkgOutputSuffix, pkgResultSuffix} {
		if info, err := os.Stat(filepath.Join(pkgUnitDir, id+suffix)); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v", suffix, info, err)
		}
	}
}

func TestPackageManagerUnitReportsFailures(t *testing.T) {
	f := &fakePkgUnit{output: "dpkg: error processing archive (--unpack)\n",
		exit: &execx.ExitError{Name: "apt-get", ExitCode: 100, Stderr: "E: Sub-process /usr/bin/dpkg returned an error code (1)"}}
	f.use(t)
	var log bytes.Buffer
	err := runPackageManager(t.Context(), &log, "apt-get", "install", "-y", "linux-image-amd64")
	var ee *execx.ExitError
	if !errors.As(err, &ee) || ee.Name != "apt-get" || ee.ExitCode != 100 || !strings.Contains(ee.Stderr, "returned an error code") {
		t.Fatalf("exit status lost: %v", err)
	}
	if !strings.Contains(log.String(), "dpkg: error processing archive") {
		t.Fatalf("log:\n%s", log.String())
	}

	// The command's own time limit, inside the unit.
	f.mu.Lock()
	f.exit = fmt.Errorf("execx: dnf: %w", context.DeadlineExceeded)
	f.mu.Unlock()
	if err := runPackageManager(t.Context(), io.Discard, "dnf", "upgrade", "-y"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("time limit: %v", err)
	}
	// A command that could not start.
	f.mu.Lock()
	f.exit = fmt.Errorf("%w: dnf", execx.ErrNotFound)
	f.mu.Unlock()
	if err := runPackageManager(t.Context(), io.Discard, "dnf", "upgrade", "-y"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing command: %v", err)
	}

	// A unit that ends without a result (killed, out of memory) is a
	// failure, never a success.
	f.mu.Lock()
	f.exit, f.noResult = nil, true
	f.mu.Unlock()
	if err := runPackageManager(t.Context(), io.Discard, "dnf", "upgrade", "-y"); err == nil || !strings.Contains(err.Error(), "ended without a result") {
		t.Fatalf("unit without a result: %v", err)
	}

	// systemd-run fails: reported as it is, and no unit is stopped (one
	// that exists then is not this process's).
	f.mu.Lock()
	f.noResult, f.ctl = false, nil
	f.startErr = &execx.ExitError{Name: "systemd-run", ExitCode: 1, Stderr: "Failed to start transient service unit: Unit cairn-pkg.service already exists."}
	f.mu.Unlock()
	err = runPackageManager(t.Context(), io.Discard, "dnf", "upgrade", "-y")
	if !errors.As(err, &ee) || ee.Name != "systemd-run" {
		t.Fatalf("failed start: %v", err)
	}
	if _, ctl, _ := f.snapshot(); len(ctl) != 0 {
		t.Fatalf("systemctl %v", ctl)
	}
}

func TestPackageManagerUnitOneAtATime(t *testing.T) {
	f := &fakePkgUnit{hold: make(chan struct{})}
	f.use(t)
	errs := make(chan error, 3)
	for _, p := range []string{"a", "b", "c"} {
		go func() { errs <- runPackageManager(context.Background(), io.Discard, "apt-get", "install", "-y", p) }()
	}
	waitFor(t, "the first transaction", func() bool { _, _, jobs := f.snapshot(); return len(jobs) == 1 })
	time.Sleep(50 * time.Millisecond)
	if starts, _, _ := f.snapshot(); len(starts) != 1 {
		t.Fatalf("%d units started while the first ran", len(starts))
	}
	if !PkgUnitBusy() {
		t.Fatal("not busy while a transaction runs")
	}
	close(f.hold)
	for range 3 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	starts, _, jobs := f.snapshot()
	f.mu.Lock()
	atOnce := f.maxRunning
	f.mu.Unlock()
	if len(starts) != 3 || len(jobs) != 3 || atOnce != 1 {
		t.Fatalf("starts %d, commands %d, at once %d", len(starts), len(jobs), atOnce)
	}
	if PkgUnitBusy() {
		t.Fatal("busy after the last transaction")
	}
}

// A transaction outlives the helper process that started it: the next
// process waits for the unit instead of starting a second package manager.
func TestPackageManagerUnitWaitsForARunningTransaction(t *testing.T) {
	f := &fakePkgUnit{state: "active", output: "done\n"}
	f.use(t)
	if !PkgUnitBusy() {
		t.Fatal("a running unit is not busy")
	}
	var log lockedBuffer
	done := make(chan error, 1)
	go func() { done <- runPackageManager(context.Background(), &log, "dnf", "makecache", "--assumeyes") }()
	waitFor(t, "the notice", func() bool { return strings.Contains(log.String(), "Waiting for the package manager transaction") })
	if starts, _, _ := f.snapshot(); len(starts) != 0 {
		t.Fatal("started a unit while another ran")
	}
	f.set("deactivating")
	time.Sleep(30 * time.Millisecond)
	if starts, _, _ := f.snapshot(); len(starts) != 0 {
		t.Fatal("started a unit while the other was ending")
	}
	f.set("inactive")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if starts, _, _ := f.snapshot(); len(starts) != 1 || !strings.HasSuffix(log.String(), "$ dnf makecache --assumeyes\ndone\n") {
		t.Fatalf("starts %d, log:\n%s", len(starts), log.String())
	}

	// A canceled operation stops waiting, and stops nothing: the unit is
	// not its own.
	f.set("active")
	ctx, cancel := context.WithCancel(context.Background())
	go func() { done <- runPackageManager(ctx, io.Discard, "dnf", "makecache", "--assumeyes") }()
	time.Sleep(30 * time.Millisecond)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled while waiting: %v", err)
	}
	if starts, ctl, _ := f.snapshot(); len(starts) != 1 || len(ctl) != 0 {
		t.Fatalf("starts %d, systemctl %v", len(starts), ctl)
	}

	// A failed unit that is still loaded would refuse the next start.
	f.set("failed")
	if err := runPackageManager(context.Background(), io.Discard, "dnf", "makecache", "--assumeyes"); err != nil {
		t.Fatal(err)
	}
	if _, ctl, _ := f.snapshot(); !slices.Equal(ctl, []string{"reset-failed cairn-pkg.service"}) {
		t.Fatalf("systemctl %v", ctl)
	}

	// Without an answer from systemd nothing is known to run.
	f.set("")
	if PkgUnitBusy() {
		t.Fatal("busy without an answer")
	}
}

func TestCanceledOperationStopsThePackageManagerUnit(t *testing.T) {
	f := &fakePkgUnit{hold: make(chan struct{}), output: "Get:1 http://deb.debian.org/debian trixie InRelease\n"}
	f.use(t)
	o, r := newTestOps(t)
	RegisterPkg(r, o, "apt")
	ref, err := call[helperapi.OpRef](t, r, helperapi.ActionPkgRefresh, helperapi.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the command", func() bool { _, _, jobs := f.snapshot(); return len(jobs) == 1 })
	// What the command printed so far is in the operation's log already.
	waitFor(t, "the output", func() bool {
		res, _ := o.Get(ref.ID, 0)
		return strings.Contains(res.Log, "$ apt-get update\nGet:1 http://deb.debian.org/debian trixie InRelease\n")
	})
	if _, err := o.Cancel(ref.ID); err != nil {
		t.Fatal(err)
	}
	waitOp(t, o, ref.ID, helperapi.OpCanceled)
	if _, ctl, _ := f.snapshot(); !slices.Equal(ctl, []string{"stop cairn-pkg.service"}) {
		t.Fatalf("systemctl %v", ctl)
	}
	if o.Held(LockPkg) || PkgUnitBusy() {
		t.Fatal("still busy after the cancel")
	}
}

// Containers have no systemd and the helper no sandbox there: the command
// runs as it always did.
func TestPackageManagerWithoutSystemd(t *testing.T) {
	oldRun, oldSystemd := runCommand, systemdRunning
	t.Cleanup(func() { runCommand, systemdRunning = oldRun, oldSystemd })
	systemdRunning = func() bool { return false }
	var got []string
	runCommand = func(_ context.Context, log io.Writer, name string, args ...string) error {
		got = append(got, name+" "+strings.Join(args, " "))
		_, _ = io.WriteString(log, "ok\n")
		return nil
	}
	var log bytes.Buffer
	// pkgUnitExec is the refusing default here: touching systemd fails.
	if err := runPackageManager(t.Context(), &log, "dnf", "upgrade", "-y", "--exclude=cairn"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"dnf upgrade -y --exclude=cairn"}) || log.String() != "ok\n" {
		t.Fatalf("ran %q, log %q", got, log.String())
	}
	if PkgUnitBusy() {
		t.Fatal("busy without systemd")
	}
	// Only the package managers, with or without systemd.
	for _, systemd := range []bool{false, true} {
		systemdRunning = func() bool { return systemd }
		if err := runPackageManager(t.Context(), io.Discard, "systemctl", "start", "x"); err == nil || len(got) != 1 {
			t.Fatalf("systemd %v: another command accepted (%v, ran %q)", systemd, err, got)
		}
	}
}

func TestRunPkgUnitRefusesOtherJobs(t *testing.T) {
	f := &fakePkgUnit{}
	f.use(t)
	if err := os.MkdirAll(pkgUnitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", "../../etc/x", "ABCDEF0123", "abc", strings.Repeat("a", 33)} {
		if err := RunPkgUnit(id); err == nil {
			t.Errorf("job name %q accepted", id)
		}
	}
	// No job of that name.
	const id = "0123456789abcde"
	if err := RunPkgUnit(id); err == nil {
		t.Fatal("a job that does not exist ran")
	}
	// A job that names something else than a package manager does not
	// run, and says so in its result.
	for _, job := range []string{
		`{"id":"0123456789abcde","name":"/bin/rm","args":["-rf","/"]}`,
		`{"id":"0123456789abcde","name":"systemctl","args":["poweroff"]}`,
		`{"id":"fedcba9876543210","name":"apt-get","args":["update"]}`,
		`not json`,
	} {
		for _, suffix := range []string{pkgTakenSuffix, pkgResultSuffix} {
			_ = os.Remove(filepath.Join(pkgUnitDir, id+suffix))
		}
		if err := os.WriteFile(filepath.Join(pkgUnitDir, id+pkgJobSuffix), []byte(job), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := RunPkgUnit(id); err == nil {
			t.Errorf("job %s ran", job)
		}
		if res, ok := (pkgJob{ID: id}).result(); !ok || res.Error == "" || res.ExitCode == 0 {
			t.Errorf("job %s: result %+v", job, res)
		}
	}
	if _, _, jobs := f.snapshot(); len(jobs) != 0 {
		t.Fatalf("commands ran: %+v", jobs)
	}
}

// Restarting the unit (systemctl restart, or a tool that restarts services
// after an upgrade) starts its command line again: the package manager
// must not run a second time.
func TestRunPkgUnitRunsAJobOnce(t *testing.T) {
	f := &fakePkgUnit{}
	f.use(t)
	if err := os.MkdirAll(pkgUnitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	const id = "0123456789abcde"
	job := pkgJob{ID: id}
	write := func() {
		t.Helper()
		if err := os.WriteFile(job.path(pkgJobSuffix), []byte(`{"id":"`+id+`","name":"apt-get","args":["install","-y","nginx"]}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Restarted after the command ended: the result stands.
	write()
	if err := RunPkgUnit(id); err != nil {
		t.Fatal(err)
	}
	if err := RunPkgUnit(id); err == nil {
		t.Fatal("a job that had ended ran again")
	}
	if res, ok := job.result(); !ok || res.ExitCode != 0 || res.Error != "" {
		t.Fatalf("result changed: %+v", res)
	}
	// Restarted while the command ran (the first run was killed and left
	// no result): the helper is told, and nothing runs.
	if err := os.Remove(job.path(pkgResultSuffix)); err != nil {
		t.Fatal(err)
	}
	if err := RunPkgUnit(id); err == nil {
		t.Fatal("a job that was started before ran again")
	}
	if res, ok := job.result(); !ok || !strings.Contains(res.Error, "was restarted while the package manager ran") {
		t.Fatalf("result: %+v", res)
	}
	if _, _, jobs := f.snapshot(); len(jobs) != 1 {
		t.Fatalf("the command ran %d times", len(jobs))
	}
}

func TestPackageManagerUnitFilesArePruned(t *testing.T) {
	f := &fakePkgUnit{}
	f.use(t)
	if err := os.MkdirAll(pkgUnitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	old, recent := filepath.Join(pkgUnitDir, "00000000000000a.log"), filepath.Join(pkgUnitDir, "00000000000000b.log")
	for p, age := range map[string]time.Duration{old: pkgUnitKeep + time.Hour, recent: time.Hour} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(-age)
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
	if err := runPackageManager(t.Context(), io.Discard, "apt-get", "update"); err != nil {
		t.Fatal(err)
	}
	// The recent one may be of a transaction another process started.
	if fileExists(old) || !fileExists(recent) {
		t.Fatalf("old kept: %v, recent kept: %v", fileExists(old), fileExists(recent))
	}
}

// The system update is where the sandbox did its damage: a kernel package
// or a package that rebuilds the initramfs. It reaches the package manager
// through the unit only.
func TestSystemUpdateRunsInThePackageManagerUnit(t *testing.T) {
	f := &fakePkgUnit{}
	f.use(t)
	oldRun, oldQuery := runCommand, queryCommand
	t.Cleanup(func() { runCommand, queryCommand = oldRun, oldQuery })
	runCommand = func(_ context.Context, _ io.Writer, name string, args ...string) error {
		t.Errorf("%s %s ran in the helper's own unit", name, strings.Join(args, " "))
		return nil
	}
	queryCommand = func(_ context.Context, name string, args ...string) (string, error) {
		if name == "apt-get" && slices.Equal(args, []string{"-s", "--with-new-pkgs", "upgrade"}) {
			return "Inst linux-image-amd64 [6.12.57-1] (6.12.63-1 Debian-Security:13/stable-security [amd64])\n" +
				"Inst linux-image-6.12.63+deb13-amd64 (6.12.63-1 Debian-Security:13/stable-security [amd64])\n" +
				"Inst udev [257.8-1~deb13u2] (257.9-1~deb13u1 Debian:13.2/stable [amd64])\n" +
				"Inst cairn [0.0.8] (0.0.9 cairn:stable [amd64])\n", nil
		}
		return "", nil
	}
	for _, c := range []struct {
		manager string
		req     helperapi.UpdatesInstallRequest
		want    string
	}{
		{"apt", helperapi.UpdatesInstallRequest{}, "apt-get install -y --only-upgrade -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold linux-image-amd64 udev"},
		{"apt", helperapi.UpdatesInstallRequest{SecurityOnly: true}, "apt-get install -y --only-upgrade -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold linux-image-amd64"},
		{"dnf", helperapi.UpdatesInstallRequest{}, "dnf upgrade -y --exclude=cairn"},
		{"dnf", helperapi.UpdatesInstallRequest{SecurityOnly: true}, "dnf upgrade -y --exclude=cairn --security"},
	} {
		f.mu.Lock()
		f.starts, f.jobs = nil, nil
		f.mu.Unlock()
		h := &Host{StateDir: t.TempDir(), Root: t.TempDir(), Software: &Software{Manager: c.manager, Log: slog.New(slog.DiscardHandler)}}
		if _, err := h.installUpdates(t.Context(), helperapi.Caller{}, c.req, io.Discard); err != nil {
			t.Fatalf("%s: %v", c.manager, err)
		}
		starts, _, jobs := f.snapshot()
		if len(starts) != 1 || len(jobs) != 1 || jobs[0].Name+" "+strings.Join(jobs[0].Args, " ") != c.want {
			t.Errorf("%s %+v: %d units, commands %+v", c.manager, c.req, len(starts), jobs)
		}
	}

	// A failed transaction fails the update.
	f.mu.Lock()
	f.exit = &execx.ExitError{Name: "dnf", ExitCode: 1, Stderr: "Error: Transaction test error"}
	f.mu.Unlock()
	h := &Host{StateDir: t.TempDir(), Root: t.TempDir(), Software: &Software{Manager: "dnf", Log: slog.New(slog.DiscardHandler)}}
	if _, err := h.installUpdates(t.Context(), helperapi.Caller{}, helperapi.UpdatesInstallRequest{}, io.Discard); err == nil {
		t.Fatal("a failed transaction reported as success")
	}
}

// The panel's own update does not start while a transaction that an
// earlier helper process started still runs.
func TestUpdateRefusedWhileThePackageManagerUnitRuns(t *testing.T) {
	f := &fakePkgUnit{state: "active"}
	f.use(t)
	o, _ := newTestOps(t)
	o.Busy = map[string]func() bool{LockPkg: PkgUnitBusy}
	u := &Updater{StateDir: t.TempDir(), Version: "0.1.0", Format: "deb", Arch: "amd64", Log: slog.New(slog.DiscardHandler), Ops: o}
	if _, err := u.Start(context.Background(), "0.2.0"); code(err) != helperapi.CodePkgBusy {
		t.Fatalf("update while the unit runs: %v", err)
	}
	if o.Held(LockPkg) {
		t.Fatal("lock taken")
	}
	f.set("inactive")
	if release, ok := o.TryLock(LockPkg); !ok {
		t.Fatal("lock refused after the unit ended")
	} else {
		release()
	}
}

// Every package manager command that changes the system goes through
// runPackageManager: runCommand would run it in the helper's sandbox.
func TestPackageManagerNeverRunsInTheHelperUnit(t *testing.T) {
	managers := map[string]bool{`"apt-get"`: true, `"apt"`: true, `"dnf"`: true, `"yum"`: true, `"dpkg"`: true, `"rpm"`: true}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, e.Name(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			fn, ok := call.Fun.(*ast.Ident)
			if !ok || (fn.Name != "runCommand" && fn.Name != "runPackageManager") {
				return true
			}
			for _, a := range call.Args {
				if lit, ok := a.(*ast.BasicLit); ok && lit.Kind == token.STRING && managers[lit.Value] {
					checked++
					if fn.Name == "runCommand" {
						t.Errorf("%s: runCommand runs %s in the helper's own unit", fset.Position(call.Pos()), lit.Value)
					}
				}
			}
			return true
		})
	}
	if checked == 0 {
		t.Fatal("found no package manager command at all")
	}
}
