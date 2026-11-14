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
	staleJobAfter  = 30 * time.Minute
	installTimeout = 15 * time.Minute
	logTailBytes   = 8 << 10
)

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

	mu sync.Mutex
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

// Status returns the last job, or an idle job.
func (u *Updater) Status() helperapi.UpdateJob {
	return readJob(u.statusPath())
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
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Start verifies the requested version against the signed release index,
// downloads and checks its package, and launches the detached runner.
func (u *Updater) Start(ctx context.Context, version string) (helperapi.UpdateJob, error) {
	u.mu.Lock()
	defer u.mu.Unlock()

	if job := u.Status(); job.State == "running" && time.Since(time.Unix(job.StartedAt, 0)) < staleJobAfter {
		return job, apiErr(helperapi.CodeUpdateInProgress, "")
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
	job := helperapi.UpdateJob{State: "running", Version: rel.Version, StartedAt: time.Now().Unix()}
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
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		// No systemd (containers, tests): run in-process.
		go func() {
			if err := RunUpdate(u.StateDir, file, version, previous, u.Log); err != nil {
				u.Log.Error("update failed", "err", err)
			}
		}()
		return nil
	}
	// A transient unit of its own: the package's maintainer scripts restart
	// cairn-helper and cairn-web, and the update must survive that.
	args := []string{
		"--unit", fmt.Sprintf("cairn-update-%d", time.Now().Unix()),
		"--description", "Cairn update to " + version,
		"--collect", "--quiet",
		u.Executable, "run-update",
		"--state-dir", u.StateDir, "--file", file, "--version", version,
	}
	if previous != "" {
		args = append(args, "--previous", previous)
	}
	_, err := execx.Run(context.Background(), execx.Cmd{Name: "systemd-run", Args: args, Timeout: time.Minute})
	return err
}

// RunUpdate installs file, rolling back to previous on failure. It runs in
// the detached unit ("cairn-helper run-update") or in-process without systemd.
func RunUpdate(stateDir, file, version, previous string, log *slog.Logger) error {
	statusPath := filepath.Join(stateDir, "update-status.json")
	job := readJob(statusPath)
	job.State, job.Version, job.Error = "running", version, ""
	if job.StartedAt == 0 {
		job.StartedAt = time.Now().Unix()
	}
	_ = writeJob(statusPath, job)

	var logBuf strings.Builder
	err := installer(file, false, &logBuf)
	job.FinishedAt = time.Now().Unix()
	if err == nil {
		job.State = "succeeded"
		job.Log = tailString(logBuf.String(), logTailBytes)
		_ = os.WriteFile(filepath.Join(stateDir, "updates", "current"), []byte(filepath.Base(file)+"\n"), 0o600)
		pruneUpdates(filepath.Join(stateDir, "updates"), file, previous)
		log.Info("update finished", "version", version)
		return writeJob(statusPath, job)
	}

	job.State = "failed"
	switch {
	case previous == "":
		job.Error = "installation failed; no previous package is available for a rollback"
	case installer(previous, true, &logBuf) == nil:
		job.Error = "installation failed; the previous version was restored"
	default:
		job.Error = "installation failed and restoring the previous version also failed"
	}
	job.Log = tailString(logBuf.String(), logTailBytes)
	log.Error("update failed", "version", version, "err", err, "result", job.Error)
	_ = writeJob(statusPath, job)
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
