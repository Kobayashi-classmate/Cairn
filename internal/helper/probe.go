// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Kobayashi-classmate/Cairn/catalog"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/sources"
)

const (
	probeWindow   = 25 * time.Second
	maxCandidates = 24
	maxSourceFile = 1 << 20
)

var defaultProber = &sources.Prober{}

func (s *Software) prober() *sources.Prober {
	if s.Sources != nil {
		return s.Sources
	}
	return defaultProber
}

// allowedBy says which candidates a policy allows: "official" keeps the
// addresses the upstream publishes itself (its own address and its own
// mirror sites).
func allowedBy(policy string) func(sources.Candidate) bool {
	return func(c sources.Candidate) bool {
		return policy != helperapi.PolicyOfficial || c.Kind == sources.KindOfficial || c.Kind == sources.KindOfficialMirror
	}
}

// probe implements pkg.probe: the download sources of each requested line,
// grouped by where the install downloads from.
func (s *Software) probe(ctx context.Context, _ helperapi.Caller, req helperapi.ProbeRequest) (helperapi.ProbeResult, error) {
	start := time.Now()
	s.probing.Lock() // one probe at a time; each sends a few dozen requests
	defer s.probing.Unlock()
	ctx, cancel := context.WithTimeout(ctx, probeWindow)
	defer cancel()

	var slots []*helperapi.SourceSlot
	var runs []func()
	byID := map[string]*helperapi.SourceSlot{}
	add := func(id, kind, swID string, run func(*helperapi.SourceSlot)) {
		if sl, ok := byID[id]; ok {
			if !contains(sl.Software, swID) {
				sl.Software = append(sl.Software, swID)
			}
			return
		}
		sl := &helperapi.SourceSlot{ID: id, Kind: kind, Software: []string{swID}, Candidates: []sources.Candidate{}}
		byID[id] = sl
		slots = append(slots, sl)
		runs = append(runs, func() { run(sl) })
	}
	// A line shows only the sources it is installed from: a runtime its
	// download, a vendor line its repository, a system line the system's
	// main repositories (an install can use another candidate for them).
	mainSlot := func(swID string) {
		if _, ok := s.systemRepo(catalog.RoleMain); ok {
			add("system:"+catalog.RoleMain, helperapi.SlotSystem, swID, func(sl *helperapi.SourceSlot) { s.probeInstallSource(ctx, sl, req.Policy) })
		}
	}
	var pkgItems []lineItem
	var libs []string
	plans := make([]helperapi.LinePlan, len(req.Items))
	for i, it := range req.Items {
		sw, o, err := s.installableOffer(it)
		if err != nil {
			return helperapi.ProbeResult{}, err
		}
		runs = append(runs, func() { plans[i] = s.plan(ctx, sw, o) })
		if o.Method == catalog.MethodStandalone {
			libs = append(libs, o.Packages...)
		} else {
			pkgItems = append(pkgItems, lineItem{req: it, sw: sw, o: o})
		}
		switch o.Method {
		case catalog.MethodStandalone:
			add("runtime:"+sw.ID+":"+o.Line, helperapi.SlotRuntime, sw.ID, func(sl *helperapi.SourceSlot) {
				s.probeRuntime(ctx, sl, o, req.Policy)
			})
		case catalog.MethodVendor:
			if r := s.Catalog.Repos[o.Repo]; r.APT != nil && s.Manager == "apt" || r.DNF != nil && r.DNF.BaseURL != "" && s.Manager == "dnf" {
				add("repo:"+o.Repo, helperapi.SlotRepo, sw.ID, func(sl *helperapi.SourceSlot) { s.probeRepo(ctx, sl, r, o.Line) })
			}
		default:
			mainSlot(sw.ID)
		}
	}
	var est *helperapi.PackageEstimate
	if len(pkgItems) > 0 || len(libs) > 0 {
		runs = append(runs, func() { est = s.estimate(ctx, pkgItems, libs) })
	}
	var wg sync.WaitGroup
	for _, run := range runs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			run()
		}()
	}
	wg.Wait()
	res := helperapi.ProbeResult{Slots: make([]helperapi.SourceSlot, 0, len(slots)), Packages: est, Plan: plans, Took: time.Since(start).Milliseconds()}
	for _, sl := range slots {
		res.Slots = append(res.Slots, *sl)
	}
	return res, nil
}

// plan says what installing a line will do: the repository it adds, the
// stream it enables, the exact package versions (when the indexes have
// them already), the libraries of a runtime and the services it starts.
func (s *Software) plan(ctx context.Context, sw *catalog.Software, o catalog.Offer) helperapi.LinePlan {
	p := helperapi.LinePlan{Software: sw.ID, Line: o.Line, Method: o.Method, Repo: o.Repo, Stream: o.Stream,
		Packages: []helperapi.InstalledPackage{}, Services: o.Services}
	p.RepoReady = o.Method != catalog.MethodVendor || s.repoConfigured(o.Repo, o.Line)
	if o.Method == catalog.MethodStandalone {
		p.Libraries = s.missing(ctx, o.Packages)
		return p
	}
	var versions map[string]string
	if p.RepoReady {
		versions, _ = s.candidates(ctx, o, true, nil) // unknown versions stay "": resolved after refreshing
	}
	for _, name := range o.Packages {
		p.Packages = append(p.Packages, helperapi.InstalledPackage{Name: name, Version: versions[name]})
	}
	return p
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func slotError(err error) *helperapi.Error {
	var he *helperapi.Error
	if errors.As(err, &he) {
		return he
	}
	return &helperapi.Error{Code: CodeRuntimeMetadata, Params: map[string]any{"reason": tailString(err.Error(), 300)}}
}

// probeRuntime checks where a runtime's newest build of the line can be
// downloaded from and recommends the fastest allowed source.
func (s *Software) probeRuntime(ctx context.Context, sl *helperapi.SourceSlot, o catalog.Offer, policy string) {
	if s.Runtimes == nil {
		sl.Error = &helperapi.Error{Code: helperapi.CodePkgNotAvailable}
		return
	}
	rel, err := s.Runtimes.resolve(ctx, o.Provider, o.Line)
	if err != nil {
		sl.Error = slotError(err)
		return
	}
	sl.Version, sl.File, sl.Selectable = rel.Version, fileName(rel.URL), true
	cands := filter(s.Runtimes.candidates(o.Provider, rel.URL), allowedBy(policy))
	s.prober().ProbeFiles(ctx, cands)
	for _, c := range cands {
		if c.Eligible && c.Size > 0 && sl.Size == 0 {
			sl.Size = c.Size
		}
	}
	if pick := sources.Fastest(cands, allowedBy(policy)); pick >= 0 {
		cands[pick].Recommended = true
		sl.Selected = cands[pick].ID
	} else {
		sl.Warning = helperapi.WarnNoEligible
	}
	if sl.Warning == "" && !cands[0].Eligible {
		sl.Warning = helperapi.WarnOfficialUnverified
	}
	sl.Candidates = cands
}

func fileName(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return path.Base(u)
	}
	return path.Base(p.Path)
}

func filter(cands []sources.Candidate, keep func(sources.Candidate) bool) []sources.Candidate {
	out := cands[:0]
	for _, c := range cands {
		if keep(c) {
			out = append(out, c)
		}
	}
	return out
}

// probeRepo checks a vendor repository at its official address (the only
// one the panel configures for now).
func (s *Software) probeRepo(ctx context.Context, sl *helperapi.SourceSlot, r catalog.Repo, line string) {
	sl.Repo = r.ID
	name := instance(r, line)
	if s.Manager == "apt" {
		uri := s.expand(r.APT.URI, line)
		_, err := os.Stat(s.path("/etc/apt/sources.list.d/" + name + ".sources"))
		sl.Candidates = []sources.Candidate{{ID: sources.IDOfficial, Kind: sources.KindOfficial, Name: sources.Host(uri), URL: uri, InUse: err == nil}}
		comps := make([]string, len(r.APT.Components))
		for i, c := range r.APT.Components {
			comps[i] = s.expand(c, line)
		}
		sl.Suites = []string{s.expand(r.APT.Suite, line)}
		s.prober().ProbeAPT(ctx, sl.Candidates, sources.APTTarget{Suites: sl.Suites, Components: comps, Arch: dpkgArch(s.Arch)})
	} else {
		base := sources.ExpandVars(s.expand(r.DNF.BaseURL, line), s.dnfVars())
		_, err := os.Stat(s.path("/etc/yum.repos.d/" + name + ".repo"))
		sl.Candidates = []sources.Candidate{{ID: sources.IDOfficial, Kind: sources.KindOfficial, Name: sources.Host(base), URL: base, InUse: err == nil}}
		s.prober().ProbeDNF(ctx, sl.Candidates)
	}
	sl.Selected = sources.IDOfficial
	if !sl.Candidates[0].Eligible {
		sl.Warning = helperapi.WarnInUseUnusable
	}
}

// systemRepo returns the catalog's system repository entry of a role for
// this distribution and architecture.
func (s *Software) systemRepo(role string) (catalog.SystemRepo, bool) {
	for _, r := range s.Catalog.Mirrors.Systems {
		if r.Distro == s.OS.ID && r.Role == role && (len(r.Arch) == 0 || contains(r.Arch, s.Arch)) {
			if (s.Manager == "apt") == (len(r.Suites) > 0) {
				return r, true
			}
		}
	}
	return catalog.SystemRepo{}, false
}

func dpkgArch(goarch string) string { return goarch } // amd64, arm64 are the same

func basearch(goarch string) string {
	switch goarch {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	}
	return goarch
}

// dnfVars are the dnf variables of this system: the built-in ones and those
// defined in /etc/dnf/vars.
func (s *Software) dnfVars() map[string]string {
	major, _, _ := strings.Cut(s.OS.VersionID, ".")
	vars := map[string]string{"basearch": basearch(s.Arch), "arch": basearch(s.Arch), "releasever": major, "releasever_major": major}
	files, _ := filepath.Glob(s.path("/etc/dnf/vars/*"))
	for _, f := range files {
		name := filepath.Base(f)
		if data, err := readSmall(f); err == nil && idName(name) {
			vars[name] = strings.TrimSpace(string(data))
		}
	}
	return vars
}

func idName(s string) bool {
	for _, r := range s {
		if r != '_' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return s != ""
}

func readSmall(p string) ([]byte, error) {
	f, err := os.Open(p) //nolint:gosec // G304: system configuration paths.
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, maxSourceFile)
	n, err := f.Read(buf)
	if err != nil && n == 0 {
		return nil, err
	}
	return buf[:n], nil
}

// readConfig reads the system's files matching the patterns, keyed by
// path, skipping the files the panel manages itself (cairn-*).
func (s *Software) readConfig(patterns ...string) map[string][]byte {
	out := map[string][]byte{}
	for _, pat := range patterns {
		files, _ := filepath.Glob(s.path(pat))
		for _, f := range files {
			if strings.HasPrefix(filepath.Base(f), "cairn-") {
				continue
			}
			if data, err := readSmall(f); err == nil {
				out[f] = data
			}
		}
	}
	return out
}

// systemCandidates lists the candidates of a role's system repositories:
// the official address first, then the addresses the system uses now, then
// the official mirror network. It fills in the slot's role and, for apt,
// the suites the role covers. all lifts the cap on the number of
// candidates: a check of one candidate must find it wherever the mirror
// list put it this time.
func (s *Software) systemCandidates(ctx context.Context, sl *helperapi.SourceSlot, role string, all bool) []sources.Candidate {
	sys, _ := s.systemRepo(role)
	sl.Role = role
	vars := s.dnfVars()
	official := sources.ExpandVars(s.expand(sys.Official, ""), vars)
	cands := []sources.Candidate{{ID: sources.IDOfficial, Kind: sources.KindOfficial, Name: sources.Host(official), URL: official}}
	have := func(u string) int {
		for i, c := range cands {
			if sources.SameBase(c.Addr(), u) {
				return i
			}
		}
		return -1
	}
	addCand := func(u, kind string, inUse bool) {
		u = sources.ExpandVars(u, vars)
		if i := have(u); i >= 0 {
			cands[i].InUse = cands[i].InUse || inUse
			return
		}
		if (!all && len(cands) >= maxCandidates) || (!strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://")) {
			return
		}
		// IDs are host names, so that the probe and the install name a
		// candidate alike (validated as helperapi source IDs: lower case).
		host := strings.ToLower(sources.Host(u))
		id := host
		for k := 2; candidateIndex(cands, id) >= 0; k++ {
			id = host + "-" + strconv.Itoa(k)
		}
		c := sources.Candidate{ID: id, Kind: kind, Name: sources.Host(u), InUse: inUse}
		// A source in use may carry credentials (a company mirror): they stay
		// in FullURL, which the probe and a switch use and nothing outputs.
		c.SetURL(u)
		cands = append(cands, c)
	}
	var registry []string
	if sys.MirrorList != "" {
		if data, err := s.prober().FetchList(ctx, sources.ExpandVars(s.expand(sys.MirrorList, ""), vars)); err == nil {
			if sys.MirrorListFormat == "metalink" {
				registry = sources.ParseMetalink(data)
			} else {
				registry = sources.ParseMirrorList(data)
			}
		}
	}
	inRegistry := func(u string) bool {
		for _, r := range append(append([]string{}, sys.Mirrors...), registry...) {
			if sources.SameBase(sources.ExpandVars(r, vars), u) {
				return true
			}
		}
		return false
	}
	kindOf := func(u string) string {
		if inRegistry(u) {
			return sources.KindOfficialMirror
		}
		return sources.KindOther
	}

	if s.Manager == "apt" {
		suites := make([]string, len(sys.Suites))
		for i, su := range sys.Suites {
			suites[i] = s.expand(su, "")
		}
		sl.Suites = suites
		for _, e := range sources.ParseAPTSources(s.readConfig(aptSourceFiles...)) {
			if contains(suites, e.Suite) {
				u := s.resolveAPTURI(e.URI)
				addCand(strings.TrimSuffix(u, "/"), kindOf(u), true)
			}
		}
		for _, u := range append(append([]string{}, sys.Mirrors...), registry...) {
			addCand(strings.TrimSuffix(u, "/"), sources.KindOfficialMirror, false)
		}
		return cands
	}
	for _, sec := range sources.ParseRepoFiles(s.readConfig(dnfRepoFiles)) {
		if sec.ID != sys.Section || !sec.Enabled {
			continue
		}
		switch {
		case len(sec.BaseURLs) > 0:
			addCand(sec.BaseURLs[0], kindOf(sources.ExpandVars(sec.BaseURLs[0], vars)), true)
		case sec.MirrorList != "" || sec.Metalink != "":
			// dnf uses the list's mirrors in order: the first one is what
			// the system downloads from.
			var list []string
			if sec.Metalink != "" {
				if data, err := s.prober().FetchList(ctx, sources.ExpandVars(sec.Metalink, vars)); err == nil {
					list = sources.ParseMetalink(data)
				}
			} else if data, err := s.prober().FetchList(ctx, sources.ExpandVars(sec.MirrorList, vars)); err == nil {
				list = sources.ParseMirrorList(data)
			}
			if len(list) > 0 {
				addCand(list[0], sources.KindOfficialMirror, true)
			}
		}
	}
	for _, u := range registry {
		addCand(u, sources.KindOfficialMirror, false)
	}
	return cands
}

// The system's repository files.
var (
	aptSourceFiles = []string{"/etc/apt/sources.list", "/etc/apt/sources.list.d/*.list", "/etc/apt/sources.list.d/*.sources"}
	dnfRepoFiles   = "/etc/yum.repos.d/*.repo"
)

// probeSystem compares the system repositories in use for a role with the
// distribution's official address and its official mirror network (the
// Host page's view: every candidate, the one in use selected). The
// system's sources change only on the user's request (syssrc.switch); an
// install can use another candidate for its own transaction only.
//
// only, when given, probes just those candidate IDs (with the official one
// and the ones in use): a switch checks its target again without the
// shortlist of a full probe skipping it.
func (s *Software) probeSystem(ctx context.Context, sl *helperapi.SourceSlot, role string, only ...string) {
	cands := s.systemCandidates(ctx, sl, role, len(only) > 0)
	s.measureSystem(ctx, sl, onlyCandidates(cands, only))
	for _, c := range sl.Candidates {
		if c.InUse && sl.Selected == "" {
			sl.Selected = c.ID
		}
	}
}

// measureSystem probes the candidates of a system slot (the official one
// first) and sets the slot's warnings about the source in use.
func (s *Software) measureSystem(ctx context.Context, sl *helperapi.SourceSlot, cands []sources.Candidate) {
	s.measureWith(ctx, s.prober(), sl, cands)
}

func (s *Software) measureWith(ctx context.Context, p *sources.Prober, sl *helperapi.SourceSlot, cands []sources.Candidate) {
	if s.Manager == "apt" {
		p.ProbeAPT(ctx, cands, sources.APTTarget{Suites: sl.Suites, Components: []string{"main"}, Arch: dpkgArch(s.Arch)})
	} else {
		p.ProbeDNF(ctx, cands)
	}
	sl.Candidates = cands
	for _, c := range cands {
		if !c.InUse {
			continue
		}
		switch c.Status {
		case sources.StatusBehind:
			sl.Warning = helperapi.WarnInUseBehind
		case sources.StatusNotFound, sources.StatusUnreachable:
			if sl.Warning == "" {
				sl.Warning = helperapi.WarnInUseUnusable
			}
		}
	}
	if sl.Warning == "" && len(cands) > 0 && !cands[0].Eligible {
		sl.Warning = helperapi.WarnOfficialUnverified
	}
}

// installCandidate reports whether an install may use a system candidate
// under a policy. On dnf a candidate other than the one in use must be
// reached over HTTPS: the distributions' .repo files check the packages'
// signatures but not the metadata's, which over HTTP could be swapped for
// an older index (the Host page's switch has the same rule).
func (s *Software) installCandidate(policy string) func(sources.Candidate) bool {
	allowed := allowedBy(policy)
	return func(c sources.Candidate) bool {
		return allowed(c) && (s.Manager != "dnf" || c.InUse || strings.HasPrefix(c.URL, "https://"))
	}
}

// probeInstallSource is the system:main slot of an install probe. Its
// candidates are those the policy allows; an install may name any of them
// and then uses it for its own transaction only (installsource.go). The
// selected one is the source in use while it is eligible: a default
// install keeps the system's own source and indexes. Only otherwise is it
// the fastest eligible candidate.
func (s *Software) probeInstallSource(ctx context.Context, sl *helperapi.SourceSlot, policy string) {
	keep := s.installCandidate(policy)
	cands := s.systemCandidates(ctx, sl, catalog.RoleMain, false)
	kept := []sources.Candidate{cands[0]} // the official address: the reference
	for _, c := range cands[1:] {
		if keep(c) {
			kept = append(kept, c)
		}
	}
	s.measureSystem(ctx, sl, kept)
	sl.Selectable = true
	pick := slices.IndexFunc(kept, func(c sources.Candidate) bool { return c.InUse && c.Eligible })
	if pick < 0 {
		pick = sources.Fastest(kept, keep)
	}
	if pick < 0 {
		if sl.Warning == "" {
			sl.Warning = helperapi.WarnNoEligible
		}
		return
	}
	kept[pick].Recommended = true
	sl.Selected = kept[pick].ID
}

// onlyCandidates keeps the official candidate, the ones in use and those
// named in only (all of them when only is empty).
func onlyCandidates(cands []sources.Candidate, only []string) []sources.Candidate {
	if len(only) == 0 {
		return cands
	}
	var out []sources.Candidate
	for _, c := range cands {
		if c.ID == sources.IDOfficial || c.InUse || contains(only, c.ID) {
			out = append(out, c)
		}
	}
	return out
}

// resolveAPTURI turns apt's "mirror+file:///etc/apt/mirrors/debian.list"
// (Debian's cloud images) into the first mirror of that list, the one apt
// tries first; other addresses are returned as they are.
func (s *Software) resolveAPTURI(uri string) string {
	rest, ok := strings.CutPrefix(uri, "mirror+file:")
	if !ok {
		return uri
	}
	data, err := readSmall(s.path(strings.TrimPrefix(rest, "//")))
	if err != nil {
		return uri
	}
	for _, line := range strings.Split(string(data), "\n") {
		if f := strings.Fields(line); len(f) > 0 && !strings.HasPrefix(f[0], "#") &&
			(strings.HasPrefix(f[0], "https://") || strings.HasPrefix(f[0], "http://")) {
			return f[0]
		}
	}
	return uri
}

func candidateIndex(cands []sources.Candidate, id string) int {
	for i, c := range cands {
		if c.ID == id {
			return i
		}
	}
	return -1
}

// estimate asks the package manager, from its current indexes and without
// changing anything, how much the lines would download. Lines whose vendor
// repository is not added yet are left out.
func (s *Software) estimate(ctx context.Context, items []lineItem, libs []string) *helperapi.PackageEstimate {
	est := &helperapi.PackageEstimate{}
	var names []string
	for _, it := range items {
		if it.o.Method == catalog.MethodVendor && !s.repoConfigured(it.o.Repo, it.o.Line) {
			est.Partial = true
			continue
		}
		names = append(names, it.o.Packages...)
	}
	names = append(names, s.missing(ctx, libs)...)
	if len(names) == 0 {
		est.Known = true
		return est
	}
	if s.Manager == "apt" {
		out, err := queryCommand(ctx, "apt-get", append([]string{"install", "--print-uris", "-qq", "--no-install-recommends"}, names...)...)
		if err == nil {
			est.Bytes, est.Packages = parsePrintURIs(out)
			est.Known = true
		}
		return est
	}
	// Cache only: the probe must not refresh the metadata. dnf exits
	// non-zero after --assumeno; the summary is still printed.
	out, _ := queryCommand(ctx, "dnf", append([]string{"install", "--assumeno", "-C"}, names...)...)
	if bytes, pkgs, ok := parseDNFSummary(out); ok {
		est.Bytes, est.Packages, est.Known = bytes, pkgs, true
	}
	return est
}

// repoConfigured reports whether a vendor repository (and what it needs)
// is set up already.
func (s *Software) repoConfigured(id, line string) bool {
	r, ok := s.Catalog.Repos[id]
	if !ok {
		return false
	}
	for _, req := range r.Requires {
		if !s.repoConfigured(req, line) {
			return false
		}
	}
	name := instance(r, line)
	switch {
	case s.Manager == "apt" && r.APT != nil:
		_, err := os.Stat(s.path("/etc/apt/sources.list.d/" + name + ".sources"))
		return err == nil
	case s.Manager == "dnf" && r.DNF != nil && r.DNF.ReleasePackage != "":
		return len(s.missing(context.Background(), []string{r.DNF.ReleasePackage})) == 0
	case s.Manager == "dnf" && r.DNF != nil:
		_, err := os.Stat(s.path("/etc/yum.repos.d/" + name + ".repo"))
		return err == nil
	}
	return false
}

// parsePrintURIs sums the sizes in "apt-get --print-uris" output lines:
// 'URL' file size hash.
func parsePrintURIs(out string) (int64, int) {
	var total int64
	n := 0
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) < 3 || !strings.HasPrefix(f[0], "'") {
			continue
		}
		if v, err := strconv.ParseInt(f[2], 10, 64); err == nil && v >= 0 {
			total += v
			n++
		}
	}
	return total, n
}

var (
	dnfDownload = regexp.MustCompile(`(?m)^Total download size:\s*([0-9.]+)\s*([kMG]?)`)
	dnfCount    = regexp.MustCompile(`(?m)^\s*(?:Install|Upgrade)\s+([0-9]+)\s+Packages?`)
)

// parseDNFSummary reads the transaction summary of "dnf install
// --assumeno": the total download size and the number of packages.
func parseDNFSummary(out string) (int64, int, bool) {
	m := dnfDownload.FindStringSubmatch(out)
	if m == nil {
		if strings.Contains(out, "Nothing to do") {
			return 0, 0, true
		}
		return 0, 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, 0, false
	}
	mult := map[string]float64{"": 1, "k": 1 << 10, "M": 1 << 20, "G": 1 << 30}[m[2]]
	n := 0
	for _, c := range dnfCount.FindAllStringSubmatch(out, -1) {
		k, _ := strconv.Atoi(c[1])
		n += k
	}
	return int64(v * mult), n, true
}
