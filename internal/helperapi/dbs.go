// SPDX-License-Identifier: AGPL-3.0-only

package helperapi

import (
	"net"
	"regexp"
	"strings"
)

// Database actions. The engine of a request is "mysql" (MariaDB or MySQL,
// whichever is installed), "postgresql-<line>" (one cluster per line) or
// "redis" (Redis or Valkey).
const (
	ActionDBStatus    = "db.status"     // synchronous
	ActionDBCreate    = "db.create"     // operation
	ActionDBDrop      = "db.drop"       // operation
	ActionDBUserSet   = "db.user_set"   // operation: create or change a user
	ActionDBUserDrop  = "db.user_drop"  // operation
	ActionDBRemote    = "db.remote"     // operation: listen on every address or locally only
	ActionDBDump      = "db.dump"       // operation
	ActionDBRestore   = "db.restore"    // operation
	ActionDBPGCluster = "db.pg_cluster" // operation: create a line's main cluster
	ActionDBService   = "db.service"    // operation: start, stop or restart an engine
	ActionRedisConfig = "redis.config"  // operation
	ActionRedisScan   = "redis.scan"    // synchronous
	ActionRedisGet    = "redis.get"     // synchronous
	ActionRedisDelete = "redis.delete"  // synchronous
	ActionRedisSetTTL = "redis.ttl"     // synchronous
)

// Database error codes.
const (
	CodeDBUnsupported   = "db.unsupported"
	CodeDBEngineMissing = "db.engine_missing" // not installed
	CodeDBNotRunning    = "db.not_running"
	CodeDBExists        = "db.exists"
	CodeDBNotFound      = "db.not_found"
	CodeDBUserNotFound  = "db.user_not_found"
	CodeDBUserExists    = "db.user_exists"        // a new user's name is taken
	CodeDBUserOwns      = "db.user_owns_database" // a PostgreSQL role owning a database
	CodeDBQuery         = "db.query_failed"
	CodeDBReserved      = "db.reserved_name"
	CodeDBNoCluster     = "db.no_cluster"
	CodeDBDumpNotFound  = "db.dump_not_found"
	CodeDBAccessDenied  = "db.access_denied"     // the helper cannot log in as the administrator
	CodeDBSystemd       = "db.systemd_required"  // restarting the service needs systemd
	CodeDBPasswordNeed  = "db.password_required" // a new user (or host entry) needs a password
	CodeDBRestartFailed = "db.restart_failed"
	// CodeDBConfigUnsafe: a configuration file of the server is a link,
	// a pipe or another special file, has a second name, or a directory
	// on its path is a link (params: path, reason); nothing was changed.
	CodeDBConfigUnsafe = "db.config_unsafe"
	// CodeDBUserPrivileged: the user can do more than use single databases
	// (a superuser, a global privilege, a role; params: name, privilege),
	// so the panel neither changes nor drops it.
	CodeDBUserPrivileged = "db.user_privileged"
	// CodeDBUnsafeEntry: the server holds a name the helper would have to
	// put into a statement of its own but does not take (quotes,
	// backslashes, spaces, control characters; params: value); nothing was
	// changed.
	CodeDBUnsafeEntry = "db.unsafe_entry"
	// CodeDBRemoteNotApplied: after the restart the server does not listen
	// where it was told to (another setting wins; params: engine, listen);
	// the previous configuration is back.
	CodeDBRemoteNotApplied = "db.remote_not_applied"
	// Restores (params: database, copy when one was taken, detail): the
	// copy of the database taken first could not be made, so nothing was
	// restored (CodeDBCopyFailed); the dump failed and the database was put
	// back from the copy (CodeDBRestoreRolledBack); the dump failed partway
	// and the database could not be put back, or there was no copy
	// (CodeDBRestorePartial); the dump failed inside its single
	// transaction, so nothing changed (CodeDBRestoreUnchanged).
	CodeDBCopyFailed        = "db.copy_failed"
	CodeDBRestoreRolledBack = "db.restore_rolled_back"
	CodeDBRestorePartial    = "db.restore_partial"
	CodeDBRestoreUnchanged  = "db.restore_unchanged"
	CodeRedisNeedsPass      = "redis.password_required" // remote access needs a password
	CodeRedisCommand        = "redis.command_failed"
	CodeRedisNoKey          = "redis.key_not_found"
)

// Database engine kinds.
const (
	DBMySQL      = "mysql"
	DBPostgreSQL = "postgresql"
	DBRedis      = "redis"
)

var (
	dbEnginePattern = regexp.MustCompile(`^(mysql|redis|postgresql-[0-9]{1,3})$`)
	// Names are plain identifiers: quoting them can never go wrong.
	mysqlName = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)
	mysqlUser = regexp.MustCompile(`^[A-Za-z0-9_]{1,32}$`)
	pgName    = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)
	// Passwords avoid quotes, backslashes, dollars and spaces, so they are
	// safe in SQL literals and connection strings alike.
	dbPassword = regexp.MustCompile(`^[A-Za-z0-9!#%*+,.:=?@^_~-]{8,128}$`)
	dumpName   = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,160}\.(sql|sql\.gz|dump|rdb)$`)
)

// Names no user may create or drop.
var (
	reservedMySQLDBs   = []string{"mysql", "information_schema", "performance_schema", "sys"}
	reservedMySQLUsers = []string{"root", "mysql", "mariadb.sys", "debian-sys-maint", "mysql.sys", "mysql.session", "mysql.infoschema"}
	reservedPGNames    = []string{"postgres", "template0", "template1", "public"}
)

// EngineKind returns the kind of an engine ID.
func EngineKind(engine string) string {
	if strings.HasPrefix(engine, "postgresql-") {
		return DBPostgreSQL
	}
	return engine
}

func validEngine(engine string) error {
	if !dbEnginePattern.MatchString(engine) {
		return invalid("engine", "oneof")
	}
	return nil
}

// ValidDBName reports whether name can name a database of the engine
// (reserved names excluded).
func ValidDBName(engine, name string) bool {
	switch EngineKind(engine) {
	case DBMySQL:
		return mysqlName.MatchString(name) && !containsString(reservedMySQLDBs, strings.ToLower(name))
	case DBPostgreSQL:
		return pgName.MatchString(name) && !containsString(reservedPGNames, name) && !strings.HasPrefix(name, "pg_")
	}
	return false
}

// ValidDBUser reports whether name can name a database user of the engine.
func ValidDBUser(engine, name string) bool {
	switch EngineKind(engine) {
	case DBMySQL:
		return mysqlUser.MatchString(name) && !containsString(reservedMySQLUsers, strings.ToLower(name))
	case DBPostgreSQL:
		return pgName.MatchString(name) && !containsString(reservedPGNames, name) && !strings.HasPrefix(name, "pg_")
	}
	return false
}

// ValidDBPassword reports whether s can be a database password.
func ValidDBPassword(s string) bool { return dbPassword.MatchString(s) }

// ValidDBHost reports whether s is where a database user may connect
// from: "localhost", "%" (anywhere), an IPv4 address or an IPv4 CIDR.
func ValidDBHost(s string) bool {
	switch {
	case s == "localhost" || s == "%":
		return true
	case strings.Contains(s, "/"):
		ip, n, err := net.ParseCIDR(s)
		return err == nil && ip.To4() != nil && n != nil
	}
	ip := net.ParseIP(s)
	return ip != nil && ip.To4() != nil
}

// DBCreateRequest creates a database. Owner, when set, is an existing user
// that gets every privilege on it (on PostgreSQL it owns the database).
type DBCreateRequest struct {
	Engine string `json:"engine"`
	Name   string `json:"name"`
	Owner  string `json:"owner,omitempty"`
}

// Validate implements the helper's request validation hook.
func (r DBCreateRequest) Validate() error {
	if err := validEngine(r.Engine); err != nil || EngineKind(r.Engine) == DBRedis {
		return invalid("engine", "oneof")
	}
	if !ValidDBName(r.Engine, r.Name) {
		return invalid("name", "db_name")
	}
	if r.Owner != "" && !ValidDBUser(r.Engine, r.Owner) {
		return invalid("owner", "db_user")
	}
	return nil
}

// DBRef names a database or a user of an engine.
type DBRef struct {
	Engine string `json:"engine"`
	Name   string `json:"name"`
}

// Validate implements the helper's request validation hook; the name may
// be a database or a user name (both patterns are the same shape).
func (r DBRef) Validate() error {
	if err := validEngine(r.Engine); err != nil || EngineKind(r.Engine) == DBRedis {
		return invalid("engine", "oneof")
	}
	if !ValidDBName(r.Engine, r.Name) && !ValidDBUser(r.Engine, r.Name) {
		return invalid("name", "db_name")
	}
	return nil
}

// Grant levels.
const (
	GrantAll      = "all"
	GrantReadOnly = "readonly"
)

// DBGrant is a user's access to one database.
type DBGrant struct {
	Database string `json:"database"`
	Level    string `json:"level"`
}

// DBUserRequest makes a user match: it exists with Password (empty keeps
// the current one), may connect from Hosts and has exactly Grants. For an
// existing user, nil Hosts keep where it may connect from and nil Grants
// keep its privileges untouched (a user the panel did not create has
// privileges the panel does not know); an empty Grants list revokes them.
type DBUserRequest struct {
	Engine string `json:"engine"`
	Name   string `json:"name"`
	// New: the user must not exist yet (the panel never takes over a user
	// it did not make by creating one of the same name).
	New      bool      `json:"new,omitempty"`
	Password string    `json:"password,omitempty"`
	Hosts    []string  `json:"hosts"`
	Grants   []DBGrant `json:"grants"`
}

// Validate implements the helper's request validation hook.
func (r DBUserRequest) Validate() error {
	if err := validEngine(r.Engine); err != nil || EngineKind(r.Engine) == DBRedis {
		return invalid("engine", "oneof")
	}
	if !ValidDBUser(r.Engine, r.Name) {
		return invalid("name", "db_user")
	}
	if r.Password != "" && !ValidDBPassword(r.Password) {
		return invalid("password", "db_password")
	}
	// A new user needs hosts; for an existing one nil keeps them.
	if (r.New || r.Hosts != nil) && (len(r.Hosts) == 0 || len(r.Hosts) > 32) {
		return invalid("hosts", "count")
	}
	for _, h := range r.Hosts {
		if !ValidDBHost(h) {
			return invalid("hosts", "db_host")
		}
	}
	if len(r.Grants) > 64 {
		return invalid("grants", "count")
	}
	for _, g := range r.Grants {
		if !ValidDBName(r.Engine, g.Database) || (g.Level != GrantAll && g.Level != GrantReadOnly) {
			return invalid("grants", "grant")
		}
	}
	return nil
}

// DBRemoteRequest turns remote access on (listen on every address) or off.
type DBRemoteRequest struct {
	Engine  string `json:"engine"`
	Enabled bool   `json:"enabled"`
}

// Validate implements the helper's request validation hook.
func (r DBRemoteRequest) Validate() error { return validEngine(r.Engine) }

// DBEngineRef names an engine.
type DBEngineRef struct {
	Engine string `json:"engine"`
}

// Validate implements the helper's request validation hook.
func (r DBEngineRef) Validate() error { return validEngine(r.Engine) }

// DBDumpRequest dumps one database (Name empty for Redis: a snapshot).
type DBDumpRequest struct {
	Engine string `json:"engine"`
	Name   string `json:"name,omitempty"`
}

// Validate implements the helper's request validation hook.
func (r DBDumpRequest) Validate() error {
	if err := validEngine(r.Engine); err != nil {
		return err
	}
	if EngineKind(r.Engine) == DBRedis {
		if r.Name != "" {
			return invalid("name", "unexpected")
		}
		return nil
	}
	if !ValidDBName(r.Engine, r.Name) {
		return invalid("name", "db_name")
	}
	return nil
}

// DBRestoreRequest loads a dump into a database. The helper connects as
// User (a user with every privilege on the database, normally its owner)
// rather than as the administrator, so a dump can never change more than
// that user could.
type DBRestoreRequest struct {
	Engine   string `json:"engine"`
	Name     string `json:"name"`
	File     string `json:"file"`
	User     string `json:"user"`
	Password string `json:"password"`
	// SkipCopy restores without first dumping the database as it is (the
	// copy a failed restore is undone from); for a database that cannot be
	// dumped any more, or does not fit twice.
	SkipCopy bool `json:"skipCopy,omitempty"`
}

// Validate implements the helper's request validation hook.
func (r DBRestoreRequest) Validate() error {
	if err := validEngine(r.Engine); err != nil || EngineKind(r.Engine) == DBRedis {
		return invalid("engine", "oneof")
	}
	if !ValidDBName(r.Engine, r.Name) {
		return invalid("name", "db_name")
	}
	if !ValidDumpName(r.File) {
		return invalid("file", "dump_name")
	}
	if !ValidDBUser(r.Engine, r.User) {
		return invalid("user", "db_user")
	}
	if !ValidDBPassword(r.Password) {
		return invalid("password", "db_password")
	}
	return nil
}

// Service actions.
const (
	ServiceStart   = "start"
	ServiceStop    = "stop"
	ServiceRestart = "restart"
)

// DBServiceRequest starts, stops or restarts an engine.
type DBServiceRequest struct {
	Engine string `json:"engine"`
	Action string `json:"action"`
}

// Validate implements the helper's request validation hook.
func (r DBServiceRequest) Validate() error {
	if err := validEngine(r.Engine); err != nil {
		return err
	}
	if r.Action != ServiceStart && r.Action != ServiceStop && r.Action != ServiceRestart {
		return invalid("action", "oneof")
	}
	return nil
}

// ValidDumpName reports whether s names a dump file.
// Hidden names are the helper's partial files.
func ValidDumpName(s string) bool {
	return dumpName.MatchString(s) && !strings.Contains(s, "..") && !strings.HasPrefix(s, ".")
}

// DumpInfo describes a dump file.
type DumpInfo struct {
	Engine    string `json:"engine"`
	Database  string `json:"database,omitempty"`
	File      string `json:"file"`
	SizeBytes int64  `json:"sizeBytes"`
	CreatedAt int64  `json:"createdAt"`
}

// DBInfo is a database as the engine reports it.
type DBInfo struct {
	Name      string `json:"name"`
	SizeBytes int64  `json:"sizeBytes"`
	Owner     string `json:"owner,omitempty"`
}

// DBUserInfo is a user as the engine reports it.
type DBUserInfo struct {
	Name  string   `json:"name"`
	Hosts []string `json:"hosts,omitempty"`
}

// RedisInfo is the state of the Redis/Valkey instance.
type RedisInfo struct {
	PasswordSet     bool   `json:"passwordSet"`
	MaxMemoryMB     int    `json:"maxMemoryMb"`
	MaxMemoryPolicy string `json:"maxMemoryPolicy"`
	Persistence     string `json:"persistence"` // rdb | aof | both | none
	Keys            int64  `json:"keys"`
	UsedMemory      int64  `json:"usedMemory"`
	// Keyspace counts the keys of each database that has any: what those
	// who may not browse the keys see of them.
	Keyspace []RedisKeyspace `json:"keyspace"`
}

// RedisKeyspace is the number of keys of one database, and how many of
// them expire.
type RedisKeyspace struct {
	DB      int   `json:"db"`
	Keys    int64 `json:"keys"`
	Expires int64 `json:"expires"`
}

// DBEngineInfo is one installed database engine.
type DBEngineInfo struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Software string `json:"software"` // mariadb | mysql | postgresql | redis | valkey
	Service  string `json:"service,omitempty"`
	Line     string `json:"line"`
	Version  string `json:"version,omitempty"`
	Running  bool   `json:"running"`
	Port     int    `json:"port,omitempty"`
	Remote   bool   `json:"remote"`
	// NoCluster is set for a PostgreSQL line without its main cluster.
	NoCluster bool         `json:"noCluster,omitempty"`
	Databases []DBInfo     `json:"databases"`
	Users     []DBUserInfo `json:"users"`
	Redis     *RedisInfo   `json:"redis,omitempty"`
	Error     *Error       `json:"error,omitempty"`
}

// DBStatus is the result of db.status.
type DBStatus struct {
	Supported bool           `json:"supported"`
	Reason    string         `json:"reason,omitempty"`
	Engines   []DBEngineInfo `json:"engines"`
	// Unmanaged lists installed engines the panel does not manage on this
	// system yet (catalog IDs: "postgresql", "mysql" on EL).
	Unmanaged []string `json:"unmanaged"`
}

// Redis persistence modes.
var RedisPersistence = []string{"rdb", "aof", "both", "none"}

// Redis eviction policies.
var RedisPolicies = []string{"noeviction", "allkeys-lru", "allkeys-lfu", "volatile-lru", "volatile-lfu", "allkeys-random", "volatile-random", "volatile-ttl"}

// RedisConfigRequest changes the Redis/Valkey instance. A nil field stays.
type RedisConfigRequest struct {
	// Password "" removes it.
	Password        *string `json:"password,omitempty"`
	MaxMemoryMB     *int    `json:"maxMemoryMb,omitempty"`
	MaxMemoryPolicy *string `json:"maxMemoryPolicy,omitempty"`
	Persistence     *string `json:"persistence,omitempty"`
}

// Validate implements the helper's request validation hook.
func (r RedisConfigRequest) Validate() error {
	if r.Password != nil && *r.Password != "" && !ValidDBPassword(*r.Password) {
		return invalid("password", "db_password")
	}
	if r.MaxMemoryMB != nil && (*r.MaxMemoryMB < 0 || *r.MaxMemoryMB > 1<<20) {
		return invalid("maxMemoryMb", "range")
	}
	if r.MaxMemoryPolicy != nil && !containsString(RedisPolicies, *r.MaxMemoryPolicy) {
		return invalid("maxMemoryPolicy", "oneof")
	}
	if r.Persistence != nil && !containsString(RedisPersistence, *r.Persistence) {
		return invalid("persistence", "oneof")
	}
	return nil
}

// Redis key browser limits.
const (
	RedisScanMax   = 500
	RedisValueMax  = 64 << 10
	RedisItemsMax  = 200
	RedisDeleteMax = 500
)

// RedisScanRequest lists keys matching Pattern (glob), from Cursor.
type RedisScanRequest struct {
	DB      int    `json:"db"`
	Pattern string `json:"pattern"`
	Cursor  string `json:"cursor"`
	Count   int    `json:"count"`
}

var redisCursor = regexp.MustCompile(`^[0-9]{1,20}$`)

func validKey(k string) bool { return len(k) > 0 && len(k) <= 1024 }

// Validate implements the helper's request validation hook.
func (r RedisScanRequest) Validate() error {
	if r.DB < 0 || r.DB > 15 {
		return invalid("db", "range")
	}
	if len(r.Pattern) > 256 {
		return invalid("pattern", "max")
	}
	if r.Cursor != "" && !redisCursor.MatchString(r.Cursor) {
		return invalid("cursor", "cursor")
	}
	if r.Count < 0 || r.Count > RedisScanMax {
		return invalid("count", "range")
	}
	return nil
}

// RedisKey is a key with its type and time to live (-1: none).
type RedisKey struct {
	Key  string `json:"key"`
	Type string `json:"type"`
	TTL  int64  `json:"ttl"`
}

// RedisScanResponse is one page of keys; Cursor "0" means the end.
type RedisScanResponse struct {
	Keys   []RedisKey `json:"keys"`
	Cursor string     `json:"cursor"`
}

// RedisKeyRequest names a key.
type RedisKeyRequest struct {
	DB  int    `json:"db"`
	Key string `json:"key"`
}

// Validate implements the helper's request validation hook.
func (r RedisKeyRequest) Validate() error {
	if r.DB < 0 || r.DB > 15 {
		return invalid("db", "range")
	}
	if !validKey(r.Key) {
		return invalid("key", "key")
	}
	return nil
}

// RedisValue is a preview of a key's value: Value for strings, Items for
// lists, sets, sorted sets (member then score), hashes (field then value)
// and streams; Truncated when not all of it is shown.
type RedisValue struct {
	Key       string   `json:"key"`
	Type      string   `json:"type"`
	TTL       int64    `json:"ttl"`
	Length    int64    `json:"length"`
	Value     string   `json:"value,omitempty"`
	Items     []string `json:"items,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
}

// RedisDeleteRequest deletes keys.
type RedisDeleteRequest struct {
	DB   int      `json:"db"`
	Keys []string `json:"keys"`
}

// Validate implements the helper's request validation hook.
func (r RedisDeleteRequest) Validate() error {
	if r.DB < 0 || r.DB > 15 {
		return invalid("db", "range")
	}
	if len(r.Keys) == 0 || len(r.Keys) > RedisDeleteMax {
		return invalid("keys", "count")
	}
	for _, k := range r.Keys {
		if !validKey(k) {
			return invalid("keys", "key")
		}
	}
	return nil
}

// RedisTTLRequest sets a key's time to live (0: persist).
type RedisTTLRequest struct {
	DB      int    `json:"db"`
	Key     string `json:"key"`
	Seconds int64  `json:"seconds"`
}

// Validate implements the helper's request validation hook.
func (r RedisTTLRequest) Validate() error {
	if err := (RedisKeyRequest{DB: r.DB, Key: r.Key}).Validate(); err != nil {
		return err
	}
	if r.Seconds < 0 || r.Seconds > 10*365*24*3600 {
		return invalid("seconds", "range")
	}
	return nil
}

// RedisDeleteResponse is how many keys went.
type RedisDeleteResponse struct {
	Deleted int64 `json:"deleted"`
}
