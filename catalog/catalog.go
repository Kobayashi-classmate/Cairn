// SPDX-License-Identifier: AGPL-3.0-only

// Package catalog is the software catalog: which software the panel can
// install, the version lines it offers, and where each line comes from on
// each supported release. It is data only (TOML files embedded at build
// time); the installer acts on it, nothing here runs anything.
//
// Source rules (decided by the project owner, see the design docs):
//   - a line may come from several sources: the system repositories, the
//     software's official repository or download, or a source that is not
//     official, which needs the owner's recorded approval (Repo.Approved),
//     as for the PHP repositories;
//   - a line's default source is the system repositories' when they have
//     the line, else the others in catalog order; one whose updates for
//     the line have ended comes last, and the user may pick any of them
//     (with a warning for an ended one). The distribution maintains its own
//     packages, so a system line counts as supported after upstream
//     support ended;
//   - the recommended line is upstream's (the newest long-term support
//     line); PHP, which has no official repository, recommends the
//     system's own line;
//   - a line whose upstream support has ended stays installable, with a
//     warning, and is never recommended. One that is installed stays
//     known, with its packages, services and files, so that it can be
//     managed and removed.
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

//go:embed index.toml software/*.toml repos/*.toml mirrors.toml suites.toml rewrites.toml
var files embed.FS

// Format is the index format this panel reads (index.toml). A change that
// an older panel would misread raises it; such an index is refused.
const Format = 1

// Index describes the catalog as a whole (index.toml).
type Index struct {
	// Format is the index format (Format).
	Format int `toml:"format"`
	// Serial numbers the published indexes: an update only ever replaces
	// an index with one of a higher serial.
	Serial int64 `toml:"serial"`
}

// Methods, in order of preference.
const (
	MethodSystem     = "system"     // the distribution's own repositories
	MethodVendor     = "vendor"     // an additional package repository
	MethodStandalone = "standalone" // an official build unpacked under /opt/cairn/runtimes
	MethodContainer  = "container"  // runs in a container (Docker module)
)

// Categories group software in the UI.
var Categories = []string{"web", "runtime", "database", "cache", "tool"}

// Capabilities the panel itself looks software up by (Provides). Sites,
// databases and extensions ask for a capability, never for a software ID.
const (
	CapWebServer  = "web-server"     // serves the sites (nginx)
	CapPHPFPM     = "php-fpm"        // runs the PHP of sites
	CapMySQL      = "mysql-protocol" // MariaDB, MySQL
	CapPostgreSQL = "postgresql"
	CapRedis      = "redis-protocol" // Redis, Valkey
)

// Software is one installable product.
type Software struct {
	ID       string `toml:"id" json:"id"`
	Name     string `toml:"name" json:"name"` // product name, not translated
	Category string `toml:"category" json:"category"`
	Homepage string `toml:"homepage" json:"homepage"`
	// Provides lists the capabilities it offers (CapWebServer, ...).
	Provides []string `toml:"provides" json:"provides"`
	Lines    []Line   `toml:"lines" json:"-"`
	Sources  []Source `toml:"sources" json:"-"`
	// PHP holds what only PHP has.
	PHP *PHPSoftware `toml:"php" json:"-"`
	// Recommend is how the recommended line is chosen: "" for upstream's
	// (the newest long-term line, else the newest one that is not a
	// mainline), RecommendSystem for the system's own line first (PHP,
	// which has no official repository).
	Recommend string `toml:"recommend" json:"-"`
	// Single software has one line installed at a time: its lines share
	// package names, files and ports. Conflicts lists other software that
	// cannot be installed next to it for the same reasons.
	Single    bool     `toml:"single" json:"single"`
	Conflicts []string `toml:"conflicts" json:"conflicts,omitempty"`
	// Data is the data directory its lines share, and software that
	// conflicts with it may use too: removing a line keeps it, and the next
	// line installed starts on it.
	Data *DataDir `toml:"data" json:"-"`
}

// DataDir is a data directory that records which server used it last.
type DataDir struct {
	Path string `toml:"path"`
	// Format is how the directory records the server (DataMySQL).
	Format string `toml:"format"`
	// Product is the name that record gives this software.
	Product string `toml:"product"`
}

// DataMySQL is the data directory of MariaDB and MySQL: the version in
// mariadb_upgrade_info or mysql_upgrade_info ("11.8.9-MariaDB"), else the
// creator of the InnoDB redo log ("MySQL 8.4.13").
const DataMySQL = "mysql"

// RecommendSystem recommends the system's own line first.
const RecommendSystem = "system"

// PHPSoftware is the PHP part of a software entry.
type PHPSoftware struct {
	// Extensions are the PHP extensions the panel installs and removes; a
	// source's extension family picks their package names.
	Extensions []Extension `toml:"extensions"`
}

// Extensions returns the PHP extensions of sw (none for other software).
func (s *Software) Extensions() []Extension {
	if s.PHP == nil {
		return nil
	}
	return s.PHP.Extensions
}

// ProvidesCap reports whether sw offers the capability.
func (s *Software) ProvidesCap(capability string) bool {
	return slices.Contains(s.Provides, capability)
}

// Extension families: how a PHP source names its extension packages.
const (
	ExtDebian = "deb"  // Debian and Ubuntu's own PHP, and the sury and ondrej builds
	ExtEL     = "el"   // Enterprise Linux's own PHP (AppStream)
	ExtRemi   = "remi" // Remi's parallel-installable builds
)

// ExtFamilies lists the extension families.
var ExtFamilies = []string{ExtDebian, ExtEL, ExtRemi}

// Extension is a PHP extension that can be added to a line.
type Extension struct {
	ID   string `toml:"id"`
	Name string `toml:"name"` // product name, not translated
	// Modules are the PHP modules it provides, lowercase as "php -m"
	// prints them.
	Modules []string `toml:"modules"`
	// Packages maps an extension family to the package name template
	// ({line}, {line_nodot}); a family without an entry does not build it.
	Packages map[string]string `toml:"packages"`
}

// ConfigFile is a configuration file of a line that administrators may
// edit in the panel.
type ConfigFile struct {
	ID   string `toml:"id" json:"id"`
	Path string `toml:"path" json:"path"`
	// Check is the command (argv template) that validates the file once
	// written: a non-zero exit, or "syntax error" in its output, fails.
	Check    []string `toml:"check" json:"-"`
	HasCheck bool     `toml:"-" json:"check"`
	// Apply is what the service needs to read the file: reload, restart
	// or none.
	Apply string `toml:"apply" json:"apply"`
	// Service is the unit to reload or restart (template); empty means the
	// line's first service.
	Service string `toml:"service" json:"service,omitempty"`
}

// Ways a changed configuration file is applied.
const (
	ApplyReload  = "reload"
	ApplyRestart = "restart"
	ApplyNone    = "none"
)

func (c ConfigFile) expand(line string, services []string) ConfigFile {
	out := ConfigFile{ID: c.ID, Path: expand(c.Path, line), Apply: c.Apply, Service: expand(c.Service, line), HasCheck: len(c.Check) > 0}
	for _, a := range c.Check {
		out.Check = append(out.Check, expand(a, line))
	}
	if out.Service == "" && out.Apply != ApplyNone && len(services) > 0 {
		out.Service = services[0]
	}
	return out
}

// ExtensionPackage returns the package of ext for an offer, or "" when the
// offer's source does not build it.
func (e Extension) ExtensionPackage(o Offer) string {
	if o.Ext == "" {
		return ""
	}
	return expand(e.Packages[o.Ext], o.Line)
}

// Line is a version line users choose from, such as PHP "8.4".
type Line struct {
	Version string `toml:"version"`
	// EOL is the end of upstream security support (YYYY-MM-DD); empty when
	// not announced.
	EOL string `toml:"eol"`
	// LTS marks long-term support lines; the newest one is recommended.
	LTS bool `toml:"lts"`
	// Mainline marks upstream's development branch (nginx mainline): it can
	// be installed but is never recommended.
	Mainline bool `toml:"mainline"`
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
	// Companions are packages (templates) that come with the line's own and
	// go with it: the package with the server and its service when the
	// line's own is a metapackage depending on it (mysql-server ->
	// mysql-server-8.0), one holding the service of the line's binary
	// (nginx-common), or a weak dependency that depends on them
	// (MariaDB-server-compat). They are removed with the line when
	// installed, and play no part in whether it is.
	Companions []string `toml:"companions"`
	// Stream is an EL module stream template such as "php:{line}".
	Stream string `toml:"stream"`
	// Arch limits the source to these Go architectures; empty means all.
	Arch []string `toml:"arch"`
	// Services are the systemd units (templates) started after install.
	Services []string `toml:"services"`
	// Configs are the line's editable configuration files.
	Configs []ConfigFile `toml:"configs"`
	// PHP holds what only a PHP source has.
	PHP *PHPSource `toml:"php"`
}

// PHPSource is the PHP part of a source.
type PHPSource struct {
	// FPM describes the line's PHP-FPM (templates), for per-site pools.
	FPM *FPM `toml:"fpm"`
	// CLI is the line's command-line PHP (template), which lists the
	// loaded modules.
	CLI string `toml:"cli"`
	// Ext is the extension family (ExtDebian, ...).
	Ext string `toml:"ext"`
}

// FPM is where a PHP line's FastCGI process manager keeps its pools.
type FPM struct {
	PoolDir   string `toml:"pool_dir" json:"poolDir"`
	Binary    string `toml:"binary" json:"binary"`
	Service   string `toml:"service" json:"service"`
	PidFile   string `toml:"pid_file" json:"pidFile"`
	SocketDir string `toml:"socket_dir" json:"socketDir"`
}

func (f *FPM) expand(line string) *FPM {
	if f == nil {
		return nil
	}
	return &FPM{PoolDir: expand(f.PoolDir, line), Binary: expand(f.Binary, line), Service: expand(f.Service, line),
		PidFile: expand(f.PidFile, line), SocketDir: expand(f.SocketDir, line)}
}

// RewriteRule is a built-in URL rewrite template for sites (rewrites.toml):
// nginx directives for the server block, shipped with the panel.
type RewriteRule struct {
	ID    string   `toml:"id" json:"id"`
	Name  string   `toml:"name" json:"name"` // product name, not translated
	Types []string `toml:"types" json:"types"`
	Body  string   `toml:"body" json:"-"`
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
	// PinLine is for a repository that serves every line in one place
	// (Redis): the packages of the installed line are pinned to that line,
	// so that system updates keep to it. On EL that hides the repository's
	// other packages, so a line must list every package it needs from it.
	PinLine bool `toml:"pin_line" json:"-"`
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
	// ModuleHotfixes keeps the repository's packages visible while a
	// module stream of the same name is enabled (nginx.org, Redis on EL 9).
	ModuleHotfixes bool `toml:"module_hotfixes"`
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
	Index    Index
	Software []Software
	Repos    map[string]Repo
	Mirrors  Mirrors
	Suites   []Suite
	Rewrites []RewriteRule
}

// Providing returns the software offering the capability, in catalog order.
func (c *Catalog) Providing(capability string) []*Software {
	var out []*Software
	for i := range c.Software {
		if c.Software[i].ProvidesCap(capability) {
			out = append(out, &c.Software[i])
		}
	}
	return out
}

// Rewrite returns the rewrite rule with id.
func (c *Catalog) Rewrite(id string) (RewriteRule, bool) {
	for _, r := range c.Rewrites {
		if r.ID == id {
			return r, true
		}
	}
	return RewriteRule{}, false
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
	// Roots are the official base addresses as the distribution's .repo
	// files write them (dnf variables unexpanded); switching the source
	// moves every section whose baseurl starts with one of them.
	Roots []string `toml:"roots"`
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
// (software/*.toml, repos/*.toml, optionally index.toml, mirrors.toml and
// suites.toml); tests use it.
func LoadFS(fsys fs.FS) (*Catalog, error) { return load(fsys) }

func load(fsys fs.FS) (*Catalog, error) {
	c := &Catalog{Repos: map[string]Repo{}, Index: Index{Format: Format}}
	// index.toml is optional in test catalogs.
	if _, err := fs.Stat(fsys, "index.toml"); err == nil {
		if err := decode(fsys, "index.toml", &c.Index); err != nil {
			return nil, err
		}
		if c.Index.Format != Format || c.Index.Serial <= 0 {
			return nil, fmt.Errorf("catalog: index format %d (this panel reads %d) or serial %d", c.Index.Format, Format, c.Index.Serial)
		}
	}
	repoFiles, err := fs.Glob(fsys, "repos/*.toml")
	if err != nil {
		return nil, err
	}
	for _, f := range repoFiles {
		var r Repo
		if err := decode(fsys, f, &r); err != nil {
			return nil, err
		}
		if r.ID != strings.TrimSuffix(path.Base(f), ".toml") || r.ID == OriginSystem {
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
	for _, s := range c.Software {
		for _, id := range s.Conflicts {
			if _, ok := c.Find(id); !ok || id == s.ID {
				return nil, fmt.Errorf("catalog: %s: bad conflict %q", s.ID, id)
			}
		}
	}
	// rewrites.toml, suites.toml and mirrors.toml are optional in test
	// catalogs.
	if _, err := fs.Stat(fsys, "rewrites.toml"); err == nil {
		var doc struct {
			Rules []RewriteRule `toml:"rule"`
		}
		if err := decode(fsys, "rewrites.toml", &doc); err != nil {
			return nil, err
		}
		for _, r := range doc.Rules {
			if err := checkRewrite(r); err != nil {
				return nil, fmt.Errorf("catalog: rewrite %q: %w", r.ID, err)
			}
		}
		c.Rewrites = doc.Rules
	}
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

// Site types a rewrite rule may apply to.
var siteTypes = []string{"static", "php"}

func checkRewrite(r RewriteRule) error {
	if !idPattern.MatchString(r.ID) || r.Name == "" || strings.TrimSpace(r.Body) == "" || len(r.Types) == 0 {
		return fmt.Errorf("bad id, name, body or types")
	}
	for _, t := range r.Types {
		if !slices.Contains(siteTypes, t) {
			return fmt.Errorf("unknown site type %q", t)
		}
	}
	// Rules stay inside the site: no files read from elsewhere, no
	// includes, no upstreams.
	for _, bad := range []string{"alias", "include", "root ", "proxy_pass", "fastcgi_pass", "load_module", "lua", "perl", "js_"} {
		if strings.Contains(r.Body, bad) {
			return fmt.Errorf("directive %q is not allowed in a rewrite rule", strings.TrimSpace(bad))
		}
	}
	return nil
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
	if len(s.Provides) == 0 {
		return fmt.Errorf("provides nothing")
	}
	if s.Recommend != "" && s.Recommend != RecommendSystem {
		return fmt.Errorf("unknown recommendation rule %q", s.Recommend)
	}
	if d := s.Data; d != nil && (!strings.HasPrefix(d.Path, "/") || strings.Contains(d.Path, "..") || d.Format != DataMySQL || d.Product == "") {
		return fmt.Errorf("data needs an absolute path, a known format and the product name")
	}
	for i, p := range s.Provides {
		if !idPattern.MatchString(p) || slices.Contains(s.Provides[:i], p) {
			return fmt.Errorf("bad or duplicate capability %q", p)
		}
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
		if src.PHP != nil && src.PHP.Ext != "" && len(s.Extensions()) == 0 {
			return fmt.Errorf("source %d: extension family without extensions", i)
		}
	}
	var extIDs []string
	for _, e := range s.Extensions() {
		if !extPattern.MatchString(e.ID) || e.Name == "" || len(e.Modules) == 0 || slices.Contains(extIDs, e.ID) {
			return fmt.Errorf("extension %q: bad or duplicate id, or no name or modules", e.ID)
		}
		extIDs = append(extIDs, e.ID)
		for _, m := range e.Modules {
			if m != strings.ToLower(m) || m == "" {
				return fmt.Errorf("extension %s: modules must be lowercase", e.ID)
			}
		}
		for fam, p := range e.Packages {
			if !slices.Contains(ExtFamilies, fam) || !packagePattern.MatchString(p) {
				return fmt.Errorf("extension %s: bad family %q or package %q", e.ID, fam, p)
			}
			if err := checkPlaceholders(p); err != nil {
				return fmt.Errorf("extension %s: %w", e.ID, err)
			}
		}
	}
	return nil
}

var (
	extPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,31}$`)
	configPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	packagePattern = regexp.MustCompile(`^[a-z0-9{}][a-z0-9.+_{}-]*$`)
)

func checkPlaceholders(p string) error {
	for _, ph := range placeholder.FindAllString(p, -1) {
		if ph != "{line}" && ph != "{line_nodot}" {
			return fmt.Errorf("unknown placeholder %s", ph)
		}
	}
	return nil
}

func checkConfigs(src Source) error {
	var ids []string
	for _, cf := range src.Configs {
		if !configPattern.MatchString(cf.ID) || slices.Contains(ids, cf.ID) {
			return fmt.Errorf("config %q: bad or duplicate id", cf.ID)
		}
		ids = append(ids, cf.ID)
		if !strings.HasPrefix(cf.Path, "/") || strings.Contains(cf.Path, "..") {
			return fmt.Errorf("config %s: path must be absolute", cf.ID)
		}
		if len(cf.Check) > 0 && !strings.HasPrefix(cf.Check[0], "/") {
			return fmt.Errorf("config %s: check must start with an absolute path", cf.ID)
		}
		switch cf.Apply {
		case ApplyReload, ApplyRestart:
			if cf.Service == "" && len(src.Services) == 0 {
				return fmt.Errorf("config %s: %s needs a service", cf.ID, cf.Apply)
			}
		case ApplyNone:
			if cf.Service != "" {
				return fmt.Errorf("config %s: a service without apply", cf.ID)
			}
		default:
			return fmt.Errorf("config %s: apply must be reload, restart or none", cf.ID)
		}
		for _, t := range append([]string{cf.Path, cf.Service}, cf.Check...) {
			if err := checkPlaceholders(t); err != nil {
				return fmt.Errorf("config %s: %w", cf.ID, err)
			}
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
	if err := checkConfigs(src); err != nil {
		return err
	}
	templates := append(append(append(slices.Clone(src.Packages), src.Companions...), src.Services...), src.Stream)
	if p := src.PHP; p != nil {
		if p.FPM == nil || !strings.HasPrefix(p.CLI, "/") {
			return fmt.Errorf("php needs fpm and an absolute cli path")
		}
		if p.Ext != "" && !slices.Contains(ExtFamilies, p.Ext) {
			return fmt.Errorf("unknown extension family %q", p.Ext)
		}
		f := p.FPM
		if !strings.HasPrefix(f.PoolDir, "/") || !strings.HasPrefix(f.Binary, "/") || f.Service == "" || !strings.HasPrefix(f.PidFile, "/") || !strings.HasPrefix(f.SocketDir, "/") {
			return fmt.Errorf("fpm needs absolute paths and a service")
		}
		templates = append(templates, p.CLI, f.PoolDir, f.Binary, f.Service, f.PidFile, f.SocketDir)
	}
	for _, p := range templates {
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
		if src.Provider == "" || src.Provider == OriginSystem {
			return fmt.Errorf("standalone source without a provider")
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

// Reasons a line cannot be installed on a system.
const (
	// ReasonEndOfLife is not the catalog's: the web API gives it when an
	// install of a line past its upstream support was not acknowledged.
	ReasonEndOfLife = "end_of_life"
	ReasonNotBuilt  = "not_built" // nobody builds it for this release
	ReasonArch      = "arch"      // it is built for this release, not for this architecture
)

// OriginSystem is the origin of a line from the system repositories; a
// vendor line's origin is its repository ID, a prebuilt line's its
// provider.
const OriginSystem = "system"

// Origin names a source among those of a line.
func (src *Source) Origin() string {
	switch src.Method {
	case MethodVendor:
		return src.Repo
	case MethodStandalone:
		return src.Provider
	}
	return OriginSystem
}

// SourceInfo is one of the sources a line can be installed from here.
type SourceInfo struct {
	Origin   string `json:"origin"`
	Method   string `json:"method"`
	Repo     string `json:"repo,omitempty"`
	Provider string `json:"provider,omitempty"`
	// EndOfLife is true when the line gets no more updates from this
	// source: upstream support ended, and the distribution does not
	// maintain it there.
	EndOfLife bool `json:"endOfLife"`
	// Default is the source used unless another is asked for.
	Default bool `json:"default"`
}

// Offer is one line as offered on a given system, from one of its sources:
// the default one (Offers) or one asked for (OfferFrom).
type Offer struct {
	Line string `json:"line"`
	// Method says where the line comes from here; empty when nobody builds
	// it for this system. It does not say that the line can be installed:
	// see Installable.
	Method   string   `json:"method,omitempty"`
	Repo     string   `json:"repo,omitempty"`
	Provider string   `json:"provider,omitempty"`
	Origin   string   `json:"origin,omitempty"`
	Packages []string `json:"packages,omitempty"`
	// Companions are removed with the line (Source.Companions).
	Companions []string `json:"-"`
	Stream     string   `json:"stream,omitempty"`
	Services   []string `json:"services,omitempty"`
	FPM        *FPM     `json:"fpm,omitempty"`
	CLI        string   `json:"-"`
	Ext        string   `json:"-"`
	// Extensions is true when the line's extensions can be managed.
	Extensions  bool         `json:"extensions,omitempty"`
	Configs     []ConfigFile `json:"configs,omitempty"`
	Recommended bool         `json:"recommended"`
	LTS         bool         `json:"lts"`
	Mainline    bool         `json:"mainline,omitempty"`
	EOL         string       `json:"eol,omitempty"`
	// EndOfLife is true when the line gets no more updates from this
	// offer's source (system packages stay maintained by the distribution
	// and do not count as ended). Such a line can still be installed, with
	// a warning, but is never the recommended one.
	EndOfLife bool `json:"endOfLife"`
	// Installable is true when the line can be installed here now: when
	// somebody builds it for this system. Whether a line is installed is
	// not the catalog's to know; only new installations look at this field.
	Installable bool `json:"installable"`
	// Reason says why a line is not installable.
	Reason string `json:"reason,omitempty"`
	// Sources are every source the line can come from here, the default
	// one first.
	Sources []SourceInfo `json:"sources,omitempty"`
}

// System identifies the machine offers are computed for.
type System struct {
	Chain []string // platform profile chain, nearest first
	Arch  string   // Go architecture
	Now   time.Time
}

func (s System) forRelease(src Source) bool {
	for _, rel := range src.Releases {
		if rel == "*" || slices.Contains(s.Chain, rel) {
			return true
		}
	}
	return false
}

func (s System) matches(src Source) bool {
	return (len(src.Arch) == 0 || slices.Contains(src.Arch, s.Arch)) && s.forRelease(src)
}

func expand(tmpl, line string) string {
	return strings.NewReplacer("{line}", line, "{line_nodot}", strings.ReplaceAll(line, ".", "")).Replace(tmpl)
}

// ended reports whether line l gets no more updates from src as of today:
// upstream support ended and the source is not the distribution's.
func ended(l Line, src *Source, today string) bool {
	return l.EOL != "" && l.EOL < today && src.Method != MethodSystem
}

// sources returns the sources of a line on sys, the default one first: the
// system repositories' when they have the line, then the others in catalog
// order, those whose updates for the line ended last. A line installed
// from any of them is still recognized as such (the helper records which
// one it used), so the order may change with the date.
func (sw *Software) sources(l Line, sys System) []*Source {
	today := sys.Now.Format(time.DateOnly)
	var out []*Source
	for i := range sw.Sources {
		if src := &sw.Sources[i]; slices.Contains(src.Lines, l.Version) && sys.matches(*src) {
			out = append(out, src)
		}
	}
	rank := func(src *Source) int {
		switch {
		case src.Method == MethodSystem:
			return 0
		case !ended(l, src, today):
			return 1
		}
		return 2
	}
	slices.SortStableFunc(out, func(a, b *Source) int { return rank(a) - rank(b) })
	return out
}

// builtElsewhere reports whether some source builds line l for sys's
// release on other architectures only.
func (sw *Software) builtElsewhere(l Line, sys System) bool {
	for _, src := range sw.Sources {
		if slices.Contains(src.Lines, l.Version) && sys.forRelease(src) {
			return true
		}
	}
	return false
}

// resolve describes line l from src, or as not installable when src is nil.
func (sw *Software) resolve(l Line, src *Source, sys System) Offer {
	today := sys.Now.Format(time.DateOnly)
	o := Offer{Line: l.Version, LTS: l.LTS, Mainline: l.Mainline, EOL: l.EOL, EndOfLife: l.EOL != "" && l.EOL < today}
	if src == nil {
		o.Reason = ReasonNotBuilt
		if sw.builtElsewhere(l, sys) {
			o.Reason = ReasonArch
		}
		return o
	}
	o.Method, o.Repo, o.Provider, o.Origin = src.Method, src.Repo, src.Provider, src.Origin()
	for _, p := range src.Packages {
		o.Packages = append(o.Packages, expand(p, l.Version))
	}
	for _, p := range src.Companions {
		o.Companions = append(o.Companions, expand(p, l.Version))
	}
	if src.Stream != "" {
		o.Stream = expand(src.Stream, l.Version)
	}
	for _, u := range src.Services {
		o.Services = append(o.Services, expand(u, l.Version))
	}
	if p := src.PHP; p != nil {
		o.FPM = p.FPM.expand(l.Version)
		o.CLI = expand(p.CLI, l.Version)
		o.Ext = p.Ext
		o.Extensions = p.Ext != "" && len(sw.Extensions()) > 0
	}
	for _, cf := range src.Configs {
		o.Configs = append(o.Configs, cf.expand(l.Version, o.Services))
	}
	o.EndOfLife = ended(l, src, today)
	o.Installable = true
	return o
}

// offer describes line l from the source with origin ("" for the default
// one), with every source the line has here.
func (sw *Software) offer(l Line, sys System, origin string) (Offer, bool) {
	today := sys.Now.Format(time.DateOnly)
	srcs := sw.sources(l, sys)
	var chosen *Source
	var infos []SourceInfo
	for i, src := range srcs {
		if (origin == "" && i == 0) || (origin != "" && src.Origin() == origin) {
			chosen = src
		}
		infos = append(infos, SourceInfo{Origin: src.Origin(), Method: src.Method, Repo: src.Repo, Provider: src.Provider,
			EndOfLife: ended(l, src, today), Default: i == 0})
	}
	if origin != "" && chosen == nil {
		return Offer{}, false
	}
	o := sw.resolve(l, chosen, sys)
	o.Sources = infos
	return o, true
}

// Offers resolves every line of sw for sys from its default source, newest
// first as in the catalog, and marks the recommended line among those that
// can be installed and still get updates. That is upstream's: the newest
// long-term support line, else the newest line that is not a mainline,
// else the newest line. Software with Recommend "system" (PHP) prefers the
// system repositories' line (the newest of them).
//
// A line past its upstream support that the distribution does not maintain
// is marked EndOfLife. It stays installable: the user is warned and asked
// to acknowledge it, and nothing recommends it.
func (sw *Software) Offers(sys System) []Offer {
	offers := make([]Offer, 0, len(sw.Lines))
	for _, l := range sw.Lines {
		o, _ := sw.offer(l, sys, "")
		offers = append(offers, o)
	}
	pick := func(ok func(Offer) bool) bool {
		for i := range offers {
			if offers[i].Installable && !offers[i].EndOfLife && ok(offers[i]) {
				offers[i].Recommended = true
				return true
			}
		}
		return false
	}
	_ = (sw.Recommend == RecommendSystem && pick(func(o Offer) bool { return o.Method == MethodSystem })) ||
		pick(func(o Offer) bool { return o.LTS }) ||
		pick(func(o Offer) bool { return !o.Mainline }) ||
		pick(func(Offer) bool { return true })
	return offers
}

// OfferFrom resolves one line of sw for sys from the source with origin
// ("" for the default one); false when the line or that source is not
// offered here. Recommended is left unset.
func (sw *Software) OfferFrom(sys System, line, origin string) (Offer, bool) {
	for _, l := range sw.Lines {
		if l.Version == line {
			return sw.offer(l, sys, origin)
		}
	}
	return Offer{}, false
}
