// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/buildinfo"
	"github.com/Kobayashi-classmate/Cairn/internal/cloud"
	"github.com/Kobayashi-classmate/Cairn/internal/config"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

type updateEnv struct {
	u        *Updater
	cfgPath  string
	detached []string
	pkg      []byte
}

// newUpdateEnv serves a signed release index listing 0.2.0 (stable),
// 0.3.0-beta.1 (beta) and 0.1.0, plus the 0.2.0 package file.
func newUpdateEnv(t *testing.T, tamperPackage bool) *updateEnv {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	pkg := []byte("pretend this is cairn_0.2.0_amd64.deb")
	sum := sha256.Sum256(pkg)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idx := cloud.Releases{Type: "cairn.releases", GeneratedAt: 1, Releases: []cloud.Release{
			{Version: "0.1.0", Channel: "stable", Packages: []cloud.Package{{Format: "deb", Arch: "amd64", URL: srv.URL + "/pkg/cairn_0.1.0_amd64.deb", SHA256: hex.EncodeToString(sum[:])}}},
			{Version: "0.2.0", Channel: "stable", Packages: []cloud.Package{{Format: "deb", Arch: "amd64", URL: srv.URL + "/pkg/cairn_0.2.0_amd64.deb", SHA256: hex.EncodeToString(sum[:])}}},
			{Version: "0.3.0-beta.1", Channel: "beta", Packages: []cloud.Package{{Format: "deb", Arch: "amd64", URL: srv.URL + "/pkg/cairn_0.3.0_amd64.deb", SHA256: hex.EncodeToString(sum[:])}}},
		}}
		body, _ := json.Marshal(idx)
		switch r.URL.Path {
		case "/feed/releases.json":
			_, _ = w.Write(body)
		case "/feed/releases.json.sig":
			_, _ = w.Write(cloud.Sign(body, priv))
		default:
			if !strings.HasPrefix(r.URL.Path, "/pkg/") {
				http.NotFound(w, r)
				return
			}
			if tamperPackage {
				_, _ = w.Write([]byte("evil"))
				return
			}
			_, _ = w.Write(pkg)
		}
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	cfg := config.Default()
	cfg.Cloud.BaseURL = srv.URL + "/feed"
	cfg.Cloud.TrustedKeys = []string{base64.StdEncoding.EncodeToString(pub)}
	cfgPath := filepath.Join(dir, "config.toml")
	if err := config.Save(cfgPath, cfg, 0o640, -1, -1); err != nil {
		t.Fatal(err)
	}
	env := &updateEnv{cfgPath: cfgPath, pkg: pkg}
	env.u = &Updater{
		ConfigPath: cfgPath, StateDir: filepath.Join(dir, "state"), Version: "0.1.0", Format: "deb", Arch: "amd64",
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Detach: func(file, version, previous string) error {
			env.detached = append(env.detached, file+"|"+version+"|"+previous)
			return nil
		},
	}
	if err := os.MkdirAll(env.u.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// What Detach started goes on: systemd lists its unit while the test
	// runs (fakeUnits changes that).
	oldSystemd, oldQuery := systemdRunning, queryCommand
	t.Cleanup(func() { systemdRunning, queryCommand = oldSystemd, oldQuery })
	systemdRunning = func() bool { return true }
	queryCommand = func(context.Context, string, ...string) (string, error) {
		var out strings.Builder
		for i := range env.detached {
			fmt.Fprintf(&out, "cairn-update-%d.service loaded active running Cairn update\n", i)
		}
		return out.String(), nil
	}
	return env
}

func code(err error) string {
	var he *helperapi.Error
	if errors.As(err, &he) {
		return he.Code
	}
	return ""
}

func TestUpdateStartVerifiesEverything(t *testing.T) {
	ctx := context.Background()
	env := newUpdateEnv(t, false)

	for version, want := range map[string]string{
		"9.9.9":        helperapi.CodeUpdateNotAvailable, // not in the index
		"0.1.0":        helperapi.CodeUpdateNotAvailable, // not newer
		"0.3.0-beta.1": helperapi.CodeUpdateNotAvailable, // beta while on stable
	} {
		if _, err := env.u.Start(ctx, version); code(err) != want {
			t.Errorf("%s: want %s, got %v", version, want, err)
		}
	}

	job, err := env.u.Start(ctx, "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if job.State != "running" || len(env.detached) != 1 {
		t.Fatalf("job %+v detached %v", job, env.detached)
	}
	file := strings.Split(env.detached[0], "|")[0]
	if got, _ := os.ReadFile(file); string(got) != string(env.pkg) {
		t.Fatalf("downloaded package differs: %q", got)
	}
	if _, err := env.u.Start(ctx, "0.2.0"); code(err) != helperapi.CodeUpdateInProgress {
		t.Fatalf("second start while running: %v", err)
	}
}

func TestUpdateRefusals(t *testing.T) {
	ctx := context.Background()

	tampered := newUpdateEnv(t, true)
	if _, err := tampered.u.Start(ctx, "0.2.0"); code(err) != helperapi.CodeUpdateVerification {
		t.Fatalf("tampered package: %v", err)
	}
	if len(tampered.detached) != 0 {
		t.Fatal("tampered package was handed to the installer")
	}
	entries, _ := os.ReadDir(tampered.u.packagesDir())
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".deb") {
			t.Fatalf("tampered package kept on disk: %s", e.Name())
		}
	}

	dev := newUpdateEnv(t, false)
	dev.u.Version = "8772440-dirty"
	if _, err := dev.u.Start(ctx, "0.2.0"); code(err) != helperapi.CodeUpdateNotAvailable {
		t.Fatalf("dev build: %v", err)
	}

	untrusted := newUpdateEnv(t, false)
	cfg, _ := config.Load(untrusted.cfgPath)
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	cfg.Cloud.TrustedKeys = []string{base64.StdEncoding.EncodeToString(other)}
	_ = config.Save(untrusted.cfgPath, cfg, 0o640, -1, -1)
	if _, err := untrusted.u.Start(ctx, "0.2.0"); code(err) != helperapi.CodeUpdateVerification {
		t.Fatalf("index signed by an untrusted key: %v", err)
	}

	beta := newUpdateEnv(t, false)
	cfg, _ = config.Load(beta.cfgPath)
	cfg.Cloud.Channel = "beta"
	_ = config.Save(beta.cfgPath, cfg, 0o640, -1, -1)
	if _, err := beta.u.Start(ctx, "0.3.0-beta.1"); err != nil {
		t.Fatalf("beta channel should accept the beta release: %v", err)
	}
}

// Without systemd nothing restarts the services, so only the installation
// decides; a failed one is rolled back all the same.
func TestRunUpdateRollsBack(t *testing.T) {
	dir := t.TempDir()
	updates := filepath.Join(dir, "updates")
	_ = os.MkdirAll(updates, 0o700)
	newPkg := filepath.Join(updates, "cairn_0.2.0_amd64.deb")
	oldPkg := filepath.Join(updates, "cairn_0.1.0_amd64.deb")
	stale := filepath.Join(updates, "cairn_0.0.9_amd64.deb")
	for _, p := range []string{newPkg, oldPkg, stale} {
		_ = os.WriteFile(p, []byte("x"), 0o600)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	oldSystemd, oldHealth := systemdRunning, checkHealth
	defer func() { installer, systemdRunning, checkHealth = install, oldSystemd, oldHealth }()
	systemdRunning = func() bool { return false }
	checkHealth = func(context.Context, config.Config, string) error {
		t.Error("services checked without systemd")
		return nil
	}
	run := UpdateRun{StateDir: dir, ConfigPath: filepath.Join(dir, "absent.toml"), File: newPkg, Version: "0.2.0",
		Previous: oldPkg, From: "0.1.0", Log: log}

	var calls []string
	installer = func(file string, downgrade bool, out *strings.Builder) error {
		calls = append(calls, filepath.Base(file))
		if file == newPkg {
			out.WriteString("dpkg: error processing\n")
			return errors.New("exit 1")
		}
		return nil
	}
	if err := RunUpdate(run); err == nil {
		t.Fatal("failure not reported")
	}
	job := readJob(filepath.Join(dir, "update-status.json"))
	if job.State != "failed" || !strings.Contains(job.Error, "previous version was restored") || !strings.Contains(job.Log, "dpkg: error") {
		t.Fatalf("job %+v", job)
	}
	if len(calls) != 2 || calls[1] != "cairn_0.1.0_amd64.deb" {
		t.Fatalf("rollback not attempted: %v", calls)
	}

	installer = func(string, bool, *strings.Builder) error { return nil }
	if err := RunUpdate(run); err != nil {
		t.Fatal(err)
	}
	job = readJob(filepath.Join(dir, "update-status.json"))
	cur, _ := os.ReadFile(filepath.Join(updates, "current"))
	if job.State != "succeeded" || strings.TrimSpace(string(cur)) != "cairn_0.2.0_amd64.deb" {
		t.Fatalf("success: %+v current=%q", job, cur)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("old packages beyond the previous one should be pruned")
	}
	if _, err := os.Stat(oldPkg); err != nil {
		t.Fatal("the previous package must be kept for rollback")
	}
}

// fakeUpdateSystem stands in for systemd, the package manager and the panel's
// services in a run under systemd. calls lists what the run did, in order.
type fakeUpdateSystem struct {
	t       *testing.T
	run     UpdateRun
	data    string // the panel's data directory
	calls   []string
	failOn  map[string]error           // a call (as listed in calls) that fails
	onCall  map[string]func()          // runs when the call is made
	healthy func(version string) error // the health check's answer
}

func newFakeUpdateSystem(t *testing.T) *fakeUpdateSystem {
	t.Helper()
	dir := t.TempDir()
	sys := &fakeUpdateSystem{t: t, data: filepath.Join(dir, "data"), failOn: map[string]error{}, onCall: map[string]func(){},
		healthy: func(string) error { return nil }}
	updates := filepath.Join(dir, "state", "updates")
	if err := os.MkdirAll(updates, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sys.data, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"cairn_0.2.0_amd64.deb", "cairn_0.1.0_amd64.deb"} {
		_ = os.WriteFile(filepath.Join(updates, p), []byte("x"), 0o600)
	}
	// The database of 0.1.0, as cairn-web left it when it was killed: a
	// log with committed transactions next to the file.
	sys.write("cairn.db", "database of 0.1.0")
	sys.write("cairn.db-wal", "log of 0.1.0")
	cfg := config.Default()
	cfg.Paths.DataDir = sys.data
	cfgPath := filepath.Join(dir, "config.toml")
	if err := config.Save(cfgPath, cfg, 0o640, -1, -1); err != nil {
		t.Fatal(err)
	}
	sys.run = UpdateRun{StateDir: filepath.Join(dir, "state"), ConfigPath: cfgPath,
		File: filepath.Join(updates, "cairn_0.2.0_amd64.deb"), Version: "0.2.0",
		Previous: filepath.Join(updates, "cairn_0.1.0_amd64.deb"), From: "0.1.0",
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	oldSystemd, oldQuery, oldHealth := systemdRunning, queryCommand, checkHealth
	t.Cleanup(func() {
		systemdRunning, queryCommand, checkHealth, installer = oldSystemd, oldQuery, oldHealth, install
	})
	systemdRunning = func() bool { return true }
	queryCommand = func(_ context.Context, name string, args ...string) (string, error) {
		return "", sys.call(name + " " + strings.Join(args, " "))
	}
	installer = func(file string, downgrade bool, out *strings.Builder) error {
		what := "install " + filepath.Base(file)
		if downgrade {
			what = "downgrade " + filepath.Base(file)
		}
		return sys.call(what)
	}
	checkHealth = func(_ context.Context, c config.Config, version string) error {
		if c.Paths.DataDir != sys.data {
			t.Errorf("health check got another configuration: %+v", c.Paths)
		}
		if err := sys.call("health " + version); err != nil {
			return err
		}
		return sys.healthy(version)
	}
	return sys
}

func (s *fakeUpdateSystem) call(what string) error {
	s.calls = append(s.calls, what)
	if f := s.onCall[what]; f != nil {
		f()
	}
	return s.failOn[what]
}

func (s *fakeUpdateSystem) write(name, content string) {
	s.t.Helper()
	if err := os.WriteFile(filepath.Join(s.data, name), []byte(content), 0o600); err != nil {
		s.t.Fatal(err)
	}
}

func (s *fakeUpdateSystem) read(name string) string {
	data, err := os.ReadFile(filepath.Join(s.data, name))
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(data)
}

func (s *fakeUpdateSystem) job() helperapi.UpdateJob {
	return readJob(filepath.Join(s.run.StateDir, "update-status.json"))
}

func (s *fakeUpdateSystem) expectCalls(want ...string) {
	s.t.Helper()
	if strings.Join(s.calls, "\n") != strings.Join(want, "\n") {
		s.t.Fatalf("calls:\n  %s\nwant:\n  %s", strings.Join(s.calls, "\n  "), strings.Join(want, "\n  "))
	}
}

const (
	stopWeb  = "systemctl stop cairn-web.service"
	resetWeb = "systemctl reset-failed cairn-web.service"
	startWeb = "systemctl start cairn-web.service"
)

func TestRunUpdateWaitsForTheNewVersion(t *testing.T) {
	sys := newFakeUpdateSystem(t)
	// The copy is complete before anything is installed.
	sys.onCall["install cairn_0.2.0_amd64.deb"] = func() {
		got, err := os.ReadFile(filepath.Join(sys.run.StateDir, "updates", "database", "cairn.db-wal"))
		if err != nil || string(got) != "log of 0.1.0" {
			t.Errorf("database not copied before the installation: %q %v", got, err)
		}
	}
	if err := RunUpdate(sys.run); err != nil {
		t.Fatal(err)
	}
	sys.expectCalls(stopWeb, "install cairn_0.2.0_amd64.deb", resetWeb, startWeb, "health 0.2.0")
	job := sys.job()
	cur, _ := os.ReadFile(filepath.Join(sys.run.StateDir, "updates", "current"))
	if job.State != "succeeded" || strings.TrimSpace(string(cur)) != "cairn_0.2.0_amd64.deb" {
		t.Fatalf("job %+v, current %q", job, cur)
	}
	if !strings.Contains(job.Log, "version 0.2.0 answers") {
		t.Fatalf("log: %s", job.Log)
	}
}

func TestRunUpdateRollsBackAVersionThatDoesNotRun(t *testing.T) {
	sys := newFakeUpdateSystem(t)
	if os.Geteuid() == 0 {
		// The panel's files belong to its account, not to root.
		for _, n := range []string{"cairn.db", "cairn.db-wal"} {
			if err := os.Chown(filepath.Join(sys.data, n), 4321, 4321); err != nil {
				t.Fatal(err)
			}
		}
	}
	// 0.2.0 migrates the database as it starts, then fails.
	sys.healthy = func(v string) error {
		if v == "0.2.0" {
			sys.write("cairn.db", "database migrated by 0.2.0")
			sys.write("cairn.db-wal", "log of 0.2.0")
			sys.write("cairn.db-shm", "index of 0.2.0")
			return errors.New("cairn-web.service does not answer: connection refused")
		}
		return nil
	}
	err := RunUpdate(sys.run)
	t.Logf("outcome: %v", err)
	if err == nil {
		t.Fatal("a version that does not run counted as installed")
	}
	sys.expectCalls(stopWeb, "install cairn_0.2.0_amd64.deb", resetWeb, startWeb, "health 0.2.0",
		stopWeb, "downgrade cairn_0.1.0_amd64.deb", resetWeb, startWeb, "health 0.1.0")
	job := sys.job()
	if job.State != "failed" || !strings.Contains(job.Error, "version 0.2.0 was installed but does not run") ||
		!strings.Contains(job.Error, "the previous version was restored, with the database from before the update") {
		t.Fatalf("job %+v", job)
	}
	if got := sys.read("cairn.db"); got != "database of 0.1.0" {
		t.Fatalf("database not put back: %q", got)
	}
	if got := sys.read("cairn.db-wal"); got != "log of 0.1.0" {
		t.Fatalf("log not put back: %q", got)
	}
	if _, err := os.Lstat(filepath.Join(sys.data, "cairn.db-shm")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the newer version's index was left next to the older database: %v", err)
	}
	if os.Geteuid() == 0 {
		for _, n := range []string{"cairn.db", "cairn.db-wal"} {
			fi, _ := os.Stat(filepath.Join(sys.data, n))
			if uid, gid := fileOwner(fi); uid != 4321 || gid != 4321 || fi.Mode().Perm() != 0o600 {
				t.Fatalf("%s restored as %d:%d %v", n, uid, gid, fi.Mode())
			}
		}
	}
	if cur, _ := os.ReadFile(filepath.Join(sys.run.StateDir, "updates", "current")); len(cur) != 0 {
		t.Fatalf("rollback pointer moved by a failed update: %q", cur)
	}
}

func TestRunUpdateReportsAFailedRollback(t *testing.T) {
	for name, tc := range map[string]struct {
		setup func(*fakeUpdateSystem)
		want  string
	}{
		"previous version does not run either": {
			setup: func(s *fakeUpdateSystem) {
				s.healthy = func(v string) error { return errors.New("no answer from " + v) }
			},
			want: "restoring the previous version also failed: it was installed again, with the database from before the update, but does not run: no answer from 0.1.0",
		},
		"previous package does not install": {
			setup: func(s *fakeUpdateSystem) {
				s.healthy = func(string) error { return errors.New("no answer") }
				s.failOn["downgrade cairn_0.1.0_amd64.deb"] = errors.New("exit 1")
			},
			want: "restoring the previous version also failed: its installation failed",
		},
		"database copy damaged": {
			setup: func(s *fakeUpdateSystem) {
				s.healthy = func(string) error { return errors.New("no answer") }
				s.onCall["downgrade cairn_0.1.0_amd64.deb"] = func() {
					_ = os.WriteFile(filepath.Join(s.run.StateDir, "updates", "database", "cairn.db"), []byte("garbage"), 0o600)
				}
			},
			want: "its database could not be put back",
		},
	} {
		t.Run(name, func(t *testing.T) {
			sys := newFakeUpdateSystem(t)
			tc.setup(sys)
			err := RunUpdate(sys.run)
			job := sys.job()
			t.Logf("outcome: %s", job.Error)
			if err == nil || job.State != "failed" || !strings.Contains(job.Error, tc.want) || strings.Contains(job.Error, "was restored") {
				t.Fatalf("job %+v", job)
			}
			// cairn-web is started again whatever happened.
			if sys.calls[len(sys.calls)-1] != startWeb && sys.calls[len(sys.calls)-1] != "health 0.1.0" {
				t.Fatalf("panel not started again: %v", sys.calls)
			}
		})
	}
}

func TestRunUpdateInstallsNothingWithoutADatabaseCopy(t *testing.T) {
	for name, setup := range map[string]func(*fakeUpdateSystem){
		"database is a link": func(s *fakeUpdateSystem) {
			outside := filepath.Join(t.TempDir(), "secret.db")
			_ = os.WriteFile(outside, []byte("not the panel's"), 0o600)
			_ = os.Remove(filepath.Join(s.data, "cairn.db"))
			if err := os.Symlink(outside, filepath.Join(s.data, "cairn.db")); err != nil {
				t.Fatal(err)
			}
		},
		"database has another name": func(s *fakeUpdateSystem) {
			if err := os.Link(filepath.Join(s.data, "cairn.db"), filepath.Join(t.TempDir(), "other")); err != nil {
				t.Skip("no hard link across these directories:", err)
			}
		},
		"no database": func(s *fakeUpdateSystem) { _ = os.Remove(filepath.Join(s.data, "cairn.db")) },
		"panel does not stop": func(s *fakeUpdateSystem) {
			s.failOn[stopWeb] = errors.New("execx: systemctl exited with status 1")
		},
	} {
		t.Run(name, func(t *testing.T) {
			sys := newFakeUpdateSystem(t)
			setup(sys)
			err := RunUpdate(sys.run)
			job := sys.job()
			t.Logf("outcome: %s", job.Error)
			if err == nil || job.State != "failed" || !strings.Contains(job.Error, "nothing was installed") {
				t.Fatalf("job %+v", job)
			}
			for _, c := range sys.calls {
				if strings.HasPrefix(c, "install ") {
					t.Fatalf("installed without a copy of the database: %v", sys.calls)
				}
			}
			if sys.calls[len(sys.calls)-1] != startWeb {
				t.Fatalf("panel not started again: %v", sys.calls)
			}
		})
	}
}

func TestRestoreDatabaseReplacesLinksWithoutFollowingThem(t *testing.T) {
	data, dest := t.TempDir(), filepath.Join(t.TempDir(), "database")
	_ = os.WriteFile(filepath.Join(data, "cairn.db"), []byte("db"), 0o600)
	if err := backupDatabase(data, dest, "0.1.0"); err != nil {
		t.Fatal(err)
	}
	// What the panel account may have put there meanwhile.
	outside := t.TempDir()
	for _, n := range []string{"cairn.db", "cairn.db-wal", "cairn.db-shm"} {
		target := filepath.Join(outside, n)
		_ = os.WriteFile(target, []byte("outside"), 0o600)
		_ = os.Remove(filepath.Join(data, n))
		if err := os.Symlink(target, filepath.Join(data, n)); err != nil {
			t.Fatal(err)
		}
	}
	if err := restoreDatabase(data, dest); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(filepath.Join(data, "cairn.db")); err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("database not a file of its own: %v %v", fi, err)
	}
	if got, _ := os.ReadFile(filepath.Join(data, "cairn.db")); string(got) != "db" {
		t.Fatalf("database content %q", got)
	}
	for _, n := range []string{"cairn.db-wal", "cairn.db-shm"} {
		if _, err := os.Lstat(filepath.Join(data, n)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("%s left in place: %v", n, err)
		}
	}
	for _, n := range []string{"cairn.db", "cairn.db-wal", "cairn.db-shm"} {
		if got, _ := os.ReadFile(filepath.Join(outside, n)); string(got) != "outside" {
			t.Fatalf("%s outside the data directory was written: %q", n, got)
		}
	}
	entries, _ := os.ReadDir(data)
	if len(entries) != 1 {
		t.Fatalf("temporary files left: %v", entries)
	}
}

// writeRunning leaves a job as a run that started at started.
func writeRunning(t *testing.T, u *Updater, started int64) {
	t.Helper()
	if err := writeJob(u.statusPath(), helperapi.UpdateJob{State: jobRunning, Version: "0.2.0", StartedAt: started}); err != nil {
		t.Fatal(err)
	}
}

func fakeUnits(t *testing.T, systemd bool, list func() (string, error)) {
	t.Helper()
	oldSystemd, oldQuery := systemdRunning, queryCommand
	t.Cleanup(func() { systemdRunning, queryCommand = oldSystemd, oldQuery })
	systemdRunning = func() bool { return systemd }
	queryCommand = func(_ context.Context, name string, args ...string) (string, error) {
		if name != "systemctl" || len(args) == 0 || args[0] != "list-units" || args[len(args)-1] != "cairn-update-*" {
			t.Errorf("unexpected command %s %v", name, args)
		}
		return list()
	}
}

func TestUpdateStatusSettlesAnInterruptedRun(t *testing.T) {
	env := newUpdateEnv(t, false)
	writeRunning(t, env.u, time.Now().Unix())
	// The machine restarted: no update unit exists any more.
	fakeUnits(t, true, func() (string, error) { return "", nil })
	env.u.Recover()
	job := readJob(env.u.statusPath())
	if job.State != jobInterrupted || job.FinishedAt == 0 || !strings.Contains(job.Error, "dpkg --configure -a") {
		t.Fatalf("job left as %+v", job)
	}
	if env.u.Running() {
		t.Fatal("an interrupted update still holds the package manager")
	}
	// It can be tried again.
	if job, err := env.u.Start(context.Background(), "0.2.0"); err != nil || job.State != jobRunning || len(env.detached) != 1 {
		t.Fatalf("retry: %+v %v (detached %v)", job, err, env.detached)
	}
}

func TestUpdateStatusKeepsARunThatGoesOn(t *testing.T) {
	env := newUpdateEnv(t, false)
	ctx := context.Background()
	// Started long ago by another helper process, and still running.
	writeRunning(t, env.u, time.Now().Add(-2*staleJobAfter).Unix())
	for _, line := range []string{
		"cairn-update-1764000000.service loaded active running Cairn update to 0.2.0",
		"\u25cf cairn-update-1764000000.service loaded deactivating stop-post Cairn update to 0.2.0",
	} {
		fakeUnits(t, true, func() (string, error) { return line + "\n", nil })
		if job := env.u.Status(); job.State != jobRunning {
			t.Fatalf("%q: %+v", line, job)
		}
		if !env.u.Running() {
			t.Fatalf("%q: not running", line)
		}
		if _, err := env.u.Start(ctx, "0.2.0"); code(err) != helperapi.CodeUpdateInProgress {
			t.Fatalf("%q: second start: %v", line, err)
		}
	}
	// A finished unit that systemd still lists does not count.
	fakeUnits(t, true, func() (string, error) {
		return "cairn-update-1764000000.service loaded failed failed Cairn update to 0.2.0\n", nil
	})
	if job := env.u.Status(); job.State != jobInterrupted {
		t.Fatalf("failed unit: %+v", job)
	}
}

func TestUpdateStatusReadsTheOutcomeOfARunThatJustEnded(t *testing.T) {
	env := newUpdateEnv(t, false)
	started := time.Now().Unix()
	writeRunning(t, env.u, started)
	// The run writes its outcome and ends between the two reads.
	fakeUnits(t, true, func() (string, error) {
		_ = writeJob(env.u.statusPath(), helperapi.UpdateJob{State: "succeeded", Version: "0.2.0", StartedAt: started, FinishedAt: started + 1})
		return "", nil
	})
	if job := env.u.Status(); job.State != "succeeded" {
		t.Fatalf("outcome replaced: %+v", job)
	}
}

func TestUpdateStatusWithoutSystemd(t *testing.T) {
	env := newUpdateEnv(t, false)
	fakeUnits(t, false, func() (string, error) {
		t.Error("systemd asked without systemd")
		return "", nil
	})
	writeRunning(t, env.u, time.Now().Unix())
	env.u.inProcess.Store(true)
	if job := env.u.Status(); job.State != jobRunning {
		t.Fatalf("run in this process: %+v", job)
	}
	// A restarted helper has no run of its own.
	env.u.inProcess.Store(false)
	if job := env.u.Status(); job.State != jobInterrupted {
		t.Fatalf("run of an earlier process: %+v", job)
	}
}

func TestUpdateStatusWhenSystemdDoesNotAnswer(t *testing.T) {
	env := newUpdateEnv(t, false)
	fakeUnits(t, true, func() (string, error) { return "", errors.New("Failed to connect to bus") })
	writeRunning(t, env.u, time.Now().Unix())
	if job := env.u.Status(); job.State != jobRunning {
		t.Fatalf("recent run given up: %+v", job)
	}
	writeRunning(t, env.u, time.Now().Add(-staleJobAfter-time.Minute).Unix())
	if job := env.u.Status(); job.State != jobInterrupted {
		t.Fatalf("run older than any run can last: %+v", job)
	}
}

func TestHealthWait(t *testing.T) {
	ctx := context.Background()
	quick := healthWait{timeout: 300 * time.Millisecond, settle: 20 * time.Millisecond, interval: time.Millisecond}

	// Answers after a while and keeps answering.
	n := 0
	h := quick
	h.check = func(context.Context, string) error {
		if n++; n < 5 {
			return errors.New("connection refused")
		}
		return nil
	}
	h.restarts = func(context.Context) (int, error) { return 3, nil }
	if err := h.wait(ctx, "0.2.0"); err != nil {
		t.Fatalf("steady answers: %v", err)
	}

	// Answers, but systemd restarts it all the time.
	restarts := 0
	h.check = func(context.Context, string) error { return nil }
	h.restarts = func(context.Context) (int, error) { restarts++; return restarts, nil }
	if err := h.wait(ctx, "0.2.0"); err == nil || !strings.Contains(err.Error(), "restarted") {
		t.Fatalf("crash loop: %v", err)
	}

	// Never answers: the last reason is reported.
	h.check = func(_ context.Context, v string) error { return errors.New("runs version 0.1.0, not " + v) }
	h.restarts = func(context.Context) (int, error) { return 0, nil }
	start := time.Now()
	if err := h.wait(ctx, "0.2.0"); err == nil || !strings.Contains(err.Error(), "runs version 0.1.0") {
		t.Fatalf("no answer: %v", err)
	}
	if time.Since(start) < quick.timeout {
		t.Fatal("gave up before the timeout")
	}
}

func TestPanelProbe(t *testing.T) {
	version := buildinfo.Version
	var answer atomic.Value
	answer.Store(version)
	web := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/meta" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"version": answer.Load(), "setupRequired": false})
	}))
	t.Cleanup(web.Close)

	sock := filepath.Join(t.TempDir(), "helper.sock")
	l, err := Listen(sock, -1)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	RegisterCore(reg, Deps{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	srv := &Server{Registry: reg, Allowed: func(uint32) bool { return true }, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = srv.Serve(ctx, l); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	cfg := config.Default()
	cfg.Server.Listen = web.Listener.Addr().String()
	cfg.Paths.HelperSocket = sock
	p := newPanelProbe(cfg)
	if err := p.check(callCtx(t), version); err != nil {
		t.Fatalf("both services answer: %v", err)
	}
	if err := p.check(callCtx(t), "99.0.0"); err == nil || !strings.Contains(err.Error(), "helper runs version") {
		t.Fatalf("helper of another version: %v", err)
	}
	answer.Store("0.0.1")
	if err := p.check(callCtx(t), version); err == nil || !strings.Contains(err.Error(), "cairn-web.service runs version") {
		t.Fatalf("panel of another version: %v", err)
	}
	web.Close()
	if err := p.check(callCtx(t), version); err == nil || !strings.Contains(err.Error(), "does not answer") {
		t.Fatalf("panel down: %v", err)
	}
}

func TestMetaURL(t *testing.T) {
	for listen, want := range map[string]string{
		"0.0.0.0:28443":     "https://127.0.0.1:28443/api/v1/meta",
		":28443":            "https://127.0.0.1:28443/api/v1/meta",
		"[::]:28443":        "https://[::1]:28443/api/v1/meta",
		"192.0.2.10:28443":  "https://192.0.2.10:28443/api/v1/meta",
		"[2001:db8::1]:443": "https://[2001:db8::1]:443/api/v1/meta",
		"no port":           "",
	} {
		if got := metaURL(listen); got != want {
			t.Errorf("%s: %q, want %q", listen, got, want)
		}
	}
}
