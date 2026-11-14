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

//go:embed software/*.toml repos/*.toml
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
	// replaced ("8.4", "84").
	Packages []string `toml:"packages"`
	// Stream is an EL module stream template such as "php:{line}".
	Stream string `toml:"stream"`
	// Arch limits the source to these Go architectures; empty means all.
	Arch []string `toml:"arch"`
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
}

// Catalog is the loaded catalog.
type Catalog struct {
	Software []Software
	Repos    map[string]Repo
}

var (
	idPattern   = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	datePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	placeholder = regexp.MustCompile(`\{[^}]*\}`)
)

// Load parses and checks the embedded catalog.
func Load() (*Catalog, error) { return load(files) }

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
	return c, nil
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
	for _, p := range append(slices.Clone(src.Packages), src.Stream) {
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
