// SPDX-License-Identifier: AGPL-3.0-only

package sources

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// site serves fixed documents by path, with Range support; "html:" bodies
// are served as web pages (the soft 404 some mirrors answer with).
type site struct {
	*httptest.Server
	docs map[string]string
}

func newSite(t *testing.T) *site {
	t.Helper()
	s := &site{docs: map[string]string{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := s.docs[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if html, ok := strings.CutPrefix(body, "html:"); ok {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(html))
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader([]byte(body)))
	}))
	t.Cleanup(s.Close)
	return s
}

// smallSamples lets the tiny test documents count as speed samples.
func smallSamples(t *testing.T) {
	old := minSample
	minSample = 1
	t.Cleanup(func() { minSample = old })
}

func byID(cands []Candidate, id string) Candidate {
	for _, c := range cands {
		if c.ID == id {
			return c
		}
	}
	return Candidate{}
}

func TestProbeFiles(t *testing.T) {
	smallSamples(t)
	s := newSite(t)
	archive := strings.Repeat("x", 3000)
	s.docs["/official/node.tar.gz"] = archive
	s.docs["/good/node.tar.gz"] = archive
	s.docs["/short/node.tar.gz"] = archive[:2000]
	s.docs["/page/node.tar.gz"] = "html:<html>not here</html>"
	cands := []Candidate{
		{ID: IDOfficial, URL: s.URL + "/official/node.tar.gz"},
		{ID: "good", URL: s.URL + "/good/node.tar.gz"},
		{ID: "short", URL: s.URL + "/short/node.tar.gz"},
		{ID: "page", URL: s.URL + "/page/node.tar.gz"},
		{ID: "gone", URL: s.URL + "/gone/node.tar.gz"},
	}
	p := &Prober{Sample: 1024}
	p.ProbeFiles(context.Background(), cands)
	for id, want := range map[string]string{IDOfficial: StatusInSync, "good": StatusInSync, "short": StatusMismatch, "page": StatusNotFound, "gone": StatusNotFound} {
		c := byID(cands, id)
		if c.Status != want || c.Eligible != (want == StatusInSync) {
			t.Errorf("%s: %s eligible=%v, want %s", id, c.Status, c.Eligible, want)
		}
	}
	if c := byID(cands, "good"); c.Speed <= 0 {
		t.Errorf("speed not measured: %+v", c)
	}
	if i := Fastest(cands, func(Candidate) bool { return true }); i != 0 && i != 1 {
		t.Errorf("fastest picked %d", i)
	}
	if i := Fastest(cands, func(c Candidate) bool { return c.ID == "short" }); i != -1 {
		t.Errorf("an ineligible candidate was picked: %d", i)
	}
	// Without the official answer, sizes cannot be compared; the checksum
	// after the download still decides.
	cands = []Candidate{{ID: IDOfficial, URL: s.URL + "/missing"}, {ID: "good", URL: s.URL + "/good/node.tar.gz"}}
	p.ProbeFiles(context.Background(), cands)
	if c := byID(cands, "good"); c.Status != StatusUnverified || !c.Eligible {
		t.Errorf("unverified mirror: %+v", c)
	}
}

func inRelease(date string, hashes map[string]string) string {
	var b strings.Builder
	b.WriteString("-----BEGIN PGP SIGNED MESSAGE-----\nHash: SHA256\n\nOrigin: Test\nCodename: trixie\n")
	b.WriteString("Date: " + date + "\nSHA256:\n")
	for path, h := range hashes {
		fmt.Fprintf(&b, " %s %8d %s\n %s %8d %s.xz\n", h, 1000, path, h+"x", 400, path)
	}
	b.WriteString("-----BEGIN PGP SIGNATURE-----\n\nwsFcBAEB\n-----END PGP SIGNATURE-----\n")
	return b.String()
}

func TestParseRelease(t *testing.T) {
	r, err := ParseRelease([]byte(inRelease("Sat, 12 Sep 2026 07:55:41 UTC", map[string]string{"main/binary-amd64/Packages": "aa"})))
	if err != nil {
		t.Fatal(err)
	}
	if r.Date.UTC().Format(time.RFC3339) != "2026-09-12T07:55:41Z" || r.SHA256["main/binary-amd64/Packages"] != "aa" ||
		r.Sizes["main/binary-amd64/Packages.xz"] != 400 {
		t.Fatalf("%+v", r)
	}
	if _, err := ParseRelease([]byte("<html>nope</html>")); err == nil {
		t.Fatal("a page without an index list was accepted")
	}
}

func TestProbeAPT(t *testing.T) {
	smallSamples(t)
	s := newSite(t)
	const now, older = "Sat, 14 Nov 2026 20:03:59 UTC", "Fri, 13 Nov 2026 04:03:02 UTC"
	put := func(base, suite, date, hash string) {
		s.docs[base+"/dists/"+suite+"/InRelease"] = inRelease(date, map[string]string{"main/binary-amd64/Packages": hash})
		s.docs[base+"/dists/"+suite+"/main/binary-amd64/Packages.xz"] = strings.Repeat("p", 400)
	}
	for _, b := range []string{"/official", "/synced", "/inuse"} {
		put(b, "trixie", now, "h1")
		put(b, "trixie-updates", now, "u1")
	}
	put("/lagging", "trixie", now, "h1")
	put("/lagging", "trixie-updates", older, "u0")
	put("/nomain", "trixie", now, "")
	put("/nomain", "trixie-updates", now, "u1")
	delete(s.docs, "/nomain/dists/trixie/InRelease")
	s.docs["/nomain/dists/trixie/InRelease"] = inRelease(now, map[string]string{"contrib/binary-amd64/Packages": "c"})
	cands := []Candidate{
		{ID: IDOfficial, URL: s.URL + "/official"},
		{ID: "inuse", URL: s.URL + "/inuse/", InUse: true},
		{ID: "synced", URL: s.URL + "/synced"},
		{ID: "lagging", URL: s.URL + "/lagging"},
		{ID: "nomain", URL: s.URL + "/nomain"},
		{ID: "down", URL: s.URL + "/down"},
	}
	p := &Prober{Sample: 256, Shortlist: 10}
	p.ProbeAPT(context.Background(), cands, APTTarget{Suites: []string{"trixie", "trixie-updates"}, Components: []string{"main"}, Arch: "amd64"})
	for id, want := range map[string]string{IDOfficial: StatusInSync, "inuse": StatusInSync, "synced": StatusInSync,
		"lagging": StatusBehind, "nomain": StatusNotFound, "down": StatusNotFound} {
		if c := byID(cands, id); c.Status != want {
			t.Errorf("%s: %s, want %s (%+v)", id, c.Status, want, c)
		}
	}
	if c := byID(cands, "lagging"); c.LagSeconds != 40*3600+57 || c.Eligible {
		t.Errorf("lag: %+v", c)
	}
	if c := byID(cands, "synced"); c.Speed <= 0 || c.IndexTime == 0 {
		t.Errorf("not measured: %+v", c)
	}

	// With a short list, only the fastest to answer are compared; the
	// official and in-use candidates always are.
	cands = []Candidate{
		{ID: IDOfficial, URL: s.URL + "/official"},
		{ID: "inuse", URL: s.URL + "/inuse", InUse: true},
		{ID: "a", URL: s.URL + "/synced"},
		{ID: "b", URL: s.URL + "/lagging"},
		{ID: "c", URL: s.URL + "/down"},
	}
	p = &Prober{Sample: 256, Shortlist: 1}
	p.ProbeAPT(context.Background(), cands, APTTarget{Suites: []string{"trixie", "trixie-updates"}, Components: []string{"main"}, Arch: "amd64"})
	compared, skipped := 0, 0
	for _, c := range cands[2:] {
		switch c.Status {
		case StatusSkipped:
			skipped++
		case StatusInSync, StatusBehind:
			compared++
		}
	}
	if compared != 1 || skipped != 1 || byID(cands, "c").Status != StatusNotFound || byID(cands, "inuse").Status != StatusInSync {
		t.Errorf("shortlist: %+v", cands)
	}
}

func repomd(checksum string, ts int64) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<repomd xmlns="http://linux.duke.edu/metadata/repo">
  <revision>%d</revision>
  <data type="primary">
    <checksum type="sha256">%s</checksum>
    <location href="repodata/%s-primary.xml.gz"/>
    <timestamp>%d</timestamp>
    <size>500</size>
  </data>
  <data type="filelists">
    <checksum type="sha256">ff</checksum>
    <location href="repodata/ff-filelists.xml.gz"/>
    <timestamp>%d</timestamp>
  </data>
</repomd>`, ts, checksum, checksum, ts, ts-10)
}

func TestProbeDNF(t *testing.T) {
	s := newSite(t)
	put := func(base, sum string, ts int64) {
		s.docs[base+"/repodata/repomd.xml"] = repomd(sum, ts)
		s.docs[base+"/repodata/"+sum+"-primary.xml.gz"] = strings.Repeat("r", 500)
	}
	put("/official", "abc", 1794000000)
	put("/synced", "abc", 1794000000)
	put("/old", "old", 1793990000)
	cands := []Candidate{
		{ID: IDOfficial, URL: s.URL + "/official/"},
		{ID: "synced", URL: s.URL + "/synced"},
		{ID: "old", URL: s.URL + "/old/"},
		{ID: "gone", URL: s.URL + "/gone/"},
	}
	(&Prober{Sample: 128}).ProbeDNF(context.Background(), cands)
	for id, want := range map[string]string{IDOfficial: StatusInSync, "synced": StatusInSync, "old": StatusBehind, "gone": StatusNotFound} {
		if c := byID(cands, id); c.Status != want {
			t.Errorf("%s: %s, want %s", id, c.Status, want)
		}
	}
	if c := byID(cands, "old"); c.LagSeconds != 10000 {
		t.Errorf("lag: %+v", c)
	}
	if _, err := ParseRepomd([]byte(`<repomd><data type="primary"><checksum>x</checksum><location href="../../etc/passwd"/></data></repomd>`)); err == nil {
		t.Error("a primary location leaving the repository was accepted")
	}
}

func TestParseAPTSources(t *testing.T) {
	got := ParseAPTSources(map[string][]byte{
		"/etc/apt/sources.list": []byte(`# comment
deb http://archive.ubuntu.com/ubuntu/ jammy main restricted
deb [arch=amd64 signed-by=/k.gpg] http://security.ubuntu.com/ubuntu/ jammy-security main # trailing
deb-src http://archive.ubuntu.com/ubuntu/ jammy main
`),
		"/etc/apt/sources.list.d/debian.sources": []byte(`Types: deb
# http://snapshot.debian.org/archive/debian/20261104T212339Z
URIs: http://deb.debian.org/debian
Suites: trixie trixie-updates
Components: main
Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg

Types: deb deb-src
URIs: http://deb.debian.org/debian-security
Suites: trixie-security
Components: main

Types: deb
URIs: http://disabled.example/debian
Suites: trixie
Components: main
Enabled: no
`),
	})
	var flat []string
	for _, e := range got {
		flat = append(flat, e.URI+" "+e.Suite+" "+strings.Join(e.Components, ","))
	}
	want := []string{
		"http://archive.ubuntu.com/ubuntu/ jammy main,restricted",
		"http://security.ubuntu.com/ubuntu/ jammy-security main",
		"http://deb.debian.org/debian trixie main",
		"http://deb.debian.org/debian trixie-updates main",
		"http://deb.debian.org/debian-security trixie-security main",
	}
	if strings.Join(flat, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s", strings.Join(flat, "\n"))
	}
}

func TestParseRepoFilesAndLists(t *testing.T) {
	secs := ParseRepoFiles(map[string][]byte{"/etc/yum.repos.d/rocky.repo": []byte(`[baseos]
name=Rocky Linux $releasever - BaseOS
mirrorlist=https://mirrors.rockylinux.org/mirrorlist?arch=$basearch&repo=BaseOS-$releasever$rltype
#baseurl=http://dl.rockylinux.org/$contentdir/$releasever/BaseOS/$basearch/os/
enabled=1

[crb]
baseurl=https://a.example/$releasever/
  https://b.example/${releasever}/
enabled=0
`)})
	if len(secs) != 2 || secs[0].ID != "baseos" || !secs[0].Enabled || secs[0].MirrorList == "" || len(secs[0].BaseURLs) != 0 ||
		secs[1].Enabled || len(secs[1].BaseURLs) != 2 {
		t.Fatalf("%+v", secs)
	}
	vars := map[string]string{"releasever": "9", "basearch": "x86_64", "rltype": ""}
	if got := ExpandVars(secs[0].MirrorList, vars); got != "https://mirrors.rockylinux.org/mirrorlist?arch=x86_64&repo=BaseOS-9" {
		t.Errorf("expand: %s", got)
	}
	if got := ExpandVars(secs[1].BaseURLs[1], vars); got != "https://b.example/9/" {
		t.Errorf("expand braces: %s", got)
	}
	if got := ExpandVars("$unknown/x", vars); got != "$unknown/x" {
		t.Errorf("unknown variable: %s", got)
	}
	list := ParseMirrorList([]byte("# repo = rocky-BaseOS-9.8 country = US\nhttp://m1.example/rocky/9/\nftp://old.example/\n## http://disabled.example/\nhttps://m2.example/x/\n"))
	if strings.Join(list, " ") != "http://m1.example/rocky/9/ https://m2.example/x/" {
		t.Errorf("mirror list: %v", list)
	}
	ml := ParseMetalink([]byte(`<?xml version="1.0"?>
<metalink version="3.0" xmlns="http://www.metalinker.org/">
 <files><file name="repomd.xml"><resources maxconnections="1">
  <url protocol="https" type="https" location="US" preference="98">https://b.example/epel/9/Everything/x86_64/repodata/repomd.xml</url>
  <url protocol="rsync" type="rsync" location="US" preference="99">rsync://c.example/epel/9/repodata/repomd.xml</url>
  <url protocol="https" type="https" location="US" preference="100">https://a.example/epel/9/Everything/x86_64/repodata/repomd.xml</url>
 </resources></file></files>
</metalink>`))
	if strings.Join(ml, " ") != "https://a.example/epel/9/Everything/x86_64/ https://b.example/epel/9/Everything/x86_64/" {
		t.Errorf("metalink: %v", ml)
	}
}

func TestSameBase(t *testing.T) {
	if !SameBase("http://deb.debian.org/debian/", "https://deb.debian.org/debian") || SameBase("http://deb.debian.org/debian", "http://deb.debian.org/debian-security") {
		t.Fatal("SameBase")
	}
	if rangeTotal("bytes 0-1023/31234567") != 31234567 || rangeTotal("bytes */0") != 0 || rangeTotal("") != 0 {
		t.Fatal("rangeTotal")
	}
}
