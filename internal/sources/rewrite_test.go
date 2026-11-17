// SPDX-License-Identifier: AGPL-3.0-only

package sources

import (
	"errors"
	"testing"
)

const debianSources = `Types: deb
# http://snapshot.debian.org/archive/debian/20261104T212339Z
URIs: http://deb.debian.org/debian
Suites: trixie trixie-updates
Components: main
Signed-By: /usr/share/keyrings/debian-archive-keyring.pgp

Types: deb
URIs: http://deb.debian.org/debian-security
Suites: trixie-security
Components: main
Signed-By: /usr/share/keyrings/debian-archive-keyring.pgp
`

func TestRewriteAPTDeb822(t *testing.T) {
	files := map[string][]byte{"/etc/apt/sources.list.d/debian.sources": []byte(debianSources)}
	out, n, err := RewriteAPT(files, []string{"trixie", "trixie-updates"}, []string{"trixie-security"},
		"https://deb.debian.org/debian/", "http://ftp.cn.debian.org/debian")
	if err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	want := `Types: deb
# http://snapshot.debian.org/archive/debian/20261104T212339Z
URIs: http://ftp.cn.debian.org/debian
Suites: trixie trixie-updates
Components: main
Signed-By: /usr/share/keyrings/debian-archive-keyring.pgp

Types: deb
URIs: http://deb.debian.org/debian-security
Suites: trixie-security
Components: main
Signed-By: /usr/share/keyrings/debian-archive-keyring.pgp
`
	if got := string(out["/etc/apt/sources.list.d/debian.sources"]); got != want {
		t.Fatalf("got:\n%s", got)
	}
	// The security role leaves the main stanza alone.
	out, n, err = RewriteAPT(files, []string{"trixie-security"}, []string{"trixie", "trixie-updates"},
		"http://deb.debian.org/debian-security", "https://security.debian.org/debian-security")
	if err != nil || n != 1 || string(out["/etc/apt/sources.list.d/debian.sources"]) == debianSources {
		t.Fatalf("%d %v", n, err)
	}
	// A stanza serving both roles from one address cannot move.
	mixed := map[string][]byte{"/etc/apt/sources.list.d/ubuntu.sources": []byte("Types: deb\nURIs: http://archive.ubuntu.com/ubuntu/\nSuites: noble noble-updates\n noble-security\nComponents: main\n")}
	if _, _, err := RewriteAPT(mixed, []string{"noble", "noble-updates"}, []string{"noble-security"}, "http://archive.ubuntu.com/ubuntu", "http://mirror.example/ubuntu"); !errors.Is(err, ErrMixed) {
		t.Fatalf("mixed: %v", err)
	}
	// Nothing matches: no change.
	if out, n, _ := RewriteAPT(files, []string{"bookworm"}, nil, "http://deb.debian.org/debian", "http://x/debian"); n != 0 || len(out) != 0 {
		t.Fatalf("no match changed %d", n)
	}
}

func TestRewriteAPTOneLine(t *testing.T) {
	list := "# main\ndeb [arch=amd64 signed-by=/k.gpg] http://archive.ubuntu.com/ubuntu jammy main restricted\n" +
		"deb-src http://archive.ubuntu.com/ubuntu jammy main\n" +
		"deb http://archive.ubuntu.com/ubuntu jammy-updates main # updates\n" +
		"deb http://security.ubuntu.com/ubuntu jammy-security main\n" +
		"deb http://archive.ubuntu.com/ubuntu jammy-backports main\n"
	out, n, err := RewriteAPT(map[string][]byte{"/etc/apt/sources.list": []byte(list)}, []string{"jammy", "jammy-updates"}, []string{"jammy-security"},
		"http://archive.ubuntu.com/ubuntu/", "https://mirror.example.com/ubuntu")
	if err != nil || n != 3 {
		t.Fatalf("%d %v", n, err)
	}
	want := "# main\ndeb [arch=amd64 signed-by=/k.gpg] https://mirror.example.com/ubuntu jammy main restricted\n" +
		"deb-src https://mirror.example.com/ubuntu jammy main\n" +
		"deb https://mirror.example.com/ubuntu jammy-updates main # updates\n" +
		"deb http://security.ubuntu.com/ubuntu jammy-security main\n" +
		"deb http://archive.ubuntu.com/ubuntu jammy-backports main\n"
	if got := string(out["/etc/apt/sources.list"]); got != want {
		t.Fatalf("got:\n%s", got)
	}
}

const rockyRepo = `[baseos]
name=Rocky Linux $releasever - BaseOS
mirrorlist=https://mirrors.rockylinux.org/mirrorlist?arch=$basearch&repo=BaseOS-$releasever$rltype
#baseurl=http://dl.rockylinux.org/$contentdir/$releasever/BaseOS/$basearch/os/
gpgcheck=1
enabled=1

[appstream]
name=Rocky Linux $releasever - AppStream
mirrorlist=https://mirrors.rockylinux.org/mirrorlist?arch=$basearch&repo=AppStream-$releasever$rltype
#baseurl=http://dl.rockylinux.org/$contentdir/$releasever/AppStream/$basearch/os/
enabled=1

[local]
name=Something else
baseurl=https://example.org/repo/
`

func TestRewriteDNF(t *testing.T) {
	root := MirrorRoot("https://mirror.example.com/rocky/9.6/AppStream/x86_64/os/")
	if root != "https://mirror.example.com/rocky/" {
		t.Fatalf("root %q", root)
	}
	out, n := RewriteDNF(map[string][]byte{"/etc/yum.repos.d/rocky.repo": []byte(rockyRepo)},
		[]string{"https://dl.rockylinux.org/$contentdir/"}, root)
	if n != 2 {
		t.Fatalf("sections %d", n)
	}
	want := `[baseos]
name=Rocky Linux $releasever - BaseOS
#mirrorlist=https://mirrors.rockylinux.org/mirrorlist?arch=$basearch&repo=BaseOS-$releasever$rltype
baseurl=https://mirror.example.com/rocky/$releasever/BaseOS/$basearch/os/
gpgcheck=1
enabled=1

[appstream]
name=Rocky Linux $releasever - AppStream
#mirrorlist=https://mirrors.rockylinux.org/mirrorlist?arch=$basearch&repo=AppStream-$releasever$rltype
baseurl=https://mirror.example.com/rocky/$releasever/AppStream/$basearch/os/
enabled=1

[local]
name=Something else
baseurl=https://example.org/repo/
`
	if got := string(out["/etc/yum.repos.d/rocky.repo"]); got != want {
		t.Fatalf("got:\n%s", got)
	}
	// Switching again starts from the mirror now in use.
	again, n := RewriteDNF(out, []string{"https://dl.rockylinux.org/$contentdir/", root}, "https://other.example/rocky/")
	if n != 2 || string(again["/etc/yum.repos.d/rocky.repo"]) == want {
		t.Fatalf("again %d", n)
	}
	if MirrorRoot("https://example.org/repo/") != "" {
		t.Fatal("odd address got a root")
	}
}
