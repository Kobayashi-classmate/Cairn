// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/cloud"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, p, content string) string {
	t.Helper()
	must(t, os.WriteFile(p, []byte(content), 0o600))
	return p
}

// setup creates a key and a key list that trusts it.
func setup(t *testing.T) (dir, key, keys string) {
	t.Helper()
	dir = t.TempDir()
	key = filepath.Join(dir, "release.key")
	var out strings.Builder
	must(t, run([]string{"keygen", "-out", key}, &out))
	if fi, _ := os.Stat(key); fi.Mode().Perm() != 0o600 {
		t.Fatalf("key mode %v", fi.Mode())
	}
	if err := run([]string{"keygen", "-out", key}, io.Discard); err == nil {
		t.Fatal("keygen overwrote an existing key")
	}
	var pub strings.Builder
	must(t, run([]string{"pubkey", "-key", key}, &pub))
	if !strings.Contains(out.String(), strings.Fields(pub.String())[0]) {
		t.Fatalf("keygen output lacks the public key:\n%s", out.String())
	}
	keys = write(t, filepath.Join(dir, "keys.txt"), "# test\n"+pub.String())
	return dir, key, keys
}

func TestSignVerifyAndSums(t *testing.T) {
	dir, key, keys := setup(t)
	a := write(t, filepath.Join(dir, "a.deb"), "alpha")
	sumsFile := filepath.Join(dir, "SHA256SUMS")
	must(t, run([]string{"sums", "-out", sumsFile, a}, io.Discard))
	got, _ := os.ReadFile(sumsFile)
	if string(got) != "8ed3f6ad685b959ead7022518e1af76cd816f8e8ec7ccdda1ed4018e8f2223f8  a.deb\n" {
		t.Fatalf("SHA256SUMS: %q", got)
	}
	must(t, run([]string{"sign", "-key", key, sumsFile}, io.Discard))
	must(t, run([]string{"verify", "-keys", keys, sumsFile}, io.Discard))

	must(t, os.WriteFile(sumsFile, []byte("tampered\n"), 0o600))
	if err := run([]string{"verify", "-keys", keys, sumsFile}, io.Discard); err == nil {
		t.Fatal("tampered file verified")
	}
	// The built-in list does not hold the test key.
	if err := run([]string{"verify", sumsFile}, io.Discard); err == nil {
		t.Fatal("verified against keys that do not include the signer")
	}
}

func TestInstallerFillsKeys(t *testing.T) {
	dir, _, keys := setup(t)
	tmpl := write(t, filepath.Join(dir, "install.sh"), "#!/bin/sh\nTRUSTED_KEYS=\"\"\necho $TRUSTED_KEYS\n")
	out := filepath.Join(dir, "out.sh")
	must(t, run([]string{"installer", "-keys", keys, "-in", tmpl, "-out", out}, io.Discard))
	data, _ := os.ReadFile(out)
	list, _ := os.ReadFile(keys)
	if !strings.Contains(string(data), `TRUSTED_KEYS="`+cloud.ParseKeyList(string(list))[0]+`"`) {
		t.Fatalf("keys not filled in:\n%s", data)
	}
	// A template without exactly one placeholder is refused, and so is an
	// empty key list (an installer that trusts nothing is useless).
	bad := write(t, filepath.Join(dir, "bad.sh"), "TRUSTED_KEYS=\"x\"\n")
	if err := run([]string{"installer", "-keys", keys, "-in", bad, "-out", out}, io.Discard); err == nil {
		t.Fatal("template without placeholder accepted")
	}
	empty := write(t, filepath.Join(dir, "empty.txt"), "# none\n")
	if err := run([]string{"installer", "-keys", empty, "-in", tmpl, "-out", out}, io.Discard); err == nil {
		t.Fatal("empty key list accepted")
	}

	// -version fills the minimum version the installer takes without --version.
	withMin := write(t, filepath.Join(dir, "min.sh"), "#!/bin/sh\nTRUSTED_KEYS=\"\"\nMIN_VERSION=\"\"\n")
	must(t, run([]string{"installer", "-keys", keys, "-version", "0.3.0", "-in", withMin, "-out", out}, io.Discard))
	if data, _ := os.ReadFile(out); !strings.Contains(string(data), "\nMIN_VERSION=\"0.3.0\"\n") {
		t.Fatalf("minimum version not filled in:\n%s", data)
	}
	if err := run([]string{"installer", "-keys", keys, "-version", "0.3.0", "-in", tmpl, "-out", out}, io.Discard); err == nil {
		t.Fatal("-version accepted for a template without MIN_VERSION")
	}
	if err := run([]string{"installer", "-keys", keys, "-version", "v0.3", "-in", withMin, "-out", out}, io.Discard); err == nil {
		t.Fatal("malformed -version accepted")
	}
}

func TestReleasesIndex(t *testing.T) {
	dir, key, keys := setup(t)
	clock := time.Unix(1_800_000_000, 0)
	now = func() time.Time { return clock }
	t.Cleanup(func() { now = time.Now })

	pkgs := func(v string) []string {
		return []string{
			write(t, filepath.Join(dir, "cairn_"+v+"_amd64.deb"), "deb "+v),
			write(t, filepath.Join(dir, "cairn-"+v+"-1.aarch64.rpm"), "rpm "+v),
		}
	}
	notes := write(t, filepath.Join(dir, "notes.md"), "  First release.\n")
	idx := filepath.Join(dir, "releases.json")
	args := []string{"releases", "-version", "0.1.0", "-assets-url", "https://example.com/v0.1.0/", "-notes", "en=" + notes, "-out", idx}
	must(t, run(append(args, pkgs("0.1.0")...), io.Discard))
	must(t, run([]string{"sign", "-key", key, idx}, io.Discard))

	// The second run builds on the signed first index; the clock stands still,
	// yet generatedAt must still grow.
	next := filepath.Join(dir, "next.json")
	args = []string{"releases", "-keys", keys, "-in", idx, "-version", "0.2.0-beta.1", "-security",
		"-assets-url", "https://example.com/v0.2.0-beta.1", "-out", next}
	must(t, run(append(args, pkgs("0.2.0-beta.1")...), io.Discard))
	var got cloud.Releases
	data, _ := os.ReadFile(next)
	must(t, json.Unmarshal(data, &got))
	if got.Type != "cairn.releases" || got.GeneratedAt != clock.Unix()+1 || len(got.Releases) != 2 {
		t.Fatalf("index: %+v", got)
	}
	beta, stable := got.Releases[0], got.Releases[1]
	if beta.Version != "0.2.0-beta.1" || beta.Channel != "beta" || !beta.Security {
		t.Fatalf("beta entry: %+v", beta)
	}
	if stable.Channel != "stable" || stable.Notes["en"] != "First release." {
		t.Fatalf("stable entry: %+v", stable)
	}
	deb, rpm := stable.Package("deb", "amd64"), stable.Package("rpm", "arm64")
	if deb == nil || rpm == nil || deb.URL != "https://example.com/v0.1.0/cairn_0.1.0_amd64.deb" || deb.Size != 9 {
		t.Fatalf("packages: %+v", stable.Packages)
	}

	// An index whose signature does not check out is never built upon.
	must(t, os.WriteFile(idx, append(data, ' '), 0o600))
	if err := run(append(args, pkgs("0.2.0-beta.1")...), io.Discard); err == nil {
		t.Fatal("built on an index with a bad signature")
	}
	// Unknown package names and a "v" prefix are refused.
	odd := write(t, filepath.Join(dir, "cairn.tar.gz"), "x")
	if err := run([]string{"releases", "-version", "0.3.0", "-assets-url", "https://e.com", "-out", next, odd}, io.Discard); err == nil {
		t.Fatal("unknown package type accepted")
	}
	if err := run([]string{"releases", "-version", "v0.3.0", "-assets-url", "https://e.com", "-out", next, pkgs("0.3.0")[0]}, io.Discard); err == nil {
		t.Fatal("v-prefixed version accepted")
	}
}

func TestAnnouncementsValidation(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "announcements.json")
	ok := `{"items":[{"id":"2026-11-maint","level":"warning","category":"maintenance","versions":">=0.1.0 <0.3.0",
		"families":["debian"],"title":{"en":"Maintenance"},"body":{"en":"Downloads pause on Sunday."},"link":"https://example.com/m"}]}`
	must(t, run([]string{"announcements", "-in", write(t, filepath.Join(dir, "ok.json"), ok), "-out", out}, io.Discard))
	var got cloud.Announcements
	data, _ := os.ReadFile(out)
	must(t, json.Unmarshal(data, &got))
	if got.Type != "cairn.announcements" || got.GeneratedAt == 0 || got.Items[0].PublishedAt == 0 {
		t.Fatalf("not stamped: %+v", got)
	}

	for name, item := range map[string]string{
		"bad level":      `{"id":"a","level":"urgent","category":"general","title":{"en":"t"},"body":{"en":"b"}}`,
		"http link":      `{"id":"a","level":"info","category":"general","title":{"en":"t"},"body":{"en":"b"},"link":"http://x"}`,
		"no English":     `{"id":"a","level":"info","category":"general","title":{"zh-CN":"t"},"body":{"en":"b"}}`,
		"bad constraint": `{"id":"a","level":"info","category":"general","title":{"en":"t"},"body":{"en":"b"},"versions":"~>1"}`,
		"bad family":     `{"id":"a","level":"info","category":"general","title":{"en":"t"},"body":{"en":"b"},"families":["arch"]}`,
		"unknown field":  `{"id":"a","level":"info","category":"general","title":{"en":"t"},"body":{"en":"b"},"script":"x"}`,
		"duplicate id": `{"id":"a","level":"info","category":"general","title":{"en":"t"},"body":{"en":"b"}},
			{"id":"a","level":"info","category":"general","title":{"en":"t"},"body":{"en":"b"}}`,
	} {
		in := write(t, filepath.Join(dir, "bad.json"), `{"items":[`+item+`]}`)
		if err := run([]string{"announcements", "-in", in, "-out", out}, io.Discard); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
