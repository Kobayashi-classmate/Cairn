// SPDX-License-Identifier: AGPL-3.0-only

// Package platform identifies the running Linux distribution and resolves the
// profile describing its differences (package manager, unit names, users,
// security modules, ...). Business code works with logical names only; all
// per-distribution facts live in the embedded profile files, so supporting a
// new distribution means adding data, not code.
package platform

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

//go:embed profiles/*.toml
var profileFS embed.FS

// Support tiers, see the distribution support matrix in the design docs.
const (
	TierUnverified = 0 // reached through ID_LIKE fallback, not tested
	Tier1          = 1 // smoke-tested on every commit, end-to-end before release
	Tier2          = 2 // smoke-tested, features as declared by the profile
)

// Profile describes one distribution release. Profiles form an inheritance
// chain (e.g. rocky-9 -> el-9 -> el) and are merged child-over-parent.
type Profile struct {
	Name           string            `toml:"name"`
	Inherits       string            `toml:"inherits"`
	Family         string            `toml:"family"`          // debian | el
	Tier           int               `toml:"tier"`            // see Tier* constants
	MinVersion     string            `toml:"min_version"`     // oldest supported VERSION_ID
	Unsupported    string            `toml:"unsupported"`     // non-empty: refuse with this reason
	PackageManager string            `toml:"package_manager"` // apt | dnf
	WebUser        string            `toml:"web_user"`        // user the web server runs as
	Firewall       string            `toml:"firewall"`        // default manager: none | ufw | firewalld
	MAC            string            `toml:"mac"`             // none | apparmor | selinux
	Services       map[string]string `toml:"services"`        // logical service -> systemd unit
}

// UnsupportedError reports that the running system cannot host Cairn.
type UnsupportedError struct {
	OS     OSRelease
	Reason string
}

func (e *UnsupportedError) Error() string {
	name := e.OS.PrettyName
	if name == "" {
		name = strings.TrimSpace(e.OS.ID + " " + e.OS.VersionID)
	}
	return fmt.Sprintf("platform: %s is not supported: %s", name, e.Reason)
}

// IsUnsupported reports whether err is an *UnsupportedError.
func IsUnsupported(err error) bool {
	var ue *UnsupportedError
	return errors.As(err, &ue)
}

// idAliases maps distribution IDs onto shared family profiles. The raw ID is
// always tried first, so a distribution can override its family (for example
// CentOS Stream is tier 2 while Rocky and Alma are tier 1).
var idAliases = map[string]string{
	"rocky":     "el",
	"almalinux": "el",
	"rhel":      "el",
	"centos":    "el",
}

// Detect resolves the profile of the running system.
func Detect() (*Profile, OSRelease, error) {
	osr, err := ReadOSRelease()
	if err != nil {
		return nil, OSRelease{}, err
	}
	p, err := Resolve(osr)
	return p, osr, err
}

// Resolve finds and loads the profile matching osr.
//
// Lookup order: the exact ID with its version (most specific first), then the
// ID's family alias with the version, then the ID_LIKE entries without a
// version. A profile reached through ID_LIKE is marked TierUnverified, because
// a derivative's VERSION_ID says nothing about its base release.
func Resolve(osr OSRelease) (*Profile, error) {
	var candidates []string
	for _, id := range []string{osr.ID, idAliases[osr.ID]} {
		if id == "" {
			continue
		}
		candidates = append(candidates, versionedNames(id, osr.VersionID)...)
	}
	for _, like := range osr.IDLike {
		if alias, ok := idAliases[like]; ok {
			like = alias
		}
		candidates = append(candidates, like)
	}

	exact := len(candidates) - len(osr.IDLike)
	for i, name := range candidates {
		if !profileExists(name) {
			continue
		}
		p, err := Load(name)
		if err != nil {
			return nil, err
		}
		if p.Unsupported != "" {
			return nil, &UnsupportedError{OS: osr, Reason: p.Unsupported}
		}
		if i >= exact {
			p.Tier = TierUnverified
			return p, nil
		}
		if p.MinVersion != "" && compareVersions(osr.VersionID, p.MinVersion) < 0 {
			return nil, &UnsupportedError{OS: osr, Reason: "version is older than " + p.MinVersion}
		}
		return p, nil
	}
	return nil, &UnsupportedError{OS: osr, Reason: "unknown distribution"}
}

// versionedNames returns "<id>-<v1.v2.v3>", "<id>-<v1.v2>", "<id>-<v1>", "<id>".
func versionedNames(id, version string) []string {
	var names []string
	parts := strings.Split(version, ".")
	for n := len(parts); n >= 1 && version != ""; n-- {
		names = append(names, id+"-"+strings.Join(parts[:n], "."))
	}
	return append(names, id)
}

func profileExists(name string) bool {
	_, err := fs.Stat(profileFS, profilePath(name))
	return err == nil
}

func profilePath(name string) string { return path.Join("profiles", name+".toml") }

// Load reads a profile by name and merges its inheritance chain.
func Load(name string) (*Profile, error) {
	merged, err := loadMerged(name, map[string]bool{})
	if err != nil {
		return nil, err
	}
	raw, err := toml.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("platform: re-encode profile %s: %w", name, err)
	}
	var p Profile
	if err := toml.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("platform: decode profile %s: %w", name, err)
	}
	p.Name = name
	p.Inherits = ""
	return &p, nil
}

func loadMerged(name string, seen map[string]bool) (map[string]any, error) {
	if seen[name] {
		return nil, fmt.Errorf("platform: profile inheritance cycle at %s", name)
	}
	seen[name] = true

	data, err := profileFS.ReadFile(profilePath(name))
	if err != nil {
		return nil, fmt.Errorf("platform: profile %s: %w", name, err)
	}
	var own map[string]any
	if err := toml.Unmarshal(data, &own); err != nil {
		return nil, fmt.Errorf("platform: parse profile %s: %w", name, err)
	}
	parent, _ := own["inherits"].(string)
	if parent == "" {
		return own, nil
	}
	base, err := loadMerged(parent, seen)
	if err != nil {
		return nil, err
	}
	return mergeMaps(base, own), nil
}

// mergeMaps deep-merges src over dst; nested tables merge, other values replace.
func mergeMaps(dst, src map[string]any) map[string]any {
	out := make(map[string]any, len(dst)+len(src))
	for k, v := range dst {
		out[k] = v
	}
	for k, v := range src {
		if sm, ok := v.(map[string]any); ok {
			if dm, ok := out[k].(map[string]any); ok {
				out[k] = mergeMaps(dm, sm)
				continue
			}
		}
		out[k] = v
	}
	return out
}

// Service returns the systemd unit for a logical service name.
func (p *Profile) Service(logical string) (string, bool) {
	unit, ok := p.Services[logical]
	return unit, ok
}

// Chain returns name followed by the profiles it inherits from, nearest
// first (for example "el-9", "el"). Data keyed by release, such as the
// software catalog, matches against this chain.
func Chain(name string) ([]string, error) {
	var chain []string
	for name != "" {
		if slices.Contains(chain, name) {
			return nil, fmt.Errorf("platform: profile inheritance cycle at %s", name)
		}
		data, err := profileFS.ReadFile(profilePath(name))
		if err != nil {
			return nil, fmt.Errorf("platform: profile %s: %w", name, err)
		}
		var own struct {
			Inherits string `toml:"inherits"`
		}
		if err := toml.Unmarshal(data, &own); err != nil {
			return nil, fmt.Errorf("platform: parse profile %s: %w", name, err)
		}
		chain = append(chain, name)
		name = own.Inherits
	}
	return chain, nil
}

// Names lists every profile name.
func Names() []string {
	entries, _ := fs.ReadDir(profileFS, "profiles")
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if n, ok := strings.CutSuffix(e.Name(), ".toml"); ok {
			names = append(names, n)
		}
	}
	return names
}
