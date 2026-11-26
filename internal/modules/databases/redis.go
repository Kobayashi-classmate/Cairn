// SPDX-License-Identifier: AGPL-3.0-only

package databases

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/jobs"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/modutil"
)

// Redis or Valkey: the system instance's settings and a key browser.

type redisConfigRequest struct {
	// Password: null keeps it, "" removes it; GeneratePassword sets a new
	// random one.
	Password         *string `json:"password,omitempty"`
	GeneratePassword bool    `json:"generatePassword,omitempty"`
	MaxMemoryMB      *int    `json:"maxMemoryMb,omitempty"`
	MaxMemoryPolicy  *string `json:"maxMemoryPolicy,omitempty"`
	Persistence      *string `json:"persistence,omitempty"`
}

func (m *Module) redisRoutes(r *api.Router, doc func(string) api.RouteOption) {
	r.Handle("PUT /api/v1/databases/redis/config", m.redisConfig, api.Require(Manage), api.StepUp(), api.Body(redisConfigRequest{}),
		doc("Change the Redis password, memory limit or persistence (starts a job)"), api.Returns(202, jobResponse{}))
	r.Handle("GET /api/v1/databases/redis/password", m.redisPassword, api.Require(Manage), api.StepUp(),
		doc("Show the Redis password, if the panel set it"), api.Returns(200, passwordView{}))
	// Values hold sessions and tokens, and so do the names of many keys
	// (PHPREDIS_SESSION:<id>): listing keys and reading values is for those
	// who may change them, after a second verification. Those who may only
	// read see how many keys each database has (the list's keyspace).
	r.Handle("GET /api/v1/databases/redis/keys", m.redisScan, api.Require(Manage), api.StepUp(),
		doc("Keys matching ?pattern= in ?db=, from ?cursor= (at most ?count=)"), api.Returns(200, helperapi.RedisScanResponse{}))
	r.Handle("GET /api/v1/databases/redis/value", m.redisGet, api.Require(Manage), api.StepUp(),
		doc("A preview of ?key= in ?db= (audited without the value)"), api.Returns(200, helperapi.RedisValue{}))
	r.Handle("POST /api/v1/databases/redis/delete", m.redisDelete, api.Require(Manage), api.StepUp(), api.Body(helperapi.RedisDeleteRequest{}),
		doc("Delete keys"), api.Returns(200, helperapi.RedisDeleteResponse{}))
	r.Handle("PUT /api/v1/databases/redis/ttl", m.redisTTL, api.Require(Manage), api.StepUp(), api.Body(helperapi.RedisTTLRequest{}),
		doc("Set a key's time to live in seconds (0: keep forever)"), api.Returns(204, nil))
}

func (m *Module) setKV(ctx context.Context, key, value string) error {
	if value == "" {
		_, err := m.DB.ExecContext(ctx, `DELETE FROM kv WHERE key = ?`, key)
		return err
	}
	_, err := m.DB.ExecContext(ctx, `INSERT INTO kv(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func (m *Module) redisConfig(w http.ResponseWriter, r *http.Request) error {
	var req redisConfigRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	q := helperapi.RedisConfigRequest{Password: req.Password, MaxMemoryMB: req.MaxMemoryMB, MaxMemoryPolicy: req.MaxMemoryPolicy, Persistence: req.Persistence}
	if req.GeneratePassword {
		pw := newPassword()
		q.Password = &pw
	}
	if err := q.Validate(); err != nil {
		return badRequest(err)
	}
	params := map[string]any{"password": q.Password != nil}
	if q.MaxMemoryMB != nil {
		params["maxMemoryMb"] = *q.MaxMemoryMB
	}
	if q.MaxMemoryPolicy != nil {
		params["maxMemoryPolicy"] = *q.MaxMemoryPolicy
	}
	if q.Persistence != nil {
		params["persistence"] = *q.Persistence
	}
	return m.start(w, r, "redis.config", helperapi.DBRedis, params, func(ctx context.Context, j *jobs.Job) error {
		if err := j.RunOp(helperapi.ActionRedisConfig, q, nil); err != nil {
			return err
		}
		if q.Password == nil {
			return nil
		}
		sealed := ""
		if *q.Password != "" {
			var err error
			if sealed, err = m.Box.Seal([]byte(*q.Password), redisContext); err != nil {
				return err
			}
		}
		return m.setKV(context.WithoutCancel(ctx), kvRedisPass, sealed)
	})
}

func (m *Module) redisPassword(w http.ResponseWriter, r *http.Request) error {
	sealed := m.kv(r.Context(), kvRedisPass)
	if sealed == "" {
		return api.NewError(http.StatusNotFound, codePasswordAbsent, map[string]any{"engine": helperapi.DBRedis})
	}
	pw, err := m.Box.Open(sealed, redisContext)
	if err != nil {
		return err
	}
	modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "redis.password_reveal", "ok", nil)
	return api.WriteJSON(w, http.StatusOK, passwordView{Password: string(pw)})
}

// call asks the helper synchronously.
func (m *Module) call(r *http.Request, action string, req, resp any) error {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	err := m.Helper.Call(ctx, action, callerOf(r), req, resp)
	if he, ok := helperapi.AsError(err); ok {
		status := http.StatusConflict
		switch he.Code {
		case helperapi.CodeRedisNoKey, helperapi.CodeDBEngineMissing:
			status = http.StatusNotFound
		case helperapi.CodeDBNotRunning:
			status = http.StatusServiceUnavailable
		}
		return api.NewError(status, he.Code, he.Params)
	}
	return err
}

func queryInt(r *http.Request, name string) (int, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, api.NewError(http.StatusBadRequest, "validation.invalid", map[string]any{"field": name, "rule": "integer"})
	}
	return n, nil
}

func (m *Module) redisScan(w http.ResponseWriter, r *http.Request) error {
	db, err := queryInt(r, "db")
	if err != nil {
		return err
	}
	count, err := queryInt(r, "count")
	if err != nil {
		return err
	}
	req := helperapi.RedisScanRequest{DB: db, Pattern: r.URL.Query().Get("pattern"), Cursor: r.URL.Query().Get("cursor"), Count: count}
	if err := req.Validate(); err != nil {
		return badRequest(err)
	}
	var resp helperapi.RedisScanResponse
	if err := m.call(r, helperapi.ActionRedisScan, req, &resp); err != nil {
		return err
	}
	return api.WriteJSON(w, http.StatusOK, resp)
}

func (m *Module) redisGet(w http.ResponseWriter, r *http.Request) error {
	db, err := queryInt(r, "db")
	if err != nil {
		return err
	}
	req := helperapi.RedisKeyRequest{DB: db, Key: r.URL.Query().Get("key")}
	if err := req.Validate(); err != nil {
		return badRequest(err)
	}
	var resp helperapi.RedisValue
	if err := m.call(r, helperapi.ActionRedisGet, req, &resp); err != nil {
		return err
	}
	prefix, sum := auditKey(req.Key)
	modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "redis.value_read", "ok", map[string]any{"db": req.DB, "keyPrefix": prefix, "keySha256": sum})
	return api.WriteJSON(w, http.StatusOK, resp)
}

// auditKey names a Redis key in the audit without its secret part: keys
// often hold a session ID or a token (PHPREDIS_SESSION:<id>), and auditors
// read the audit. It keeps the namespace before the first colon, cut to 32
// bytes, and the key's SHA-256, which identifies the key to whoever knows
// it.
func auditKey(key string) (prefix, sum string) {
	if i := strings.IndexByte(key, ':'); i > 0 {
		prefix = key[:min(i, 32)]
	}
	h := sha256.Sum256([]byte(key))
	return prefix, hex.EncodeToString(h[:])
}

func (m *Module) redisDelete(w http.ResponseWriter, r *http.Request) error {
	var req helperapi.RedisDeleteRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	if err := req.Validate(); err != nil {
		return badRequest(err)
	}
	var resp helperapi.RedisDeleteResponse
	if err := m.call(r, helperapi.ActionRedisDelete, req, &resp); err != nil {
		return err
	}
	modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "redis.delete", "ok", map[string]any{"db": req.DB, "keys": len(req.Keys), "deleted": resp.Deleted})
	return api.WriteJSON(w, http.StatusOK, resp)
}

func (m *Module) redisTTL(w http.ResponseWriter, r *http.Request) error {
	var req helperapi.RedisTTLRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	if err := req.Validate(); err != nil {
		return badRequest(err)
	}
	if err := m.call(r, helperapi.ActionRedisSetTTL, req, nil); err != nil {
		return err
	}
	modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "redis.ttl", "ok", map[string]any{"db": req.DB, "key": req.Key, "seconds": req.Seconds})
	w.WriteHeader(http.StatusNoContent)
	return nil
}
