// SPDX-License-Identifier: AGPL-3.0-only

package modules_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/jobs"
)

// opsSucceed makes every listed helper operation start and succeed.
func (e *env) opsSucceed(actions ...string) {
	for _, a := range actions {
		e.helper.respond(a, helperapi.OpRef{ID: opID})
	}
	e.helper.respond(helperapi.ActionOpGet, helperapi.OpGetResponse{Op: helperapi.Op{ID: opID, State: helperapi.OpSucceeded}})
}

func (c *client) runJob(r reply) map[string]any {
	c.e.t.Helper()
	final, log := waitJob(c.e.t, c, r.obj("job")["id"].(string))
	if final["state"] != jobs.Succeeded {
		c.e.t.Fatalf("job %v\n%s", final, log)
	}
	return final
}

func sentAs[T any](t *testing.T, e *env, action string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(e.helper.sent(action)), &v); err != nil {
		t.Fatalf("%s: %v", action, err)
	}
	return v
}

func mysqlStatus(dbs []helperapi.DBInfo, users []helperapi.DBUserInfo) helperapi.DBStatus {
	return helperapi.DBStatus{Supported: true, Engines: []helperapi.DBEngineInfo{{ID: "mysql", Kind: "mysql", Software: "mariadb", Line: "11.8",
		Running: true, Databases: dbs, Users: users}}}
}

func TestDatabasesUsersAndPasswords(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	e.opsSucceed(helperapi.ActionDBUserSet, helperapi.ActionDBCreate, helperapi.ActionDBDrop, helperapi.ActionDBUserDrop, helperapi.ActionDBRestore)
	e.helper.respond(helperapi.ActionDBStatus, mysqlStatus(nil, nil))

	admin.expect(admin.do("POST", "/api/v1/databases/mysql/dbs", map[string]any{"name": "mysql"}), 400, "validation.invalid")
	admin.expect(admin.do("POST", "/api/v1/databases/oracle/dbs", map[string]any{"name": "shop"}), 400, "validation.invalid")
	admin.expect(admin.do("POST", "/api/v1/databases/redis/dbs", map[string]any{"name": "shop"}), 400, "validation.invalid")

	// A database comes with a user of its name and a random password.
	admin.runJob(admin.expect(admin.do("POST", "/api/v1/databases/mysql/dbs", map[string]any{"name": "shop"}), 202, ""))
	user := sentAs[helperapi.DBUserRequest](t, e, helperapi.ActionDBUserSet)
	if user.Name != "shop" || !user.New || len(user.Password) != 20 || strings.Join(user.Hosts, ",") != "localhost,127.0.0.1" {
		t.Fatalf("user: %+v", user)
	}
	if c := sentAs[helperapi.DBCreateRequest](t, e, helperapi.ActionDBCreate); c.Name != "shop" || c.Owner != "shop" {
		t.Fatalf("create: %+v", c)
	}
	var sealed, grants string
	_ = e.db.QueryRow(`SELECT password, grants FROM db_users WHERE engine = 'mysql' AND name = 'shop'`).Scan(&sealed, &grants)
	if sealed == "" || strings.Contains(sealed, user.Password) || grants != `[{"database":"shop","level":"all"}]` {
		t.Fatalf("stored user: %q %s", sealed, grants)
	}

	e.helper.respond(helperapi.ActionDBStatus, mysqlStatus([]helperapi.DBInfo{{Name: "shop", SizeBytes: 4096}, {Name: "legacy"}},
		[]helperapi.DBUserInfo{{Name: "shop", Hosts: []string{"localhost", "127.0.0.1"}}, {Name: "old", Hosts: []string{"%"}}}))
	engines := admin.expect(admin.do("GET", "/api/v1/databases", nil), 200, "").body["engines"].([]any)
	m := engines[0].(map[string]any)
	dbs, users := m["databases"].([]any), m["users"].([]any)
	if len(dbs) != 2 || dbs[0].(map[string]any)["managed"] != false || dbs[1].(map[string]any)["owner"] != "shop" || dbs[1].(map[string]any)["managed"] != true {
		t.Fatalf("databases: %v", dbs)
	}
	if len(users) != 2 || users[0].(map[string]any)["managed"] != false || users[1].(map[string]any)["hasPassword"] != true {
		t.Fatalf("users: %v", users)
	}

	// Showing a password needs a second verification and is audited.
	admin.expect(admin.do("GET", "/api/v1/databases/mysql/users/shop/password", nil), 403, "auth.step_up_required")
	admin.elevate("")
	if got := admin.expect(admin.do("GET", "/api/v1/databases/mysql/users/shop/password", nil), 200, "").body["password"]; got != user.Password {
		t.Fatalf("revealed %v, want %s", got, user.Password)
	}
	var n int
	_ = e.db.QueryRow(`SELECT count(*) FROM audit_log WHERE action = 'db.password_reveal'`).Scan(&n)
	if n != 1 {
		t.Fatalf("reveal audits: %d", n)
	}
	admin.expect(admin.do("GET", "/api/v1/databases/mysql/users/old/password", nil), 404, "db.password_unknown")

	// Changing hosts sends the stored password along (new host entries
	// need it); the stored one stays.
	admin.runJob(admin.expect(admin.do("PUT", "/api/v1/databases/mysql/users/shop", map[string]any{"hosts": []string{"localhost", "10.0.0.0/8"},
		"grants": []any{map[string]any{"database": "shop", "level": "all"}}}), 202, ""))
	if u := sentAs[helperapi.DBUserRequest](t, e, helperapi.ActionDBUserSet); u.Password != user.Password || u.Hosts[1] != "10.0.0.0/8" {
		t.Fatalf("update: %+v", u)
	}
	admin.expect(admin.do("PUT", "/api/v1/databases/mysql/users/shop", map[string]any{"hosts": []string{"example.com"}, "grants": []any{}}), 400, "validation.invalid")
	// Without hosts and grants they stay: the helper is told to leave them,
	// the record keeps them.
	admin.runJob(admin.expect(admin.do("PUT", "/api/v1/databases/mysql/users/shop", map[string]any{"password": "An0ther-pass"}), 202, ""))
	if u := sentAs[helperapi.DBUserRequest](t, e, helperapi.ActionDBUserSet); u.Password != "An0ther-pass" || u.Hosts != nil || u.Grants != nil {
		t.Fatalf("password change: %+v", u)
	}
	var hosts string
	_ = e.db.QueryRow(`SELECT hosts, grants FROM db_users WHERE engine = 'mysql' AND name = 'shop'`).Scan(&hosts, &grants)
	if hosts != `["localhost","10.0.0.0/8"]` || grants != `[{"database":"shop","level":"all"}]` {
		t.Fatalf("after a password change: %s %s", hosts, grants)
	}
	user.Password = "An0ther-pass"
	// Hosts alone: the privileges the panel knows go along, so new host
	// entries are not left without them.
	admin.runJob(admin.expect(admin.do("PUT", "/api/v1/databases/mysql/users/shop", map[string]any{"hosts": []string{"localhost", "10.1.0.0/16"}}), 202, ""))
	if u := sentAs[helperapi.DBUserRequest](t, e, helperapi.ActionDBUserSet); len(u.Grants) != 1 || u.Grants[0].Database != "shop" || u.Hosts[1] != "10.1.0.0/16" {
		t.Fatalf("hosts change: %+v", u)
	}
	_ = e.db.QueryRow(`SELECT hosts, grants FROM db_users WHERE engine = 'mysql' AND name = 'shop'`).Scan(&hosts, &grants)
	if hosts != `["localhost","10.1.0.0/16"]` || grants != `[{"database":"shop","level":"all"}]` {
		t.Fatalf("after a hosts change: %s %s", hosts, grants)
	}
	// A user the panel did not create: a new password never revokes its
	// privileges, and the panel still does not manage it.
	admin.runJob(admin.expect(admin.do("PUT", "/api/v1/databases/mysql/users/old", map[string]any{"password": "S3t-by-panel"}), 202, ""))
	if u := sentAs[helperapi.DBUserRequest](t, e, helperapi.ActionDBUserSet); u.Name != "old" || u.Password != "S3t-by-panel" || u.Hosts != nil || u.Grants != nil {
		t.Fatalf("unmanaged password change: %+v", u)
	}
	_ = e.db.QueryRow(`SELECT count(*) FROM db_users WHERE engine = 'mysql' AND name = 'old'`).Scan(&n)
	if n != 0 {
		t.Fatal("an unmanaged user became managed")
	}

	// A read-only user cannot restore; the owner can, with its password.
	admin.runJob(admin.expect(admin.do("POST", "/api/v1/databases/mysql/users", map[string]any{"name": "report",
		"grants": []any{map[string]any{"database": "shop", "level": "readonly"}}}), 202, ""))
	admin.expect(admin.do("POST", "/api/v1/databases/mysql/users", map[string]any{"name": "report"}), 409, "db.user_exists")
	dir := filepath.Join(e.dataDir, "backups/db/mysql")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "shop_1.sql"), []byte("SELECT 1;"), 0o600); err != nil {
		t.Fatal(err)
	}
	admin.expect(admin.do("POST", "/api/v1/databases/mysql/dbs/shop/restore", map[string]any{"file": "shop_1.sql", "user": "report"}), 400, "db.restore_needs_user")
	admin.expect(admin.do("POST", "/api/v1/databases/mysql/dbs/shop/restore", map[string]any{"file": "missing.sql"}), 404, "db.dump_not_found")
	admin.runJob(admin.expect(admin.do("POST", "/api/v1/databases/mysql/dbs/shop/restore", map[string]any{"file": "shop_1.sql"}), 202, ""))
	if q := sentAs[helperapi.DBRestoreRequest](t, e, helperapi.ActionDBRestore); q.User != "shop" || q.Password != user.Password || q.File != "shop_1.sql" {
		t.Fatalf("restore: %+v", q)
	}
	var params string
	_ = e.db.QueryRow(`SELECT params FROM audit_log WHERE action = 'db.restore'`).Scan(&params)
	if strings.Contains(params, user.Password) {
		t.Fatalf("audit shows the password: %s", params)
	}

	// Dropping the database with its owner forgets both, and the other
	// user's access to it.
	admin.runJob(admin.expect(admin.do("DELETE", "/api/v1/databases/mysql/dbs/shop?dropOwner=true", nil), 202, ""))
	if d := sentAs[helperapi.DBRef](t, e, helperapi.ActionDBUserDrop); d.Name != "shop" {
		t.Fatalf("user drop: %+v", d)
	}
	_ = e.db.QueryRow(`SELECT count(*) FROM db_users WHERE name = 'shop'`).Scan(&n)
	_ = e.db.QueryRow(`SELECT grants FROM db_users WHERE name = 'report'`).Scan(&grants)
	if n != 0 || grants != "[]" {
		t.Fatalf("after drop: %d users, report grants %s", n, grants)
	}
}

// rawReply is a response that need not be JSON.
type rawReply struct {
	StatusCode int
	Header     http.Header
}

func (c *client) raw(method, path string, body []byte) (rawReply, []byte) {
	c.e.t.Helper()
	req, _ := http.NewRequest(method, c.e.srv.URL+path, bytes.NewReader(body))
	req.Header.Set(api.CSRFHeader, "1")
	res, err := c.hc.Do(req)
	if err != nil {
		c.e.t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	return rawReply{StatusCode: res.StatusCode, Header: res.Header}, data
}

func TestDatabaseDumpsAndRedis(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	e.opsSucceed(helperapi.ActionDBDump, helperapi.ActionRedisConfig)

	if res, _ := admin.raw("PUT", "/api/v1/databases/mysql/dumps/shop_up.sql", []byte("x")); res.StatusCode != 403 {
		t.Fatalf("upload without step-up: %d", res.StatusCode)
	}
	admin.elevate("")
	if res, body := admin.raw("PUT", "/api/v1/databases/mysql/dumps/shop_up.sql", []byte("CREATE TABLE t (id int);")); res.StatusCode != 201 {
		t.Fatalf("upload: %d %s", res.StatusCode, body)
	}
	if res, _ := admin.raw("PUT", "/api/v1/databases/mysql/dumps/shop_up.sql", []byte("again")); res.StatusCode != 409 {
		t.Fatalf("second upload: %d", res.StatusCode)
	}
	for _, bad := range []string{"x.rdb", "x.dump", "..%2Fescape.sql", ".hidden.sql", "x.exe"} {
		if res, _ := admin.raw("PUT", "/api/v1/databases/mysql/dumps/"+bad, []byte("x")); res.StatusCode != 400 {
			t.Errorf("upload %s: %d", bad, res.StatusCode)
		}
	}
	dumps := admin.expect(admin.do("GET", "/api/v1/databases/mysql/dumps", nil), 200, "").body["dumps"].([]any)
	if len(dumps) != 1 || dumps[0].(map[string]any)["database"] != "shop" || dumps[0].(map[string]any)["sizeBytes"] != float64(24) {
		t.Fatalf("dumps: %v", dumps)
	}
	res, body := admin.raw("GET", "/api/v1/databases/mysql/dumps/shop_up.sql", nil)
	if res.StatusCode != 200 || string(body) != "CREATE TABLE t (id int);" || res.Header.Get("Content-Disposition") != `attachment; filename="shop_up.sql"` {
		t.Fatalf("download: %d %q %v", res.StatusCode, body, res.Header)
	}
	if got := admin.expect(admin.do("GET", "/api/v1/databases/mysql/dumps/shop_up.sql?link=1", nil), 200, "").body["url"]; got != "/api/v1/databases/mysql/dumps/shop_up.sql" {
		t.Fatalf("dump link %v", got)
	}
	admin.expect(admin.do("GET", "/api/v1/databases/mysql/dumps/gone.sql?link=1", nil), 404, helperapi.CodeDBDumpNotFound)
	// A link in the dump directory is not followed out of it.
	if err := os.Symlink("/etc/passwd", filepath.Join(e.dataDir, "backups/db/mysql/link.sql")); err != nil {
		t.Fatal(err)
	}
	if res, _ := admin.raw("GET", "/api/v1/databases/mysql/dumps/link.sql", nil); res.StatusCode != 404 {
		t.Fatalf("download through a link: %d", res.StatusCode)
	}
	admin.runJob(admin.expect(admin.do("POST", "/api/v1/databases/mysql/dumps", map[string]any{"name": "shop"}), 202, ""))
	if q := sentAs[helperapi.DBDumpRequest](t, e, helperapi.ActionDBDump); q.Engine != "mysql" || q.Name != "shop" {
		t.Fatalf("dump: %+v", q)
	}
	admin.expect(admin.do("DELETE", "/api/v1/databases/mysql/dumps/shop_up.sql", nil), 204, "")
	admin.expect(admin.do("DELETE", "/api/v1/databases/mysql/dumps/shop_up.sql", nil), 404, "db.dump_not_found")

	// Redis: a generated password is kept sealed and can be shown again.
	admin.runJob(admin.expect(admin.do("PUT", "/api/v1/databases/redis/config", map[string]any{"generatePassword": true, "maxMemoryMb": 128}), 202, ""))
	q := sentAs[helperapi.RedisConfigRequest](t, e, helperapi.ActionRedisConfig)
	if q.Password == nil || len(*q.Password) != 20 || q.MaxMemoryMB == nil || *q.MaxMemoryMB != 128 {
		t.Fatalf("redis config: %+v", q)
	}
	if got := admin.expect(admin.do("GET", "/api/v1/databases/redis/password", nil), 200, "").body["password"]; got != *q.Password {
		t.Fatalf("redis password %v", got)
	}
	var params string
	_ = e.db.QueryRow(`SELECT params FROM audit_log WHERE action = 'redis.config'`).Scan(&params)
	if strings.Contains(params, *q.Password) {
		t.Fatalf("audit shows the password: %s", params)
	}
	admin.expect(admin.do("PUT", "/api/v1/databases/redis/config", map[string]any{"persistence": "sometimes"}), 400, "validation.invalid")

	e.helper.respond(helperapi.ActionRedisScan, helperapi.RedisScanResponse{Keys: []helperapi.RedisKey{{Key: "user:1", Type: "string", TTL: -1}}, Cursor: "0"})
	keys := admin.expect(admin.do("GET", "/api/v1/databases/redis/keys?db=2&pattern=user:*", nil), 200, "")
	if s := sentAs[helperapi.RedisScanRequest](t, e, helperapi.ActionRedisScan); s.DB != 2 || s.Pattern != "user:*" || len(keys.body["keys"].([]any)) != 1 {
		t.Fatalf("scan %+v %v", s, keys.body)
	}
	admin.expect(admin.do("GET", "/api/v1/databases/redis/keys?db=99", nil), 400, "validation.invalid")
	// Auditors see key names, never values.
	admin.expect(admin.do("POST", "/api/v1/users", map[string]any{"username": "aud", "password": pw, "role": "auditor"}), 201, "")
	aud := e.client()
	aud.expect(aud.login("aud", pw), 200, "")
	aud.expect(aud.do("GET", "/api/v1/databases/redis/keys?db=2", nil), 200, "")
	aud.expect(aud.do("GET", "/api/v1/databases/redis/value?db=2&key=user:1", nil), 403, "auth.forbidden")
	e.helper.mu.Lock()
	e.helper.errs = map[string]error{helperapi.ActionRedisGet: &helperapi.Error{Code: helperapi.CodeRedisNoKey}}
	e.helper.mu.Unlock()
	admin.expect(admin.do("GET", "/api/v1/databases/redis/value?key=gone", nil), 404, helperapi.CodeRedisNoKey)
	admin.expect(admin.do("POST", "/api/v1/databases/redis/delete", map[string]any{"keys": []string{"user:1"}}), 200, "")
}
