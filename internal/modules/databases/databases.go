// SPDX-License-Identifier: AGPL-3.0-only

// Package databases manages the system's database servers: MariaDB or
// MySQL, PostgreSQL (one cluster per installed line) and Redis or Valkey.
// The root helper runs every statement; this module keeps which databases
// and users the panel made, the access it gave each user, and the users'
// passwords, sealed with the master key so that they can be shown again
// after a second verification.
package databases

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/authz"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/jobs"
	"github.com/Kobayashi-classmate/Cairn/internal/module"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/modutil"
	"github.com/Kobayashi-classmate/Cairn/internal/secrets"
)

// Permissions of this module.
const (
	Read   authz.Permission = "databases.read"
	Manage authz.Permission = "databases.manage"
)

const (
	userContext  = "db-user:"
	redisContext = "redis-password"
	kvRedisPass  = "databases.redis_password"
	passwordLen  = 20
	// Error codes of this module (the helper's are in helperapi).
	codeRestoreNeeds   = "db.restore_needs_user"
	codePasswordAbsent = "db.password_unknown"
	codeDumpExists     = "db.dump_exists"
	codeUploadTooLarge = "db.upload_too_large"
)

// Module implements module.Module.
type Module struct {
	Jobs   *jobs.Manager
	Helper jobs.HelperClient
	DB     *sql.DB
	Box    *secrets.Box
	Log    *slog.Logger
	// DataDir is the panel's data directory; dumps live in its
	// backups/db/<engine>/.
	DataDir string
}

// ID implements module.Module.
func (m *Module) ID() string { return "databases" }

// Info implements module.Describer.
func (m *Module) Info() module.Info {
	return module.Info{
		Optional: true,
		Permissions: []module.Permission{
			{Name: Read, Roles: []authz.Role{authz.Operator, authz.Auditor}},
			{Name: Manage, Roles: []authz.Role{authz.Operator}},
		},
		Nav: []module.NavItem{
			{Key: "databases", LabelKey: "common.nav.databases", Icon: "database", Route: "databases", Permission: Read, Order: 18},
		},
	}
}

// Routes implements module.Module.
func (m *Module) Routes(r *api.Router) {
	doc := func(s string) api.RouteOption { return api.Doc("databases", s) }
	job := api.Returns(202, jobResponse{})
	r.Handle("GET /api/v1/databases", m.list, api.Require(Read),
		doc("Installed database servers with their databases and users"), api.Returns(200, listResponse{}))
	r.Handle("POST /api/v1/databases/{engine}/dbs", m.createDB, api.Require(Manage), api.Body(createRequest{}),
		doc("Create a database, by default with a user of the same name (starts a job)"), job)
	r.Handle("DELETE /api/v1/databases/{engine}/dbs/{name}", m.dropDB, api.Require(Manage), api.StepUp(),
		doc("Drop a database; ?dropOwner=true also drops its owner (starts a job)"), job)
	r.Handle("POST /api/v1/databases/{engine}/users", m.createUser, api.Require(Manage), api.Body(userRequest{}),
		doc("Create a user (starts a job)"), job)
	r.Handle("PUT /api/v1/databases/{engine}/users/{name}", m.updateUser, api.Require(Manage), api.StepUp(), api.Body(userRequest{}),
		doc("Change a user's password, hosts or access; an empty password keeps it (starts a job)"), job)
	r.Handle("DELETE /api/v1/databases/{engine}/users/{name}", m.dropUser, api.Require(Manage), api.StepUp(),
		doc("Drop a user (starts a job)"), job)
	r.Handle("GET /api/v1/databases/{engine}/users/{name}/password", m.revealPassword, api.Require(Manage), api.StepUp(),
		doc("Show a user's password, if the panel set it"), api.Returns(200, passwordView{}))
	r.Handle("PUT /api/v1/databases/{engine}/remote", m.remote, api.Require(Manage), api.StepUp(), api.Body(remoteRequest{}),
		doc("Listen on every address or locally only (starts a job)"), job)
	r.Handle("POST /api/v1/databases/{engine}/service", m.service, api.Require(Manage), api.Body(serviceRequest{}),
		doc("Start, stop or restart a database server (starts a job)"), job)
	r.Handle("POST /api/v1/databases/{engine}/cluster", m.cluster, api.Require(Manage), api.NoBody(),
		doc("Create the main cluster of a PostgreSQL line that has none (starts a job)"), job)
	m.dumpRoutes(r, doc)
	m.redisRoutes(r, doc)
}

// requests and views --------------------------------------------------------

type createRequest struct {
	Name string `json:"name"`
	// User owns the database; "" with NoUser unset means a user of the
	// database's name, created when the panel does not have it yet.
	User     string   `json:"user,omitempty"`
	NoUser   bool     `json:"noUser,omitempty"`
	Password string   `json:"password,omitempty"` // a new user's; "" generates one
	Hosts    []string `json:"hosts,omitempty"`    // a new user's; default local only
}

type userRequest struct {
	Name     string              `json:"name,omitempty"` // create only
	Password string              `json:"password,omitempty"`
	Hosts    []string            `json:"hosts"`
	Grants   []helperapi.DBGrant `json:"grants"`
}

type remoteRequest struct {
	Enabled bool `json:"enabled"`
}

type serviceRequest struct {
	Action string `json:"action"`
}

type passwordView struct {
	Password string `json:"password"`
}

type dbView struct {
	Name      string `json:"name"`
	SizeBytes int64  `json:"sizeBytes"`
	Owner     string `json:"owner,omitempty"`
	// Managed: the panel created it. Missing: the panel created it but
	// the server no longer has it.
	Managed   bool  `json:"managed"`
	Missing   bool  `json:"missing,omitempty"`
	CreatedAt int64 `json:"createdAt,omitempty"`
}

type userView struct {
	Name        string              `json:"name"`
	Hosts       []string            `json:"hosts"`
	Grants      []helperapi.DBGrant `json:"grants"`
	Managed     bool                `json:"managed"`
	Missing     bool                `json:"missing,omitempty"`
	HasPassword bool                `json:"hasPassword"`
	UpdatedAt   int64               `json:"updatedAt,omitempty"`
}

type engineView struct {
	helperapi.DBEngineInfo
	Databases []dbView   `json:"databases"`
	Users     []userView `json:"users"`
	// RedisPasswordKnown: the panel can show the Redis password.
	RedisPasswordKnown bool `json:"redisPasswordKnown,omitempty"`
}

type listResponse struct {
	Supported       bool         `json:"supported"`
	Reason          string       `json:"reason,omitempty"`
	StatusAvailable bool         `json:"statusAvailable"`
	Engines         []engineView `json:"engines"`
}

type jobResponse struct {
	Job jobs.Record `json:"job"`
}

// helpers -------------------------------------------------------------------

func callerOf(r *http.Request) helperapi.Caller {
	who := modutil.ActorOf(r)
	return helperapi.Caller{UserID: who.UserID, Username: who.Username, Remote: api.ClientIP(r), Source: who.Source}
}

// badRequest turns a helperapi validation error into a 400.
func badRequest(err error) error {
	var he *helperapi.Error
	if errors.As(err, &he) {
		return api.NewError(http.StatusBadRequest, he.Code, he.Params)
	}
	return err
}

func engineOf(r *http.Request) (string, error) {
	e := r.PathValue("engine")
	if err := (helperapi.DBEngineRef{Engine: e}).Validate(); err != nil {
		return "", badRequest(err)
	}
	return e, nil
}

// sqlEngineOf is engineOf for MySQL and PostgreSQL only.
func sqlEngineOf(r *http.Request) (string, error) {
	e, err := engineOf(r)
	if err == nil && helperapi.EngineKind(e) == helperapi.DBRedis {
		err = api.NewError(http.StatusBadRequest, "validation.invalid", map[string]any{"field": "engine", "rule": "oneof"})
	}
	return e, err
}

const passwordChars = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"

// newPassword returns a random password that is safe in any connection
// string.
func newPassword() string {
	b := make([]byte, passwordLen)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = passwordChars[int(b[i])%len(passwordChars)]
	}
	return string(b)
}

func defaultHosts(engine string) []string {
	if helperapi.EngineKind(engine) == helperapi.DBMySQL {
		// Clients reach "localhost" over the socket and 127.0.0.1 over TCP.
		return []string{"localhost", "127.0.0.1"}
	}
	return []string{"localhost"}
}

func (m *Module) status(ctx context.Context, r *http.Request) (helperapi.DBStatus, bool) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var st helperapi.DBStatus
	if err := m.Helper.Call(ctx, helperapi.ActionDBStatus, callerOf(r), nil, &st); err != nil {
		m.Log.Warn("database status unavailable", "err", err)
		return st, false
	}
	return st, true
}

// userRow is a db_users row.
type userRow struct {
	engine, name, sealed string
	hosts                []string
	grants               []helperapi.DBGrant
	createdAt, updatedAt int64
}

func scanUser(sc interface{ Scan(...any) error }) (userRow, error) {
	var u userRow
	var hosts, grants string
	if err := sc.Scan(&u.engine, &u.name, &u.sealed, &hosts, &grants, &u.createdAt, &u.updatedAt); err != nil {
		return u, err
	}
	err1, err2 := json.Unmarshal([]byte(hosts), &u.hosts), json.Unmarshal([]byte(grants), &u.grants)
	return u, errors.Join(err1, err2)
}

const selectUser = `SELECT engine, name, password, hosts, grants, created_at, updated_at FROM db_users`

func (m *Module) loadUser(ctx context.Context, engine, name string) (userRow, bool, error) {
	u, err := scanUser(m.DB.QueryRowContext(ctx, selectUser+` WHERE engine = ? AND name = ?`, engine, name))
	if errors.Is(err, sql.ErrNoRows) {
		return u, false, nil
	}
	return u, err == nil, err
}

func (m *Module) users(ctx context.Context, engine string) ([]userRow, error) {
	rows, err := m.DB.QueryContext(ctx, selectUser+` WHERE engine = ? ORDER BY name`, engine)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []userRow
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (m *Module) password(u userRow) (string, error) {
	if u.sealed == "" {
		return "", nil
	}
	data, err := m.Box.Open(u.sealed, userContext+u.engine+":"+u.name)
	return string(data), err
}

// saveUser stores a user as the helper now has it; password "" keeps the
// stored one.
func (m *Module) saveUser(ctx context.Context, engine, name, password string, hosts []string, grants []helperapi.DBGrant) error {
	sealed := ""
	if password != "" {
		var err error
		if sealed, err = m.Box.Seal([]byte(password), userContext+engine+":"+name); err != nil {
			return err
		}
	}
	if grants == nil {
		grants = []helperapi.DBGrant{}
	}
	h, _ := json.Marshal(hosts)
	g, _ := json.Marshal(grants)
	now := time.Now().Unix()
	_, err := m.DB.ExecContext(ctx, `INSERT INTO db_users (engine, name, password, hosts, grants, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (engine, name) DO UPDATE SET password = CASE WHEN excluded.password = '' THEN db_users.password ELSE excluded.password END,
		hosts = excluded.hosts, grants = excluded.grants, updated_at = excluded.updated_at`,
		engine, name, sealed, string(h), string(g), now, now)
	return err
}

// list ----------------------------------------------------------------------

func (m *Module) list(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	st, known := m.status(ctx, r)
	resp := listResponse{Supported: st.Supported || !known, Reason: st.Reason, StatusAvailable: known, Engines: []engineView{}}
	for _, e := range st.Engines {
		v, err := m.engineView(ctx, e)
		if err != nil {
			return err
		}
		resp.Engines = append(resp.Engines, v)
	}
	return api.WriteJSON(w, http.StatusOK, resp)
}

// engineView merges what the server reports with the panel's records.
func (m *Module) engineView(ctx context.Context, e helperapi.DBEngineInfo) (engineView, error) {
	v := engineView{DBEngineInfo: e, Databases: []dbView{}, Users: []userView{}}
	v.DBEngineInfo.Databases, v.DBEngineInfo.Users = nil, nil
	if e.Kind == helperapi.DBRedis {
		v.RedisPasswordKnown = m.kv(ctx, kvRedisPass) != ""
		return v, nil
	}
	records := map[string]dbView{}
	rows, err := m.DB.QueryContext(ctx, `SELECT name, owner, created_at FROM db_databases WHERE engine = ?`, e.ID)
	if err != nil {
		return v, err
	}
	for rows.Next() {
		var d dbView
		if err := rows.Scan(&d.Name, &d.Owner, &d.CreatedAt); err != nil {
			_ = rows.Close()
			return v, err
		}
		d.Managed = true
		records[d.Name] = d
	}
	_ = rows.Close()
	for _, d := range e.Databases {
		dv := dbView{Name: d.Name, SizeBytes: d.SizeBytes, Owner: d.Owner}
		if rec, ok := records[d.Name]; ok {
			dv.Managed, dv.CreatedAt = true, rec.CreatedAt
			if rec.Owner != "" {
				dv.Owner = rec.Owner
			}
			delete(records, d.Name)
		}
		v.Databases = append(v.Databases, dv)
	}
	// Only a running server tells what is missing.
	for _, d := range records {
		d.Missing = e.Running
		v.Databases = append(v.Databases, d)
	}
	sort.Slice(v.Databases, func(i, j int) bool { return v.Databases[i].Name < v.Databases[j].Name })

	recs, err := m.users(ctx, e.ID)
	if err != nil {
		return v, err
	}
	byName := map[string]userRow{}
	for _, u := range recs {
		byName[u.name] = u
	}
	for _, u := range e.Users {
		uv := userView{Name: u.Name, Hosts: u.Hosts, Grants: []helperapi.DBGrant{}}
		if rec, ok := byName[u.Name]; ok {
			uv.Managed, uv.Grants, uv.HasPassword, uv.UpdatedAt = true, rec.grants, rec.sealed != "", rec.updatedAt
			if helperapi.EngineKind(e.ID) == helperapi.DBPostgreSQL {
				uv.Hosts = rec.hosts // the server only knows the remote ones
			}
			delete(byName, u.Name)
		}
		if uv.Hosts == nil {
			uv.Hosts = []string{}
		}
		v.Users = append(v.Users, uv)
	}
	for _, rec := range byName {
		v.Users = append(v.Users, userView{Name: rec.name, Hosts: rec.hosts, Grants: rec.grants, Managed: true, Missing: e.Running,
			HasPassword: rec.sealed != "", UpdatedAt: rec.updatedAt})
	}
	sort.Slice(v.Users, func(i, j int) bool { return v.Users[i].Name < v.Users[j].Name })
	return v, nil
}

// jobs ----------------------------------------------------------------------

// start runs fn as a job of the engine and audits it.
func (m *Module) start(w http.ResponseWriter, r *http.Request, kind, engine string, params map[string]any, fn jobs.Func) error {
	who := modutil.ActorOf(r)
	params["engine"] = engine
	job, err := m.Jobs.Start(r.Context(), jobs.Spec{
		Kind: kind, Params: params, Permission: Read, ManagePermission: Manage, Locks: []string{"db:" + engine},
		Actor: jobs.Actor{UserID: who.UserID, Username: who.Username, Source: who.Source, Remote: api.ClientIP(r)},
	}, fn)
	if err != nil {
		return err
	}
	audit := map[string]any{"job": job.ID}
	for k, v := range params {
		audit[k] = v
	}
	modutil.Audit(r, m.DB, m.Log, who, kind, "ok", audit)
	return api.WriteJSON(w, http.StatusAccepted, jobResponse{Job: job})
}

func (m *Module) createDB(w http.ResponseWriter, r *http.Request) error {
	engine, err := sqlEngineOf(r)
	if err != nil {
		return err
	}
	var req createRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	owner := ""
	if !req.NoUser {
		owner = req.User
		if owner == "" {
			owner = req.Name
		}
	}
	if err := (helperapi.DBCreateRequest{Engine: engine, Name: req.Name, Owner: owner}).Validate(); err != nil {
		return badRequest(err)
	}
	ctx := r.Context()
	// The owner is created unless the panel already has it.
	var newUser *helperapi.DBUserRequest
	if owner != "" {
		_, have, err := m.loadUser(ctx, engine, owner)
		if err != nil {
			return err
		}
		if !have {
			u := helperapi.DBUserRequest{Engine: engine, Name: owner, New: true, Password: req.Password, Hosts: req.Hosts, Grants: []helperapi.DBGrant{}}
			if u.Password == "" {
				u.Password = newPassword()
			}
			if len(u.Hosts) == 0 {
				u.Hosts = defaultHosts(engine)
			}
			if err := u.Validate(); err != nil {
				return badRequest(err)
			}
			newUser = &u
		}
	}
	params := map[string]any{"database": req.Name}
	if owner != "" {
		params["user"] = owner
	}
	return m.start(w, r, "db.create", engine, params, func(ctx context.Context, j *jobs.Job) error {
		ctx = context.WithoutCancel(ctx)
		if newUser != nil {
			if err := j.RunOp(helperapi.ActionDBUserSet, *newUser, nil); err != nil {
				return err
			}
			if err := m.saveUser(ctx, engine, owner, newUser.Password, newUser.Hosts, nil); err != nil {
				return err
			}
			j.Logf("User %s created.\n", owner)
		}
		if err := j.RunOp(helperapi.ActionDBCreate, helperapi.DBCreateRequest{Engine: engine, Name: req.Name, Owner: owner}, nil); err != nil {
			return err
		}
		if _, err := m.DB.ExecContext(ctx, `INSERT INTO db_databases (engine, name, owner, created_at) VALUES (?, ?, ?, ?)
			ON CONFLICT (engine, name) DO UPDATE SET owner = excluded.owner`, engine, req.Name, owner, time.Now().Unix()); err != nil {
			return err
		}
		if owner == "" {
			return nil
		}
		u, _, err := m.loadUser(ctx, engine, owner)
		if err != nil {
			return err
		}
		grants := slices.DeleteFunc(u.grants, func(g helperapi.DBGrant) bool { return g.Database == req.Name })
		return m.saveUser(ctx, engine, owner, "", u.hosts, append(grants, helperapi.DBGrant{Database: req.Name, Level: helperapi.GrantAll}))
	})
}

func (m *Module) dropDB(w http.ResponseWriter, r *http.Request) error {
	engine, err := sqlEngineOf(r)
	if err != nil {
		return err
	}
	name := r.PathValue("name")
	if err := (helperapi.DBRef{Engine: engine, Name: name}).Validate(); err != nil || !helperapi.ValidDBName(engine, name) {
		return api.NewError(http.StatusBadRequest, "validation.invalid", map[string]any{"field": "name", "rule": "db_name"})
	}
	dropOwner := r.URL.Query().Get("dropOwner") == "true"
	var owner string
	_ = m.DB.QueryRowContext(r.Context(), `SELECT owner FROM db_databases WHERE engine = ? AND name = ?`, engine, name).Scan(&owner)
	params := map[string]any{"database": name}
	if dropOwner && owner != "" {
		params["user"] = owner
	}
	return m.start(w, r, "db.drop", engine, params, func(ctx context.Context, j *jobs.Job) error {
		ctx = context.WithoutCancel(ctx)
		err := j.RunOp(helperapi.ActionDBDrop, helperapi.DBRef{Engine: engine, Name: name}, nil)
		if he, ok := helperapi.AsError(err); ok && he.Code == helperapi.CodeDBNotFound {
			j.Logf("The database was already gone.\n")
			err = nil
		}
		if err != nil {
			return err
		}
		if err := m.forgetDB(ctx, engine, name); err != nil {
			return err
		}
		if !dropOwner || owner == "" {
			return nil
		}
		return m.runDropUser(ctx, j, engine, owner)
	})
}

// forgetDB removes a dropped database from the records.
func (m *Module) forgetDB(ctx context.Context, engine, name string) error {
	if _, err := m.DB.ExecContext(ctx, `DELETE FROM db_databases WHERE engine = ? AND name = ?`, engine, name); err != nil {
		return err
	}
	users, err := m.users(ctx, engine)
	if err != nil {
		return err
	}
	for _, u := range users {
		kept := slices.DeleteFunc(slices.Clone(u.grants), func(g helperapi.DBGrant) bool { return g.Database == name })
		if len(kept) != len(u.grants) {
			if err := m.saveUser(ctx, engine, u.name, "", u.hosts, kept); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *Module) createUser(w http.ResponseWriter, r *http.Request) error {
	engine, err := sqlEngineOf(r)
	if err != nil {
		return err
	}
	var req userRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	if _, have, err := m.loadUser(r.Context(), engine, req.Name); err != nil {
		return err
	} else if have {
		return api.NewError(http.StatusConflict, helperapi.CodeDBUserExists, map[string]any{"name": req.Name})
	}
	u := helperapi.DBUserRequest{Engine: engine, Name: req.Name, New: true, Password: req.Password, Hosts: req.Hosts, Grants: req.Grants}
	if u.Password == "" {
		u.Password = newPassword()
	}
	if len(u.Hosts) == 0 {
		u.Hosts = defaultHosts(engine)
	}
	if u.Grants == nil {
		u.Grants = []helperapi.DBGrant{}
	}
	if err := u.Validate(); err != nil {
		return badRequest(err)
	}
	return m.start(w, r, "db.user_create", engine, map[string]any{"user": u.Name}, func(ctx context.Context, j *jobs.Job) error {
		if err := j.RunOp(helperapi.ActionDBUserSet, u, nil); err != nil {
			return err
		}
		return m.saveUser(context.WithoutCancel(ctx), engine, u.Name, u.Password, u.Hosts, u.Grants)
	})
}

func (m *Module) updateUser(w http.ResponseWriter, r *http.Request) error {
	engine, err := sqlEngineOf(r)
	if err != nil {
		return err
	}
	var req userRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	name := r.PathValue("name")
	if req.Name != "" && req.Name != name {
		return api.NewError(http.StatusBadRequest, "validation.invalid", map[string]any{"field": "name", "rule": "immutable"})
	}
	rec, have, err := m.loadUser(r.Context(), engine, name)
	if err != nil {
		return err
	}
	u := helperapi.DBUserRequest{Engine: engine, Name: name, Password: req.Password, Hosts: req.Hosts, Grants: req.Grants}
	if u.Grants == nil {
		u.Grants = []helperapi.DBGrant{}
	}
	if err := u.Validate(); err != nil {
		return badRequest(err)
	}
	// New host entries need the password; the panel sends the one it has.
	send := u
	if send.Password == "" && have {
		if send.Password, err = m.password(rec); err != nil {
			return err
		}
	}
	params := map[string]any{"user": name, "password": req.Password != ""}
	return m.start(w, r, "db.user_update", engine, params, func(ctx context.Context, j *jobs.Job) error {
		if err := j.RunOp(helperapi.ActionDBUserSet, send, nil); err != nil {
			return err
		}
		return m.saveUser(context.WithoutCancel(ctx), engine, name, req.Password, u.Hosts, u.Grants)
	})
}

func (m *Module) dropUser(w http.ResponseWriter, r *http.Request) error {
	engine, err := sqlEngineOf(r)
	if err != nil {
		return err
	}
	name := r.PathValue("name")
	if !helperapi.ValidDBUser(engine, name) {
		return api.NewError(http.StatusBadRequest, "validation.invalid", map[string]any{"field": "name", "rule": "db_user"})
	}
	return m.start(w, r, "db.user_drop", engine, map[string]any{"user": name}, func(ctx context.Context, j *jobs.Job) error {
		return m.runDropUser(context.WithoutCancel(ctx), j, engine, name)
	})
}

func (m *Module) runDropUser(ctx context.Context, j *jobs.Job, engine, name string) error {
	err := j.RunOp(helperapi.ActionDBUserDrop, helperapi.DBRef{Engine: engine, Name: name}, nil)
	if he, ok := helperapi.AsError(err); ok && he.Code == helperapi.CodeDBUserNotFound {
		j.Logf("The user was already gone.\n")
		err = nil
	}
	if err != nil {
		return err
	}
	if _, err := m.DB.ExecContext(ctx, `DELETE FROM db_users WHERE engine = ? AND name = ?`, engine, name); err != nil {
		return err
	}
	_, err = m.DB.ExecContext(ctx, `UPDATE db_databases SET owner = '' WHERE engine = ? AND owner = ?`, engine, name)
	return err
}

func (m *Module) revealPassword(w http.ResponseWriter, r *http.Request) error {
	engine, err := sqlEngineOf(r)
	if err != nil {
		return err
	}
	name := r.PathValue("name")
	rec, have, err := m.loadUser(r.Context(), engine, name)
	if err != nil {
		return err
	}
	if !have || rec.sealed == "" {
		return api.NewError(http.StatusNotFound, codePasswordAbsent, map[string]any{"user": name})
	}
	pw, err := m.password(rec)
	if err != nil {
		return err
	}
	modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "db.password_reveal", "ok", map[string]any{"engine": engine, "user": name})
	return api.WriteJSON(w, http.StatusOK, passwordView{Password: pw})
}

func (m *Module) remote(w http.ResponseWriter, r *http.Request) error {
	engine, err := engineOf(r)
	if err != nil {
		return err
	}
	var req remoteRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	return m.start(w, r, "db.remote", engine, map[string]any{"enabled": req.Enabled}, func(_ context.Context, j *jobs.Job) error {
		return j.RunOp(helperapi.ActionDBRemote, helperapi.DBRemoteRequest{Engine: engine, Enabled: req.Enabled}, nil)
	})
}

func (m *Module) service(w http.ResponseWriter, r *http.Request) error {
	engine, err := engineOf(r)
	if err != nil {
		return err
	}
	var req serviceRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	q := helperapi.DBServiceRequest{Engine: engine, Action: req.Action}
	if err := q.Validate(); err != nil {
		return badRequest(err)
	}
	return m.start(w, r, "db.service", engine, map[string]any{"action": req.Action}, func(_ context.Context, j *jobs.Job) error {
		return j.RunOp(helperapi.ActionDBService, q, nil)
	})
}

func (m *Module) cluster(w http.ResponseWriter, r *http.Request) error {
	engine, err := engineOf(r)
	if err != nil {
		return err
	}
	if helperapi.EngineKind(engine) != helperapi.DBPostgreSQL {
		return api.NewError(http.StatusBadRequest, "validation.invalid", map[string]any{"field": "engine", "rule": "oneof"})
	}
	return m.start(w, r, "db.cluster", engine, map[string]any{}, func(_ context.Context, j *jobs.Job) error {
		return j.RunOp(helperapi.ActionDBPGCluster, helperapi.DBEngineRef{Engine: engine}, nil)
	})
}

func (m *Module) kv(ctx context.Context, key string) string {
	var v string
	_ = m.DB.QueryRowContext(ctx, `SELECT value FROM kv WHERE key = ?`, key).Scan(&v)
	return v
}
