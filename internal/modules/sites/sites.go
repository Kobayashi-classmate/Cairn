// SPDX-License-Identifier: AGPL-3.0-only

// Package sites manages web sites: static sites, PHP sites with a PHP-FPM
// pool of their own, and reverse proxies. The panel keeps what each site
// should be (its spec); every change runs as a job in which the root
// helper renders the configuration, lets nginx and php-fpm check it, and
// reloads them, restoring the previous files when anything fails.
package sites

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Kobayashi-classmate/Cairn/catalog"
	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/authz"
	"github.com/Kobayashi-classmate/Cairn/internal/events"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/jobs"
	"github.com/Kobayashi-classmate/Cairn/internal/module"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/modutil"
)

// Permissions of this module.
const (
	Read   authz.Permission = "sites.read"
	Manage authz.Permission = "sites.manage"
)

// Site states.
const (
	StateApplying = "applying"
	StateActive   = "active"
	StateFailed   = "failed"
)

// Module implements module.Module.
type Module struct {
	Catalog *catalog.Catalog
	Jobs    *jobs.Manager
	Helper  jobs.HelperClient
	DB      *sql.DB
	Events  *events.Bus
	Log     *slog.Logger
	// PanelPort is never given to apps or streams.
	PanelPort int
	// Certs and Backups add each site's certificate and backups to the
	// views; Modules tells whether their modules are on. Nil: left out.
	Certs   module.CertLookup
	Backups module.BackupLookup
	Modules module.Enabler

	health healthState
}

// ID implements module.Module.
func (m *Module) ID() string { return "sites" }

// Info implements module.Describer.
func (m *Module) Info() module.Info {
	return module.Info{
		Optional: true,
		Permissions: []module.Permission{
			{Name: Read, Roles: []authz.Role{authz.Operator, authz.Auditor}},
			{Name: Manage, Roles: []authz.Role{authz.Operator}},
		},
		Nav: []module.NavItem{
			{Key: "sites", LabelKey: "common.nav.sites", Icon: "globe", Route: "sites", Permission: Read, Group: module.GroupApps, Order: 15},
		},
	}
}

// Routes implements module.Module.
func (m *Module) Routes(r *api.Router) {
	doc := func(s string) api.RouteOption { return api.Doc("sites", s) }
	r.Handle("GET /api/v1/sites", m.list, api.Require(Read),
		doc("Sites, and what this system offers them (nginx, PHP lines)"), api.Returns(200, listResponse{}))
	r.Handle("GET /api/v1/sites/rewrites", m.rewrites, api.Require(Read),
		doc("Built-in URL rewrite rules"), api.Returns(200, rewritesResponse{}))
	r.Handle("POST /api/v1/sites", m.create, api.Require(Manage), api.Body(siteRequest{}),
		doc("Create a site (starts a job)"), api.Returns(202, jobResponse{}))
	r.Handle("GET /api/v1/sites/{code}", m.get, api.Require(Read), doc("One site"), api.Returns(200, siteView{}))
	r.Handle("PUT /api/v1/sites/{code}", m.update, api.Require(Manage), api.Body(siteRequest{}),
		doc("Change a site; the code and type stay (starts a job)"), api.Returns(202, jobResponse{}))
	r.Handle("POST /api/v1/sites/{code}/start", m.start, api.Require(Manage), api.NoBody(),
		doc("Serve the site again (starts a job)"), api.Returns(202, jobResponse{}))
	r.Handle("POST /api/v1/sites/{code}/stop", m.stop, api.Require(Manage), api.NoBody(),
		doc("Stop the site: it answers 503 (starts a job)"), api.Returns(202, jobResponse{}))
	r.Handle("DELETE /api/v1/sites/{code}", m.remove, api.Require(Manage), api.StepUp(),
		doc("Delete a site; ?deleteFiles=true also deletes its directory and users (starts a job)"), api.Returns(202, jobResponse{}))
	r.Handle("GET /api/v1/sites/{code}/log", m.log, api.Require(Read),
		doc("Read a site's nginx log: kind=access|error, offset (-1 for the last part)"), api.Returns(200, helperapi.SiteLogResponse{}))
	r.Handle("GET /api/v1/sites/{code}/app", m.appStatus, api.Require(Read),
		doc("An app site's process as systemd reports it"), api.Returns(200, appStatusView{}))
	r.Handle("GET /api/v1/sites/{code}/app/log", m.appLog, api.Require(Read),
		doc("An app site's journal: the last lines, or those after ?cursor="), api.Returns(200, helperapi.AppLogResponse{}))
	r.Handle("POST /api/v1/sites/{code}/restart", m.restart, api.Require(Manage), api.NoBody(),
		doc("Restart an app site's process (starts a job)"), api.Returns(202, jobResponse{}))
	m.streamRoutes(r, doc)
}

// siteRequest is a site as the API takes it. Code is optional on create
// (derived from the first domain); Type cannot change on update.
type siteRequest struct {
	Code     string               `json:"code,omitempty"`
	Type     string               `json:"type"`
	Domains  []string             `json:"domains"`
	Root     string               `json:"root,omitempty"`
	Index    []string             `json:"index,omitempty"`
	Rewrite  string               `json:"rewrite,omitempty"`
	PHP      string               `json:"php,omitempty"`
	Writable []string             `json:"writable,omitempty"`
	Proxy    *helperapi.ProxySpec `json:"proxy,omitempty"`
	// App is the process of an app site; a port of 0 gets a free one.
	App *helperapi.AppSpec `json:"app,omitempty"`
	// TLS serves the site over HTTPS with a certificate (certs module); the
	// helper checks that it covers every domain.
	TLS *helperapi.SiteTLS `json:"tls,omitempty"`
}

type siteView struct {
	helperapi.SiteSpec
	State     string `json:"state"`
	ErrorCode string `json:"errorCode,omitempty"`
	LastJob   string `json:"lastJob,omitempty"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
	// Dir, Owner and Runner are the site's directory and users.
	Dir    string `json:"dir"`
	Owner  string `json:"owner"`
	Runner string `json:"runner"`
	// Drift is true when the helper's applied spec differs from this one
	// (a change failed, or the helper's state was lost).
	Drift bool `json:"drift"`
	// Cert is the certificate TLS names, when it exists.
	Cert *module.CertInfo `json:"cert,omitempty"`
	// Backup is how the site is backed up; nil when no plan includes it.
	Backup *module.SiteBackup `json:"backup,omitempty"`
}

type listResponse struct {
	Supported bool   `json:"supported"`
	Reason    string `json:"reason,omitempty"`
	// StatusAvailable is false when the helper could not be asked.
	StatusAvailable bool `json:"statusAvailable"`
	Nginx           struct {
		Installed bool `json:"installed"`
		Running   bool `json:"running"`
	} `json:"nginx"`
	PHP   []string   `json:"php"`
	Sites []siteView `json:"sites"`
	// Systemd is false where apps cannot run.
	Systemd bool `json:"systemd"`
	// Backups is true when the sites' backups are included (the backups
	// module is on and could be asked).
	Backups bool `json:"backups"`
}

type rewritesResponse struct {
	Rules []catalog.RewriteRule `json:"rules"`
}

type jobResponse struct {
	Job jobs.Record `json:"job"`
}

func callerOf(r *http.Request) helperapi.Caller {
	who := modutil.ActorOf(r)
	return helperapi.Caller{UserID: who.UserID, Username: who.Username, Remote: api.ClientIP(r), Source: who.Source}
}

func (m *Module) status(ctx context.Context, r *http.Request) (helperapi.SiteStatus, bool) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var st helperapi.SiteStatus
	if err := m.Helper.Call(ctx, helperapi.ActionSiteStatus, callerOf(r), nil, &st); err != nil {
		m.Log.Warn("site status unavailable", "err", err)
		return st, false
	}
	return st, true
}

type row struct {
	spec      helperapi.SiteSpec
	state     string
	errorCode string
	lastJob   string
	createdAt int64
	updatedAt int64
}

const selectSite = `SELECT spec, state, error_code, last_job, created_at, updated_at FROM sites`

func scanRow(sc interface{ Scan(...any) error }) (row, error) {
	var rw row
	var spec string
	if err := sc.Scan(&spec, &rw.state, &rw.errorCode, &rw.lastJob, &rw.createdAt, &rw.updatedAt); err != nil {
		return rw, err
	}
	return rw, json.Unmarshal([]byte(spec), &rw.spec)
}

func (m *Module) load(ctx context.Context, code string) (row, error) {
	rw, err := scanRow(m.DB.QueryRowContext(ctx, selectSite+` WHERE code = ?`, code))
	if errors.Is(err, sql.ErrNoRows) {
		return rw, api.NewError(http.StatusNotFound, helperapi.CodeSiteNotFound, map[string]any{"site": code})
	}
	return rw, err
}

func view(rw row, applied map[string]helperapi.SiteState, known bool) siteView {
	v := siteView{SiteSpec: rw.spec, State: rw.state, ErrorCode: rw.errorCode, LastJob: rw.lastJob, CreatedAt: rw.createdAt, UpdatedAt: rw.updatedAt,
		Dir: "/srv/www/" + rw.spec.Code, Owner: "cw-" + rw.spec.Code, Runner: "cw-" + rw.spec.Code + "-run"}
	if v.Domains == nil {
		v.Domains = []string{}
	}
	if known {
		a, ok := applied[rw.spec.Code]
		got, _ := json.Marshal(a.Spec)
		want, _ := json.Marshal(rw.spec)
		v.Drift = !ok || string(got) != string(want)
	}
	return v
}

func (m *Module) list(w http.ResponseWriter, r *http.Request) error {
	st, known := m.status(r.Context(), r)
	resp := listResponse{Supported: st.Supported, Reason: st.Reason, StatusAvailable: known, PHP: st.PHP, Sites: []siteView{}}
	if !known {
		resp.Supported = true // unknown; the helper answers when asked to change something
	}
	if resp.PHP == nil {
		resp.PHP = []string{}
	}
	resp.Nginx.Installed, resp.Nginx.Running, resp.Systemd = st.Nginx.Installed, st.Nginx.Running, st.Systemd || !known
	rows, err := m.DB.QueryContext(r.Context(), selectSite+` ORDER BY code`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		rw, err := scanRow(rows)
		if err != nil {
			return err
		}
		resp.Sites = append(resp.Sites, view(rw, st.Sites, known))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	resp.Backups = m.decorate(r, resp.Sites)
	return api.WriteJSON(w, http.StatusOK, resp)
}

// decorate adds each site's certificate and backups from the modules that
// own them, and reports whether the backups could be included.
func (m *Module) decorate(r *http.Request, views []siteView) (backups bool) {
	on := func(id string) bool { return m.Modules == nil || m.Modules.Enabled(id) }
	if m.Certs != nil && on("certs") {
		var ids []string
		for _, v := range views {
			if v.TLS != nil && v.TLS.Cert != "" {
				ids = append(ids, v.TLS.Cert)
			}
		}
		certs, err := m.Certs.CertInfo(r.Context(), ids)
		if err != nil {
			m.Log.Warn("site certificates unavailable", "err", err)
		}
		for i := range views {
			if tls := views[i].TLS; tls != nil {
				if c, ok := certs[tls.Cert]; ok {
					views[i].Cert = &c
				}
			}
		}
	}
	if m.Backups != nil && on("backups") {
		found, err := m.Backups.SiteBackups(r)
		if err != nil {
			m.Log.Warn("site backups unavailable", "err", err)
			return false
		}
		for i := range views {
			if b, ok := found[views[i].Code]; ok {
				views[i].Backup = &b
			}
		}
		return true
	}
	return false
}

// Summarize implements module.Summarizer: sites whose last change failed.
func (m *Module) Summarize(r *http.Request, can func(authz.Permission) bool) (module.Summary, error) {
	if !can(Read) {
		return module.Summary{}, nil
	}
	var failed int
	if err := m.DB.QueryRowContext(r.Context(), `SELECT count(*) FROM sites WHERE state = ?`, StateFailed).Scan(&failed); err != nil {
		return module.Summary{}, err
	}
	if failed == 0 {
		return module.Summary{}, nil
	}
	return module.Summary{Notices: []module.Check{{Code: "sites.failed", Status: module.StatusBad, Route: "sites",
		Params: map[string]any{"count": failed}}}}, nil
}

func (m *Module) rewrites(w http.ResponseWriter, _ *http.Request) error {
	resp := rewritesResponse{Rules: append([]catalog.RewriteRule{}, m.Catalog.Rewrites...)}
	return api.WriteJSON(w, http.StatusOK, resp)
}

func (m *Module) get(w http.ResponseWriter, r *http.Request) error {
	rw, err := m.load(r.Context(), r.PathValue("code"))
	if err != nil {
		return err
	}
	st, known := m.status(r.Context(), r)
	v := []siteView{view(rw, st.Sites, known)}
	m.decorate(r, v)
	return api.WriteJSON(w, http.StatusOK, v[0])
}

var codeJunk = regexp.MustCompile(`[^a-z0-9]+`)

// deriveCode makes a site code from a domain: "www.example.com" gives
// "example-com".
func deriveCode(domain string) string {
	d := strings.TrimPrefix(strings.TrimPrefix(domain, "*."), "www.")
	c := strings.Trim(codeJunk.ReplaceAllString(d, "-"), "-")
	if c == "" || c[0] < 'a' || c[0] > 'z' {
		c = "site-" + c
	}
	if len(c) > 20 {
		c = strings.TrimRight(c[:20], "-")
	}
	return strings.TrimRight(c, "-")
}

// spec turns a request into a complete spec and checks it.
func (m *Module) spec(req siteRequest, code string, enabled bool) (helperapi.SiteSpec, error) {
	s := helperapi.SiteSpec{Code: code, Type: req.Type, Root: req.Root, Index: req.Index, Rewrite: req.Rewrite,
		PHP: req.PHP, Writable: req.Writable, Proxy: req.Proxy, App: req.App, TLS: req.TLS, Enabled: enabled}
	for _, d := range req.Domains {
		s.Domains = append(s.Domains, strings.ToLower(strings.TrimSpace(d)))
	}
	if err := s.Validate(); err != nil {
		var he *helperapi.Error
		if errors.As(err, &he) {
			return s, api.NewError(http.StatusBadRequest, he.Code, he.Params)
		}
		return s, err
	}
	if s.Rewrite != "" {
		rule, ok := m.Catalog.Rewrite(s.Rewrite)
		if !ok || !containsString(rule.Types, s.Type) {
			return s, api.NewError(http.StatusBadRequest, helperapi.CodeSiteRewrite, map[string]any{"rewrite": s.Rewrite})
		}
	}
	return s, nil
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// claimDomains records the site's domains, refusing ones another site has.
func claimDomains(ctx context.Context, tx *sql.Tx, code string, domains []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM site_domains WHERE code = ?`, code); err != nil {
		return err
	}
	for _, d := range domains {
		var other string
		err := tx.QueryRowContext(ctx, `SELECT code FROM site_domains WHERE domain = ?`, d).Scan(&other)
		if err == nil {
			return api.NewError(http.StatusConflict, helperapi.CodeSiteDomainTaken, map[string]any{"domain": d, "site": other})
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO site_domains (domain, code) VALUES (?, ?)`, d, code); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) create(w http.ResponseWriter, r *http.Request) error {
	var req siteRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	code := req.Code
	if code == "" && len(req.Domains) > 0 {
		code = deriveCode(strings.ToLower(strings.TrimSpace(req.Domains[0])))
		base := code
		for i := 2; m.exists(r.Context(), code); i++ {
			suffix := "-" + strconv.Itoa(i)
			code = strings.TrimRight(base[:min(len(base), 20-len(suffix))], "-") + suffix
		}
	}
	if err := m.assignPort(r.Context(), &req, code); err != nil {
		return err
	}
	s, err := m.spec(req, code, true)
	if err != nil {
		return err
	}
	tx, err := m.DB.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	data, _ := json.Marshal(s)
	now := time.Now().Unix()
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO sites (code, type, spec, state, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		s.Code, s.Type, string(data), StateApplying, now, now); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return api.NewError(http.StatusConflict, "site.code_taken", map[string]any{"code": s.Code})
		}
		return err
	}
	if err := claimDomains(r.Context(), tx, s.Code, s.Domains); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return m.applyJob(w, r, "site.create", s)
}

func (m *Module) exists(ctx context.Context, code string) bool {
	var n int
	_ = m.DB.QueryRowContext(ctx, `SELECT count(*) FROM sites WHERE code = ?`, code).Scan(&n)
	return n > 0
}

func (m *Module) update(w http.ResponseWriter, r *http.Request) error {
	var req siteRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	rw, err := m.load(r.Context(), r.PathValue("code"))
	if err != nil {
		return err
	}
	if (req.Code != "" && req.Code != rw.spec.Code) || req.Type != rw.spec.Type {
		return api.NewError(http.StatusBadRequest, "validation.invalid", map[string]any{"field": "type", "rule": "immutable"})
	}
	if req.App != nil && req.App.Port == 0 && rw.spec.App != nil {
		req.App.Port = rw.spec.App.Port // keep the port it has
	}
	if err := m.assignPort(r.Context(), &req, rw.spec.Code); err != nil {
		return err
	}
	s, err := m.spec(req, rw.spec.Code, rw.spec.Enabled)
	if err != nil {
		return err
	}
	if err := m.save(r.Context(), s, true); err != nil {
		return err
	}
	return m.applyJob(w, r, "site.update", s)
}

// save stores a spec (and, when they changed, its domains) as applying.
func (m *Module) save(ctx context.Context, s helperapi.SiteSpec, domains bool) error {
	tx, err := m.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	data, _ := json.Marshal(s)
	if _, err := tx.ExecContext(ctx, `UPDATE sites SET spec = ?, state = ?, error_code = '', updated_at = ? WHERE code = ?`,
		string(data), StateApplying, time.Now().Unix(), s.Code); err != nil {
		return err
	}
	if domains {
		if err := claimDomains(ctx, tx, s.Code, s.Domains); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (m *Module) start(w http.ResponseWriter, r *http.Request) error { return m.toggle(w, r, true) }
func (m *Module) stop(w http.ResponseWriter, r *http.Request) error  { return m.toggle(w, r, false) }

func (m *Module) toggle(w http.ResponseWriter, r *http.Request, enabled bool) error {
	rw, err := m.load(r.Context(), r.PathValue("code"))
	if err != nil {
		return err
	}
	s := rw.spec
	s.Enabled = enabled
	if err := m.save(r.Context(), s, false); err != nil {
		return err
	}
	return m.applyJob(w, r, map[bool]string{true: "site.start", false: "site.stop"}[enabled], s)
}

// applyJob starts the job that applies a spec through the helper.
func (m *Module) applyJob(w http.ResponseWriter, r *http.Request, kind string, s helperapi.SiteSpec) error {
	who := modutil.ActorOf(r)
	params := map[string]any{"site": s.Code, "domain": s.Domains[0]}
	job, err := m.Jobs.Start(r.Context(), jobs.Spec{
		Kind: kind, Params: params, Permission: Read, ManagePermission: Manage, Locks: []string{"site:" + s.Code},
		Actor: jobs.Actor{UserID: who.UserID, Username: who.Username, Source: who.Source, Remote: api.ClientIP(r)},
	}, func(ctx context.Context, j *jobs.Job) error {
		var res helperapi.SiteResult
		err := j.RunOp(helperapi.ActionSiteApply, s, &res)
		state, code := StateActive, ""
		if err != nil {
			state = StateFailed
			if he, ok := helperapi.AsError(err); ok {
				code = he.Code
			} else {
				code = api.ErrInternal.Code
			}
		} else {
			j.Logf("Directory %s, owner %s, runtime user %s.\n", res.Dir, res.Owner, res.Runner)
		}
		if _, dbErr := m.DB.ExecContext(context.WithoutCancel(ctx), `UPDATE sites SET state = ?, error_code = ?, last_job = ?, updated_at = ? WHERE code = ?`,
			state, code, j.ID(), time.Now().Unix(), s.Code); dbErr != nil {
			m.Log.Error("saving site state failed", "site", s.Code, "err", dbErr)
		}
		return err
	})
	if err != nil {
		return err
	}
	_, _ = m.DB.ExecContext(r.Context(), `UPDATE sites SET last_job = ? WHERE code = ?`, job.ID, s.Code)
	modutil.Audit(r, m.DB, m.Log, who, kind, "ok", map[string]any{"site": s.Code, "type": s.Type, "domains": s.Domains, "job": job.ID})
	return api.WriteJSON(w, http.StatusAccepted, jobResponse{Job: job})
}

func (m *Module) remove(w http.ResponseWriter, r *http.Request) error {
	rw, err := m.load(r.Context(), r.PathValue("code"))
	if err != nil {
		return err
	}
	deleteFiles := r.URL.Query().Get("deleteFiles") == "true"
	code := rw.spec.Code
	who := modutil.ActorOf(r)
	job, err := m.Jobs.Start(r.Context(), jobs.Spec{
		Kind: "site.delete", Params: map[string]any{"site": code, "domain": rw.spec.Domains[0], "deleteFiles": deleteFiles},
		Permission: Read, ManagePermission: Manage, Locks: []string{"site:" + code},
		Actor: jobs.Actor{UserID: who.UserID, Username: who.Username, Source: who.Source, Remote: api.ClientIP(r)},
	}, func(ctx context.Context, j *jobs.Job) error {
		err := j.RunOp(helperapi.ActionSiteRemove, helperapi.SiteRemoveRequest{Code: code, DeleteFiles: deleteFiles}, nil)
		// A site the helper never applied (its creation failed) can go.
		if he, ok := helperapi.AsError(err); ok && he.Code == helperapi.CodeSiteNotFound {
			j.Logf("The site was not configured on the system.\n")
			err = nil
		}
		if err != nil {
			return err
		}
		_, err = m.DB.ExecContext(context.WithoutCancel(ctx), `DELETE FROM sites WHERE code = ?`, code)
		return err
	})
	if err != nil {
		return err
	}
	modutil.Audit(r, m.DB, m.Log, who, "site.delete", "ok", map[string]any{"site": code, "deleteFiles": deleteFiles, "job": job.ID})
	return api.WriteJSON(w, http.StatusAccepted, jobResponse{Job: job})
}

func (m *Module) log(w http.ResponseWriter, r *http.Request) error {
	rw, err := m.load(r.Context(), r.PathValue("code"))
	if err != nil {
		return err
	}
	req := helperapi.SiteLogRequest{Code: rw.spec.Code, Kind: r.URL.Query().Get("kind"), Offset: -1}
	if req.Kind == "" {
		req.Kind = helperapi.SiteLogAccess
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		v, err := strconv.ParseInt(o, 10, 64)
		if err != nil {
			return api.NewError(http.StatusBadRequest, "validation.invalid", map[string]any{"field": "offset", "rule": "integer"})
		}
		req.Offset = v
	}
	if err := req.Validate(); err != nil {
		var he *helperapi.Error
		if errors.As(err, &he) {
			return api.NewError(http.StatusBadRequest, he.Code, he.Params)
		}
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	var resp helperapi.SiteLogResponse
	if err := m.Helper.Call(ctx, helperapi.ActionSiteLog, callerOf(r), req, &resp); err != nil {
		return err
	}
	return api.WriteJSON(w, http.StatusOK, resp)
}
