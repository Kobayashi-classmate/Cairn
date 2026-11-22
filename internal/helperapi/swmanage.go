// SPDX-License-Identifier: AGPL-3.0-only

package helperapi

import "regexp"

// Actions on an installed line: its services, its PHP extensions and its
// configuration files. As with installing, the web process names only a
// catalog entry, a line and catalog IDs; the helper resolves units,
// packages and paths from its own copy of the catalog.
const (
	ActionPkgService     = "pkg.service"      // operation: start, stop, restart or reload the line's services
	ActionPkgExtStatus   = "pkg.ext_status"   // synchronous: the line's extensions
	ActionPkgExtApply    = "pkg.ext_apply"    // operation: install and remove extensions
	ActionPkgConfigRead  = "pkg.config_read"  // synchronous: a configuration file or one of its kept versions
	ActionPkgConfigWrite = "pkg.config_write" // operation: check, save and apply a configuration file
)

// Error codes of these actions.
const (
	CodePkgServiceNone       = "pkg.service_none"
	CodePkgServiceNoReload   = "pkg.service_no_reload"
	CodePkgExtUnsupported    = "pkg.ext_unsupported"
	CodePkgExtUnknown        = "pkg.ext_unknown"
	CodePkgExtUnavailable    = "pkg.ext_unavailable"
	CodePkgExtNeeded         = "pkg.ext_needed"
	CodePkgConfigUnknown     = "pkg.config_unknown"
	CodePkgConfigMissing     = "pkg.config_missing"
	CodePkgConfigChanged     = "pkg.config_changed"
	CodePkgConfigLocked      = "pkg.config_locked"
	CodePkgConfigTooLarge    = "pkg.config_too_large"
	CodePkgConfigInvalid     = "pkg.config_invalid"
	CodePkgConfigApplyFailed = "pkg.config_apply_failed"
	CodePkgConfigVersion     = "pkg.config_version_unknown"
	CodePkgConfigNotText     = "pkg.config_not_text"
	// CodePkgRemoveTakes: removing the line would remove other installed
	// packages with it (params: packages, by).
	CodePkgRemoveTakes = "pkg.remove_takes"
	// CodePkgInUse: sites use the line (params: software, line, sites).
	CodePkgInUse = "pkg.in_use"
)

// ServiceReload reloads a line's services (beside ServiceStart,
// ServiceStop and ServiceRestart).
const ServiceReload = "reload"

// ConfigMax is the largest configuration file shown and saved.
const ConfigMax = 512 << 10

// ConfigVersionsKept is how many earlier versions of a file are kept.
const ConfigVersionsKept = 10

var (
	configIDPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	extIDPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,31}$`)
	versionIDPattern = regexp.MustCompile(`^[0-9]{1,20}$`)
	sha256Pattern    = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ValidConfigID reports whether s can name a catalog configuration file.
func ValidConfigID(s string) bool { return configIDPattern.MatchString(s) }

// ValidExtensionID reports whether s can name a catalog extension.
func ValidExtensionID(s string) bool { return extIDPattern.MatchString(s) }

func validLine(software, line string) error {
	if !softwareIDPattern.MatchString(software) || !linePattern.MatchString(line) {
		return invalid("software", "catalog")
	}
	return nil
}

// LineRef names an installed line.
type LineRef struct {
	Software string `json:"software"`
	Line     string `json:"line"`
}

// Validate implements the helper's request validation hook.
func (r LineRef) Validate() error { return validLine(r.Software, r.Line) }

// ServiceRequest starts, stops, restarts or reloads every service of a line.
type ServiceRequest struct {
	Software string `json:"software"`
	Line     string `json:"line"`
	Action   string `json:"action"`
}

// Validate implements the helper's request validation hook.
func (r ServiceRequest) Validate() error {
	if err := validLine(r.Software, r.Line); err != nil {
		return err
	}
	switch r.Action {
	case ServiceStart, ServiceStop, ServiceRestart, ServiceReload:
		return nil
	}
	return invalid("action", "oneof")
}

// ExtensionInfo is one catalog extension of a PHP line on this system.
type ExtensionInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Modules are the PHP modules it provides, lowercase as "php -m"
	// prints them.
	Modules []string `json:"modules"`
	// Available: it can be installed here (a package is known for the
	// line's source), or it is built in.
	Available bool `json:"available"`
	Installed bool `json:"installed"` // its package is installed
	Loaded    bool `json:"loaded"`    // every one of its modules is loaded
	// Builtin: loaded without a package of its own (compiled in, or part
	// of the line's common package).
	Builtin bool `json:"builtin"`
}

// ExtStatus is the result of pkg.ext_status.
type ExtStatus struct {
	Extensions []ExtensionInfo `json:"extensions"`
	// Modules lists every module the line's PHP loads, lowercase, sorted;
	// nil when the line's PHP could not be run.
	Modules []string `json:"modules"`
}

// ExtApplyRequest installs and removes extensions of a PHP line.
type ExtApplyRequest struct {
	Software string   `json:"software"`
	Line     string   `json:"line"`
	Install  []string `json:"install,omitempty"`
	Remove   []string `json:"remove,omitempty"`
}

// MaxExtensionsPerApply caps one pkg.ext_apply.
const MaxExtensionsPerApply = 32

// Validate implements the helper's request validation hook.
func (r ExtApplyRequest) Validate() error {
	if err := validLine(r.Software, r.Line); err != nil {
		return err
	}
	n := len(r.Install) + len(r.Remove)
	if n == 0 || n > MaxExtensionsPerApply {
		return invalid("install", "count")
	}
	seen := map[string]bool{}
	for _, id := range append(append([]string{}, r.Install...), r.Remove...) {
		if !ValidExtensionID(id) {
			return invalid("install", "extension")
		}
		if seen[id] {
			return invalid("install", "unique")
		}
		seen[id] = true
	}
	return nil
}

// ExtApplyResult is the result of pkg.ext_apply.
type ExtApplyResult struct {
	Installed []InstalledPackage `json:"installed,omitempty"`
	Removed   []string           `json:"removed,omitempty"`
	// Reloaded is the PHP-FPM service reloaded afterwards ("" when it was
	// not running).
	Reloaded string `json:"reloaded,omitempty"`
}

// ConfigRequest reads a configuration file of a line, or with Version one
// of the versions kept from earlier saves.
type ConfigRequest struct {
	Software string `json:"software"`
	Line     string `json:"line"`
	Config   string `json:"config"`
	Version  string `json:"version,omitempty"`
}

// Validate implements the helper's request validation hook.
func (r ConfigRequest) Validate() error {
	if err := validLine(r.Software, r.Line); err != nil {
		return err
	}
	if !ValidConfigID(r.Config) {
		return invalid("config", "id")
	}
	if r.Version != "" && !versionIDPattern.MatchString(r.Version) {
		return invalid("version", "id")
	}
	return nil
}

// ConfigVersion is an earlier version of a file, kept when it was replaced.
type ConfigVersion struct {
	ID      string `json:"id"`
	SavedAt int64  `json:"savedAt"` // unix seconds
	Size    int64  `json:"size"`
}

// ConfigFile is a configuration file as pkg.config_read returns it.
type ConfigFile struct {
	ID      string `json:"id"`
	Path    string `json:"path"`
	Content string `json:"content"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"modTime"`
	// Editable is false when the server forbids root-level changes from
	// the panel ([security] root_access = "never").
	Editable bool `json:"editable"`
	// Versions are the kept earlier versions, newest first (only when the
	// file itself was read).
	Versions []ConfigVersion `json:"versions"`
	// SavedAt is set when a kept version was read.
	SavedAt int64 `json:"savedAt,omitempty"`
}

// ConfigWriteRequest saves a configuration file. Base is the SHA-256 of
// the content it was edited from: a file changed since fails
// (pkg.config_changed).
type ConfigWriteRequest struct {
	Software string `json:"software"`
	Line     string `json:"line"`
	Config   string `json:"config"`
	Content  string `json:"content"`
	Base     string `json:"base"`
}

// Validate implements the helper's request validation hook.
func (r ConfigWriteRequest) Validate() error {
	if err := (ConfigRequest{Software: r.Software, Line: r.Line, Config: r.Config}).Validate(); err != nil {
		return err
	}
	if len(r.Content) > ConfigMax {
		return &Error{Code: CodePkgConfigTooLarge, Params: map[string]any{"max": ConfigMax}}
	}
	if !sha256Pattern.MatchString(r.Base) {
		return invalid("base", "sha256")
	}
	return nil
}

// ConfigWriteResult is the result of pkg.config_write.
type ConfigWriteResult struct {
	SHA256 string `json:"sha256"`
	// Changed is false when the content was the same as the file's.
	Changed bool `json:"changed"`
	// Applied names the service reloaded or restarted ("" when it was not
	// running or the file needs none).
	Applied string `json:"applied,omitempty"`
}

// LinePlan is what installing a line will do, as pkg.probe sees it.
type LinePlan struct {
	Software string `json:"software"`
	Line     string `json:"line"`
	Method   string `json:"method"`
	Repo     string `json:"repo,omitempty"`
	// RepoReady: the vendor repository is set up already.
	RepoReady bool   `json:"repoReady"`
	Stream    string `json:"stream,omitempty"`
	// Packages carry the exact versions that will be installed; a version
	// is "" when it cannot be known yet (the repository is added first, or
	// the indexes do not have the line).
	Packages  []InstalledPackage `json:"packages"`
	Libraries []string           `json:"libraries,omitempty"`
	Services  []string           `json:"services,omitempty"`
}
