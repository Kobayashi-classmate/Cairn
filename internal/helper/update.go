// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/cloud"
	"github.com/Kobayashi-classmate/Cairn/internal/config"
	"github.com/Kobayashi-classmate/Cairn/internal/execx"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/semver"
)

// Update limits.
const (
	maxPackageSize = 300 << 20
	installTimeout = 15 * time.Minute
	logTailBytes   = 8 << 10
	// serviceTimeout bounds one systemctl call; stopping waits for
	// systemd's 90 seconds between SIGTERM and SIGKILL.
	serviceTimeout = 2 * time.Minute
	// updateRunLimit is the longest a run can take: two installations
	// (the update and a rollback) of two commands each, two health checks
	// and the services stopped and started around them, plus copying the
	// database twice. Its unit is stopped after that (RuntimeMaxSec).
	updateRunLimit = 4*installTimeout + 2*healthTimeout + 6*serviceTimeout + 5*time.Minute
	// staleJobAfter is how long a running job counts as running while
	// systemd cannot say whether its unit still exists.
	staleJobAfter = updateRunLimit
)

// Job states besides those of helperapi.UpdateJob's comment.
const (
	jobRunning     = "running"
	jobInterrupted = "interrupted"
)

// updateUnitPrefix names the transient units of update runs.
const updateUnitPrefix = "cairn-update-"

var packageName = regexp.MustCompile(`^[A-Za-z0-9._+~-]+\.(deb|rpm)$`)

// Updater updates the cairn package itself, and nothing else on the system.
type Updater struct {
	ConfigPath string
	StateDir   string // /var/lib/cairn-helper
	Version    string // running version
	Format     string // deb | rpm, from the platform profile
	Arch       string // Go architecture
	Executable string // path of cairn-helper, for the detached runner
	HTTP       *http.Client
	Log        *slog.Logger
	// Detach starts the runner outside the helper's own unit. It defaults
	// to systemd-run when systemd is running, otherwise an in-process
	// goroutine (containers and tests).
	Detach func(file, version, previous string) error
	// Ops, when set, coordinates with package operations: an update does not
	// start while one holds the package manager lock.
	Ops *Ops

	mu sync.Mutex
	// inProcess is set while a run goes on in this process (no systemd).
	inProcess atomic.Bool
}

// Running reports whether an update is in progress. Package operations
// wait while it is.
func (u *Updater) Running() bool {
	return u.Status().State == jobRunning
}

func (u *Updater) statusPath() string  { return filepath.Join(u.StateDir, "update-status.json") }
func (u *Updater) packagesDir() string { return filepath.Join(u.StateDir, "updates") }

func apiErr(code, reason string) error {
	var params map[string]any
	if reason != "" {
		params = map[string]any{"reason": reason}
	}
	return &helperapi.Error{Code: code, Params: params}
}

// Status returns the last job, or an idle job. A job still marked running
// whose run has ended without saying how (the machine restarted, its unit
// was killed) becomes interrupted: otherwise it would block every later
// update and package operation for good.
func (u *Updater) Status() helperapi.UpdateJob {
	if !u.mu.TryLock() {
		// Start is preparing a run: the job it writes is not settled yet.
		return readJob(u.statusPath())
	}
	defer u.mu.Unlock()
	return u.settle()
}

// Recover settles what the previous helper process left, when it starts.
func (u *Updater) Recover() { u.Status() }

// settle returns the last job, marking a run that no longer exists as
// interrupted. Callers hold u.mu.
func (u *Updater) settle() helperapi.UpdateJob {
	job := readJob(u.statusPath())
	if job.State != jobRunning || u.runAlive(job) {
		return job
	}
	// A run writes its outcome before it ends: read again now that it has.
	now := readJob(u.statusPath())
	if now.State != jobRunning || now.StartedAt != job.StartedAt {
		return now
	}
	now.State, now.FinishedAt = jobInterrupted, time.Now().Unix()
	now.Error = interruptedHint(u.Format)
	if err := writeJob(u.statusPath(), now); err != nil {
		u.Log.Warn("update: recording an interrupted update failed", "err", err)
	}
	u.Log.Warn("update interrupted: its run ended without finishing", "version", now.Version)
	return now
}

// interruptedHint is the error of an interrupted job: the package manager
// may have stopped half-way through.
func interruptedHint(format string) string {
	check := "dpkg --audit, then dpkg --configure -a"
	if format == "rpm" {
		check = "rpm -q cairn and dnf check"
	}
	return "the update was interrupted before it finished (for example by a restart of the server); " +
		"check the package manager on the server (" + check + ") and that the panel runs, then try again"
}

// runAlive reports whether the run of job still goes on.
func (u *Updater) runAlive(job helperapi.UpdateJob) bool {
	if u.inProcess.Load() {
		return true
	}
	if !systemdRunning() {
		// Runs go on in the helper's own process here, and this one has none.
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Any update unit counts, whichever helper process started it: an
	// older version's runner has its own unit name and no record of it.
	out, err := queryCommand(ctx, "systemctl", "list-units", "--type=service", "--all", "--plain", "--no-legend", updateUnitPrefix+"*")
	if err != nil {
		// systemd did not answer (it may be restarting itself): trust the
		// job for as long as a run can last.
		return time.Since(time.Unix(job.StartedAt, 0)) < staleJobAfter
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		for i := range f {
			// UNIT LOAD ACTIVE SUB DESCRIPTION
			if strings.HasPrefix(f[i], updateUnitPrefix) && i+2 < len(f) && pkgUnitRunning(f[i+2]) {
				return true
			}
		}
	}
	return false
}

func readJob(p string) helperapi.UpdateJob {
	var job helperapi.UpdateJob
	data, err := os.ReadFile(p) //nolint:gosec // G304: fixed path inside the helper state directory.
	if err != nil || json.Unmarshal(data, &job) != nil {
		return helperapi.UpdateJob{State: "idle"}
	}
	return job
}

func writeJob(p string, job helperapi.UpdateJob) error {
	data, err := json.Marshal(job)
	if err != nil {
		return err
	}
	return writeFileAs(p, data, 0o600)
}

// Start verifies the requested version against the signed release index,
// downloads and checks its package, and launches the detached runner.
func (u *Updater) Start(ctx context.Context, version string) (helperapi.UpdateJob, error) {
	u.mu.Lock()
	defer u.mu.Unlock()

	if job := u.settle(); job.State == jobRunning {
		return job, apiErr(helperapi.CodeUpdateInProgress, "")
	}
	if u.Ops != nil {
		release, ok := u.Ops.TryLock(LockPkg)
		if !ok {
			return helperapi.UpdateJob{}, apiErr(helperapi.CodePkgBusy, "another package operation is running")
		}
		// Held while preparing; the detached run is covered by Running.
		defer release()
	}
	current, err := semver.Parse(u.Version)
	if err != nil {
		return helperapi.UpdateJob{}, apiErr(helperapi.CodeUpdateNotAvailable, "development builds cannot be updated")
	}
	if u.Format == "" {
		return helperapi.UpdateJob{}, apiErr(helperapi.CodeUpdateNotAvailable, "unsupported platform")
	}
	cfg, err := config.Load(u.ConfigPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return helperapi.UpdateJob{}, err
	}
	keys, err := cloud.NewKeyring(cfg.Cloud.TrustedKeys)
	if err != nil {
		return helperapi.UpdateJob{}, apiErr(helperapi.CodeUpdateVerification, err.Error())
	}
	client := &cloud.Client{BaseURL: cfg.Cloud.BaseURL, Keys: keys, HTTP: u.HTTP}
	idx, err := client.FetchReleases(ctx, 0)
	switch {
	case errors.Is(err, cloud.ErrNoTrustedKey), errors.Is(err, cloud.ErrSignature):
		return helperapi.UpdateJob{}, apiErr(helperapi.CodeUpdateVerification, err.Error())
	case err != nil:
		return helperapi.UpdateJob{}, apiErr(helperapi.CodeUpdateDownload, err.Error())
	}

	rel := idx.Find(version)
	if rel == nil {
		return helperapi.UpdateJob{}, apiErr(helperapi.CodeUpdateNotAvailable, "version not in the release index")
	}
	target, err := semver.Parse(rel.Version)
	if err != nil || semver.Compare(target, current) <= 0 {
		return helperapi.UpdateJob{}, apiErr(helperapi.CodeUpdateNotAvailable, "not newer than the running version")
	}
	if rel.Channel != "stable" && (rel.Channel != "beta" || cfg.Cloud.Channel != "beta") {
		return helperapi.UpdateJob{}, apiErr(helperapi.CodeUpdateNotAvailable, "release is not on the configured channel")
	}
	pkg := rel.Package(u.Format, u.Arch)
	if pkg == nil {
		return helperapi.UpdateJob{}, apiErr(helperapi.CodeUpdateNotAvailable, "no package for this system")
	}

	file, err := u.download(ctx, pkg)
	if err != nil {
		return helperapi.UpdateJob{}, err
	}
	previous := u.currentPackage()
	job := helperapi.UpdateJob{State: jobRunning, Version: rel.Version, StartedAt: time.Now().Unix()}
	if err := writeJob(u.statusPath(), job); err != nil {
		return job, err
	}
	detach := u.Detach
	if detach == nil {
		detach = u.defaultDetach
	}
	if err := detach(file, rel.Version, previous); err != nil {
		job.State, job.Error, job.FinishedAt = "failed", "could not start the update: "+err.Error(), time.Now().Unix()
		_ = writeJob(u.statusPath(), job)
		return job, err
	}
	u.Log.Info("update started", "version", rel.Version, "package", filepath.Base(file))
	return job, nil
}

// download fetches pkg into the packages directory and checks its SHA-256
// against the signed index while streaming.
func (u *Updater) download(ctx context.Context, pkg *cloud.Package) (string, error) {
	pu, err := url.Parse(pkg.URL)
	if err != nil {
		return "", apiErr(helperapi.CodeUpdateDownload, "bad package URL")
	}
	name := path.Base(pu.Path)
	if !packageName.MatchString(name) {
		return "", apiErr(helperapi.CodeUpdateDownload, "unexpected package file name")
	}
	if err := os.MkdirAll(u.packagesDir(), 0o700); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pkg.URL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Cairn")
	hc := u.HTTP
	if hc == nil {
		hc = cloud.NewHTTPClient(10 * time.Minute)
	}
	res, err := hc.Do(req)
	if err != nil {
		return "", apiErr(helperapi.CodeUpdateDownload, err.Error())
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", apiErr(helperapi.CodeUpdateDownload, fmt.Sprintf("HTTP %d", res.StatusCode))
	}
	final := filepath.Join(u.packagesDir(), name)
	tmp, err := os.CreateTemp(u.packagesDir(), ".download-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no-op after the rename
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(res.Body, maxPackageSize+1))
	if err == nil {
		// Kept to go back to later: on the disk before it has its name.
		err = fsync(tmp)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", apiErr(helperapi.CodeUpdateDownload, err.Error())
	}
	if n > maxPackageSize {
		return "", apiErr(helperapi.CodeUpdateDownload, "package too large")
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, pkg.SHA256) {
		return "", apiErr(helperapi.CodeUpdateVerification, "package checksum does not match the signed index")
	}
	if err := os.Rename(tmp.Name(), final); err != nil {
		return "", err
	}
	return final, nil
}

// currentPackage returns the package file of the running version, if known
// (recorded by the installer and by every successful update).
func (u *Updater) currentPackage() string {
	data, err := os.ReadFile(filepath.Join(u.packagesDir(), "current")) //nolint:gosec // G304: fixed path.
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(string(data))
	if !packageName.MatchString(name) {
		return ""
	}
	p := filepath.Join(u.packagesDir(), name)
	// name matched packageName above, so it cannot contain a path separator.
	if _, err := os.Stat(p); err != nil { //nolint:gosec // G703: see above.
		return ""
	}
	return p
}

func (u *Updater) defaultDetach(file, version, previous string) error {
	run := UpdateRun{StateDir: u.StateDir, ConfigPath: u.ConfigPath, File: file, Version: version,
		Previous: previous, From: u.Version, Log: u.Log}
	if !systemdRunning() {
		// No systemd (containers, tests): run in-process.
		u.inProcess.Store(true)
		go func() {
			defer u.inProcess.Store(false)
			if err := RunUpdate(run); err != nil {
				u.Log.Error("update failed", "err", err)
			}
		}()
		return nil
	}
	// A transient unit of its own: the package's maintainer scripts restart
	// cairn-helper, and the update must survive that. The run stops
	// cairn-web while it installs; whatever ends the unit, killed or
	// timed out, cairn-web is started again after it (an update is
	// requested through cairn-web, so it was running).
	args := []string{
		"--unit", fmt.Sprintf("%s%d", updateUnitPrefix, time.Now().Unix()),
		"--description", "Cairn update to " + version,
		"--property", fmt.Sprintf("RuntimeMaxSec=%d", int(updateRunLimit/time.Second)),
		"--property", "ExecStopPost=/usr/bin/systemctl --no-block start " + panelUnit,
		"--collect", "--quiet",
		u.Executable, "run-update",
		"--state-dir", run.StateDir, "--config", run.ConfigPath, "--file", file, "--version", version, "--from", run.From,
	}
	if previous != "" {
		args = append(args, "--previous", previous)
	}
	_, err := execx.Run(context.Background(), execx.Cmd{Name: "systemd-run", Args: args, Timeout: time.Minute})
	return err
}

// UpdateRun is one run of an update.
type UpdateRun struct {
	StateDir   string // the helper's state directory
	ConfigPath string // the panel's configuration
	File       string // the verified package to install
	Version    string // its version
	Previous   string // package file of the running version, for a rollback ("" when unknown)
	From       string // the running version
	Log        *slog.Logger
}

// RunUpdate installs the new version and makes sure that it runs, or puts
// the previous version back. It runs in the detached unit ("cairn-helper
// run-update") or in-process without systemd.
//
// Under systemd the run owns cairn-web for its duration: it stops it, so
// that the database is at rest, copies the database, installs, starts
// cairn-web again and waits for both services of the new version to answer
// (checkHealth). When the new version does not answer, it stops cairn-web,
// installs the previous package, puts the database back (the new version
// may have migrated it; whatever was written meanwhile is lost) and checks
// that the previous version answers. Without systemd nothing manages the
// services: they are not restarted, so the new version is neither started
// nor checked here, and only the installation decides.
func RunUpdate(r UpdateRun) error {
	statusPath := filepath.Join(r.StateDir, "update-status.json")
	job := readJob(statusPath)
	job.State, job.Version, job.Error = jobRunning, r.Version, ""
	if job.StartedAt == 0 {
		job.StartedAt = time.Now().Unix()
	}
	_ = writeJob(statusPath, job)

	run := &updateRunner{UpdateRun: r, managed: systemdRunning(), backup: filepath.Join(r.StateDir, "updates", "database")}
	err := run.update()
	job.FinishedAt = time.Now().Unix()
	job.Log = tailString(run.out.String(), logTailBytes)
	if err == nil {
		job.State = "succeeded"
		_ = os.WriteFile(filepath.Join(r.StateDir, "updates", "current"), []byte(filepath.Base(r.File)+"\n"), 0o600)
		pruneUpdates(filepath.Join(r.StateDir, "updates"), r.File, r.Previous)
		r.Log.Info("update finished", "version", r.Version)
		return writeJob(statusPath, job)
	}
	job.State = "failed"
	job.Error = err.Error()
	r.Log.Error("update failed", "version", r.Version, "result", job.Error)
	_ = writeJob(statusPath, job)
	return err
}

// updateRunner carries one run's state.
type updateRunner struct {
	UpdateRun
	managed bool   // systemd runs the services: stop, start and check them
	backup  string // where the database is copied to
	out     strings.Builder
	cfg     config.Config
	// newRan is set once cairn-web was started on the new version, which
	// may have migrated the database.
	newRan bool
}

func (r *updateRunner) logf(format string, args ...any) {
	fmt.Fprintf(&r.out, "== "+format+"\n", args...)
}

// update runs the update and returns why it failed, the outcome of the
// rollback included; nil when the new version runs.
func (r *updateRunner) update() error {
	var err error
	if r.cfg, err = config.Load(r.ConfigPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("the configuration could not be read, nothing was installed: %w", err)
	}
	if !r.managed {
		r.logf("no systemd: the services are not restarted, so the new version is not started or checked, and the database is not copied")
	} else if err := r.prepare(); err != nil {
		return r.startAgain(fmt.Errorf("%w; nothing was installed", err))
	}
	var cause error
	// The installer's output, its errors included, is in the log.
	if installer(r.File, false, &r.out) != nil {
		cause = errors.New("installation failed")
	} else if r.managed {
		r.newRan = true
		err := r.startPanel()
		if err == nil {
			err = checkHealth(context.Background(), r.cfg, r.Version)
		}
		if err != nil {
			cause = fmt.Errorf("version %s was installed but does not run: %w", r.Version, err)
		}
	}
	if cause == nil {
		if r.managed {
			r.logf("version %s answers; the database from before the update stays in %s until the next update", r.Version, r.backup)
		}
		return nil
	}
	r.logf("%v", cause)
	return r.rollback(cause)
}

// prepare stops cairn-web and copies its database.
func (r *updateRunner) prepare() error {
	r.logf("stopping %s", panelUnit)
	if err := serviceCtl("stop", panelUnit); err != nil {
		return fmt.Errorf("the panel could not be stopped for the update: %w", err)
	}
	r.logf("copying the panel database to %s", r.backup)
	if err := backupDatabase(r.cfg.Paths.DataDir, r.backup, r.From); err != nil {
		return fmt.Errorf("the panel database could not be copied: %w", err)
	}
	return nil
}

// startPanel starts cairn-web, whatever state its unit was left in.
func (r *updateRunner) startPanel() error {
	if !r.managed {
		return nil
	}
	r.logf("starting %s", panelUnit)
	// After a crash loop systemd refuses to start it again for a while.
	_ = serviceCtl("reset-failed", panelUnit)
	if err := serviceCtl("start", panelUnit); err != nil {
		return fmt.Errorf("%s could not be started: %w", panelUnit, err)
	}
	return nil
}

// startAgain starts cairn-web after the run failed with outcome, which
// then also says when that did not work.
func (r *updateRunner) startAgain(outcome error) error {
	if err := r.startPanel(); err != nil {
		return fmt.Errorf("%w; %w", outcome, err)
	}
	return outcome
}

// rollback puts the previous version back after cause and returns the
// whole outcome. It claims a restored version only when it answers again.
func (r *updateRunner) rollback(cause error) error {
	if r.Previous == "" {
		// Without the previous program the database stays as the new
		// version left it: the copy alone would only be migrated again.
		if r.newRan {
			return r.startAgain(fmt.Errorf("%w; no previous package is available for a rollback (the database from before the update is in %s)", cause, r.backup))
		}
		return r.startAgain(fmt.Errorf("%w; no previous package is available for a rollback", cause))
	}
	if r.managed {
		r.logf("stopping %s for the rollback", panelUnit)
		if err := serviceCtl("stop", panelUnit); err != nil {
			return r.startAgain(fmt.Errorf("%w; restoring the previous version also failed: %s could not be stopped: %w", cause, panelUnit, err))
		}
	}
	r.logf("installing the previous package %s", filepath.Base(r.Previous))
	if installer(r.Previous, true, &r.out) != nil {
		return r.startAgain(fmt.Errorf("%w; restoring the previous version also failed: its installation failed", cause))
	}
	withDB := ""
	if r.newRan {
		r.logf("putting back the database from before the update (what was written since is lost)")
		if err := restoreDatabase(r.cfg.Paths.DataDir, r.backup); err != nil {
			return r.startAgain(fmt.Errorf("%w; restoring the previous version also failed: it was installed again, but its database could not be put back (the copy is in %s): %w",
				cause, r.backup, err))
		}
		withDB = ", with the database from before the update"
	}
	if r.managed {
		err := r.startPanel()
		if err == nil {
			err = checkHealth(context.Background(), r.cfg, r.From)
		}
		if err != nil {
			return fmt.Errorf("%w; restoring the previous version also failed: it was installed again%s, but does not run: %w", cause, withDB, err)
		}
	}
	r.logf("the previous version is back%s", withDB)
	return fmt.Errorf("%w; the previous version was restored%s", cause, withDB)
}

// serviceCtl runs systemctl on a unit of the panel.
func serviceCtl(action, unit string) error {
	ctx, cancel := context.WithTimeout(context.Background(), serviceTimeout)
	defer cancel()
	_, err := queryCommand(ctx, "systemctl", action, unit)
	return err
}

// install installs one package file. Only this package is touched; another
// package changes only if the new version needs a dependency that is missing.
// installer is replaceable in tests, which must never run dpkg or rpm.
var installer = install

func install(file string, downgrade bool, out *strings.Builder) error {
	run := func(name string, args ...string) error {
		res, err := execx.Run(context.Background(), execx.Cmd{
			Name: name, Args: args, Timeout: installTimeout, MaxOutput: 256 << 10,
			Env: []string{"DEBIAN_FRONTEND=noninteractive"},
		})
		fmt.Fprintf(out, "$ %s %s\n%s%s", name, strings.Join(args, " "), res.Stdout, res.Stderr)
		return err
	}
	if strings.HasSuffix(file, ".deb") {
		if run("dpkg", "-i", file) == nil {
			return nil
		}
		args := []string{"install", "-y", "--no-install-recommends"}
		if downgrade {
			args = append(args, "--allow-downgrades")
		}
		return run("apt-get", append(args, file)...)
	}
	args := []string{"-U"}
	if downgrade {
		// rpm often refuses an upgrade before changing anything, so the
		// version being restored may still be installed: --replacepkgs.
		args = append(args, "--oldpackage", "--replacepkgs")
	}
	if run("rpm", append(args, file)...) == nil {
		return nil
	}
	if downgrade {
		return run("dnf", "downgrade", "-y", file)
	}
	return run("dnf", "install", "-y", file)
}

// pruneUpdates keeps only the current and the previous package files.
func pruneUpdates(dir string, keep ...string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if e.Name() == "current" || !packageName.MatchString(e.Name()) {
			continue
		}
		kept := false
		for _, k := range keep {
			if k == p {
				kept = true
			}
		}
		if !kept {
			_ = os.Remove(p)
		}
	}
}

func tailString(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// RegisterUpdate adds the update actions.
func RegisterUpdate(r *Registry, u *Updater) {
	Register(r, helperapi.ActionUpdate, func(ctx context.Context, c helperapi.Caller, req helperapi.UpdateRequest) (helperapi.UpdateJob, error) {
		job, err := u.Start(ctx, strings.TrimSpace(req.Version))
		if err == nil {
			u.Log.Info("update requested", "version", req.Version, "user", c.Username)
		}
		return job, err
	})
	Register(r, helperapi.ActionUpdateStatus, func(context.Context, helperapi.Caller, helperapi.Empty) (helperapi.UpdateJob, error) {
		return u.Status(), nil
	})
}
