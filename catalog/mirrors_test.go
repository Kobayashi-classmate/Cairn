// SPDX-License-Identifier: AGPL-3.0-only

package catalog

import (
	"testing"
	"testing/fstest"
)

func TestMirrorRewrites(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	// Official URLs as the providers' metadata gives them (2026-11-14).
	official := map[string]string{
		"go":                      "https://go.dev/dl/go1.27.4.linux-amd64.tar.gz",
		"nodejs":                  "https://nodejs.org/dist/v24.22.3/node-v24.22.3-linux-x64.tar.gz",
		"python-build-standalone": "https://github.com/astral-sh/python-build-standalone/releases/download/20261110/cpython-3.13.18+20261110-x86_64-unknown-linux-gnu-install_only_stripped.tar.gz",
		"temurin":                 "https://github.com/adoptium/temurin21-binaries/releases/download/jdk-21.0.13%2B9/OpenJDK21U-jdk_x64_linux_hotspot_21.0.13_9.tar.gz",
	}
	want := map[string]string{
		"go/google-cn":                      "https://golang.google.cn/dl/go1.27.4.linux-amd64.tar.gz",
		"nodejs/npmmirror":                  "https://cdn.npmmirror.com/binaries/node/v24.22.3/node-v24.22.3-linux-x64.tar.gz",
		"nodejs/tuna":                       "https://mirrors.tuna.tsinghua.edu.cn/nodejs-release/v24.22.3/node-v24.22.3-linux-x64.tar.gz",
		"python-build-standalone/npmmirror": "https://registry.npmmirror.com/-/binary/python-build-standalone/20261110/cpython-3.13.18+20261110-x86_64-unknown-linux-gnu-install_only_stripped.tar.gz",
		"temurin/tuna":                      "https://mirrors.tuna.tsinghua.edu.cn/Adoptium/21/jdk/x64/linux/OpenJDK21U-jdk_x64_linux_hotspot_21.0.13_9.tar.gz",
	}
	for _, rw := range c.Mirrors.Rewrites {
		key := rw.Provider + "/" + rw.Operator
		if got := rw.Apply(official[rw.Provider]); got != want[key] {
			t.Errorf("%s: %q, want %q", key, got, want[key])
		}
		delete(want, key)
		if rw.Apply("https://other.example/node-v24.tar.gz") != "" {
			t.Errorf("%s applies to a foreign URL", key)
		}
	}
	if len(want) > 0 {
		t.Errorf("rewrites missing: %v", want)
	}
	if len(c.Mirrors.Systems) == 0 {
		t.Error("no system repositories")
	}
}

func TestMirrorsRejectBadData(t *testing.T) {
	base := `id = "x"
name = "X"
category = "runtime"
lines = [{ version = "1" }]
[[sources]]
method = "standalone"
provider = "p"
releases = ["*"]
lines = ["1"]
`
	op := `[[operator]]
id = "m"
name = "M"
homepage = "https://m.example/"
`
	rewrite := func(provider, pattern, url string) string {
		return "[[rewrite]]\nprovider = \"" + provider + "\"\noperator = \"m\"\npattern = '" + pattern + "'\nurl = '" + url + "'\n"
	}
	approved := op + "approved = \"2026-11-14\"\n"
	for name, mirrors := range map[string]string{
		"operator without approval": op + rewrite("p", `^https://a/(x)$`, "https://m/$1"),
		"unanchored pattern":        approved + rewrite("p", `https://a/(x)`, "https://m/$1"),
		"plain http target":         approved + rewrite("p", `^https://a/(x)$`, "http://m/$1"),
		"unknown provider":          approved + rewrite("q", `^https://a/(x)$`, "https://m/$1"),
		"system without suites":     "[[system]]\ndistro = \"debian\"\nrole = \"main\"\nofficial = \"https://deb.example/debian\"\n",
	} {
		fsys := fstest.MapFS{"software/x.toml": {Data: []byte(base)}, "mirrors.toml": {Data: []byte(mirrors)}}
		if _, err := load(fsys); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	fsys := fstest.MapFS{"software/x.toml": {Data: []byte(base)}, "mirrors.toml": {Data: []byte(approved + rewrite("p", `^https://a/(x)$`, "https://m/$1"))}}
	if _, err := load(fsys); err != nil {
		t.Errorf("valid mirrors refused: %v", err)
	}
}
