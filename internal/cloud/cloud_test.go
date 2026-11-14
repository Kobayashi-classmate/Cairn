// SPDX-License-Identifier: AGPL-3.0-only

package cloud

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/semver"
)

func newKey(t *testing.T) (ed25519.PrivateKey, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv, base64.StdEncoding.EncodeToString(pub)
}

func TestSignAndVerify(t *testing.T) {
	priv, pub := newKey(t)
	other, otherPub := newKey(t)
	kr, err := NewKeyring([]string{pub})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"type":"cairn.releases"}`)
	if err := kr.Verify(data, Sign(data, priv)); err != nil {
		t.Fatal(err)
	}
	if err := kr.Verify([]byte(`{"type":"cairn.releases","x":1}`), Sign(data, priv)); !errors.Is(err, ErrSignature) {
		t.Fatal("modified data verified")
	}
	if err := kr.Verify(data, Sign(data, other)); !errors.Is(err, ErrSignature) {
		t.Fatal("untrusted key verified")
	}
	if err := kr.Verify(data, []byte("garbage")); !errors.Is(err, ErrSignature) {
		t.Fatal("garbage signature verified")
	}
	empty, _ := NewKeyring(nil)
	if err := empty.Verify(data, Sign(data, priv)); !errors.Is(err, ErrNoTrustedKey) {
		t.Fatalf("empty keyring: %v", err)
	}
	if _, err := NewKeyring([]string{"not-a-key"}); err == nil {
		t.Fatal("bad key accepted")
	}
	_ = otherPub
}

func feedServer(t *testing.T, priv ed25519.PrivateKey, files map[string]any) *httptest.Server {
	t.Helper()
	blobs := map[string][]byte{}
	for name, v := range files {
		b, _ := json.Marshal(v)
		blobs[name] = b
		blobs[name+".sig"] = Sign(b, priv)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ua := r.Header.Get("User-Agent"); ua != "Cairn" {
			t.Errorf("User-Agent %q leaks details", ua)
		}
		b, ok := blobs[strings.TrimPrefix(r.URL.Path, "/feed/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	}))
}

func sha(c byte) string { return strings.Repeat(string(c), 64) }

func TestFetchAndSelectRelease(t *testing.T) {
	priv, pub := newKey(t)
	kr, _ := NewKeyring([]string{pub})
	idx := Releases{Type: "cairn.releases", GeneratedAt: 100, Releases: []Release{
		{Version: "0.1.0", Channel: "stable", Packages: []Package{{Format: "deb", Arch: "amd64", URL: "https://x/a.deb", SHA256: sha('a')}}},
		{Version: "0.2.0", Channel: "stable", Packages: []Package{{Format: "deb", Arch: "amd64", URL: "https://x/b.deb", SHA256: sha('b')}, {Format: "rpm", Arch: "amd64", URL: "https://x/b.rpm", SHA256: sha('c')}}},
		{Version: "0.3.0-beta.1", Channel: "beta", Packages: []Package{{Format: "deb", Arch: "amd64", URL: "https://x/c.deb", SHA256: sha('d')}}},
		{Version: "0.4.0", Channel: "stable", Packages: []Package{{Format: "rpm", Arch: "arm64", URL: "https://x/d.rpm", SHA256: sha('e')}}},
	}}
	srv := feedServer(t, priv, map[string]any{ReleasesFile: idx})
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/feed", Keys: kr}

	r, err := c.FetchReleases(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	cur, _ := semver.Parse("0.1.0")
	rel, pkg := r.Latest(cur, "stable", "deb", "amd64")
	if rel == nil || rel.Version != "0.2.0" || pkg.URL != "https://x/b.deb" {
		t.Fatalf("stable deb/amd64: %+v", rel)
	}
	if rel, _ := r.Latest(cur, "beta", "deb", "amd64"); rel == nil || rel.Version != "0.3.0-beta.1" {
		t.Fatalf("beta channel: %+v", rel)
	}
	// 0.4.0 has no deb/amd64 package, so it is not offered.
	newest, _ := semver.Parse("0.2.0")
	if rel, _ := r.Latest(newest, "stable", "deb", "amd64"); rel != nil {
		t.Fatalf("nothing newer expected, got %s", rel.Version)
	}
	if _, err := c.FetchReleases(context.Background(), 101); !errors.Is(err, ErrRollback) {
		t.Fatalf("rollback not detected: %v", err)
	}

	// A different key or a missing signature is fatal.
	_, otherPub := newKey(t)
	wrong, _ := NewKeyring([]string{otherPub})
	if _, err := (&Client{BaseURL: srv.URL + "/feed", Keys: wrong}).FetchReleases(context.Background(), 0); !errors.Is(err, ErrSignature) {
		t.Fatalf("wrong key: %v", err)
	}
	if _, err := (&Client{BaseURL: srv.URL + "/feed", Keys: kr}).FetchAnnouncements(context.Background(), 0); err == nil {
		t.Fatal("missing feed should fail")
	}
}

func TestAnnouncementFiltering(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	a := Announcements{Items: []Announcement{
		{ID: "all"},
		{ID: "expired", ExpiresAt: now.Unix() - 1},
		{ID: "old-versions", Versions: "<0.2.0"},
		{ID: "this-version", Versions: ">=0.2.0 <0.3.0", Link: "https://example.com/a"},
		{ID: "el-only", Families: []string{"el"}},
		{ID: "bad-link", Link: "javascript:alert(1)"},
		{ID: ""},
	}}
	v, _ := semver.Parse("0.2.5")
	got := map[string]Announcement{}
	for _, it := range a.Applicable(&v, "debian", now) {
		got[it.ID] = it
	}
	for _, want := range []string{"all", "this-version", "bad-link"} {
		if _, ok := got[want]; !ok {
			t.Errorf("%s missing", want)
		}
	}
	for _, not := range []string{"expired", "old-versions", "el-only", ""} {
		if _, ok := got[not]; ok {
			t.Errorf("%q should be filtered", not)
		}
	}
	if got["bad-link"].Link != "" || got["this-version"].Link == "" {
		t.Fatal("only https links may pass")
	}
	// Development builds see only unconstrained messages.
	dev := a.Applicable(nil, "debian", now)
	for _, it := range dev {
		if it.Versions != "" {
			t.Fatalf("dev build got versioned message %s", it.ID)
		}
	}
}

func TestBuiltinKeysParse(t *testing.T) {
	// A malformed entry in trusted_keys.txt would break every update check.
	if _, err := NewKeyring(nil); err != nil {
		t.Fatalf("trusted_keys.txt: %v", err)
	}
	got := ParseKeyList("# comment\n\n  AAAA trailing note\n#BBBB\nCCCC\n")
	if len(got) != 2 || got[0] != "AAAA" || got[1] != "CCCC" {
		t.Fatalf("ParseKeyList: %q", got)
	}
}
