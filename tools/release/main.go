// SPDX-License-Identifier: AGPL-3.0-only

// Command release is the maintainers' publishing tool. It creates the release
// signing key, writes checksums, signs files and maintains the signed feed
// that every panel reads (releases.json, announcements.json). It is not part
// of any package.
//
//	release keygen -out FILE
//	release pubkey -key FILE
//	release sums -out FILE PATH...
//	release sign -key FILE PATH...
//	release verify [-keys FILE] PATH...
//	release installer [-keys FILE] -in packaging/install.sh -out FILE
//	release releases [-keys FILE] [-in FILE] -version V -assets-url URL
//	                 [-security] [-notes LANG=FILE]... -out FILE PACKAGE...
//	release announcements -in FILE -out FILE
//
// -keys names a key list file in the format of internal/cloud/trusted_keys.txt
// and replaces the built-in keys (for tests and self-hosted mirrors).
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/cloud"
	"github.com/Kobayashi-classmate/Cairn/internal/semver"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "release:", err)
		os.Exit(1)
	}
}

// now is replaceable in tests.
var now = time.Now

func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: release keygen|pubkey|sums|sign|verify|installer|releases|announcements [flags]")
	}
	cmds := map[string]func([]string, io.Writer) error{
		"keygen": keygen, "pubkey": pubkey, "sums": sums, "sign": sign, "verify": verify,
		"installer": installer, "releases": releases, "announcements": announcements,
	}
	cmd, ok := cmds[args[0]]
	if !ok {
		return fmt.Errorf("unknown command %q", args[0])
	}
	return cmd(args[1:], out)
}

func flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// ---- keys ------------------------------------------------------------------

func keygen(args []string, out io.Writer) error {
	fs := flags("keygen")
	file := fs.String("out", "", "where to write the private key (must not exist)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *file == "" {
		return errors.New("keygen: -out is required")
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(*file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("keygen: %w (an existing key is never overwritten)", err)
	}
	if err := pem.Encode(f, &pem.Block{Type: "PRIVATE KEY", Bytes: der}); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, `Private key written to %s (mode 0600). Keep it offline and backed up;
anyone holding it can publish updates that every Cairn installation accepts.

Public key: %s
Key ID:     %s

Next steps:
  1. Add the public key as a line to internal/cloud/trusted_keys.txt and commit it.
  2. Store the private key file's content as the GitHub Actions secret
     CAIRN_RELEASE_KEY of the repository (Settings > Secrets and variables).
`, *file, base64.StdEncoding.EncodeToString(pub), cloud.KeyID(pub))
	return err
}

func loadPrivateKey(file string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("%s: not a PEM private key", file)
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	priv, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%s: not an ed25519 key", file)
	}
	return priv, nil
}

func pubkey(args []string, out io.Writer) error {
	fs := flags("pubkey")
	keyFile := fs.String("key", "", "private key file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	priv, err := loadPrivateKey(*keyFile)
	if err != nil {
		return err
	}
	pub := priv.Public().(ed25519.PublicKey)
	_, err = fmt.Fprintf(out, "%s %s\n", base64.StdEncoding.EncodeToString(pub), cloud.KeyID(pub))
	return err
}

// trustedKeys returns the keys of the list file, or the built-in keys.
func trustedKeys(file string) ([]string, error) {
	keys := cloud.BuiltinKeys
	if file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		keys = cloud.ParseKeyList(string(data))
	}
	if len(keys) == 0 {
		return nil, errors.New("no trusted keys: run `make release-keygen` and add the public key to internal/cloud/trusted_keys.txt")
	}
	if _, err := cloud.KeyringOf(keys); err != nil {
		return nil, err
	}
	return keys, nil
}

func keyring(file string) (cloud.Keyring, error) {
	keys, err := trustedKeys(file)
	if err != nil {
		return nil, err
	}
	return cloud.KeyringOf(keys)
}

// ---- checksums and signatures ------------------------------------------

func fileSHA256(p string) (string, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, err
}

// sums writes a SHA256SUMS file (sha256sum format, base names only).
func sums(args []string, _ io.Writer) error {
	fs := flags("sums")
	file := fs.String("out", "", "output file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *file == "" || fs.NArg() == 0 {
		return errors.New("sums: -out and at least one file are required")
	}
	lines := map[string]string{}
	for _, p := range fs.Args() {
		name := filepath.Base(p)
		if _, dup := lines[name]; dup {
			return fmt.Errorf("sums: two files named %s", name)
		}
		sum, _, err := fileSHA256(p)
		if err != nil {
			return err
		}
		lines[name] = sum + "  " + name + "\n"
	}
	names := make([]string, 0, len(lines))
	for n := range lines {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		b.WriteString(lines[n])
	}
	return os.WriteFile(*file, []byte(b.String()), 0o644)
}

func sign(args []string, out io.Writer) error {
	fs := flags("sign")
	keyFile := fs.String("key", "", "private key file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	priv, err := loadPrivateKey(*keyFile)
	if err != nil {
		return err
	}
	pub := priv.Public().(ed25519.PublicKey)
	self := cloud.Keyring{cloud.KeyID(pub): pub}
	for _, p := range fs.Args() {
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sig := cloud.Sign(data, priv)
		if err := self.Verify(data, sig); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		if err := os.WriteFile(p+".sig", sig, 0o644); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(out, "signed %s with key %s\n", p, cloud.KeyID(pub)); err != nil {
			return err
		}
	}
	return nil
}

func verify(args []string, out io.Writer) error {
	fs := flags("verify")
	keysFile := fs.String("keys", "", "key list file (default: built-in keys)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	kr, err := keyring(*keysFile)
	if err != nil {
		return err
	}
	for _, p := range fs.Args() {
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sig, err := os.ReadFile(p + ".sig")
		if err != nil {
			return err
		}
		if err := kr.Verify(data, sig); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		if _, err := fmt.Fprintf(out, "%s: signature OK\n", p); err != nil {
			return err
		}
	}
	return nil
}

// ---- installer -------------------------------------------------------------

var keysLine = regexp.MustCompile(`(?m)^TRUSTED_KEYS=""$`)

// installer fills the trusted keys into the online installer.
func installer(args []string, _ io.Writer) error {
	fs := flags("installer")
	keysFile := fs.String("keys", "", "key list file (default: built-in keys)")
	in := fs.String("in", "packaging/install.sh", "installer template")
	file := fs.String("out", "", "output file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	keys, err := trustedKeys(*keysFile)
	if err != nil {
		return err
	}
	src, err := os.ReadFile(*in)
	if err != nil {
		return err
	}
	if n := len(keysLine.FindAllIndex(src, -1)); n != 1 {
		return fmt.Errorf("installer: expected one empty TRUSTED_KEYS line in %s, found %d", *in, n)
	}
	filled := keysLine.ReplaceAll(src, []byte(`TRUSTED_KEYS="`+strings.Join(keys, " ")+`"`))
	return os.WriteFile(*file, filled, 0o755)
}

// ---- releases.json ---------------------------------------------------------

var (
	debArch = regexp.MustCompile(`_(amd64|arm64|loong64)\.deb$`)
	rpmArch = regexp.MustCompile(`\.(x86_64|aarch64|loongarch64)\.rpm$`)
	rpmToGo = map[string]string{"x86_64": "amd64", "aarch64": "arm64", "loongarch64": "loong64"}
)

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// readSigned loads a signed feed file and checks its signature.
func readSigned(file string, kr cloud.Keyring, v any) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(file + ".sig")
	if err != nil {
		return err
	}
	if err := kr.Verify(data, sig); err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	return json.Unmarshal(data, v)
}

func writeJSON(file string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(file, append(data, '\n'), 0o644)
}

// releases adds one release to the index (replacing an entry with the same
// version) and writes the result.
func releases(args []string, _ io.Writer) error {
	fs := flags("releases")
	keysFile := fs.String("keys", "", "key list file for checking -in (default: built-in keys)")
	in := fs.String("in", "", "current releases.json, with its .sig next to it (omit for the first release)")
	file := fs.String("out", "", "output file")
	version := fs.String("version", "", "release version")
	assetsURL := fs.String("assets-url", "", "base URL the packages are downloaded from")
	security := fs.Bool("security", false, "the release fixes a security problem")
	var notes multiFlag
	fs.Var(&notes, "notes", "LANG=FILE with the release notes in that language (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	v, err := semver.Parse(*version)
	if err != nil || strings.HasPrefix(*version, "v") {
		return fmt.Errorf("releases: -version %q must be MAJOR.MINOR.PATCH[-PRE] without a v", *version)
	}
	if u, err := url.Parse(*assetsURL); err != nil || u.Scheme != "https" && u.Scheme != "http" || u.Host == "" {
		return fmt.Errorf("releases: -assets-url %q must be an absolute URL", *assetsURL)
	}
	if *file == "" || fs.NArg() == 0 {
		return errors.New("releases: -out and at least one package are required")
	}

	idx := cloud.Releases{}
	if *in != "" {
		kr, err := keyring(*keysFile)
		if err != nil {
			return err
		}
		if err := readSigned(*in, kr, &idx); err != nil {
			return err
		}
		if idx.Type != "cairn.releases" {
			return fmt.Errorf("%s: not a release index", *in)
		}
	}

	rel := cloud.Release{Version: v.String(), Channel: "stable", PublishedAt: now().Unix(), Security: *security, Notes: map[string]string{}}
	if len(v.Pre) > 0 {
		rel.Channel = "beta"
	}
	for _, n := range notes {
		lang, p, ok := strings.Cut(n, "=")
		if !ok || lang == "" {
			return fmt.Errorf("releases: -notes %q must be LANG=FILE", n)
		}
		text, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel.Notes[lang] = strings.TrimSpace(string(text))
	}
	base := strings.TrimSuffix(*assetsURL, "/")
	for _, p := range fs.Args() {
		name := filepath.Base(p)
		pkg := cloud.Package{URL: base + "/" + url.PathEscape(name)}
		if m := debArch.FindStringSubmatch(name); m != nil {
			pkg.Format, pkg.Arch = "deb", m[1]
		} else if m := rpmArch.FindStringSubmatch(name); m != nil {
			pkg.Format, pkg.Arch = "rpm", rpmToGo[m[1]]
		} else {
			return fmt.Errorf("releases: cannot tell format and architecture of %s", name)
		}
		if slices.ContainsFunc(rel.Packages, func(q cloud.Package) bool { return q.Format == pkg.Format && q.Arch == pkg.Arch }) {
			return fmt.Errorf("releases: two %s packages for %s", pkg.Format, pkg.Arch)
		}
		if pkg.SHA256, pkg.Size, err = fileSHA256(p); err != nil {
			return err
		}
		rel.Packages = append(rel.Packages, pkg)
	}

	idx.Type = "cairn.releases"
	idx.Releases = slices.DeleteFunc(idx.Releases, func(r cloud.Release) bool { return r.Version == rel.Version })
	idx.Releases = append(idx.Releases, rel)
	for _, r := range idx.Releases {
		if _, err := semver.Parse(r.Version); err != nil {
			return fmt.Errorf("releases: bad version %q in the index", r.Version)
		}
	}
	slices.SortFunc(idx.Releases, func(a, b cloud.Release) int {
		va, _ := semver.Parse(a.Version)
		vb, _ := semver.Parse(b.Version)
		return semver.Compare(vb, va) // newest first
	})
	idx.GeneratedAt = max(now().Unix(), idx.GeneratedAt+1) // panels refuse older indexes
	return writeJSON(*file, idx)
}

// ---- announcements.json ----------------------------------------------------

var announcementID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// announcements validates a hand-edited message list and stamps it for
// publishing. Sign the output with `release sign`.
func announcements(args []string, out io.Writer) error {
	fs := flags("announcements")
	in := fs.String("in", "", "edited announcements file")
	file := fs.String("out", "", "output file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	data, err := os.ReadFile(*in)
	if err != nil {
		return err
	}
	var a cloud.Announcements
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return fmt.Errorf("%s: %w", *in, err)
	}
	seen := map[string]bool{}
	for i := range a.Items {
		it := &a.Items[i]
		if err := checkAnnouncement(it); err != nil {
			return fmt.Errorf("item %d (%q): %w", i, it.ID, err)
		}
		if seen[it.ID] {
			return fmt.Errorf("item %d: duplicate id %q", i, it.ID)
		}
		seen[it.ID] = true
		if it.PublishedAt == 0 {
			it.PublishedAt = now().Unix()
		}
		if it.ExpiresAt != 0 && it.ExpiresAt <= it.PublishedAt {
			return fmt.Errorf("item %q: expiresAt is not after publishedAt", it.ID)
		}
	}
	a.Type = "cairn.announcements"
	a.GeneratedAt = max(now().Unix(), a.GeneratedAt+1)
	if err := writeJSON(*file, a); err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "%d announcements written to %s; sign it before publishing\n", len(a.Items), *file)
	return err
}

func checkAnnouncement(it *cloud.Announcement) error {
	switch {
	case !announcementID.MatchString(it.ID):
		return errors.New("id must match " + announcementID.String())
	case !slices.Contains([]string{"info", "warning", "critical"}, it.Level):
		return fmt.Errorf("level %q is not info, warning or critical", it.Level)
	case !slices.Contains([]string{"release", "security", "maintenance", "general"}, it.Category):
		return fmt.Errorf("category %q is not release, security, maintenance or general", it.Category)
	case strings.TrimSpace(it.Title["en"]) == "" || strings.TrimSpace(it.Body["en"]) == "":
		return errors.New("title and body need English text (\"en\")")
	}
	for _, f := range it.Families {
		if f != "debian" && f != "el" {
			return fmt.Errorf("family %q is not debian or el", f)
		}
	}
	if _, err := semver.Satisfies(semver.Version{}, it.Versions); err != nil {
		return fmt.Errorf("versions: %w", err)
	}
	if it.Link != "" {
		if u, err := url.Parse(it.Link); err != nil || u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("link %q must be an https URL", it.Link)
		}
	}
	return nil
}
