// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Kobayashi-classmate/Cairn/catalog"
	"github.com/Kobayashi-classmate/Cairn/internal/execx"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/sources"
)

// Runtime error codes.
const (
	CodeRuntimeMetadata = "pkg.metadata_failed"
	CodeRuntimeDownload = "pkg.download_failed"
	CodeRuntimeChecksum = "pkg.checksum_mismatch"
	CodeRuntimeArchive  = "pkg.archive_invalid"
	CodeRuntimeBroken   = "pkg.runtime_broken"
)

// LockRuntimes serializes runtime installs (downloads of hundreds of MB).
const LockRuntimes = "runtimes"

const (
	maxMetadata    = 32 << 20
	maxArchive     = 1536 << 20
	maxExtracted   = 6 << 30
	maxEntries     = 200000
	downloadWindow = 45 * time.Minute
)

// Replaceable in tests: runs a freshly unpacked runtime to check that it
// works on this system and is the expected version.
var versionCommand = func(ctx context.Context, bin string, args ...string) (string, error) {
	res, err := execx.Run(ctx, execx.Cmd{Name: bin, Args: args, Timeout: time.Minute, MaxOutput: 64 << 10})
	return string(res.Stdout) + string(res.Stderr), err
}

// Runtimes installs prebuilt language runtimes (catalog lines with the
// "standalone" method) under Dir, one directory per exact version plus a
// symlink per line (/opt/cairn/runtimes/nodejs/24 -> 24.22.3) for projects
// to use. Exact versions and checksums come from each project's official
// metadata over HTTPS; an archive is unpacked only after its checksum
// matched, and only inside its own directory.
type Runtimes struct {
	Dir  string // /opt/cairn/runtimes
	Arch string // Go architecture
	HTTP *http.Client
	// Bases overrides the providers' official base URLs (tests).
	Bases map[string]string
	// Mirrors are the catalog's download mirrors; Prober compares them with
	// the official download when the install names no source.
	Mirrors *catalog.Mirrors
	Prober  *sources.Prober
}

// runtimeRelease is the exact build chosen for a line.
type runtimeRelease struct {
	Version string
	URL     string
	Hash    string // hex
	SHA512  bool   // otherwise SHA-256
	// Check is the command (relative to the runtime directory) whose output
	// must contain Version.
	Check []string
}

type provider struct {
	base    string
	resolve func(ctx context.Context, r *Runtimes, base, line string) (runtimeRelease, error)
}

var providers = map[string]provider{
	"nodejs":                  {"https://nodejs.org/dist", resolveNode},
	"go":                      {"https://go.dev/dl", resolveGo},
	"temurin":                 {"https://api.adoptium.net/v3", resolveTemurin},
	"dotnet":                  {"https://builds.dotnet.microsoft.com/dotnet", resolveDotnet},
	"python-build-standalone": {"https://raw.githubusercontent.com/astral-sh/python-build-standalone/latest-release", resolvePython},
}

func (r *Runtimes) client() *http.Client {
	if r.HTTP != nil {
		return r.HTTP
	}
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, ResponseHeaderTimeout: time.Minute}}
}

func (r *Runtimes) get(ctx context.Context, u string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Cairn")
	res, err := r.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", u, res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err == nil && int64(len(data)) > limit {
		err = fmt.Errorf("%s: response too large", u)
	}
	return data, err
}

func (r *Runtimes) getJSON(ctx context.Context, u string, v any) error {
	data, err := r.get(ctx, u, maxMetadata)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// resolve asks the provider for the newest build of line.
func (r *Runtimes) resolve(ctx context.Context, providerID, line string) (runtimeRelease, error) {
	p, ok := providers[providerID]
	if !ok {
		return runtimeRelease{}, pkgErr(helperapi.CodePkgNotAvailable, map[string]any{"provider": providerID})
	}
	base := p.base
	if b := r.Bases[providerID]; b != "" {
		base = b
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	rel, err := p.resolve(ctx, r, strings.TrimSuffix(base, "/"), line)
	if err != nil {
		var he *helperapi.Error
		if errors.As(err, &he) {
			return rel, err
		}
		return rel, pkgErr(CodeRuntimeMetadata, map[string]any{"provider": providerID, "reason": tailString(err.Error(), 300)})
	}
	if u, err := url.Parse(rel.URL); err != nil || u.Scheme != "https" || len(rel.Hash) < 64 {
		return rel, pkgErr(CodeRuntimeMetadata, map[string]any{"provider": providerID, "reason": "metadata without an https URL or checksum"})
	}
	return rel, nil
}

func archOf(goarch string, names map[string]string) (string, error) {
	if a, ok := names[goarch]; ok {
		return a, nil
	}
	return "", pkgErr(helperapi.CodePkgNotAvailable, map[string]any{"arch": goarch})
}

func resolveNode(ctx context.Context, r *Runtimes, base, line string) (runtimeRelease, error) {
	arch, err := archOf(r.Arch, map[string]string{"amd64": "x64", "arm64": "arm64"})
	if err != nil {
		return runtimeRelease{}, err
	}
	var index []struct {
		Version string   `json:"version"`
		Files   []string `json:"files"`
	}
	if err := r.getJSON(ctx, base+"/index.json", &index); err != nil {
		return runtimeRelease{}, err
	}
	// index.json lists releases newest first.
	for _, e := range index {
		if !strings.HasPrefix(e.Version, "v"+line+".") || !slices.Contains(e.Files, "linux-"+arch) {
			continue
		}
		file := "node-" + e.Version + "-linux-" + arch + ".tar.gz"
		sums, err := r.get(ctx, base+"/"+e.Version+"/SHASUMS256.txt", 1<<20)
		if err != nil {
			return runtimeRelease{}, err
		}
		sum := findSum(string(sums), file)
		if sum == "" {
			return runtimeRelease{}, fmt.Errorf("no checksum for %s", file)
		}
		v := strings.TrimPrefix(e.Version, "v")
		return runtimeRelease{Version: v, URL: base + "/" + e.Version + "/" + file, Hash: sum, Check: []string{"bin/node", "--version"}}, nil
	}
	return runtimeRelease{}, fmt.Errorf("no Node.js %s release for linux-%s", line, arch)
}

func resolveGo(ctx context.Context, r *Runtimes, base, line string) (runtimeRelease, error) {
	var releases []struct {
		Version string `json:"version"`
		Stable  bool   `json:"stable"`
		Files   []struct {
			Filename, OS, Arch, SHA256, Kind string
		} `json:"files"`
	}
	if err := r.getJSON(ctx, base+"/?mode=json&include=all", &releases); err != nil {
		return runtimeRelease{}, err
	}
	// Listed newest first.
	for _, rel := range releases {
		if !rel.Stable || (rel.Version != "go"+line && !strings.HasPrefix(rel.Version, "go"+line+".")) {
			continue
		}
		for _, f := range rel.Files {
			if f.OS == "linux" && f.Arch == r.Arch && f.Kind == "archive" {
				v := strings.TrimPrefix(rel.Version, "go")
				return runtimeRelease{Version: v, URL: base + "/" + f.Filename, Hash: f.SHA256, Check: []string{"bin/go", "version"}}, nil
			}
		}
	}
	return runtimeRelease{}, fmt.Errorf("no Go %s release for linux/%s", line, r.Arch)
}

func resolveTemurin(ctx context.Context, r *Runtimes, base, line string) (runtimeRelease, error) {
	arch, err := archOf(r.Arch, map[string]string{"amd64": "x64", "arm64": "aarch64"})
	if err != nil {
		return runtimeRelease{}, err
	}
	var assets []struct {
		Binary struct {
			Package struct {
				Link     string `json:"link"`
				Checksum string `json:"checksum"`
			} `json:"package"`
		} `json:"binary"`
		Version struct {
			// As the JDK reports itself: "21.0.13+9-LTS", "1.8.0_472-b08".
			OpenJDK string `json:"openjdk_version"`
		} `json:"version"`
	}
	q := fmt.Sprintf("%s/assets/latest/%s/hotspot?architecture=%s&image_type=jdk&os=linux&vendor=eclipse", base, url.PathEscape(line), arch)
	if err := r.getJSON(ctx, q, &assets); err != nil {
		return runtimeRelease{}, err
	}
	if len(assets) == 0 {
		return runtimeRelease{}, fmt.Errorf("no Temurin %s build for linux/%s", line, arch)
	}
	a := assets[0]
	return runtimeRelease{Version: a.Version.OpenJDK, URL: a.Binary.Package.Link, Hash: a.Binary.Package.Checksum,
		Check: []string{"bin/java", "-version"}}, nil
}

func resolveDotnet(ctx context.Context, r *Runtimes, base, line string) (runtimeRelease, error) {
	arch, err := archOf(r.Arch, map[string]string{"amd64": "x64", "arm64": "arm64"})
	if err != nil {
		return runtimeRelease{}, err
	}
	var meta struct {
		Releases []struct {
			SDK struct {
				Version string `json:"version"`
				Files   []struct {
					Name, RID, URL, Hash string
				} `json:"files"`
			} `json:"sdk"`
		} `json:"releases"`
	}
	if err := r.getJSON(ctx, base+"/release-metadata/"+url.PathEscape(line)+".0/releases.json", &meta); err != nil {
		return runtimeRelease{}, err
	}
	// The newest release comes first; its SDK includes the runtimes.
	for _, rel := range meta.Releases {
		for _, f := range rel.SDK.Files {
			if f.RID == "linux-"+arch && strings.HasSuffix(f.Name, ".tar.gz") {
				return runtimeRelease{Version: rel.SDK.Version, URL: f.URL, Hash: f.Hash, SHA512: true, Check: []string{"dotnet", "--list-sdks"}}, nil
			}
		}
	}
	return runtimeRelease{}, fmt.Errorf("no .NET %s SDK for linux-%s", line, arch)
}

func resolvePython(ctx context.Context, r *Runtimes, base, line string) (runtimeRelease, error) {
	triple, err := archOf(r.Arch, map[string]string{"amd64": "x86_64-unknown-linux-gnu", "arm64": "aarch64-unknown-linux-gnu"})
	if err != nil {
		return runtimeRelease{}, err
	}
	var latest struct {
		Tag    string `json:"tag"`
		Prefix string `json:"asset_url_prefix"`
	}
	if err := r.getJSON(ctx, base+"/latest-release.json", &latest); err != nil {
		return runtimeRelease{}, err
	}
	if latest.Tag == "" || !strings.HasPrefix(latest.Prefix, "https://") {
		return runtimeRelease{}, errors.New("unexpected latest-release.json")
	}
	sums, err := r.get(ctx, latest.Prefix+"/SHA256SUMS", 4<<20)
	if err != nil {
		return runtimeRelease{}, err
	}
	re := regexp.MustCompile(`^([0-9a-f]{64})\s+(cpython-` + regexp.QuoteMeta(line) + `\.(\d+)\+` + regexp.QuoteMeta(latest.Tag) +
		`-` + regexp.QuoteMeta(triple) + `-install_only_stripped\.tar\.gz)$`)
	best, bestPatch := runtimeRelease{}, -1
	for _, l := range strings.Split(string(sums), "\n") {
		m := re.FindStringSubmatch(strings.TrimSpace(l))
		if m == nil {
			continue
		}
		if patch, _ := strconv.Atoi(m[3]); patch > bestPatch {
			bestPatch = patch
			best = runtimeRelease{Version: fmt.Sprintf("%s.%d", line, patch), URL: latest.Prefix + "/" + url.PathEscape(m[2]),
				Hash: m[1], Check: []string{"bin/python3", "--version"}}
		}
	}
	if bestPatch < 0 {
		return runtimeRelease{}, fmt.Errorf("no Python %s build in release %s for %s", line, latest.Tag, triple)
	}
	return best, nil
}

// findSum returns the checksum of file in a sha256sum-style list.
func findSum(list, file string) string {
	for _, l := range strings.Split(list, "\n") {
		f := strings.Fields(l)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == file {
			return f[0]
		}
	}
	return ""
}

// manifest records an installed runtime next to its directory.
type manifest struct {
	Software string `json:"software"`
	Line     string `json:"line"`
	Version  string `json:"version"`
	URL      string `json:"url"` // the official download
	Hash     string `json:"hash"`
	// Source is the candidate it was downloaded from, and From its address.
	Source      string `json:"source,omitempty"`
	From        string `json:"from,omitempty"`
	InstalledAt int64  `json:"installedAt"`
}

// candidates lists the sources of a runtime download: the official address
// first, then the catalog's mirrors of the provider.
func (r *Runtimes) candidates(providerID, official string) []sources.Candidate {
	out := []sources.Candidate{{ID: sources.IDOfficial, Kind: sources.KindOfficial, Name: sources.Host(official), URL: official}}
	if r.Mirrors == nil {
		return out
	}
	for _, rw := range r.Mirrors.Rewrites {
		u := rw.Apply(official)
		if rw.Provider != providerID || u == "" {
			continue
		}
		op, _ := r.Mirrors.Operator(rw.Operator)
		kind := sources.KindSupplementary
		if op.Official {
			kind = sources.KindOfficialMirror
		}
		out = append(out, sources.Candidate{ID: op.ID, Kind: kind, Name: op.Name, URL: u})
	}
	return out
}

func (r *Runtimes) prober() *sources.Prober {
	if r.Prober != nil {
		return r.Prober
	}
	return defaultProber
}

// sourceOrder puts the requested source first and the other allowed ones
// after it as fallbacks. Without a requested source it probes the allowed
// candidates and starts with the fastest one that serves the official file.
func (r *Runtimes) sourceOrder(ctx context.Context, log io.Writer, cands []sources.Candidate, source, policy string) ([]sources.Candidate, error) {
	cands = filter(cands, allowedBy(policy))
	if source != "" {
		i := candidateIndex(cands, source)
		if i < 0 {
			return nil, pkgErr(helperapi.CodePkgSourceUnknown, map[string]any{"source": source})
		}
		return append([]sources.Candidate{cands[i]}, append(slices.Clone(cands[:i]), cands[i+1:]...)...), nil
	}
	if len(cands) == 1 {
		return cands, nil
	}
	pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	r.prober().ProbeFiles(pctx, cands)
	cancel()
	for _, c := range cands {
		_, _ = fmt.Fprintf(log, "  source %s: %s%s\n", c.ID, c.Status, speedNote(c))
	}
	var eligible []sources.Candidate
	for _, c := range cands {
		if c.Eligible {
			eligible = append(eligible, c)
		}
	}
	if len(eligible) == 0 {
		return nil, pkgErr(helperapi.CodePkgSourceUnusable, map[string]any{"file": fileName(cands[0].URL)})
	}
	sort.SliceStable(eligible, func(a, b int) bool { return eligible[a].Speed > eligible[b].Speed })
	return eligible, nil
}

func speedNote(c sources.Candidate) string {
	if c.Speed <= 0 {
		return ""
	}
	return fmt.Sprintf(", %.1f MB/s", float64(c.Speed)/(1<<20))
}

var versionDir = regexp.MustCompile(`^[0-9][0-9A-Za-z.+_-]{0,63}$`)

func (r *Runtimes) swDir(sw string) string { return filepath.Join(r.Dir, sw) }

// Installed returns the version a line's symlink points to, if any.
func (r *Runtimes) Installed(sw, line string) string {
	target, err := os.Readlink(filepath.Join(r.swDir(sw), line))
	if err != nil || !versionDir.MatchString(target) {
		return ""
	}
	if _, err := os.Stat(filepath.Join(r.swDir(sw), target+".json")); err != nil {
		return ""
	}
	return target
}

// Install fetches, verifies and unpacks the newest build of line, from
// source (a candidate ID; "" picks the fastest allowed one) and, should that
// fail, from the other allowed sources. Every source must deliver the file
// with the official checksum.
func (r *Runtimes) Install(ctx context.Context, log io.Writer, sw, providerID, line, source, policy string) (string, error) {
	logf := func(format string, a ...any) { _, _ = fmt.Fprintf(log, format, a...) }
	rel, err := r.resolve(ctx, providerID, line)
	if err != nil {
		return "", err
	}
	if !versionDir.MatchString(rel.Version) {
		return "", pkgErr(CodeRuntimeMetadata, map[string]any{"provider": providerID, "reason": "unexpected version " + rel.Version})
	}
	logf("Newest %s %s build: %s\n  %s\n  %s %s (official)\n", sw, line, rel.Version, rel.URL, map[bool]string{true: "SHA-512", false: "SHA-256"}[rel.SHA512], rel.Hash)
	if r.Installed(sw, line) == rel.Version {
		logf("Already installed.\n")
		return rel.Version, nil
	}
	if err := os.MkdirAll(r.swDir(sw), 0o755); err != nil { //nolint:gosec // G301: runtimes are readable by every site user.
		return "", err
	}
	order, err := r.sourceOrder(ctx, log, r.candidates(providerID, rel.URL), source, policy)
	if err != nil {
		return "", err
	}
	var archive string
	var used sources.Candidate
	for i, c := range order {
		logf("Downloading from %s (%s):\n  %s\n", c.Name, c.ID, c.URL)
		from := rel
		from.URL = c.URL
		archive, err = r.download(ctx, log, from)
		if err == nil {
			used = c
			break
		}
		if ctx.Err() != nil || i == len(order)-1 {
			return "", err
		}
		logf("Download from %s failed (%s); trying %s.\n", c.ID, errorText(err), order[i+1].ID)
	}
	defer func() { _ = os.Remove(archive) }()

	staging, err := os.MkdirTemp(r.swDir(sw), ".staging-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(staging) }()
	if err := os.Chmod(staging, 0o755); err != nil { //nolint:gosec // G302: as above.
		return "", err
	}
	logf("Unpacking.\n")
	if err := extractTarGz(archive, staging); err != nil {
		return "", pkgErr(CodeRuntimeArchive, map[string]any{"reason": tailString(err.Error(), 300)})
	}
	root := singleTopDir(staging)
	out, err := versionCommand(ctx, filepath.Join(root, rel.Check[0]), rel.Check[1:]...)
	if err != nil || !strings.Contains(out, rel.Version) {
		return "", pkgErr(CodeRuntimeBroken, map[string]any{"version": rel.Version, "output": tailString(strings.TrimSpace(out), 300)})
	}
	logf("Checked: %s\n", strings.TrimSpace(strings.SplitN(out, "\n", 2)[0]))

	final := filepath.Join(r.swDir(sw), rel.Version)
	_ = os.RemoveAll(final) // a leftover of an interrupted attempt
	if err := os.Rename(root, final); err != nil {
		return "", err
	}
	m, _ := json.Marshal(manifest{Software: sw, Line: line, Version: rel.Version, URL: rel.URL, Hash: rel.Hash,
		Source: used.ID, From: used.URL, InstalledAt: time.Now().Unix()})
	if err := writeFile(final+".json", m); err != nil {
		return "", err
	}
	if err := switchLink(filepath.Join(r.swDir(sw), line), rel.Version); err != nil {
		return "", err
	}
	logf("Installed in %s (line path %s).\n", final, filepath.Join(r.swDir(sw), line))
	r.pruneLine(sw, line, rel.Version)
	return rel.Version, nil
}

// Remove deletes a line's symlink and every version installed for it.
func (r *Runtimes) Remove(sw, line string) (string, error) {
	v := r.Installed(sw, line)
	if v == "" {
		return "", pkgErr(helperapi.CodePkgNotInstalled, map[string]any{"software": sw, "line": line})
	}
	if err := os.Remove(filepath.Join(r.swDir(sw), line)); err != nil {
		return "", err
	}
	r.pruneLine(sw, line, "")
	return v, nil
}

// pruneLine removes versions of line other than keep.
func (r *Runtimes) pruneLine(sw, line, keep string) {
	entries, _ := os.ReadDir(r.swDir(sw))
	for _, e := range entries {
		v, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || v == keep || !versionDir.MatchString(v) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(r.swDir(sw), e.Name())) //nolint:gosec // G304: inside the runtimes directory.
		var m manifest
		if err != nil || json.Unmarshal(data, &m) != nil || m.Line != line {
			continue
		}
		_ = os.RemoveAll(filepath.Join(r.swDir(sw), v))
		_ = os.Remove(filepath.Join(r.swDir(sw), e.Name()))
	}
}

// switchLink points link at target atomically.
func switchLink(link, target string) error {
	tmp := link + ".new"
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, link)
}

// singleTopDir returns the only directory inside dir when the archive had
// one top-level directory (node-v24.../, go/, jdk-21.../, python/), else dir.
func singleTopDir(dir string) string {
	entries, err := os.ReadDir(dir)
	if err == nil && len(entries) == 1 && entries[0].IsDir() {
		return filepath.Join(dir, entries[0].Name())
	}
	return dir
}

func (r *Runtimes) download(ctx context.Context, log io.Writer, rel runtimeRelease) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, downloadWindow)
	defer cancel()
	failed := func(err error) error {
		return pkgErr(CodeRuntimeDownload, map[string]any{"url": rel.URL, "reason": tailString(err.Error(), 300)})
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rel.URL, nil)
	if err != nil {
		return "", failed(err)
	}
	req.Header.Set("User-Agent", "Cairn")
	res, err := r.client().Do(req)
	if err != nil {
		return "", failed(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", failed(fmt.Errorf("HTTP %d", res.StatusCode))
	}
	if err := os.MkdirAll(filepath.Join(r.Dir, ".downloads"), 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(filepath.Join(r.Dir, ".downloads"), "archive-*")
	if err != nil {
		return "", err
	}
	h := sha256.New()
	if rel.SHA512 {
		h = sha512.New()
	}
	pw := &progressWriter{log: log, total: res.ContentLength, start: time.Now()}
	n, err := io.Copy(io.MultiWriter(f, h, pw), io.LimitReader(res.Body, maxArchive+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > maxArchive {
		err = errors.New("archive too large")
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return "", failed(err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, rel.Hash) {
		_ = os.Remove(f.Name())
		return "", pkgErr(CodeRuntimeChecksum, map[string]any{"url": rel.URL, "want": rel.Hash, "got": got})
	}
	_, _ = fmt.Fprintf(log, "Downloaded %d MB in %s; checksum matches.\n", n>>20, time.Since(pw.start).Round(time.Second))
	return f.Name(), nil
}

// progressWriter logs download progress in steps of 10 %.
type progressWriter struct {
	log   io.Writer
	total int64
	done  int64
	step  int64
	start time.Time
}

func (p *progressWriter) Write(b []byte) (int, error) {
	p.done += int64(len(b))
	if p.total > 0 {
		if s := p.done * 10 / p.total; s > p.step {
			p.step = s
			_, _ = fmt.Fprintf(p.log, "  %d%% of %d MB\n", s*10, p.total>>20)
		}
	}
	return len(b), nil
}

// extractTarGz unpacks archive into dir. Every path is confined to dir by
// os.Root; links may not point outside the tree; special files are refused;
// permissions are masked to at most 0755.
func extractTarGz(archive, dir string) error {
	f, err := os.Open(archive) //nolint:gosec // G304: our own download.
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	tr := tar.NewReader(gz)
	var total int64
	for entries := 0; ; entries++ {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if entries > maxEntries {
			return errors.New("too many entries")
		}
		name := path.Clean(strings.TrimPrefix(hdr.Name, "./"))
		if name == "." {
			continue
		}
		if path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("unsafe path %q", hdr.Name)
		}
		if d := path.Dir(name); d != "." {
			if err := root.MkdirAll(d, 0o755); err != nil {
				return err
			}
		}
		mode := os.FileMode(hdr.Mode).Perm() & 0o755 //nolint:gosec // G115: tar mode bits, masked.
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(name, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			total += hdr.Size
			if total > maxExtracted {
				return errors.New("archive expands too much")
			}
			out, err := root.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode|0o400)
			if err != nil {
				return err
			}
			// G305: every path goes through os.Root, which refuses names
			// that leave dir, and link targets were checked above.
			_, err = io.Copy(out, io.LimitReader(tr, hdr.Size)) //nolint:gosec // G305: see above.
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
			if err := root.Chmod(name, mode|0o400); err != nil {
				return err
			}
		case tar.TypeSymlink:
			resolved := path.Clean(path.Join(path.Dir(name), hdr.Linkname)) //nolint:gosec // G305: this is the check.
			if path.IsAbs(hdr.Linkname) || resolved == ".." || strings.HasPrefix(resolved, "../") {
				return fmt.Errorf("link %q points outside the archive", hdr.Name)
			}
			if err := root.Symlink(hdr.Linkname, name); err != nil {
				return err
			}
		case tar.TypeLink:
			// Hard links name another entry of the archive; os.Root keeps
			// both ends inside dir.
			if err := root.Link(path.Clean(strings.TrimPrefix(hdr.Linkname, "./")), name); err != nil {
				return err
			}
		case tar.TypeXGlobalHeader:
		default:
			return fmt.Errorf("unsupported entry %q (type %c)", hdr.Name, hdr.Typeflag)
		}
	}
}

// runtimeStatus lists installed standalone lines for pkg.status.
func (r *Runtimes) status(sw string, lines []string) map[string][]helperapi.InstalledPackage {
	out := map[string][]helperapi.InstalledPackage{}
	sort.Strings(lines)
	for _, l := range lines {
		if v := r.Installed(sw, l); v != "" {
			out[l] = []helperapi.InstalledPackage{{Name: sw, Version: v}}
		}
	}
	return out
}

// errorText is a short form of an install error for the log.
func errorText(err error) string {
	var he *helperapi.Error
	if errors.As(err, &he) {
		if r, ok := he.Params["reason"].(string); ok {
			return he.Code + ": " + r
		}
		return he.Code
	}
	return tailString(err.Error(), 200)
}
