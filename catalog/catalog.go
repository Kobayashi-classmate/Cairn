// SPDX-License-Identifier: AGPL-3.0-only

// Package catalog is the software catalog: which software the panel can
// install, the version lines it offers, and where each line comes from on
// each supported release. It is data only (TOML files embedded at build
// time); the installer acts on it, nothing here runs anything.
//
// Source rules (decided by the project owner, see the design docs):
//   - a line the system repositories provide is installed from them, and it
//     is the recommended one; the distribution maintains it, so it counts as
//     supported even after upstream support ended;
//   - otherwise the software's official repository or download is used;
//     a source that is not official needs the owner's recorded approval
//     (Repo.Approved), as for the PHP repositories;
//   - lines whose upstream support has ended are never installed natively;
//     they are offered only as containers, which come with the Docker module.
package catalog

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

//go:embed software/*.toml repos/*.toml mirrors.toml suites.toml
var files embed.FS

// Methods, in order of preference.
const (
	MethodSystem     = "system"     // the distribution's own repositories
	MethodVendor     = "vendor"     // an additional package repository
	MethodStandalone = "standalone" // an official build unpacked under /opt/cairn/runtimes
	MethodContainer  = "container"  // runs in a container (Docker module)
)

// Categories group software in the UI.
var Categories = []string{"web", "runtime", "database", "cache"}

// Software is one installable product.
type Software struct {
	ID       string   `toml:"id" json:"id"`
	Name     string   `toml:"name" json:"name"` // product name, not translated
	Category string   `toml:"category" json:"category"`
	Homepage string   `toml:"homepage" json:"homepage"`
	Lines    []Line   `toml:"lines" json:"-"`
	Sources  []Source `toml:"sources" json:"-"`
}

// Line is a version line users choose from, such as PHP "8.4".
type Line struct {
	Version string `toml:"version"`
	// EOL is the end of upstream security support (YYYY-MM-DD); empty when
	// not announced.
	EOL string `toml:"eol"`
	// LTS marks long-term support lines; among runtimes without a system
	// version, the newest LTS line is recommended.
	LTS bool `toml:"lts"`
}

// Source says where some lines come from on some releases.
type Source struct {
	Method string `toml:"method"`
	// Releases are platform profile names (el-9 also covers Rocky and Alma
	// 9); "*" matches every supported release.
	Releases []string `toml:"releases"`
	Lines    []string `toml:"lines"`
	// Repo is the vendor repository ID (MethodVendor).
	Repo string `toml:"repo"`
	// Provider names the download provider (MethodStandalone).
	Provider string `toml:"provider"`
	// Packages are package name templates; {line} and {line_nodot} are
	// replaced ("8.4", "84"). For a standalone source they are the system
	// libraries the prebuilt build needs, installed from the system
	// repositories first.
	Packages []string `toml:"packages"`
	// Stream is an EL module stream template such as "php:{line}".
	Stream string `toml:"stream"`
	// Arch limits the source to these Go architectures; empty means all.
	Arch []string `toml:"arch"`
	// Services are the systemd units (templates) started after install.
	Services []string `toml:"services"`
}

// Repo is an additional package repository.
type Repo struct {
	ID       string `toml:"id" json:"id"`
	Name     string `toml:"name" json:"name"`
	Homepage string `toml:"homepage" json:"homepage"`
	// Official is true for the software publisher's own repository.
	Official bool `toml:"official" json:"official"`
	// Approved records when the project owner approved a repository that is
	// not official (YYYY-MM-DD).
	Approved string   `toml:"approved" json:"-"`
	Releases []string `toml:"releases" json:"-"`
	// Requires lists repositories this one depends on (Remi needs EPEL).
	Requires []string `toml:"requires" json:"-"`
	// APT and DNF say how to add the repository. Templates may use
	// {distro} (os-release ID), {codename}, {major} (major version) and
	// {line}; dnf variables such as $basearch are passed through.
	APT *APTRepo `toml:"apt" json:"-"`
	DNF *DNFRepo `toml:"dnf" json:"-"`
}

// APTRepo is a deb822 repository. Its packages are pinned below the
// system's (priority 100), so system packages stay the distribution's own
// builds; Prefer lists shared packages the vendor's builds need in the
// vendor's newer version (pinned above the system's).
type APTRepo struct {
	URI        string   `toml:"uri"`
	Suite      string   `toml:"suite"`
	Components []string `toml:"components"`
	Key        Key      `toml:"key"`
	Prefer     []string `toml:"prefer"`
}

// DNFRepo is a dnf repository, or a release package from the system
// repositories that configures it (EPEL).
type DNFRepo struct {
	BaseURL        string `toml:"baseurl"`
	Key            Key    `toml:"key"`
	ReleasePackage string `toml:"release_package"`
}

// Key is a repository signing key: where to download it and the primary
// key fingerprints it may contain. A downloaded key whose primary keys are
// not all listed is refused.
type Key struct {
	URL          string   `toml:"url"`
	Fingerprints []string `toml:"fingerprints"`
}

// Catalog is the loaded catalog.
type Catalog struct {
	Software []Software
	Repos    map[string]Repo
	Mirrors  Mirrors
	Suites   []Suite
}

// Suite is a preset of the first-login wizard (suites.toml).
type Suite struct {
	ID string `toml:"id" json:"id"`
	// Items list software IDs in order of preference: the first one offered
	// on the system is used.
	Items [][]string `toml:"items" json:"-"`
}

// SuiteItem is a suite item resolved for a system: the software and its
// recommended line, or Missing when none of the choices is offered.
type SuiteItem struct {
	Software string   `json:"software,omitempty"`
	Line     string   `json:"line,omitempty"`
	Choices  []string `json:"choices"`
	Missing  bool     `json:"missing,omitempty"`
}

// Resolve picks, for each item of the suite, the first software with an
// available line on sys and its recommended line.
func (c *Catalog) Resolve(su Suite, sys System) []SuiteItem {
	out := make([]SuiteItem, 0, len(su.Items))
	for _, choices := range su.Items {
		item := SuiteItem{Choices: choices, Missing: true}
		for _, id := range choices {
			sw, ok := c.Find(id)
			if !ok {
				continue
			}
			for _, o := range sw.Offers(sys) {
				if o.Recommended {
					item = SuiteItem{Software: id, Line: o.Line, Choices: choices}
				}
			}
			if !item.Missing {
				break
			}
		}
		out = append(out, item)
	}
	return out
}

// Mirrors lists download sources besides the official addresses: the
// operators of approved mirrors, rewrites of official download URLs to
// those mirrors, and the distributions' official repository addresses with
// their official mirror networks (mirrors.toml).
type Mirrors struct {
	Operators []Operator   `toml:"operator"`
	Rewrites  []Rewrite    `toml:"rewrite"`
	Systems   []SystemRepo `toml:"system"`
}

// Operator runs mirrors. One that is not the upstream itself needs the
// project owner's recorded approval.
type Operator struct {
	ID       string `toml:"id" json:"id"`
	Name     string `toml:"name" json:"name"`
	Homepage string `toml:"homepage" json:"homepage"`
	Official bool   `toml:"official" json:"official"`
	Approved string `toml:"approved" json:"-"`
}

// Rewrite maps an official download URL of a provider to the same file on
// an operator's mirror.
type Rewrite struct {
	Provider string `toml:"provider"`
	Operator string `toml:"operator"`
	// Pattern must match the whole official URL; URL may refer to its
	// groups as $1, $2...
	Pattern string `toml:"pattern"`
	URL     string `toml:"url"`
	re      *regexp.Regexp
}

// Apply returns the mirror URL of official, or "" when the rewrite does not
// apply.
func (r Rewrite) Apply(official string) string {
	if r.re == nil || !r.re.MatchString(official) {
		return ""
	}
	return r.re.ReplaceAllString(official, r.URL)
}

// SystemRepo is a distribution's official repository address for one role
// and the official mirror network it publishes.
type SystemRepo struct {
	Distro string   `toml:"distro"` // os-release ID
	Role   string   `toml:"role"`   // main | security
	Arch   []string `toml:"arch"`   // Go architectures; empty means all
	// Official is the base URL template ({codename}, {major}, dnf variables).
	Official string `toml:"official"`
	// Suites are the apt suites of the role; empty for dnf.
	Suites []string `toml:"suites"`
	// Section is the dnf .repo section the distribution ships for it.
	Section string `toml:"section"`
	// Mirrors are fixed official mirror addresses (Debian's country aliases).
	Mirrors []string `toml:"mirrors"`
	// MirrorList is the official registry answering with mirrors, one URL per
	// line or (MirrorListFormat "metalink") as a metalink document.
	MirrorList       string `toml:"mirror_list"`
	MirrorListFormat string `toml:"mirror_list_format"`
}

// Operator returns the operator with id.
func (m *Mirrors) Operator(id string) (Operator, bool) {
	for _, o := range m.Operators {
		if o.ID == id {
			return o, true
		}
	}
	return Operator{}, false
}

// Roles of system repositories.
const (
	RoleMain     = "main"
	RoleSecurity = "security"
)

var (
	idPattern   = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	fprPattern  = regexp.MustCompile(`^[0-9A-F]{40}$`)
	datePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	placeholder = regexp.MustCompile(`\{[^}]*\}`)
)

// Load parses and checks the embedded catalog.
func Load() (*Catalog, error) { return load(files) }

// LoadFS parses and checks a catalog laid out like the embedded one
// (software/*.toml, repos/*.toml, optionally mirrors.toml and suites.toml);
// tests use it.
func LoadFS(fsys fs.FS) (*Catalog, error) { return load(fsys) }

func load(fsys fs.FS) (*Catalog, error) {
	c := &Catalog{Repos: map[string]Repo{}}
	repoFiles, err := fs.Glob(fsys, "repos/*.toml")
	if err != nil {
		return nil, err
	}
	for _, f := range repoFiles {
		var r Repo
		if err := decode(fsys, f, &r); err != nil {
			return nil, err
		}
		if r.ID != strings.TrimSuffix(path.Base(f), ".toml") {
			return nil, fmt.Errorf("catalog: %s: id %q does not match the file name", f, r.ID)
		}
		if !r.Official && !datePattern.MatchString(r.Approved) {
			return nil, fmt.Errorf("catalog: repository %s is not official and has no recorded approval", r.ID)
		}
		if err := checkRepo(r); err != nil {
			return nil, fmt.Errorf("catalog: repository %s: %w", r.ID, err)
		}
		c.Repos[r.ID] = r
	}
	softFiles, err := fs.Glob(fsys, "software/*.toml")
	if err != nil {
		return nil, err
	}
	for _, f := range softFiles {
		var s Software
		if err := decode(fsys, f, &s); err != nil {
			return nil, err
		}
		if s.ID != strings.TrimSuffix(path.Base(f), ".toml") {
			return nil, fmt.Errorf("catalog: %s: id %q does not match the file name", f, s.ID)
		}
		if err := c.check(&s); err != nil {
			return nil, fmt.Errorf("catalog: %s: %w", s.ID, err)
		}
		c.Software = append(c.Software, s)
	}
	// suites.toml and mirrors.toml are optional in test catalogs.
	if _, err := fs.Stat(fsys, "suites.toml"); err == nil {
		var doc struct {
			Suites []Suite `toml:"suite"`
		}
		if err := decode(fsys, "suites.toml", &doc); err != nil {
			return nil, err
		}
		for _, su := range doc.Suites {
			if !idPattern.MatchString(su.ID) || len(su.Items) == 0 {
				return nil, fmt.Errorf("catalog: suite %q: bad id or no items", su.ID)
			}
			for _, choices := range su.Items {
				if len(choices) == 0 {
					return nil, fmt.Errorf("catalog: suite %s: empty item", su.ID)
				}
				for _, id := range choices {
					if _, ok := c.Find(id); !ok {
						return nil, fmt.Errorf("catalog: suite %s: unknown software %q", su.ID, id)
					}
				}
			}
		}
		c.Suites = doc.Suites
	}
	if _, err := fs.Stat(fsys, "mirrors.toml"); err == nil {
		if err := decode(fsys, "mirrors.toml", &c.Mirrors); err != nil {
			return nil, err
		}
	}
	if err := c.checkMirrors(); err != nil {
		return nil, fmt.Errorf("catalog: mirrors.toml: %w", err)
	}
	return c, nil
}

func (c *Catalog) checkMirrors() error {
	m := &c.Mirrors
	for _, o := range m.Operators {
		if !idPattern.MatchString(o.ID) || o.Name == "" || !strings.HasPrefix(o.Homepage, "https://") {
			return fmt.Errorf("operator %q: bad id, name or homepage", o.ID)
		}
		if !o.Official && !datePattern.MatchString(o.Approved) {
			return fmt.Errorf("operator %s is not official and has no recorded approval", o.ID)
		}
	}
	providers := map[string]bool{}
	for i := range c.Software {
		for _, src := range c.Software[i].Sources {
			if src.Method == MethodStandalone {
				providers[src.Provider] = true
			}
		}
	}
	for i := range m.Rewrites {
		r := &m.Rewrites[i]
		if _, ok := m.Operator(r.Operator); !ok || !providers[r.Provider] {
			return fmt.Errorf("rewrite %d: unknown operator %q or provider %q", i, r.Operator, r.Provider)
		}
		if !strings.HasPrefix(r.Pattern, "^https://") || !strings.HasSuffix(r.Pattern, "$") || !strings.HasPrefix(r.URL, "https://") {
			return fmt.Errorf("rewrite %d: the pattern must be anchored on an https URL and the target https", i)
		}
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			return fmt.Errorf("rewrite %d: %w", i, err)
		}
		r.re = re
	}
	for i, s := range m.Systems {
		bad := func(why string) error { return fmt.Errorf("system %d (%s %s): %s", i, s.Distro, s.Role, why) }
		switch {
		case !idPattern.MatchString(s.Distro) || (s.Role != RoleMain && s.Role != RoleSecurity):
			return bad("bad distro or role")
		case !strings.HasPrefix(s.Official, "https://") && !strings.HasPrefix(s.Official, "http://"):
			return bad("official address must be http(s)")
		case (len(s.Suites) == 0) == (s.Section == ""):
			return bad("needs either apt suites or a dnf section")
		case s.MirrorListFormat != "" && s.MirrorListFormat != "metalink":
			return bad("unknown mirror list format")
		}
		for _, u := range append(slices.Clone(s.Mirrors), s.MirrorList) {
			if u != "" && !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
				return bad("mirror addresses must be http(s)")
			}
		}
	}
	return nil
}

func checkKey(k Key) error {
	if !strings.HasPrefix(k.URL, "https://") || len(k.Fingerprints) == 0 {
		return fmt.Errorf("key needs an https URL and fingerprints")
	}
	for _, f := range k.Fingerprints {
		if !fprPattern.MatchString(f) {
			return fmt.Errorf("bad fingerprint %q", f)
		}
	}
	return nil
}

func checkRepo(r Repo) error {
	if r.APT == nil && r.DNF == nil {
		return fmt.Errorf("neither apt nor dnf details")
	}
	if a := r.APT; a != nil {
		if !strings.HasPrefix(a.URI, "https://") || a.Suite == "" || len(a.Components) == 0 {
			return fmt.Errorf("apt: uri (https), suite and components are required")
		}
		if err := checkKey(a.Key); err != nil {
			return fmt.Errorf("apt: %w", err)
		}
	}
	if d := r.DNF; d != nil {
		switch {
		case d.ReleasePackage != "" && d.BaseURL == "":
		case d.ReleasePackage == "" && strings.HasPrefix(d.BaseURL, "https://"):
			if err := checkKey(d.Key); err != nil {
				return fmt.Errorf("dnf: %w", err)
			}
		default:
			return fmt.Errorf("dnf: either an https baseurl with a key or a release package")
		}
	}
	return nil
}

func decode(fsys fs.FS, name string, v any) error {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return err
	}
	dec := toml.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("catalog: %s: %w", name, err)
	}
	return nil
}

func (c *Catalog) check(s *Software) error {
	if !idPattern.MatchString(s.ID) || s.Name == "" || !slices.Contains(Categories, s.Category) {
		return fmt.Errorf("bad id, name or category")
	}
	if len(s.Lines) == 0 {
		return fmt.Errorf("no version lines")
	}
	var versions []string
	for _, l := range s.Lines {
		if l.Version == "" || slices.Contains(versions, l.Version) {
			return fmt.Errorf("empty or duplicate line %q", l.Version)
		}
		if l.EOL != "" && !datePattern.MatchString(l.EOL) {
			return fmt.Errorf("line %s: bad eol date %q", l.Version, l.EOL)
		}
		versions = append(versions, l.Version)
	}
	for i, src := range s.Sources {
		if err := c.checkSource(src, versions); err != nil {
			return fmt.Errorf("source %d: %w", i, err)
		}
	}
	return nil
}

func (c *Catalog) checkSource(src Source, versions []string) error {
	if len(src.Releases) == 0 || len(src.Lines) == 0 {
		return fmt.Errorf("no releases or lines")
	}
	for _, l := range src.Lines {
		if !slices.Contains(versions, l) {
			return fmt.Errorf("unknown line %q", l)
		}
	}
	for _, p := range append(append(slices.Clone(src.Packages), src.Services...), src.Stream) {
		for _, ph := range placeholder.FindAllString(p, -1) {
			if ph != "{line}" && ph != "{line_nodot}" {
				return fmt.Errorf("unknown placeholder %s", ph)
			}
		}
	}
	switch src.Method {
	case MethodSystem:
		if len(src.Packages) == 0 {
			return fmt.Errorf("system source without packages")
		}
	case MethodVendor:
		repo, ok := c.Repos[src.Repo]
		if !ok || len(src.Packages) == 0 {
			return fmt.Errorf("vendor source needs a known repo and packages")
		}
		for _, rel := range src.Releases {
			if !slices.Contains(repo.Releases, rel) {
				return fmt.Errorf("repository %s does not serve %s", repo.ID, rel)
			}
		}
	case MethodStandalone:
		if src.Provider == "" {
			return fmt.Errorf("standalone source without provider")
		}
	default:
		return fmt.Errorf("unknown method %q", src.Method)
	}
	return nil
}

// Find returns the software with id.
func (c *Catalog) Find(id string) (*Software, bool) {
	for i := range c.Software {
		if c.Software[i].ID == id {
			return &c.Software[i], true
		}
	}
	return nil, false
}

// Reasons a line is not available on a system.
const (
	ReasonEndOfLife = "end_of_life" // upstream support ended: containers only
	ReasonNotBuilt  = "not_built"   // nobody builds it for this release
)

// Offer is one line as offered on a given system.
type Offer struct {
	Line        string   `json:"line"`
	Method      string   `json:"method,omitempty"` // empty: not available
	Repo        string   `json:"repo,omitempty"`
	Provider    string   `json:"provider,omitempty"`
	Packages    []string `json:"packages,omitempty"`
	Stream      string   `json:"stream,omitempty"`
	Services    []string `json:"services,omitempty"`
	Recommended bool     `json:"recommended"`
	LTS         bool     `json:"lts"`
	EOL         string   `json:"eol,omitempty"`
	// EndOfLife is true when upstream support has ended (system packages
	// stay maintained by the distribution and are still offered).
	EndOfLife bool   `json:"endOfLife"`
	Reason    string `json:"reason,omitempty"`
}

// System identifies the machine offers are computed for.
type System struct {
	Chain []string // platform profile chain, nearest first
	Arch  string   // Go architecture
	Now   time.Time
}

func (s System) matches(src Source) bool {
	if len(src.Arch) > 0 && !slices.Contains(src.Arch, s.Arch) {
		return false
	}
	for _, rel := range src.Releases {
		if rel == "*" || slices.Contains(s.Chain, rel) {
			return true
		}
	}
	return false
}

func expand(tmpl, line string) string {
	return strings.NewReplacer("{line}", line, "{line_nodot}", strings.ReplaceAll(line, ".", "")).Replace(tmpl)
}

// Offers resolves every line of sw for sys, newest first as in the catalog,
// and marks the recommended one: the system repositories' line if there is
// one (the newest of them), else the newest available LTS line, else the
// newest available line.
func (sw *Software) Offers(sys System) []Offer {
	today := sys.Now.Format(time.DateOnly)
	offers := make([]Offer, 0, len(sw.Lines))
	for _, l := range sw.Lines {
		o := Offer{Line: l.Version, LTS: l.LTS, EOL: l.EOL, EndOfLife: l.EOL != "" && l.EOL < today, Reason: ReasonNotBuilt}
		for _, src := range sw.Sources {
			if !slices.Contains(src.Lines, l.Version) || !sys.matches(src) {
				continue
			}
			if src.Method != MethodSystem && o.EndOfLife {
				o.Reason = ReasonEndOfLife
				continue
			}
			o.Method, o.Repo, o.Provider, o.Reason = src.Method, src.Repo, src.Provider, ""
			for _, p := range src.Packages {
				o.Packages = append(o.Packages, expand(p, l.Version))
			}
			if src.Stream != "" {
				o.Stream = expand(src.Stream, l.Version)
			}
			for _, u := range src.Services {
				o.Services = append(o.Services, expand(u, l.Version))
			}
			if src.Method == MethodSystem {
				o.EndOfLife = false
			}
			break
		}
		offers = append(offers, o)
	}
	pick := func(ok func(Offer) bool) bool {
		for i := range offers {
			if offers[i].Method != "" && ok(offers[i]) {
				offers[i].Recommended = true
				return true
			}
		}
		return false
	}
	_ = pick(func(o Offer) bool { return o.Method == MethodSystem }) ||
		pick(func(o Offer) bool { return o.LTS }) ||
		pick(func(Offer) bool { return true })
	return offers
}
