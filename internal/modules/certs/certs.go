// SPDX-License-Identifier: AGPL-3.0-only

// Package certs manages TLS certificates: issued through ACME (HTTP-01
// through nginx, DNS-01 through a DNS provider account) or uploaded,
// renewed automatically before they expire, and used by sites and by the
// panel itself. Private keys and ACME account keys stay with the root
// helper; this module keeps the list, the settings and the DNS provider
// credentials (sealed with the master key).
package certs

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/authz"
	"github.com/Kobayashi-classmate/Cairn/internal/dnsprov"
	"github.com/Kobayashi-classmate/Cairn/internal/events"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/jobs"
	"github.com/Kobayashi-classmate/Cairn/internal/module"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/modutil"
	"github.com/Kobayashi-classmate/Cairn/internal/secrets"
)

// Permissions of this module.
const (
	Read   authz.Permission = "certs.read"
	Manage authz.Permission = "certs.manage"
)

// Certificate states.
const (
	StateIssuing = "issuing"
	StateValid   = "valid"
	StateFailed  = "failed"
)

// Renewal: certificates are renewed this long before they expire, tried
// again at most this often, and reported when they expire this soon.
const (
	RenewBefore  = 30 * 24 * time.Hour
	RetryAfter   = 12 * time.Hour
	WarnBefore   = 7 * 24 * time.Hour
	checkEvery   = 6 * time.Hour
	firstCheck   = 2 * time.Minute
	sealContext  = "dns-account:"
	caContext    = "acme-ca:"
	kvEmail      = "acme.email"
	kvDefaultCA  = "acme.default_ca"
	kvCAPrefix   = "acme.ca."
	maxNameRunes = 64
)

// TLSReloader picks up a new panel certificate (the web process's TLS).
type TLSReloader interface{ Reload() error }

// Module implements module.Module.
type Module struct {
	Jobs   *jobs.Manager
	Helper jobs.HelperClient
	DB     *sql.DB
	Box    *secrets.Box
	Events *events.Bus
	TLS    TLSReloader
	Log    *slog.Logger
}

// ID implements module.Module.
func (m *Module) ID() string { return "certs" }

// Info implements module.Describer.
func (m *Module) Info() module.Info {
	return module.Info{
		Optional: true,
		Permissions: []module.Permission{
			{Name: Read, Roles: []authz.Role{authz.Operator, authz.Auditor}},
			{Name: Manage, Roles: []authz.Role{authz.Operator}},
		},
		Nav: []module.NavItem{
			{Key: "certs", LabelKey: "common.nav.certs", Icon: "lock", Route: "certs", Permission: Read, Group: module.GroupApps, Order: 17},
		},
	}
}

// Routes implements module.Module.
func (m *Module) Routes(r *api.Router) {
	doc := func(s string) api.RouteOption { return api.Doc("certs", s) }
	r.Handle("GET /api/v1/certs", m.list, api.Require(Read), doc("Certificates and which one the panel serves"), api.Returns(200, listResponse{}))
	r.Handle("POST /api/v1/certs", m.issue, api.Require(Manage), api.Body(issueRequest{}),
		doc("Issue a certificate through ACME (starts a job)"), api.Returns(202, jobResponse{}))
	r.Handle("POST /api/v1/certs/upload", m.upload, api.Require(Manage), api.Body(uploadRequest{}),
		doc("Store an uploaded certificate and key (starts a job)"), api.Returns(202, jobResponse{}))
	r.Handle("GET /api/v1/certs/settings", m.getSettings, api.Require(Read), doc("ACME settings"), api.Returns(200, settingsView{}))
	r.Handle("PUT /api/v1/certs/settings", m.putSettings, api.Require(Manage), api.StepUp(), api.Body(settingsRequest{}),
		doc("Change the ACME settings (contact, default CA, external account bindings)"), api.Returns(200, settingsView{}))
	r.Handle("PUT /api/v1/certs/panel", m.setPanel, api.Require(Manage), api.StepUp(), api.Body(panelRequest{}),
		doc("Serve a certificate on the panel itself (empty id: the self-signed one) (starts a job)"), api.Returns(202, jobResponse{}))
	r.Handle("GET /api/v1/certs/{id}", m.get, api.Require(Read), doc("One certificate with its chain"), api.Returns(200, certView{}))
	r.Handle("PUT /api/v1/certs/{id}", m.update, api.Require(Manage), api.Body(updateRequest{}),
		doc("Rename a certificate or turn automatic renewal on or off"), api.Returns(200, certView{}))
	r.Handle("POST /api/v1/certs/{id}/renew", m.renew, api.Require(Manage), api.NoBody(),
		doc("Renew an ACME certificate now (starts a job)"), api.Returns(202, jobResponse{}))
	r.Handle("DELETE /api/v1/certs/{id}", m.remove, api.Require(Manage), api.StepUp(),
		doc("Delete a certificate no site and not the panel uses (starts a job)"), api.Returns(202, jobResponse{}))

	r.Handle("GET /api/v1/dns/providers", m.providers, api.Require(Read), doc("DNS providers and their credential fields"),
		api.Returns(200, providersResponse{}))
	r.Handle("GET /api/v1/dns/accounts", m.listAccounts, api.Require(Read), doc("DNS provider accounts (never their secrets)"),
		api.Returns(200, accountsResponse{}))
	r.Handle("POST /api/v1/dns/accounts", m.createAccount, api.Require(Manage), api.StepUp(), api.Body(accountRequest{}),
		doc("Add a DNS provider account"), api.Returns(201, accountView{}))
	r.Handle("PUT /api/v1/dns/accounts/{id}", m.updateAccount, api.Require(Manage), api.StepUp(), api.Body(accountRequest{}),
		doc("Change a DNS provider account; empty secret fields keep their value"), api.Returns(200, accountView{}))
	r.Handle("DELETE /api/v1/dns/accounts/{id}", m.deleteAccount, api.Require(Manage), api.StepUp(),
		doc("Delete a DNS provider account no certificate uses"), api.Returns(204, nil))
	r.Handle("POST /api/v1/dns/accounts/{id}/test", m.testAccount, api.Require(Manage), api.Body(testRequest{}),
		doc("Check a DNS provider account by listing the records of a zone"), api.Returns(200, helperapi.DNSTestResult{}))
}

// views and requests ---------------------------------------------------------

type certView struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Source      string   `json:"source"`
	Domains     []string `json:"domains"`
	CA          string   `json:"ca,omitempty"`
	Challenge   string   `json:"challenge,omitempty"`
	DNSAccount  string   `json:"dnsAccount,omitempty"`
	KeyType     string   `json:"keyType,omitempty"`
	AutoRenew   bool     `json:"autoRenew"`
	State       string   `json:"state"`
	ErrorCode   string   `json:"errorCode,omitempty"`
	ErrorParams any      `json:"errorParams,omitempty"`
	NotBefore   int64    `json:"notBefore,omitempty"`
	NotAfter    int64    `json:"notAfter,omitempty"`
	Issuer      string   `json:"issuer,omitempty"`
	Serial      string   `json:"serial,omitempty"`
	Fingerprint string   `json:"fingerprint,omitempty"`
	LastJob     string   `json:"lastJob,omitempty"`
	CreatedAt   int64    `json:"createdAt"`
	UpdatedAt   int64    `json:"updatedAt"`
	// From the root helper: who uses it, and its chain.
	Sites []string              `json:"sites"`
	Panel bool                  `json:"panel"`
	Chain []helperapi.ChainCert `json:"chain,omitempty"`
}

type listResponse struct {
	Certs []certView `json:"certs"`
	// Panel is the certificate the panel serves ("" for the self-signed one).
	Panel           string `json:"panel"`
	StatusAvailable bool   `json:"statusAvailable"`
}

type issueRequest struct {
	Name       string   `json:"name,omitempty"`
	Domains    []string `json:"domains"`
	CA         string   `json:"ca,omitempty"` // default: the settings' default CA
	Challenge  string   `json:"challenge"`
	DNSAccount string   `json:"dnsAccount,omitempty"`
	KeyType    string   `json:"keyType,omitempty"` // default ec256
	AutoRenew  *bool    `json:"autoRenew,omitempty"`
}

type uploadRequest struct {
	Name    string `json:"name,omitempty"`
	CertPEM string `json:"certPem"`
	KeyPEM  string `json:"keyPem"`
}

type updateRequest struct {
	Name      string `json:"name"`
	AutoRenew bool   `json:"autoRenew"`
}

type panelRequest struct {
	ID string `json:"id"`
}

type jobResponse struct {
	Job jobs.Record `json:"job"`
}

// caSettings are a CA's stored settings (sealed): external account binding
// and, for "custom", the directory and its root.
type caSettings struct {
	EABKid    string `json:"eabKid,omitempty"`
	EABHMAC   string `json:"eabHmac,omitempty"`
	Directory string `json:"directory,omitempty"`
	RootPEM   string `json:"rootPem,omitempty"`
}

type caView struct {
	Name       string `json:"name"`
	Directory  string `json:"directory"`
	NeedsEAB   bool   `json:"needsEab"`
	EABKid     string `json:"eabKid,omitempty"`
	EABHMACSet bool   `json:"eabHmacSet"`
	RootPEM    string `json:"rootPem,omitempty"`
	// Ready is false when the CA cannot be used yet (EAB or directory missing).
	Ready bool `json:"ready"`
}

type settingsView struct {
	Email     string   `json:"email"`
	DefaultCA string   `json:"defaultCa"`
	CAs       []caView `json:"cas"`
}

type caRequest struct {
	EABKid string `json:"eabKid,omitempty"`
	// EABHMAC empty keeps the stored one; "-" clears it.
	EABHMAC   string `json:"eabHmac,omitempty"`
	Directory string `json:"directory,omitempty"`
	RootPEM   string `json:"rootPem,omitempty"`
}

type settingsRequest struct {
	Email     string               `json:"email"`
	DefaultCA string               `json:"defaultCa"`
	CAs       map[string]caRequest `json:"cas,omitempty"`
}

type providersResponse struct {
	Providers []dnsprov.Provider `json:"providers"`
}

type accountView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	// Fields holds the non-secret values; secret fields only say they are set.
	Fields    map[string]string `json:"fields"`
	SecretSet []string          `json:"secretSet"`
	UsedBy    []string          `json:"usedBy"`
	CreatedAt int64             `json:"createdAt"`
	UpdatedAt int64             `json:"updatedAt"`
}

type accountsResponse struct {
	Accounts []accountView `json:"accounts"`
}

type accountRequest struct {
	Name     string            `json:"name"`
	Provider string            `json:"provider"`
	Fields   map[string]string `json:"fields"`
}

type testRequest struct {
	Zone string `json:"zone"`
}

// helpers -----------------------------------------------------------------------

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "c" + hex.EncodeToString(b)[:15]
}

func newAccountID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "d" + hex.EncodeToString(b)[:15]
}

func callerOf(r *http.Request) helperapi.Caller {
	who := modutil.ActorOf(r)
	return helperapi.Caller{UserID: who.UserID, Username: who.Username, Remote: api.ClientIP(r), Source: who.Source}
}

func badRequest(field, rule string) error {
	return api.NewError(http.StatusBadRequest, "validation.invalid", map[string]any{"field": field, "rule": rule})
}

func asAPIError(err error) error {
	var he *helperapi.Error
	if errors.As(err, &he) {
		return api.NewError(http.StatusBadRequest, he.Code, he.Params)
	}
	return err
}

func cleanName(name, fallback string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = fallback
	}
	if len([]rune(name)) > maxNameRunes || strings.ContainsAny(name, "\x00\r\n") {
		return "", badRequest("name", "max")
	}
	return name, nil
}

func (m *Module) kv(ctx context.Context, key string) string {
	var v string
	_ = m.DB.QueryRowContext(ctx, `SELECT value FROM kv WHERE key = ?`, key).Scan(&v)
	return v
}

func (m *Module) setKV(ctx context.Context, key, value string) error {
	_, err := m.DB.ExecContext(ctx, `INSERT INTO kv(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func (m *Module) caSettings(ctx context.Context, name string) caSettings {
	var s caSettings
	sealed := m.kv(ctx, kvCAPrefix+name)
	if sealed == "" {
		return s
	}
	if data, err := m.Box.Open(sealed, caContext+name); err == nil {
		_ = json.Unmarshal(data, &s)
	}
	return s
}

func caNames() []string {
	return []string{"letsencrypt", "letsencrypt-staging", "zerossl", "google", "custom"}
}

func (m *Module) defaultCA(ctx context.Context) string {
	if v := m.kv(ctx, kvDefaultCA); v != "" {
		return v
	}
	return "letsencrypt"
}

// acmeCA builds the helper's view of a CA from the stored settings.
func (m *Module) acmeCA(ctx context.Context, name string) helperapi.ACMECA {
	s := m.caSettings(ctx, name)
	ca := helperapi.ACMECA{Name: name, EABKid: s.EABKid, EABHMAC: s.EABHMAC, Email: m.kv(ctx, kvEmail)}
	if name == "custom" {
		ca.Directory, ca.RootPEM = s.Directory, s.RootPEM
	}
	return ca
}

// status asks the root helper what it stores.
func (m *Module) status(ctx context.Context, r *http.Request) (helperapi.CertStatus, bool) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var st helperapi.CertStatus
	caller := helperapi.Caller{Source: "system"}
	if r != nil {
		caller = callerOf(r)
	}
	if err := m.Helper.Call(ctx, helperapi.ActionCertStatus, caller, nil, &st); err != nil {
		m.Log.Warn("certificate status unavailable", "err", err)
		return st, false
	}
	return st, true
}

const selectCert = `SELECT id, name, source, domains, ca, challenge, dns_account, key_type, auto_renew, state, error_code, error_params,
	not_before, not_after, issuer, serial, fingerprint, last_job, created_at, updated_at FROM certificates`

func scanCert(sc interface{ Scan(...any) error }) (certView, error) {
	var v certView
	var domains, params string
	var auto int
	err := sc.Scan(&v.ID, &v.Name, &v.Source, &domains, &v.CA, &v.Challenge, &v.DNSAccount, &v.KeyType, &auto, &v.State,
		&v.ErrorCode, &params, &v.NotBefore, &v.NotAfter, &v.Issuer, &v.Serial, &v.Fingerprint, &v.LastJob, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return v, err
	}
	v.AutoRenew = auto == 1
	_ = json.Unmarshal([]byte(domains), &v.Domains)
	if params != "" && params != "{}" {
		var p map[string]any
		if json.Unmarshal([]byte(params), &p) == nil {
			v.ErrorParams = p
		}
	}
	v.Sites = []string{}
	return v, nil
}

func (m *Module) load(ctx context.Context, id string) (certView, error) {
	v, err := scanCert(m.DB.QueryRowContext(ctx, selectCert+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return v, api.NewError(http.StatusNotFound, helperapi.CodeCertNotFound, map[string]any{"id": id})
	}
	return v, err
}

func merge(v certView, st helperapi.CertStatus) certView {
	if info, ok := st.Certs[v.ID]; ok {
		v.Sites, v.Panel, v.Chain = info.Sites, info.Panel, info.Chain
		if v.Sites == nil {
			v.Sites = []string{}
		}
	}
	return v
}

// certificates --------------------------------------------------------------

func (m *Module) list(w http.ResponseWriter, r *http.Request) error {
	st, known := m.status(r.Context(), r)
	rows, err := m.DB.QueryContext(r.Context(), selectCert+` ORDER BY not_after, name`)
	if err != nil {
		return err
	}
	defer rows.Close()
	resp := listResponse{Certs: []certView{}, Panel: st.Panel, StatusAvailable: known}
	for rows.Next() {
		v, err := scanCert(rows)
		if err != nil {
			return err
		}
		v = merge(v, st)
		v.Chain = nil
		resp.Certs = append(resp.Certs, v)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return api.WriteJSON(w, http.StatusOK, resp)
}

func (m *Module) get(w http.ResponseWriter, r *http.Request) error {
	v, err := m.load(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	st, _ := m.status(r.Context(), r)
	return api.WriteJSON(w, http.StatusOK, merge(v, st))
}

func (m *Module) update(w http.ResponseWriter, r *http.Request) error {
	var req updateRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	v, err := m.load(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	name, err := cleanName(req.Name, v.Name)
	if err != nil {
		return err
	}
	auto := 0
	if req.AutoRenew && v.Source == "acme" {
		auto = 1
	}
	if _, err := m.DB.ExecContext(r.Context(), `UPDATE certificates SET name = ?, auto_renew = ?, updated_at = ? WHERE id = ?`,
		name, auto, time.Now().Unix(), v.ID); err != nil {
		return err
	}
	modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "cert.update", "ok", map[string]any{"cert": v.ID, "name": name, "autoRenew": auto == 1})
	v, _ = m.load(r.Context(), v.ID)
	return api.WriteJSON(w, http.StatusOK, v)
}

// issueSpec checks an issue request against the settings.
func (m *Module) issueSpec(ctx context.Context, req issueRequest, id string) (helperapi.CertIssueRequest, error) {
	ca := req.CA
	if ca == "" {
		ca = m.defaultCA(ctx)
	}
	keyType := req.KeyType
	if keyType == "" {
		keyType = "ec256"
	}
	spec := helperapi.CertIssueRequest{ID: id, CA: m.acmeCA(ctx, ca), Challenge: req.Challenge, KeyType: keyType}
	for _, d := range req.Domains {
		spec.Domains = append(spec.Domains, strings.ToLower(strings.TrimSpace(d)))
	}
	if req.Challenge == helperapi.ChallengeDNS01 {
		if req.DNSAccount == "" {
			return spec, badRequest("dnsAccount", "required")
		}
		creds, err := m.credentials(ctx, req.DNSAccount)
		if err != nil {
			return spec, err
		}
		spec.DNS = &creds
	} else if req.DNSAccount != "" {
		return spec, badRequest("dnsAccount", "unexpected")
	}
	if err := spec.Validate(); err != nil {
		return spec, asAPIError(err)
	}
	return spec, nil
}

func (m *Module) issue(w http.ResponseWriter, r *http.Request) error {
	var req issueRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	id := newID()
	spec, err := m.issueSpec(r.Context(), req, id)
	if err != nil {
		return err
	}
	name, err := cleanName(req.Name, spec.Domains[0])
	if err != nil {
		return err
	}
	auto := 1
	if req.AutoRenew != nil && !*req.AutoRenew {
		auto = 0
	}
	domains, _ := json.Marshal(spec.Domains)
	now := time.Now().Unix()
	if _, err := m.DB.ExecContext(r.Context(), `INSERT INTO certificates (id, name, source, domains, ca, challenge, dns_account, key_type, auto_renew, state, created_at, updated_at)
		VALUES (?, ?, 'acme', ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, name, string(domains), spec.CA.Name, spec.Challenge, req.DNSAccount, spec.KeyType, auto, StateIssuing, now, now); err != nil {
		return err
	}
	who := modutil.ActorOf(r)
	job, err := m.startIssue(r.Context(), "cert.issue", id, req.DNSAccount, jobs.Actor{UserID: who.UserID, Username: who.Username, Source: who.Source, Remote: api.ClientIP(r)})
	if err != nil {
		return err
	}
	modutil.Audit(r, m.DB, m.Log, who, "cert.issue", "ok", map[string]any{"cert": id, "domains": spec.Domains, "ca": spec.CA.Name, "challenge": spec.Challenge, "job": job.ID})
	return api.WriteJSON(w, http.StatusAccepted, jobResponse{Job: job})
}

// startIssue starts the job issuing (or renewing) a stored ACME
// certificate; the request is rebuilt from the row and the settings when
// the job runs, so renewals use the current credentials.
func (m *Module) startIssue(ctx context.Context, kind, id, account string, actor jobs.Actor) (jobs.Record, error) {
	v, err := m.load(ctx, id)
	if err != nil {
		return jobs.Record{}, err
	}
	return m.Jobs.Start(ctx, jobs.Spec{
		Kind: kind, Params: map[string]any{"cert": id, "name": v.Name, "domain": v.Domains[0]}, Permission: Read, ManagePermission: Manage,
		Locks: []string{"cert:" + id}, Actor: actor,
	}, func(jctx context.Context, j *jobs.Job) error {
		spec, err := m.issueSpec(jctx, issueRequest{Domains: v.Domains, CA: v.CA, Challenge: v.Challenge, DNSAccount: account, KeyType: v.KeyType}, id)
		if err != nil {
			m.finish(id, j.ID(), nil, err)
			return err
		}
		var info helperapi.CertInfo
		err = j.RunOp(helperapi.ActionCertIssue, spec, &info)
		m.finish(id, j.ID(), &info, err)
		if err != nil && kind == "cert.renew" {
			code := "op.failed"
			if he, ok := helperapi.AsError(err); ok {
				code = he.Code
			}
			m.Events.Publish(events.Event{Type: events.CertRenewFailed, Level: events.Warning, DedupeKey: "cert-renew:" + id,
				Params: map[string]any{"cert": id, "name": v.Name, "domain": v.Domains[0], "code": code, "notAfter": v.NotAfter}})
		}
		return err
	})
}

// finish records the outcome of an issue, renewal or upload.
func (m *Module) finish(id, job string, info *helperapi.CertInfo, err error) {
	ctx := context.Background()
	now := time.Now().Unix()
	if err != nil {
		code, params := api.ErrInternal.Code, map[string]any{}
		if he, ok := helperapi.AsError(err); ok {
			code, params = he.Code, he.Params
		} else if ae := (*api.Error)(nil); errors.As(err, &ae) {
			code, params = ae.Code, ae.Params
		}
		p, _ := json.Marshal(params)
		// A certificate that was valid stays valid (and served) after a failed
		// renewal; only the error is recorded.
		_, dbErr := m.DB.ExecContext(ctx, `UPDATE certificates SET state = CASE WHEN not_after > ? THEN 'valid' ELSE 'failed' END,
			error_code = ?, error_params = ?, last_job = ?, renew_attempt_at = ?, updated_at = ? WHERE id = ?`, now, code, string(p), job, now, now, id)
		if dbErr != nil {
			m.Log.Error("saving certificate state failed", "cert", id, "err", dbErr)
		}
		return
	}
	domains, _ := json.Marshal(info.Domains)
	_, dbErr := m.DB.ExecContext(ctx, `UPDATE certificates SET state = 'valid', error_code = '', error_params = '{}', domains = ?, not_before = ?, not_after = ?,
		issuer = ?, serial = ?, fingerprint = ?, key_type = CASE WHEN key_type = '' THEN ? ELSE key_type END, last_job = ?, renew_attempt_at = ?, updated_at = ? WHERE id = ?`,
		string(domains), info.NotBefore, info.NotAfter, info.Issuer, info.Serial, info.Fingerprint, info.KeyType, job, now, now, id)
	if dbErr != nil {
		m.Log.Error("saving certificate state failed", "cert", id, "err", dbErr)
	}
	if info.Panel && m.TLS != nil {
		if err := m.TLS.Reload(); err != nil {
			m.Log.Warn("panel certificate reload failed", "err", err)
		}
	}
}

func (m *Module) upload(w http.ResponseWriter, r *http.Request) error {
	var req uploadRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	id := newID()
	hreq := helperapi.CertImportRequest{ID: id, CertPEM: req.CertPEM, KeyPEM: req.KeyPEM}
	if err := hreq.Validate(); err != nil {
		return asAPIError(err)
	}
	name, err := cleanName(req.Name, "")
	if err != nil {
		return err
	}
	if name == "" {
		name = "upload " + time.Now().UTC().Format(time.DateOnly)
	}
	now := time.Now().Unix()
	if _, err := m.DB.ExecContext(r.Context(), `INSERT INTO certificates (id, name, source, domains, auto_renew, state, created_at, updated_at)
		VALUES (?, ?, 'upload', '[]', 0, ?, ?, ?)`, id, name, StateIssuing, now, now); err != nil {
		return err
	}
	who := modutil.ActorOf(r)
	job, err := m.Jobs.Start(r.Context(), jobs.Spec{
		Kind: "cert.upload", Params: map[string]any{"cert": id, "name": name}, Permission: Read, ManagePermission: Manage,
		Locks: []string{"cert:" + id}, Actor: jobs.Actor{UserID: who.UserID, Username: who.Username, Source: who.Source, Remote: api.ClientIP(r)},
	}, func(_ context.Context, j *jobs.Job) error {
		var info helperapi.CertInfo
		err := j.RunOp(helperapi.ActionCertImport, hreq, &info)
		if err != nil {
			// Nothing was stored: the row goes again.
			_, _ = m.DB.ExecContext(context.Background(), `DELETE FROM certificates WHERE id = ?`, id)
			return err
		}
		m.finish(id, j.ID(), &info, nil)
		return nil
	})
	if err != nil {
		return err
	}
	modutil.Audit(r, m.DB, m.Log, who, "cert.upload", "ok", map[string]any{"cert": id, "name": name, "job": job.ID})
	return api.WriteJSON(w, http.StatusAccepted, jobResponse{Job: job})
}

func (m *Module) renew(w http.ResponseWriter, r *http.Request) error {
	v, err := m.load(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	if v.Source != "acme" {
		return api.NewError(http.StatusConflict, "cert.not_acme", map[string]any{"id": v.ID})
	}
	who := modutil.ActorOf(r)
	job, err := m.startIssue(r.Context(), "cert.renew", v.ID, v.DNSAccount, jobs.Actor{UserID: who.UserID, Username: who.Username, Source: who.Source, Remote: api.ClientIP(r)})
	if err != nil {
		return err
	}
	modutil.Audit(r, m.DB, m.Log, who, "cert.renew", "ok", map[string]any{"cert": v.ID, "name": v.Name, "job": job.ID})
	return api.WriteJSON(w, http.StatusAccepted, jobResponse{Job: job})
}

func (m *Module) remove(w http.ResponseWriter, r *http.Request) error {
	v, err := m.load(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	who := modutil.ActorOf(r)
	job, err := m.Jobs.Start(r.Context(), jobs.Spec{
		Kind: "cert.delete", Params: map[string]any{"cert": v.ID, "name": v.Name}, Permission: Read, ManagePermission: Manage,
		Locks: []string{"cert:" + v.ID}, Actor: jobs.Actor{UserID: who.UserID, Username: who.Username, Source: who.Source, Remote: api.ClientIP(r)},
	}, func(ctx context.Context, j *jobs.Job) error {
		err := j.RunOp(helperapi.ActionCertRemove, helperapi.CertRef{ID: v.ID}, nil)
		if he, ok := helperapi.AsError(err); ok && he.Code == helperapi.CodeCertNotFound {
			err = nil // never stored (a failed first issue)
		}
		if err != nil {
			return err
		}
		_, err = m.DB.ExecContext(context.WithoutCancel(ctx), `DELETE FROM certificates WHERE id = ?`, v.ID)
		return err
	})
	if err != nil {
		return err
	}
	modutil.Audit(r, m.DB, m.Log, who, "cert.delete", "ok", map[string]any{"cert": v.ID, "name": v.Name, "job": job.ID})
	return api.WriteJSON(w, http.StatusAccepted, jobResponse{Job: job})
}

func (m *Module) setPanel(w http.ResponseWriter, r *http.Request) error {
	var req panelRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	name := ""
	if req.ID != "" {
		v, err := m.load(r.Context(), req.ID)
		if err != nil {
			return err
		}
		if v.State != StateValid {
			return api.NewError(http.StatusConflict, "cert.not_valid", map[string]any{"id": v.ID})
		}
		name = v.Name
	}
	who := modutil.ActorOf(r)
	job, err := m.Jobs.Start(r.Context(), jobs.Spec{
		Kind: "cert.panel", Params: map[string]any{"cert": req.ID}, Permission: Read, ManagePermission: Manage,
		Locks: []string{"cert:panel"}, Actor: jobs.Actor{UserID: who.UserID, Username: who.Username, Source: who.Source, Remote: api.ClientIP(r)},
	}, func(_ context.Context, j *jobs.Job) error {
		if err := j.RunOp(helperapi.ActionPanelCert, helperapi.PanelCertRequest{ID: req.ID}, nil); err != nil {
			return err
		}
		if m.TLS != nil {
			if err := m.TLS.Reload(); err != nil {
				return err
			}
		}
		j.Logf("The panel serves the new certificate from now on.\n")
		return nil
	})
	if err != nil {
		return err
	}
	modutil.Audit(r, m.DB, m.Log, who, "cert.panel", "ok", map[string]any{"cert": req.ID, "name": name, "job": job.ID})
	return api.WriteJSON(w, http.StatusAccepted, jobResponse{Job: job})
}

// settings ----------------------------------------------------------------------

func (m *Module) settingsView(ctx context.Context) settingsView {
	v := settingsView{Email: m.kv(ctx, kvEmail), DefaultCA: m.defaultCA(ctx), CAs: []caView{}}
	for _, name := range caNames() {
		s := m.caSettings(ctx, name)
		cv := caView{Name: name, Directory: helperapi.ACMEDirectories[name], NeedsEAB: helperapi.ACMENeedsEAB[name],
			EABKid: s.EABKid, EABHMACSet: s.EABHMAC != ""}
		if name == "custom" {
			cv.Directory, cv.RootPEM = s.Directory, s.RootPEM
		}
		cv.Ready = (!cv.NeedsEAB || cv.EABHMACSet) && cv.Directory != ""
		v.CAs = append(v.CAs, cv)
	}
	return v
}

func (m *Module) getSettings(w http.ResponseWriter, r *http.Request) error {
	return api.WriteJSON(w, http.StatusOK, m.settingsView(r.Context()))
}

func (m *Module) putSettings(w http.ResponseWriter, r *http.Request) error {
	var req settingsRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	ctx := r.Context()
	if req.DefaultCA == "" {
		req.DefaultCA = "letsencrypt"
	}
	known := false
	for _, n := range caNames() {
		known = known || n == req.DefaultCA
	}
	if !known {
		return badRequest("defaultCa", "oneof")
	}
	// Check the result with the helper's own rules before storing it.
	stored := map[string]caSettings{}
	for name, cr := range req.CAs {
		s := m.caSettings(ctx, name)
		s.EABKid = strings.TrimSpace(cr.EABKid)
		switch cr.EABHMAC {
		case "":
		case "-":
			s.EABHMAC = ""
		default:
			s.EABHMAC = strings.TrimSpace(cr.EABHMAC)
		}
		if s.EABKid == "" {
			s.EABHMAC = ""
		}
		if name == "custom" {
			s.Directory, s.RootPEM = strings.TrimSpace(cr.Directory), strings.TrimSpace(cr.RootPEM)
		}
		ca := helperapi.ACMECA{Name: name, EABKid: s.EABKid, EABHMAC: s.EABHMAC, Directory: s.Directory, RootPEM: s.RootPEM, Email: req.Email}
		if err := (helperapi.CertIssueRequest{ID: "c000000000000000", Domains: []string{"example.com"}, CA: ca,
			Challenge: helperapi.ChallengeHTTP01, KeyType: "ec256"}).Validate(); err != nil {
			var he *helperapi.Error
			// A CA that needs EAB may be saved without it (it is just not ready).
			if errors.As(err, &he) {
				eabMissing := he.Params["field"] == "ca.eab" && he.Params["rule"] == "required"
				noDirectory := name == "custom" && s.Directory == "" && he.Params["field"] == "ca.directory"
				if !eabMissing && !noDirectory {
					return asAPIError(err)
				}
			}
		}
		stored[name] = s
	}
	if req.Email != "" {
		if err := (helperapi.CertIssueRequest{ID: "c000000000000000", Domains: []string{"example.com"}, CA: helperapi.ACMECA{Name: "letsencrypt", Email: req.Email},
			Challenge: helperapi.ChallengeHTTP01, KeyType: "ec256"}).Validate(); err != nil {
			return asAPIError(err)
		}
	}
	for name, s := range stored {
		data, _ := json.Marshal(s)
		sealed, err := m.Box.Seal(data, caContext+name)
		if err != nil {
			return err
		}
		if err := m.setKV(ctx, kvCAPrefix+name, sealed); err != nil {
			return err
		}
	}
	if err := m.setKV(ctx, kvEmail, strings.TrimSpace(req.Email)); err != nil {
		return err
	}
	if err := m.setKV(ctx, kvDefaultCA, req.DefaultCA); err != nil {
		return err
	}
	changed := make([]string, 0, len(stored))
	for name := range stored {
		changed = append(changed, name)
	}
	modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "cert.settings", "ok", map[string]any{"defaultCa": req.DefaultCA, "cas": changed})
	return api.WriteJSON(w, http.StatusOK, m.settingsView(ctx))
}

// DNS accounts -------------------------------------------------------------

func (m *Module) providers(w http.ResponseWriter, _ *http.Request) error {
	return api.WriteJSON(w, http.StatusOK, providersResponse{Providers: dnsprov.Providers})
}

func (m *Module) credentials(ctx context.Context, id string) (helperapi.DNSCredentials, error) {
	var provider, sealed string
	err := m.DB.QueryRowContext(ctx, `SELECT provider, secret FROM dns_accounts WHERE id = ?`, id).Scan(&provider, &sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return helperapi.DNSCredentials{}, api.NewError(http.StatusBadRequest, "dns.account_not_found", map[string]any{"id": id})
	}
	if err != nil {
		return helperapi.DNSCredentials{}, err
	}
	data, err := m.Box.Open(sealed, sealContext+id)
	if err != nil {
		return helperapi.DNSCredentials{}, err
	}
	var fields map[string]string
	if err := json.Unmarshal(data, &fields); err != nil {
		return helperapi.DNSCredentials{}, err
	}
	return helperapi.DNSCredentials{Provider: provider, Fields: fields}, nil
}

func (m *Module) accountView(ctx context.Context, id string) (accountView, error) {
	var v accountView
	err := m.DB.QueryRowContext(ctx, `SELECT id, name, provider, created_at, updated_at FROM dns_accounts WHERE id = ?`, id).
		Scan(&v.ID, &v.Name, &v.Provider, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return v, api.NewError(http.StatusNotFound, "dns.account_not_found", map[string]any{"id": id})
	}
	if err != nil {
		return v, err
	}
	creds, err := m.credentials(ctx, id)
	if err != nil {
		return v, err
	}
	v.Fields, v.SecretSet, v.UsedBy = map[string]string{}, []string{}, []string{}
	p, _ := dnsprov.Find(v.Provider)
	for _, f := range p.Fields {
		val := creds.Fields[f.Name]
		switch {
		case f.Secret && val != "":
			v.SecretSet = append(v.SecretSet, f.Name)
		case !f.Secret:
			v.Fields[f.Name] = val
		}
	}
	rows, err := m.DB.QueryContext(ctx, `SELECT id FROM certificates WHERE dns_account = ? ORDER BY id`, id)
	if err != nil {
		return v, err
	}
	defer rows.Close()
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return v, err
		}
		v.UsedBy = append(v.UsedBy, c)
	}
	return v, rows.Err()
}

func (m *Module) listAccounts(w http.ResponseWriter, r *http.Request) error {
	rows, err := m.DB.QueryContext(r.Context(), `SELECT id FROM dns_accounts ORDER BY name`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	resp := accountsResponse{Accounts: []accountView{}}
	for _, id := range ids {
		v, err := m.accountView(r.Context(), id)
		if err != nil {
			return err
		}
		resp.Accounts = append(resp.Accounts, v)
	}
	return api.WriteJSON(w, http.StatusOK, resp)
}

func (m *Module) saveAccount(ctx context.Context, id string, req accountRequest, keep map[string]string) error {
	fields := map[string]string{}
	for k, v := range req.Fields {
		if v = strings.TrimSpace(v); v != "" {
			fields[k] = v
		}
	}
	if p, ok := dnsprov.Find(req.Provider); ok {
		for _, f := range p.Fields {
			if f.Secret && fields[f.Name] == "" && keep[f.Name] != "" {
				fields[f.Name] = keep[f.Name]
			}
		}
	}
	if err := dnsprov.Validate(req.Provider, fields); err != nil {
		var fe *dnsprov.FieldError
		if errors.As(err, &fe) {
			return badRequest(fe.Field, fe.Rule)
		}
		return err
	}
	data, _ := json.Marshal(fields)
	sealed, err := m.Box.Seal(data, sealContext+id)
	if err != nil {
		return err
	}
	name, err := cleanName(req.Name, req.Provider)
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	_, err = m.DB.ExecContext(ctx, `INSERT INTO dns_accounts (id, name, provider, secret, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, provider = excluded.provider, secret = excluded.secret, updated_at = excluded.updated_at`,
		id, name, req.Provider, sealed, now, now)
	return err
}

func (m *Module) createAccount(w http.ResponseWriter, r *http.Request) error {
	var req accountRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	id := newAccountID()
	if err := m.saveAccount(r.Context(), id, req, nil); err != nil {
		return err
	}
	modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "dns.account_create", "ok", map[string]any{"account": id, "name": req.Name, "provider": req.Provider})
	v, err := m.accountView(r.Context(), id)
	if err != nil {
		return err
	}
	return api.WriteJSON(w, http.StatusCreated, v)
}

func (m *Module) updateAccount(w http.ResponseWriter, r *http.Request) error {
	var req accountRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	id := r.PathValue("id")
	old, err := m.credentials(r.Context(), id)
	if err != nil {
		return err
	}
	keep := map[string]string{}
	if old.Provider == req.Provider {
		keep = old.Fields
	}
	if err := m.saveAccount(r.Context(), id, req, keep); err != nil {
		return err
	}
	modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "dns.account_update", "ok", map[string]any{"account": id, "name": req.Name, "provider": req.Provider})
	v, err := m.accountView(r.Context(), id)
	if err != nil {
		return err
	}
	return api.WriteJSON(w, http.StatusOK, v)
}

func (m *Module) deleteAccount(w http.ResponseWriter, r *http.Request) error {
	v, err := m.accountView(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	if len(v.UsedBy) > 0 {
		return api.NewError(http.StatusConflict, "dns.account_in_use", map[string]any{"certs": strings.Join(v.UsedBy, " ")})
	}
	if _, err := m.DB.ExecContext(r.Context(), `DELETE FROM dns_accounts WHERE id = ?`, v.ID); err != nil {
		return err
	}
	modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "dns.account_delete", "ok", map[string]any{"account": v.ID, "name": v.Name, "provider": v.Provider})
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (m *Module) testAccount(w http.ResponseWriter, r *http.Request) error {
	var req testRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	creds, err := m.credentials(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	hreq := helperapi.DNSTestRequest{DNS: creds, Zone: strings.ToLower(strings.TrimSpace(req.Zone))}
	if err := hreq.Validate(); err != nil {
		return asAPIError(err)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	var res helperapi.DNSTestResult
	if err := m.Helper.Call(ctx, helperapi.ActionDNSTest, callerOf(r), hreq, &res); err != nil {
		if he, ok := helperapi.AsError(err); ok {
			return api.NewError(http.StatusBadGateway, he.Code, he.Params)
		}
		return err
	}
	return api.WriteJSON(w, http.StatusOK, res)
}

// renewal -----------------------------------------------------------------------

// Run renews ACME certificates before they expire, and reports
// certificates in use that are about to expire, until ctx ends.
func (m *Module) Run(ctx context.Context) {
	t := time.NewTimer(firstCheck)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		m.Check(ctx, time.Now())
		t.Reset(checkEvery)
	}
}

// Check starts the renewals that are due at now and publishes expiry
// warnings (exported for tests).
func (m *Module) Check(ctx context.Context, now time.Time) {
	rows, err := m.DB.QueryContext(ctx, `SELECT id, name, domains, source, auto_renew, state, dns_account, not_after, renew_attempt_at FROM certificates`)
	if err != nil {
		m.Log.Warn("certificate check failed", "err", err)
		return
	}
	type due struct {
		id, name, account, domain string
		notAfter                  int64
	}
	var renew, warn []due
	for rows.Next() {
		var d due
		var domains, source, state string
		var auto int
		var attempted int64
		if err := rows.Scan(&d.id, &d.name, &domains, &source, &auto, &state, &d.account, &d.notAfter, &attempted); err != nil {
			continue
		}
		var list []string
		_ = json.Unmarshal([]byte(domains), &list)
		if len(list) > 0 {
			d.domain = list[0]
		}
		left := time.Unix(d.notAfter, 0).Sub(now)
		if d.notAfter == 0 || state == StateIssuing {
			continue
		}
		if source == "acme" && auto == 1 && left < RenewBefore && now.Sub(time.Unix(attempted, 0)) > RetryAfter {
			renew = append(renew, d)
		}
		if left < WarnBefore {
			warn = append(warn, d)
		}
	}
	rows.Close()
	st, known := m.status(ctx, nil)
	for _, d := range warn {
		info, used := st.Certs[d.id]
		if known && (!used || (len(info.Sites) == 0 && !info.Panel)) {
			continue // nothing serves it
		}
		m.Events.Publish(events.Event{Type: events.CertExpiring, Level: events.Warning,
			DedupeKey: "cert-expiring:" + d.id + ":" + now.UTC().Format(time.DateOnly),
			Params:    map[string]any{"cert": d.id, "name": d.name, "domain": d.domain, "notAfter": d.notAfter, "days": int(time.Unix(d.notAfter, 0).Sub(now).Hours() / 24)}})
	}
	for _, d := range renew {
		if _, err := m.DB.ExecContext(ctx, `UPDATE certificates SET renew_attempt_at = ? WHERE id = ?`, now.Unix(), d.id); err != nil {
			continue
		}
		if _, err := m.startIssue(ctx, "cert.renew", d.id, d.account, jobs.Actor{Source: "system", Username: "cairn"}); err != nil {
			m.Log.Warn("certificate renewal not started", "cert", d.id, "err", err)
		}
	}
}

// overview -------------------------------------------------------------------

// Summarize implements module.Summarizer: the certificate the panel serves,
// and certificates that expire soon or failed.
func (m *Module) Summarize(r *http.Request, can func(authz.Permission) bool) (module.Summary, error) {
	if !can(Read) {
		return module.Summary{}, nil
	}
	st, known := m.status(r.Context(), r)
	rows, err := m.DB.QueryContext(r.Context(), `SELECT id, name, state, not_after FROM certificates`)
	if err != nil {
		return module.Summary{}, err
	}
	defer rows.Close()
	now := time.Now()
	var sum module.Summary
	var expiring, failed int
	var soonest int64
	for rows.Next() {
		var id, name, state string
		var notAfter int64
		if err := rows.Scan(&id, &name, &state, &notAfter); err != nil {
			return module.Summary{}, err
		}
		if known && id == st.Panel {
			sum.Checks = append(sum.Checks, module.Check{Code: "certs.panel", Status: module.StatusOK, Route: "certs",
				Params: map[string]any{"name": name, "notAfter": notAfter}})
		}
		info, used := st.Certs[id]
		inUse := !known || (used && (len(info.Sites) > 0 || info.Panel))
		switch {
		case state == StateFailed:
			failed++
		case state == StateValid && notAfter > 0 && inUse && time.Unix(notAfter, 0).Sub(now) < WarnBefore:
			expiring++
			if soonest == 0 || notAfter < soonest {
				soonest = notAfter
			}
		}
	}
	if err := rows.Err(); err != nil {
		return module.Summary{}, err
	}
	if known && st.Panel == "" {
		sum.Checks = append(sum.Checks, module.Check{Code: "certs.panel_self_signed", Status: module.StatusWarn, Route: "certs"})
	}
	if expiring > 0 {
		sum.Notices = append(sum.Notices, module.Check{Code: "certs.expiring", Status: module.StatusWarn, Route: "certs",
			Params: map[string]any{"count": expiring, "soonest": soonest}})
	}
	if failed > 0 {
		sum.Notices = append(sum.Notices, module.Check{Code: "certs.failed", Status: module.StatusBad, Route: "certs",
			Params: map[string]any{"count": failed}})
	}
	return sum, nil
}

// CertInfo implements module.CertLookup.
func (m *Module) CertInfo(ctx context.Context, ids []string) (map[string]module.CertInfo, error) {
	out := map[string]module.CertInfo{}
	if len(ids) == 0 {
		return out, nil
	}
	list, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	rows, err := m.DB.QueryContext(ctx, `SELECT id, name, state, not_after, auto_renew FROM certificates
		WHERE id IN (SELECT value FROM json_each(?))`, string(list))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c module.CertInfo
		var auto int
		if err := rows.Scan(&c.ID, &c.Name, &c.State, &c.NotAfter, &auto); err != nil {
			return nil, err
		}
		c.AutoRenew = auto == 1
		out[c.ID] = c
	}
	return out, rows.Err()
}
