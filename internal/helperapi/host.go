// SPDX-License-Identifier: AGPL-3.0-only

package helperapi

import (
	"regexp"
	"strings"
)

// Host management: host name, time zone and NTP, swap, reboot, system
// updates (separate from the panel's own update) and the system's package
// sources.
const (
	ActionHostStatus         = "host.status"          // synchronous
	ActionHostHostname       = "host.hostname"        // synchronous
	ActionHostTimezones      = "host.timezones"       // synchronous
	ActionHostTimezone       = "host.timezone"        // synchronous
	ActionHostNTP            = "host.ntp"             // synchronous
	ActionHostSwap           = "host.swap"            // operation
	ActionHostSwappiness     = "host.swappiness"      // synchronous
	ActionHostReboot         = "host.reboot"          // synchronous: in a few seconds
	ActionHostUpdatesCheck   = "host.updates.check"   // operation
	ActionHostUpdatesInstall = "host.updates.install" // operation
	ActionHostUpdatesAuto    = "host.updates.auto"    // operation
	ActionSysSourcesProbe    = "syssrc.probe"         // synchronous
	ActionSysSourcesSwitch   = "syssrc.switch"        // operation
	ActionSysSourcesBackups  = "syssrc.backups"       // synchronous
	ActionSysSourcesRestore  = "syssrc.restore"       // operation
)

// Host error codes.
const (
	CodeHostHostnameInvalid    = "host.hostname_invalid"
	CodeHostTimezoneInvalid    = "host.timezone_invalid"
	CodeHostNTPUnavailable     = "host.ntp_unavailable"
	CodeHostSwapUnsupported    = "host.swap_unsupported"
	CodeHostSwapNoSpace        = "host.swap_no_space"
	CodeHostSwapNotManaged     = "host.swap_not_managed"
	CodeHostUpdatesUnavailable = "host.updates_unavailable"
	CodeHostRebootFailed       = "host.reboot_failed"
	CodeHostRebootBusy         = "host.reboot_busy" // the package manager is changing packages
	CodeSysSrcNotEligible      = "syssrc.not_eligible"
	CodeSysSrcInUse            = "syssrc.in_use"
	CodeSysSrcMixed            = "syssrc.mixed"
	CodeSysSrcUpdateFailed     = "syssrc.update_failed"
	CodeSysSrcBackupNotFound   = "syssrc.backup_not_found"
	CodeSysSrcUnsupported      = "syssrc.unsupported"
	CodeSysSrcHTTPSRequired    = "syssrc.https_required"
)

// Limits.
const (
	SwapMinMB = 128
	SwapMaxMB = 65536
)

// HostNTP is the clock synchronization.
type HostNTP struct {
	Available    bool `json:"available"`
	Enabled      bool `json:"enabled"`
	Synchronized bool `json:"synchronized"`
}

// SwapDevice is one active swap area.
type SwapDevice struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"` // file | partition
	Size    int64  `json:"size"`
	Used    int64  `json:"used"`
	Managed bool   `json:"managed"`
}

// HostSwap describes swap.
type HostSwap struct {
	Total      int64        `json:"total"`
	Used       int64        `json:"used"`
	Swappiness int          `json:"swappiness"`
	Devices    []SwapDevice `json:"devices"`
	Supported  bool         `json:"supported"`
	Reason     string       `json:"reason"`
}

// HostKernel compares the running kernel with the newest installed one.
type HostKernel struct {
	Running string `json:"running"`
	Latest  string `json:"latest"`
}

// AutoUpdates is the distribution's automatic security updates.
type AutoUpdates struct {
	Available bool   `json:"available"`
	Enabled   bool   `json:"enabled"`
	Tool      string `json:"tool"`
}

// PackageUpdate is one package with a newer version.
type PackageUpdate struct {
	Name     string `json:"name"`
	From     string `json:"from"`
	To       string `json:"to"`
	Security bool   `json:"security"`
}

// SourceWarning reports a system source in use that lags behind.
type SourceWarning struct {
	Role    string `json:"role"`
	Warning string `json:"warning"`
}

// UpdateList is the result of the last update check.
type UpdateList struct {
	CheckedAt int64           `json:"checkedAt"`
	Packages  []PackageUpdate `json:"packages"`
	Sources   []SourceWarning `json:"sources"`
}

// HostStatus describes the host.
type HostStatus struct {
	Hostname       string      `json:"hostname"`
	Timezone       string      `json:"timezone"`
	NTP            HostNTP     `json:"ntp"`
	Swap           HostSwap    `json:"swap"`
	Kernel         HostKernel  `json:"kernel"`
	RebootRequired bool        `json:"rebootRequired"`
	AutoUpdates    AutoUpdates `json:"autoUpdates"`
	Updates        UpdateList  `json:"updates"`
}

var hostnameLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidHostname reports whether s is a host name (RFC 1123, lower case).
func ValidHostname(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	for _, l := range strings.Split(s, ".") {
		if !hostnameLabel.MatchString(l) {
			return false
		}
	}
	return true
}

// HostnameRequest sets the host name.
type HostnameRequest struct {
	Hostname string `json:"hostname"`
}

// Validate implements the helper's request validation hook.
func (r HostnameRequest) Validate() error {
	if !ValidHostname(r.Hostname) {
		return &Error{Code: CodeHostHostnameInvalid}
	}
	return nil
}

var timezoneName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+-]*(/[A-Za-z0-9_+-]+){0,2}$`)

// TimezoneRequest sets the time zone (the helper checks it against the
// system's list).
type TimezoneRequest struct {
	Timezone string `json:"timezone"`
}

// Validate implements the helper's request validation hook.
func (r TimezoneRequest) Validate() error {
	if !timezoneName.MatchString(r.Timezone) || len(r.Timezone) > 64 {
		return &Error{Code: CodeHostTimezoneInvalid}
	}
	return nil
}

// Timezones lists the time zones.
type Timezones struct {
	Zones []string `json:"zones"`
}

// NTPRequest switches clock synchronization.
type NTPRequest struct {
	Enabled bool `json:"enabled"`
}

// SwapRequest sizes the swap file Cairn manages (0 removes it).
type SwapRequest struct {
	SizeMB int `json:"sizeMb"`
}

// Validate implements the helper's request validation hook.
func (r SwapRequest) Validate() error {
	if r.SizeMB != 0 && (r.SizeMB < SwapMinMB || r.SizeMB > SwapMaxMB) {
		return invalid("sizeMb", "range")
	}
	return nil
}

// SwappinessRequest sets vm.swappiness.
type SwappinessRequest struct {
	Value int `json:"value"`
}

// Validate implements the helper's request validation hook.
func (r SwappinessRequest) Validate() error {
	if r.Value < 0 || r.Value > 100 {
		return invalid("value", "range")
	}
	return nil
}

// RebootResult says when the reboot starts.
type RebootResult struct {
	At int64 `json:"at"`
}

// UpdatesInstallRequest installs all updates or the security ones.
type UpdatesInstallRequest struct {
	SecurityOnly bool `json:"securityOnly"`
}

// AutoUpdatesRequest switches automatic security updates.
type AutoUpdatesRequest struct {
	Enabled bool `json:"enabled"`
}

// SysSourcesProbe is the probe of the system's sources by role.
type SysSourcesProbe struct {
	Slots []SourceSlot `json:"slots"`
}

// SysSourceSwitchRequest moves a role to one of its probed candidates.
type SysSourceSwitchRequest struct {
	Role      string `json:"role"`
	Candidate string `json:"candidate"`
}

var candidateID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// Validate implements the helper's request validation hook.
func (r SysSourceSwitchRequest) Validate() error {
	if r.Role != "main" && r.Role != "security" {
		return invalid("role", "oneof")
	}
	if !candidateID.MatchString(r.Candidate) {
		return invalid("candidate", "id")
	}
	return nil
}

// SysSourceBackup is a copy of the source files taken before a switch.
type SysSourceBackup struct {
	ID    string   `json:"id"`
	At    int64    `json:"at"`
	Role  string   `json:"role"`
	From  string   `json:"from"`
	To    string   `json:"to"`
	Files []string `json:"files"`
}

// SysSourceBackups lists the backups, newest first.
type SysSourceBackups struct {
	Backups []SysSourceBackup `json:"backups"`
}

var backupID = regexp.MustCompile(`^b[0-9]{10}[0-9a-f]{6}$`)

// SysSourceRef names a backup.
type SysSourceRef struct {
	ID string `json:"id"`
}

// Validate implements the helper's request validation hook.
func (r SysSourceRef) Validate() error {
	if !backupID.MatchString(r.ID) {
		return invalid("id", "id")
	}
	return nil
}
