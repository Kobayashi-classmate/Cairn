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
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	defer func() { installer = install }()

	var calls []string
	installer = func(file string, downgrade bool, out *strings.Builder) error {
		calls = append(calls, filepath.Base(file))
		if file == newPkg {
			out.WriteString("dpkg: error processing\n")
			return errors.New("exit 1")
		}
		return nil
	}
	if err := RunUpdate(dir, newPkg, "0.2.0", oldPkg, log); err == nil {
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
	if err := RunUpdate(dir, newPkg, "0.2.0", oldPkg, log); err != nil {
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
