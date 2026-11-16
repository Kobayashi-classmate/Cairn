// SPDX-License-Identifier: AGPL-3.0-only

package helperapi

import (
	"net"
	"regexp"
	"strconv"
	"strings"
)

// Backups: a plan names what to back up (sites, databases, directories),
// where to (the panel's backups directory and, optionally, an
// S3-compatible bucket), whom to encrypt for (age recipients: the panel
// never holds a private key) and how many to keep. The helper keeps the
// plans it was given, with their storage credentials, in its own
// root-only state, so a timer can run them without the panel.
const (
	ActionBackupPlanApply  = "backup.plan_apply"  // synchronous
	ActionBackupPlanRemove = "backup.plan_remove" // synchronous
	ActionBackupRun        = "backup.run"         // operation
	ActionBackupList       = "backup.list"        // synchronous
	ActionBackupRuns       = "backup.runs"        // synchronous
	ActionBackupDelete     = "backup.delete"      // operation
	ActionBackupPresign    = "backup.presign"     // synchronous: a download link for a remote copy
	ActionBackupFetch      = "backup.fetch"       // operation: bring a remote copy back to the local directory
	ActionBackupRestore    = "backup.restore"     // operation
	ActionBackupTest       = "backup.storage_test"
)

// Target kinds.
const (
	BackupSite      = "site"
	BackupDatabase  = "database"
	BackupDirectory = "directory"
)

// Backup error codes.
const (
	CodeBackupPlanNotFound = "backup.plan_not_found"
	CodeBackupNotFound     = "backup.not_found"
	CodeBackupStorage      = "backup.storage_failed"
	CodeBackupIdentity     = "backup.identity_required" // an encrypted backup needs the private key
	CodeBackupBadIdentity  = "backup.bad_identity"
	CodeBackupRunning      = "backup.running"
	CodeBackupNoStorage    = "backup.no_storage"
)

var (
	bucketName     = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	ageRecipient   = regexp.MustCompile(`^age1[02-9ac-hj-np-z]{58}$`)
	backupFileName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,200}$`)
	backupStamp    = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}$`)
	// Endpoints may be single-label names (localhost, a host on a private
	// network) as well as public ones.
	endpointHost = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)
)

// ValidAgeRecipient reports whether s is an age X25519 recipient.
func ValidAgeRecipient(s string) bool { return ageRecipient.MatchString(s) }

// ValidBackupName reports whether s names a backup file of a plan.
func ValidBackupName(s string) bool {
	return backupFileName.MatchString(s) && !strings.HasPrefix(s, ".") && !strings.Contains(s, "..")
}

// S3Storage is an S3-compatible bucket.
type S3Storage struct {
	// Endpoint is host[:port], without a scheme.
	Endpoint  string `json:"endpoint"`
	Region    string `json:"region,omitempty"`
	Bucket    string `json:"bucket"`
	Prefix    string `json:"prefix,omitempty"`
	AccessKey string `json:"accessKey"`
	SecretKey string `json:"secretKey,omitempty"`
	// PathStyle addresses the bucket in the path (MinIO and some others).
	PathStyle bool `json:"pathStyle,omitempty"`
	// Insecure uses plain HTTP (a local MinIO, say).
	Insecure bool `json:"insecure,omitempty"`
}

// Validate implements the helper's request validation hook.
func (s S3Storage) Validate() error {
	host, port, err := net.SplitHostPort(s.Endpoint)
	if err != nil {
		host, port = s.Endpoint, ""
	}
	if n, perr := strconv.Atoi(port); !endpointHost.MatchString(host) && net.ParseIP(host) == nil || port != "" && (perr != nil || n < 1 || n > 65535) {
		return invalid("endpoint", "host")
	}
	if !bucketName.MatchString(s.Bucket) {
		return invalid("bucket", "bucket")
	}
	if len(s.Region) > 64 || strings.ContainsAny(s.Region, " /\x00") {
		return invalid("region", "region")
	}
	if s.Prefix != "" && (!ValidFilePath(s.Prefix) || len(s.Prefix) > 200) {
		return invalid("prefix", "file_path")
	}
	if s.AccessKey == "" || len(s.AccessKey) > 256 || len(s.SecretKey) > 256 || strings.ContainsAny(s.AccessKey+s.SecretKey, "\x00\n") {
		return invalid("accessKey", "credentials")
	}
	return nil
}

// BackupTarget is one thing a plan backs up.
type BackupTarget struct {
	Kind string `json:"kind"`
	// Site is a site code.
	Site string `json:"site,omitempty"`
	// Engine and Database name a database.
	Engine   string `json:"engine,omitempty"`
	Database string `json:"database,omitempty"`
	// Root and Path name a directory as the file manager does.
	Root string `json:"root,omitempty"`
	Path string `json:"path,omitempty"`
}

// Validate implements the helper's request validation hook.
func (t BackupTarget) Validate() error {
	switch t.Kind {
	case BackupSite:
		if !ValidSiteCode(t.Site) {
			return invalid("site", "site_code")
		}
	case BackupDatabase:
		if err := validEngine(t.Engine); err != nil || EngineKind(t.Engine) == DBRedis {
			return invalid("engine", "oneof")
		}
		if !ValidDBName(t.Engine, t.Database) {
			return invalid("database", "db_name")
		}
	case BackupDirectory:
		if err := (FileRef{Root: t.Root, Path: t.Path}).Validate(); err != nil {
			return err
		}
	default:
		return invalid("kind", "oneof")
	}
	return nil
}

// Key names a target in backup file names.
func (t BackupTarget) Key() string {
	switch t.Kind {
	case BackupSite:
		return "site-" + t.Site
	case BackupDatabase:
		return "db-" + t.Engine + "-" + t.Database
	}
	key := t.Root
	if t.Path != "" {
		key += "-" + t.Path
	}
	return "dir-" + strings.NewReplacer("/", "-", ".", "-").Replace(key)
}

// Retention keeps the newest Last backups of each target and the newest
// of each of the last Daily days, Weekly weeks and Monthly months.
type Retention struct {
	Last    int `json:"last"`
	Daily   int `json:"daily"`
	Weekly  int `json:"weekly"`
	Monthly int `json:"monthly"`
}

// DefaultRetention is 7 daily, 4 weekly, 6 monthly.
var DefaultRetention = Retention{Last: 1, Daily: 7, Weekly: 4, Monthly: 6}

// BackupPlan is a plan as the helper keeps it.
type BackupPlan struct {
	ID      string         `json:"id"`
	Targets []BackupTarget `json:"targets"`
	// Storage, when set, receives a copy of every backup.
	Storage *S3Storage `json:"storage,omitempty"`
	// KeepLocal keeps the backups in the panel's directory too (always
	// true without a storage).
	KeepLocal  bool      `json:"keepLocal"`
	Retention  Retention `json:"retention"`
	Recipients []string  `json:"recipients,omitempty"`
}

// Validate implements the helper's request validation hook.
func (p BackupPlan) Validate() error {
	if !ValidPlanID(p.ID) {
		return invalid("id", "plan_id")
	}
	if len(p.Targets) == 0 || len(p.Targets) > 100 {
		return invalid("targets", "count")
	}
	seen := map[string]bool{}
	for _, t := range p.Targets {
		if err := t.Validate(); err != nil {
			return err
		}
		if seen[t.Key()] {
			return invalid("targets", "duplicate")
		}
		seen[t.Key()] = true
	}
	if p.Storage != nil {
		if err := p.Storage.Validate(); err != nil {
			return err
		}
	}
	r := p.Retention
	if r.Last < 0 || r.Daily < 0 || r.Weekly < 0 || r.Monthly < 0 || r.Last+r.Daily+r.Weekly+r.Monthly == 0 ||
		r.Last > 1000 || r.Daily > 1000 || r.Weekly > 1000 || r.Monthly > 1000 {
		return invalid("retention", "retention")
	}
	if len(p.Recipients) > 16 {
		return invalid("recipients", "count")
	}
	for _, rc := range p.Recipients {
		if !ValidAgeRecipient(rc) {
			return invalid("recipients", "age_recipient")
		}
	}
	return nil
}

// PlanRef names a plan.
type PlanRef struct {
	Plan string `json:"plan"`
}

// Validate implements the helper's request validation hook.
func (r PlanRef) Validate() error {
	if !ValidPlanID(r.Plan) {
		return invalid("plan", "plan_id")
	}
	return nil
}

// PlanRemoveRequest forgets a plan, and with Files its backups too.
type PlanRemoveRequest struct {
	Plan  string `json:"plan"`
	Files bool   `json:"files,omitempty"`
}

// Validate implements the helper's request validation hook.
func (r PlanRemoveRequest) Validate() error { return PlanRef{Plan: r.Plan}.Validate() }

// DatabaseOfBackup reads the engine and database of a database backup's
// file name (db-mysql-<name>_… or db-postgresql-<line>-<name>_…).
func DatabaseOfBackup(name string) (string, string, bool) {
	key := name
	if i := strings.LastIndex(name, "_"); i > 0 && len(name) >= i+16 && backupStamp.MatchString(name[i+1:i+16]) {
		key = name[:i]
	}
	rest, ok := strings.CutPrefix(key, "db-")
	if !ok {
		return "", "", false
	}
	if after, ok := strings.CutPrefix(rest, "mysql-"); ok {
		return DBMySQL, after, after != ""
	}
	if after, ok := strings.CutPrefix(rest, "postgresql-"); ok {
		line, db, ok := strings.Cut(after, "-")
		return "postgresql-" + line, db, ok && db != ""
	}
	return "", "", false
}

// BackupFile is a backup of a target, here and/or in the bucket.
type BackupFile struct {
	Name      string `json:"name"`
	Target    string `json:"target"`
	Size      int64  `json:"size"`
	Created   int64  `json:"created"`
	Local     bool   `json:"local"`
	Remote    bool   `json:"remote"`
	Encrypted bool   `json:"encrypted"`
}

// BackupList is the result of backup.list, newest first.
type BackupList struct {
	Files []BackupFile `json:"files"`
	// RemoteError is why the bucket could not be listed.
	RemoteError *Error `json:"remoteError,omitempty"`
}

// BackupRun is one run of a plan.
type BackupRun struct {
	Started  int64    `json:"started"`
	Finished int64    `json:"finished,omitempty"`
	OK       bool     `json:"ok"`
	Error    *Error   `json:"error,omitempty"`
	Files    []string `json:"files,omitempty"`
	Size     int64    `json:"size"`
	// Deleted counts the backups retention removed.
	Deleted int `json:"deleted,omitempty"`
}

// BackupRuns is the result of backup.runs, newest first.
type BackupRuns struct {
	Runs []BackupRun `json:"runs"`
}

// BackupFilesRequest names backups of a plan.
type BackupFilesRequest struct {
	Plan  string   `json:"plan"`
	Names []string `json:"names"`
	// Remote also deletes the bucket's copies (backup.delete).
	Remote bool `json:"remote,omitempty"`
}

// Validate implements the helper's request validation hook.
func (r BackupFilesRequest) Validate() error {
	if !ValidPlanID(r.Plan) {
		return invalid("plan", "plan_id")
	}
	if len(r.Names) == 0 || len(r.Names) > 1000 {
		return invalid("names", "count")
	}
	for _, n := range r.Names {
		if !ValidBackupName(n) {
			return invalid("names", "backup_name")
		}
	}
	return nil
}

// PresignResponse is a link to download a remote copy (minutes valid).
type PresignResponse struct {
	URL     string `json:"url"`
	Expires int64  `json:"expires"`
}

// BackupRestoreRequest unpacks a backup. Site and directory backups go
// into Dest (a directory of the file manager); database backups become a
// dump of the database module, restored from there as a database user.
// Identity is the age private key of an encrypted backup: used for this
// request only, never stored.
type BackupRestoreRequest struct {
	Plan      string  `json:"plan"`
	Name      string  `json:"name"`
	Identity  string  `json:"identity,omitempty"`
	Dest      FileRef `json:"dest"`
	Overwrite bool    `json:"overwrite,omitempty"`
}

var ageIdentity = regexp.MustCompile(`^AGE-SECRET-KEY-1[02-9AC-HJ-NP-Z]{58}$`)

// Validate implements the helper's request validation hook.
func (r BackupRestoreRequest) Validate() error {
	if !ValidPlanID(r.Plan) {
		return invalid("plan", "plan_id")
	}
	if !ValidBackupName(r.Name) {
		return invalid("name", "backup_name")
	}
	if r.Identity != "" && !ageIdentity.MatchString(r.Identity) {
		return invalid("identity", "age_identity")
	}
	if !strings.HasPrefix(r.Name, "db-") {
		return r.Dest.Validate()
	}
	return nil
}

// BackupRestoreResult says where a restored backup went.
type BackupRestoreResult struct {
	// Dump is the database dump made (engine and file), for db.restore.
	Engine string `json:"engine,omitempty"`
	Dump   string `json:"dump,omitempty"`
	Done   int    `json:"done,omitempty"`
}

// StorageTestRequest checks credentials by listing the bucket.
type StorageTestRequest struct {
	Storage S3Storage `json:"storage"`
}

// Validate implements the helper's request validation hook.
func (r StorageTestRequest) Validate() error { return r.Storage.Validate() }
