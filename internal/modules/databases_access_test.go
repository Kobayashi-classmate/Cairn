// SPDX-License-Identifier: AGPL-3.0-only

package modules_test

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// dbPeople creates an operator and an auditor and signs them in.
func dbPeople(e *env, admin *client) (op, aud *client) {
	admin.expect(admin.do("POST", "/api/v1/users", map[string]any{"username": "oper", "password": pw, "role": "operator"}), 201, "")
	admin.expect(admin.do("POST", "/api/v1/users", map[string]any{"username": "aud", "password": pw, "role": "auditor"}), 201, "")
	op, aud = e.client(), e.client()
	op.expect(op.login("oper", pw), 200, "")
	aud.expect(aud.login("aud", pw), 200, "")
	return op, aud
}

func dbToken(c *client, name string, perms ...string) string {
	return c.expect(c.do("POST", "/api/v1/me/tokens", map[string]any{"name": name, "permissions": perms, "expiresInDays": 1}), 201, "").body["token"].(string)
}

// A user the panel did not create (a superuser made by hand, a monitoring
// account) is changed and dropped by administrators only; operators see
// it. Users the panel made stay the operators'.
func TestDatabaseUsersThePanelDidNotCreate(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	e.opsSucceed(helperapi.ActionDBUserSet, helperapi.ActionDBUserDrop, helperapi.ActionDBCreate, helperapi.ActionDBDrop)
	e.helper.respond(helperapi.ActionDBStatus, helperapi.DBStatus{Supported: true, Engines: []helperapi.DBEngineInfo{{ID: "postgresql-16", Kind: "postgresql",
		Software: "postgresql", Line: "16", Running: true, Users: []helperapi.DBUserInfo{{Name: "dba", Hosts: []string{"localhost"}}}}}})
	admin.elevate("")
	op, aud := dbPeople(e, admin)
	op.elevate("")
	take := map[string]any{"password": "Taken-0ver-pass", "hosts": []string{"%"}}
	for _, c := range []struct{ method, path string }{
		{"PUT", "/api/v1/databases/postgresql-16/users/dba"},
		{"PUT", "/api/v1/databases/mysql/users/replicator"},
		{"DELETE", "/api/v1/databases/postgresql-16/users/dba"},
	} {
		var body any
		if c.method == "PUT" {
			body = take
		}
		op.expect(op.do(c.method, c.path, body), 403, "auth.forbidden")
	}
	op.expect(op.do("PUT", "/api/v1/databases/mysql/users/replicator", map[string]any{"grants": []any{}}), 403, "auth.forbidden")
	// The operator still sees the user.
	users := op.expect(op.do("GET", "/api/v1/databases", nil), 200, "").body["engines"].([]any)[0].(map[string]any)["users"].([]any)
	if len(users) != 1 || users[0].(map[string]any)["name"] != "dba" || users[0].(map[string]any)["managed"] != false {
		t.Fatalf("users %v", users)
	}
	aud.expect(aud.do("GET", "/api/v1/databases", nil), 200, "")

	// An administrator may (the helper still refuses accounts with rights
	// beyond single databases).
	admin.runJob(admin.expect(admin.do("PUT", "/api/v1/databases/postgresql-16/users/dba", map[string]any{"password": "Admin-set-pass"}), 202, ""))
	if u := sentAs[helperapi.DBUserRequest](t, e, helperapi.ActionDBUserSet); u.Name != "dba" || u.Password != "Admin-set-pass" || u.Hosts != nil || u.Grants != nil {
		t.Fatalf("helper got %+v", u)
	}
	admin.runJob(admin.expect(admin.do("DELETE", "/api/v1/databases/postgresql-16/users/dba", nil), 202, ""))

	// A user the panel made: the operator changes and drops it.
	admin.runJob(admin.expect(admin.do("POST", "/api/v1/databases/postgresql-16/users", map[string]any{"name": "app"}), 202, ""))
	op.runJob(op.expect(op.do("PUT", "/api/v1/databases/postgresql-16/users/app", map[string]any{"password": "N3w-app-pass"}), 202, ""))
	op.runJob(op.expect(op.do("DELETE", "/api/v1/databases/postgresql-16/users/app", nil), 202, ""))

	// Dropping a database with an owner the panel has no record of.
	admin.runJob(admin.expect(admin.do("POST", "/api/v1/databases/postgresql-16/dbs", map[string]any{"name": "shop"}), 202, ""))
	if _, err := e.db.Exec(`DELETE FROM db_users WHERE name = 'shop'`); err != nil {
		t.Fatal(err)
	}
	op.expect(op.do("DELETE", "/api/v1/databases/postgresql-16/dbs/shop?dropOwner=true", nil), 403, "auth.forbidden")
	op.runJob(op.expect(op.do("DELETE", "/api/v1/databases/postgresql-16/dbs/shop", nil), 202, ""))
}

// A new user with access to databases, or reachable from elsewhere, needs
// a second verification (a token cannot give one); a local user without
// access does not.
func TestDatabaseUserCreationStepUp(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	e.opsSucceed(helperapi.ActionDBUserSet)
	e.helper.respond(helperapi.ActionDBStatus, mysqlStatus([]helperapi.DBInfo{{Name: "shop"}}, nil))
	admin.elevate("")
	tok := dbToken(admin, "db", "databases.read", "databases.manage")
	plain := e.client()
	plain.expect(plain.login("admin", pw), 200, "")
	grants := []any{map[string]any{"database": "shop", "level": "all"}}
	for i, body := range []map[string]any{
		{"grants": grants},
		{"hosts": []string{"%"}},
		{"hosts": []string{"localhost", "10.0.0.0/8"}},
		{"hosts": []string{"127.0.0.0/8"}},
	} {
		body["name"] = "u" + string(rune('a'+i))
		if r := e.bearer(tok, "POST", "/api/v1/databases/mysql/users", body); r.status != 403 || r.code() != "auth.step_up_required" {
			t.Fatalf("token %v: %d %s", body, r.status, r.raw)
		}
		plain.expect(plain.do("POST", "/api/v1/databases/mysql/users", body), 403, "auth.step_up_required")
	}
	// Local only and without access: no second verification.
	for _, body := range []map[string]any{{"name": "local1"}, {"name": "local2", "hosts": []string{"localhost", "127.0.0.1"}, "grants": []any{}}} {
		r := e.bearer(tok, "POST", "/api/v1/databases/mysql/users", body)
		if r.status != 202 {
			t.Fatalf("token %v: %d %s", body, r.status, r.raw)
		}
		admin.runJob(r)
	}
	admin.runJob(admin.expect(admin.do("POST", "/api/v1/databases/mysql/users", map[string]any{"name": "full", "hosts": []string{"%"}, "grants": grants}), 202, ""))
	if u := sentAs[helperapi.DBUserRequest](t, e, helperapi.ActionDBUserSet); u.Name != "full" || len(u.Grants) != 1 || u.Hosts[0] != "%" {
		t.Fatalf("helper got %+v", u)
	}
}

// Key names hold session IDs and values hold sessions: listing keys and
// reading values need databases.manage and a second verification, and a
// value read is audited without the value. Readers see counts per database.
func TestRedisKeysAndValuesNeedStepUp(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	e.helper.respond(helperapi.ActionDBStatus, helperapi.DBStatus{Supported: true, Engines: []helperapi.DBEngineInfo{{ID: "redis", Kind: "redis",
		Software: "redis", Line: "8", Running: true, Redis: &helperapi.RedisInfo{Keys: 3, Keyspace: []helperapi.RedisKeyspace{{DB: 0, Keys: 3, Expires: 1}}}}}})
	e.helper.respond(helperapi.ActionRedisScan, helperapi.RedisScanResponse{Keys: []helperapi.RedisKey{{Key: "PHPREDIS_SESSION:abc", Type: "string", TTL: 60}}, Cursor: "0"})
	e.helper.respond(helperapi.ActionRedisGet, helperapi.RedisValue{Key: "PHPREDIS_SESSION:abc", Type: "string", Value: `user_id|i:1;csrf|s:8:"deadbeef"`})
	admin.elevate("")
	op, aud := dbPeople(e, admin)
	reader := dbToken(admin, "reader", "databases.read")
	manager := dbToken(admin, "manager", "databases.read", "databases.manage")
	keys, value := "/api/v1/databases/redis/keys?db=0&pattern=PHPREDIS_SESSION:*", "/api/v1/databases/redis/value?db=0&key=PHPREDIS_SESSION:abc"

	aud.expect(aud.do("GET", keys, nil), 403, "auth.forbidden")
	aud.expect(aud.do("GET", value, nil), 403, "auth.forbidden")
	for _, tc := range []struct{ token, code string }{{reader, "auth.forbidden"}, {manager, "auth.step_up_required"}} {
		for _, p := range []string{keys, value} {
			if r := e.bearer(tc.token, "GET", p, nil); r.status != 403 || r.code() != tc.code {
				t.Fatalf("token: %s: %d %s", p, r.status, r.raw)
			}
		}
	}
	op.expect(op.do("GET", keys, nil), 403, "auth.step_up_required")
	op.expect(op.do("GET", value, nil), 403, "auth.step_up_required")

	// Readers see how many keys each database holds.
	r := aud.expect(aud.do("GET", "/api/v1/databases", nil), 200, "")
	ks := r.body["engines"].([]any)[0].(map[string]any)["redis"].(map[string]any)["keyspace"].([]any)
	if len(ks) != 1 || ks[0].(map[string]any)["keys"] != float64(3) || strings.Contains(r.raw, "PHPREDIS_SESSION") {
		t.Fatalf("keyspace %s", r.raw)
	}

	op.elevate("")
	op.expect(op.do("GET", keys, nil), 200, "")
	var before int
	_ = e.db.QueryRow(`SELECT count(*) FROM audit_log`).Scan(&before)
	if r := op.expect(op.do("GET", value, nil), 200, ""); !strings.Contains(r.raw, "deadbeef") {
		t.Fatalf("value %s", r.raw)
	}
	var after int
	var params string
	_ = e.db.QueryRow(`SELECT count(*) FROM audit_log`).Scan(&after)
	_ = e.db.QueryRow(`SELECT params FROM audit_log WHERE action = 'redis.value_read'`).Scan(&params)
	// The key's name holds the session ID: the audit, which auditors read,
	// keeps its namespace and a fingerprint only.
	sum := sha256.Sum256([]byte("PHPREDIS_SESSION:abc"))
	if after != before+1 || !strings.Contains(params, `"keyPrefix":"PHPREDIS_SESSION"`) || !strings.Contains(params, `"keySha256":"`+hex.EncodeToString(sum[:])+`"`) ||
		!strings.Contains(params, `"db":0`) || strings.Contains(params, "deadbeef") || strings.Contains(params, "SESSION:abc") {
		t.Fatalf("audit %d -> %d: %s", before, after, params)
	}
}
