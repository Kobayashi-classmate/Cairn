// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/Kobayashi-classmate/Cairn/catalog"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/sources"
)

// Installing from another source than the system's own, for one
// transaction only. The system's source files, the apt lists and the dnf
// cache stay as they are: the package manager gets a private copy of the
// source configuration in which the system repositories of the main role
// point at the chosen candidate, its own index directory seeded from the
// system's, and every command of the transaction runs with it. The copy is
// removed afterwards, whatever the outcome.

// systemRoles are the roles the catalog describes for this distribution.
func (s *Software) systemRoles() []string {
	var roles []string
	for _, role := range []string{catalog.RoleMain, catalog.RoleSecurity} {
		if _, ok := s.systemRepo(role); ok {
			roles = append(roles, role)
		}
	}
	return roles
}

// sourceMove is how pointing the system repositories of a role at another
// candidate changes their files. Paths include Root.
type sourceMove struct {
	orig    map[string][]byte // the system's files of the kind, as read
	changed map[string][]byte // the new content of the files that change
	from    []string          // the addresses in use that move
	count   int               // entries or sections changed
}

// Why the system repositories of a role cannot move to a candidate (a
// *sources.MixedError besides these).
var (
	errNoSourceInUse = errors.New("no system source in use")
	errHTTPSRequired = errors.New("a mirror must be reached over HTTPS")
	errMirrorLayout  = errors.New("the mirror's address has an unknown layout")
	errNoEntry       = errors.New("no entry to change")
)

// planMove works out the files that change when the role's repositories
// move to the candidate to. sl is the probed slot of the role (its
// candidates in use are what moves). Nothing is written.
func (s *Software) planMove(sl helperapi.SourceSlot, role string, to sources.Candidate) (sourceMove, error) {
	var mv sourceMove
	for _, c := range sl.Candidates {
		if c.InUse {
			mv.from = append(mv.from, c.URL)
		}
	}
	if len(mv.from) == 0 {
		return mv, errNoSourceInUse
	}
	mv.changed = map[string][]byte{}
	if s.Manager == "apt" {
		mv.orig = s.readConfig(aptSourceFiles...)
		// Entries written as "mirror+file:" lists name the mirror in use
		// indirectly: they move too.
		for _, e := range sources.ParseAPTSources(mv.orig) {
			if r := s.resolveAPTURI(e.URI); r != e.URI && slices.ContainsFunc(mv.from, func(f string) bool { return sources.SameBase(f, r) }) &&
				!slices.Contains(mv.from, e.URI) {
				mv.from = append(mv.from, e.URI)
			}
		}
		var exclude []string
		for _, other := range s.systemRoles() {
			if other == role {
				continue
			}
			sys, _ := s.systemRepo(other)
			for _, su := range sys.Suites {
				exclude = append(exclude, s.expand(su, ""))
			}
		}
		work := maps.Clone(mv.orig)
		for _, f := range mv.from {
			out, n, err := sources.RewriteAPT(work, sl.Suites, exclude, f, strings.TrimSuffix(to.URL, "/"))
			if err != nil {
				return mv, err
			}
			for p, d := range out {
				mv.changed[p] = d
				work[p] = d
			}
			mv.count += n
		}
	} else {
		if !strings.HasPrefix(to.URL, "https://") {
			return mv, errHTTPSRequired
		}
		mirror := sources.MirrorRoot(to.URL)
		if mirror == "" {
			return mv, errMirrorLayout
		}
		sys, _ := s.systemRepo(role)
		roots := slices.Clone(sys.Roots)
		for _, f := range mv.from {
			if r := sources.MirrorRoot(f); r != "" {
				roots = append(roots, r)
			}
		}
		mv.orig = s.readConfig(dnfRepoFiles)
		mv.changed, mv.count = sources.RewriteDNF(mv.orig, roots, mirror)
	}
	if mv.count == 0 {
		return mv, errNoEntry
	}
	return mv, nil
}

// chosenSource is a candidate of the system's main repositories an install
// uses for its own transaction, checked again just before.
type chosenSource struct {
	cand sources.Candidate
	move sourceMove
}

// Reasons of a pkg.source_unusable error about a chosen system source.
const (
	unusableStatus  = "status"         // not eligible: behind, a different index, no answer
	unusableHTTPS   = "https_required" // dnf: the mirror is not reached over HTTPS
	unusableLayout  = "mirror_layout"  // dnf: the mirror's paths are not the distribution's
	unusableNoEntry = "no_entry"       // no repository entry of the system uses the role's suites
	unusableMixed   = "mixed"          // an entry serves the main and the security suites together
	unusableUpdate  = "update_failed"  // apt could not read the source's indexes
	unusableOlder   = "older"          // it has an older version than the system's own source
)

func sourceUnusable(id, reason string, params map[string]any) error {
	p := map[string]any{"source": id, "reason": reason}
	maps.Copy(p, params)
	return pkgErr(helperapi.CodePkgSourceUnusable, p)
}

// checker probes a chosen candidate before an install: like the probe, but
// without the speed samples the install has no use for.
func (s *Software) checker() *sources.Prober {
	p := s.prober()
	return &sources.Prober{HTTP: p.HTTP, Sample: 1, SampleTime: time.Second, Shortlist: p.Shortlist, Parallel: p.Parallel}
}

// chooseSystemSource checks the system source an install names (a
// candidate ID of the install probe's system:main slot) again. It returns
// nil when the candidate is the one in use: the install then runs with the
// system's own configuration, as without a choice. Otherwise the
// candidate must still be eligible (the official index or a newer one);
// there is no fallback to another source.
func (s *Software) chooseSystemSource(ctx context.Context, log io.Writer, id, policy string) (*chosenSource, error) {
	logf := func(format string, a ...any) { _, _ = fmt.Fprintf(log, format, a...) }
	unknown := pkgErr(helperapi.CodePkgSourceUnknown, map[string]any{"source": id})
	if _, ok := s.systemRepo(catalog.RoleMain); !ok {
		return nil, unknown
	}
	ctx, cancel := context.WithTimeout(ctx, probeWindow)
	defer cancel()
	sl := helperapi.SourceSlot{ID: "system:" + catalog.RoleMain, Kind: helperapi.SlotSystem}
	cands := s.systemCandidates(ctx, &sl, catalog.RoleMain, true)
	i := candidateIndex(cands, id)
	if i < 0 || !s.installCandidate(policy)(cands[i]) {
		return nil, unknown
	}
	if cands[i].InUse {
		logf("Package source: %s (%s), the one the system uses.\n", cands[i].Name, cands[i].URL)
		return nil, nil
	}
	logf("Checking the package source %s (%s) against the official one.\n", cands[i].Name, cands[i].URL)
	s.measureWith(ctx, s.checker(), &sl, onlyCandidates(cands, []string{id}))
	c := sl.Candidates[candidateIndex(sl.Candidates, id)]
	if !c.Eligible {
		return nil, sourceUnusable(id, unusableStatus, map[string]any{"status": c.Status})
	}
	mv, err := s.planMove(sl, catalog.RoleMain, c)
	var mixed *sources.MixedError
	switch {
	case errors.As(err, &mixed):
		return nil, sourceUnusable(id, unusableMixed, map[string]any{"file": strings.TrimPrefix(mixed.File, s.Root)})
	case errors.Is(err, errHTTPSRequired):
		return nil, sourceUnusable(id, unusableHTTPS, nil)
	case errors.Is(err, errMirrorLayout):
		return nil, sourceUnusable(id, unusableLayout, nil)
	case errors.Is(err, errNoEntry), errors.Is(err, errNoSourceInUse):
		return nil, sourceUnusable(id, unusableNoEntry, nil)
	case err != nil:
		return nil, err
	}
	return &chosenSource{cand: c, move: mv}, nil
}

// pkgConfig is the package manager configuration of one transaction: nil
// for the system's own, or a private copy (see privateConfig).
type pkgConfig struct {
	dir    string
	opts   []string // put before the command's own arguments
	source *chosenSource
}

// args puts the configuration's options before a command's arguments.
func (c *pkgConfig) args(a ...string) []string {
	if c == nil {
		return a
	}
	return append(slices.Clone(c.opts), a...)
}

// remove deletes the private copy.
func (c *pkgConfig) remove() {
	if c != nil && c.dir != "" {
		_ = os.RemoveAll(c.dir)
	}
}

const (
	pkgSourcePrefix = "cairn-pkgsrc-"
	// pkgSourceStale is the age at which a private copy left behind (the
	// helper stopped half-way) is removed. No transaction runs that long.
	pkgSourceStale = 24 * time.Hour
)

// privateConfig writes the private source configuration for a chosen
// source. It lives in /var/tmp: apt downloads as the unprivileged _apt
// user, which must reach its index directory, and /var/tmp is on disk
// (the indexes are tens of megabytes; /tmp is often in memory).
func (s *Software) privateConfig(src *chosenSource) (*pkgConfig, error) {
	base := s.path("/var/tmp")
	if err := os.MkdirAll(base, 0o755); err != nil { //nolint:gosec // G301: exists on every system; tests.
		return nil, err
	}
	removeStale(base)
	dir, err := os.MkdirTemp(base, pkgSourcePrefix)
	if err != nil {
		return nil, err
	}
	cfg := &pkgConfig{dir: dir, source: src}
	// Created 0700; _apt has to pass through to its index directory. The
	// content is copies of world-readable system files.
	if err := os.Chmod(dir, 0o755); err != nil { //nolint:gosec // G302: see above.
		cfg.remove()
		return nil, err
	}
	if s.Manager == "apt" {
		err = s.privateAPT(cfg)
	} else {
		err = s.privateDNF(cfg)
	}
	if err != nil {
		cfg.remove()
		return nil, err
	}
	return cfg, nil
}

// removeStale removes private copies older than pkgSourceStale. Only
// directories root owns: anyone can create entries in /var/tmp.
func removeStale(base string) {
	entries, _ := os.ReadDir(base)
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), pkgSourcePrefix) || !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil || time.Since(info.ModTime()) < pkgSourceStale {
			continue
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Uid == 0 {
			_ = os.RemoveAll(filepath.Join(base, e.Name()))
		}
	}
}

// privateAPT writes apt's source list and parts with the main role's
// entries moved, and seeds the index directory with the system's indexes
// (their dates kept: apt asks the servers whether they changed since), so
// that the sources that do not change need no download.
func (s *Software) privateAPT(cfg *pkgConfig) error {
	parts := filepath.Join(cfg.dir, "sources.list.d")
	lists := filepath.Join(cfg.dir, "lists")
	for _, d := range []string{parts, lists} {
		if err := os.Mkdir(d, 0o755); err != nil { //nolint:gosec // G301: read by apt's methods as _apt.
			return err
		}
	}
	mainList := s.path("/etc/apt/sources.list")
	var files []string
	if _, err := os.Stat(mainList); err == nil {
		files = append(files, mainList)
	}
	for _, pat := range aptSourceFiles[1:] {
		m, _ := filepath.Glob(s.path(pat))
		files = append(files, m...)
	}
	for _, f := range files {
		dst := filepath.Join(parts, filepath.Base(f))
		if f == mainList {
			dst = filepath.Join(cfg.dir, "sources.list")
		}
		if err := copyConfigFile(f, dst, cfg.source.move.changed[f]); err != nil {
			return err
		}
	}
	if err := copyTree(s.path("/var/lib/apt/lists"), lists, func(rel string, d fs.DirEntry) bool {
		return !d.IsDir() && rel == d.Name() && d.Name() != "lock" // the indexes, not partial/ or auxfiles/
	}); err != nil {
		return err
	}
	cfg.opts = []string{
		"-o", "Dir::Etc::SourceList=" + filepath.Join(cfg.dir, "sources.list"),
		"-o", "Dir::Etc::SourceParts=" + parts,
		"-o", "Dir::State::Lists=" + lists,
		// apt's binary caches of the indexes, rebuilt for the private ones.
		"-o", "Dir::Cache::pkgcache=" + filepath.Join(cfg.dir, "pkgcache.bin"),
		"-o", "Dir::Cache::srcpkgcache=" + filepath.Join(cfg.dir, "srcpkgcache.bin"),
	}
	return nil
}

// dnfCacheDirs are the system's metadata caches: dnf 4, dnf 5.
var dnfCacheDirs = []string{"/var/cache/dnf", "/var/cache/libdnf5"}

// privateDNF writes every .repo file (the vendor repositories' too) with
// the main role's sections moved, and a private cache seeded with the
// system's metadata: repositories whose address does not change keep
// theirs. The options are the same for dnf 4 (EL 9 and 10) and dnf 5,
// which run as root caches in system_cachedir.
func (s *Software) privateDNF(cfg *pkgConfig) error {
	repos := filepath.Join(cfg.dir, "repos")
	cache := filepath.Join(cfg.dir, "cache")
	for _, d := range []string{repos, cache} {
		if err := os.Mkdir(d, 0o755); err != nil { //nolint:gosec // G301: like the system's own.
			return err
		}
	}
	files, _ := filepath.Glob(s.path(dnfRepoFiles))
	for _, f := range files {
		if err := copyConfigFile(f, filepath.Join(repos, filepath.Base(f)), cfg.source.move.changed[f]); err != nil {
			return err
		}
	}
	for _, d := range dnfCacheDirs {
		if _, err := os.Stat(s.path(d)); err != nil {
			continue
		}
		// Metadata only: no downloaded packages, no lock files.
		err := copyTree(s.path(d), cache, func(rel string, e fs.DirEntry) bool {
			if e.IsDir() {
				return e.Name() != "packages"
			}
			return !strings.HasSuffix(e.Name(), ".pid") && !strings.HasSuffix(e.Name(), ".lock")
		})
		if err != nil {
			return err
		}
		break
	}
	cfg.opts = []string{
		"--setopt=reposdir=" + repos,
		"--setopt=cachedir=" + cache,
		"--setopt=system_cachedir=" + cache,
		// dnf-plugins-core's shell completion cache is written to
		// /var/cache/dnf whatever the cache directory, with the packages of
		// the repositories just read; the next ordinary dnf run renews it.
		"--disableplugin=generate_completion_cache",
	}
	return nil
}

// copyConfigFile copies a system configuration file into the private
// configuration, with content instead of the file's own when given, and
// the file's mode. The private configuration is thrown away after the
// command: nothing is flushed to the disk for it.
func copyConfigFile(src, dst string, content []byte) error {
	info, err := os.Stat(src)
	if err != nil || !info.Mode().IsRegular() {
		return nil //nolint:nilerr // gone or not a file: the package manager would skip it too
	}
	if content == nil {
		if content, err = readSmall(src); err != nil {
			return err
		}
	}
	return writeFileAtomic(dst, content, info.Mode().Perm(), false)
}

// copyTree copies the regular files of src that keep accepts (and the
// directories it accepts on the way) to dst, keeping their modes and
// modification times. Symbolic links are not followed.
func copyTree(src, dst string, keep func(rel string, d fs.DirEntry) bool) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == src && errors.Is(err, fs.ErrNotExist) {
				return filepath.SkipAll
			}
			return err
		}
		if p == src {
			return nil
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if !keep(rel, d) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755) //nolint:gosec // G301: like the system's own.
		}
		if !d.Type().IsRegular() {
			return nil
		}
		return copyFile(p, filepath.Join(dst, rel))
	})
}

func copyFile(src, dst string) error {
	// Not through a link: the walk saw a regular file here.
	in, err := os.OpenFile(src, os.O_RDONLY|syscall.O_NOFOLLOW, 0) //nolint:gosec // G304: system index files.
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm()) //nolint:gosec // G304: inside the private copy.
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil { // copy_file_range: a reflink where the file system has them
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chtimes(dst, info.ModTime(), info.ModTime())
}

// notOlder refuses versions from a chosen source that are older than what
// the system's own indexes offer for the line: a chosen source must never
// install an older version than the system's own source would.
func (s *Software) notOlder(ctx context.Context, cfg *pkgConfig, o catalog.Offer, chosen map[string]string) error {
	own, err := s.candidates(ctx, o, true, nil)
	if err != nil {
		return nil //nolint:nilerr // the system's indexes do not have the line: nothing to compare
	}
	for _, p := range o.Packages {
		if have := own[p]; have != "" && have != chosen[p] && s.versionLess(chosen[p], have) {
			return sourceUnusable(cfg.source.cand.ID, unusableOlder, map[string]any{"package": p, "version": chosen[p], "system": have})
		}
	}
	return nil
}

// versionLess orders package versions as the package manager does.
func (s *Software) versionLess(a, b string) bool {
	if s.Manager == "apt" {
		return debVersionCompare(a, b) < 0
	}
	return rpmEVRCompare(a, b) < 0
}
