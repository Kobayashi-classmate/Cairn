// SPDX-License-Identifier: AGPL-3.0-only

package module

import (
	"context"
	"net/http"
)

// Lookups between modules. A module that shows another module's data has a
// field of one of these interfaces, which cmd/cairn-web fills with the
// module that owns the data. A nil field, or an owner that is turned off,
// means the data is not shown.

// CertInfo is what other modules show of a certificate.
type CertInfo struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"` // issuing | valid | failed
	// NotAfter is when the certificate expires (Unix seconds; 0 before it
	// was first issued).
	NotAfter  int64 `json:"notAfter,omitempty"`
	AutoRenew bool  `json:"autoRenew"`
}

// CertLookup is implemented by the certificates module.
type CertLookup interface {
	// CertInfo returns the certificates among ids that exist, by id.
	CertInfo(ctx context.Context, ids []string) (map[string]CertInfo, error)
}

// TargetBackup is how a site or a database is backed up.
type TargetBackup struct {
	// Plans are the backup plans that include it.
	Plans []string `json:"plans"`
	// LastOKAt is when the newest successful run of those plans finished
	// (Unix seconds; 0: never).
	LastOKAt int64 `json:"lastOkAt,omitempty"`
	// LastFailedAt is when the newest failed run started, set only when it
	// is newer than the last successful one.
	LastFailedAt int64 `json:"lastFailedAt,omitempty"`
}

// BackupLookup is implemented by the backups module.
type BackupLookup interface {
	// SiteBackups reports, by site code, the sites that backup plans
	// include; r identifies the caller to the root helper.
	SiteBackups(r *http.Request) (map[string]TargetBackup, error)
	// DatabaseBackups reports the databases that backup plans include, by
	// DatabaseKey.
	DatabaseBackups(r *http.Request) (map[string]TargetBackup, error)
}

// DatabaseKey names a database of an engine in BackupLookup results.
func DatabaseKey(engine, name string) string { return engine + "/" + name }

// Enabler reports whether a module is on (the Registry).
type Enabler interface {
	Enabled(id string) bool
}
