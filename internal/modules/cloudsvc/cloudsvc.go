// SPDX-License-Identifier: AGPL-3.0-only

// Package cloudsvc is the panel side of the Cairn cloud: it pulls the signed
// official messages and release index, shows messages in the message center,
// reports available updates and asks the root helper to install one. It
// never uploads anything, and nothing it receives is executed.
package cloudsvc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/authz"
	"github.com/Kobayashi-classmate/Cairn/internal/cloud"
	"github.com/Kobayashi-classmate/Cairn/internal/config"
	"github.com/Kobayashi-classmate/Cairn/internal/events"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/module"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/modutil"
	"github.com/Kobayashi-classmate/Cairn/internal/semver"
)

// Polling cadence: every six hours with jitter, so installations do not all
// ask at the same moment.
const (
	pollEvery  = 6 * time.Hour
	pollJitter = 30 * time.Minute
)

// Helper is the subset of the helper client the module uses.
type Helper interface {
	Call(ctx context.Context, action string, caller helperapi.Caller, req, resp any) error
}

// Settings holds the [cloud] configuration at runtime.
type Settings struct {
	mu  sync.RWMutex
	cfg config.Cloud
}

// NewSettings starts from the configuration file.
func NewSettings(c config.Cloud) *Settings { return &Settings{cfg: c} }

// Get returns a copy of the settings.
func (s *Settings) Get() config.Cloud {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c := s.cfg
	c.TrustedKeys = append([]string(nil), s.cfg.TrustedKeys...)
	return c
}

func (s *Settings) set(enabled bool, baseURL, channel string) {
	s.mu.Lock()
	s.cfg.Enabled, s.cfg.BaseURL, s.cfg.Channel = enabled, baseURL, channel
	s.mu.Unlock()
}

// Module implements module.Module.
type Module struct {
	DB       *sql.DB
	Helper   Helper
	Events   *events.Bus
	Settings *Settings
	Log      *slog.Logger
	Version  string // running version
	Family   string // debian | el
	Format   string // deb | rpm
	Arch     string
	HTTP     *http.Client // nil: default client

	mu        sync.Mutex
	status    string // up_to_date | available | unavailable
	reason    string
	latest    *cloud.Release
	checkedAt int64
}

// ID implements module.Module.
func (m *Module) ID() string { return "cloud" }

// Info implements module.Describer.
func (m *Module) Info() module.Info {
	return module.Info{Nav: []module.NavItem{
		{Key: "messages", LabelKey: "common.nav.messages", Icon: "megaphone", Route: "messages", Group: module.GroupSystem, Order: 85},
	}}
}

// Routes implements module.Module.
func (m *Module) Routes(r *api.Router) {
	self := api.Require(authz.Self)
	read := api.Require(authz.SettingsRead)
	manage := api.Require(authz.SettingsManage)
	msgDoc := func(s string) api.RouteOption { return api.Doc("messages", s) }
	updDoc := func(s string) api.RouteOption { return api.Doc("updates", s) }

	r.Handle("GET /api/v1/announcements", m.listAnnouncements, self, msgDoc("Official messages for this installation"),
		api.Returns(200, announcementsResponse{}))
	r.Handle("POST /api/v1/announcements/read-all", m.readAll, self, msgDoc("Mark every message as read"), api.NoBody(), api.Returns(204, nil))
	r.Handle("POST /api/v1/announcements/{id}/read", m.readOne, self, msgDoc("Mark a message as read"), api.NoBody(), api.Returns(204, nil))

	r.Handle("GET /api/v1/updates", m.getUpdates, read, updDoc("Update status of the panel itself"), api.Returns(200, updatesResponse{}))
	r.Handle("POST /api/v1/updates/check", m.checkUpdates, manage, updDoc("Check for a newer panel version now"), api.NoBody(), api.Returns(200, updatesResponse{}))
	r.Handle("POST /api/v1/updates/apply", m.applyUpdate, manage, api.StepUp(), updDoc("Install a newer version of the panel"),
		api.Body(applyRequest{}), api.Returns(202, jobResponse{}))

	r.Handle("GET /api/v1/settings/cloud", m.getCloud, read, api.Doc("settings", "Cloud access settings"), api.Returns(200, cloudSettings{}))
	r.Handle("PUT /api/v1/settings/cloud", m.setCloud, manage, api.StepUp(), api.Doc("settings", "Change cloud access settings"),
		api.Body(cloudSettings{}), api.Returns(204, nil))
}

// Run polls the cloud until ctx is cancelled.
func (m *Module) Run(ctx context.Context) {
	// Jitter only spreads the load on the server; it needs no cryptographic randomness.
	wait := time.Duration(30+rand.IntN(150)) * time.Second //nolint:gosec // G404: see above.
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		m.Refresh(ctx)
		wait = pollEvery - pollJitter + time.Duration(rand.Int64N(int64(2*pollJitter))) //nolint:gosec // G404: jitter only.
	}
}

// Refresh pulls messages and the release index once.
func (m *Module) Refresh(ctx context.Context) {
	m.refreshAnnouncements(ctx)
	m.refreshReleases(ctx)
}

func (m *Module) client() (*cloud.Client, error) {
	cfg := m.Settings.Get()
	keys, err := cloud.NewKeyring(cfg.TrustedKeys)
	if err != nil {
		return nil, err
	}
	return &cloud.Client{BaseURL: cfg.BaseURL, Keys: keys, HTTP: m.HTTP}, nil
}

func (m *Module) current() *semver.Version {
	v, err := semver.Parse(m.Version)
	if err != nil {
		return nil
	}
	return &v
}

func (m *Module) kvGet(ctx context.Context, key string) string {
	var v string
	_ = m.DB.QueryRowContext(ctx, `SELECT value FROM kv WHERE key = ?`, key).Scan(&v)
	return v
}

func (m *Module) kvSet(ctx context.Context, key, value string) {
	if _, err := m.DB.ExecContext(ctx, `INSERT INTO kv(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
		m.Log.Warn("cloud: kv write", "key", key, "err", err)
	}
}

func (m *Module) refreshAnnouncements(ctx context.Context) {
	if !m.Settings.Get().Enabled {
		return
	}
	c, err := m.client()
	if err != nil {
		m.Log.Warn("cloud: keyring", "err", err)
		return
	}
	last, _ := strconv.ParseInt(m.kvGet(ctx, "cloud.announcements.generated"), 10, 64)
	feed, err := c.FetchAnnouncements(ctx, last)
	if err != nil {
		if !errors.Is(err, cloud.ErrNoTrustedKey) {
			m.Log.Warn("cloud: announcements", "err", err)
		}
		return
	}
	items := feed.Applicable(m.current(), m.Family, time.Now())
	keep := map[string]bool{}
	now := time.Now().Unix()
	for _, it := range items {
		keep[it.ID] = true
		title, _ := json.Marshal(it.Title)
		body, _ := json.Marshal(it.Body)
		res, err := m.DB.ExecContext(ctx,
			`INSERT INTO announcements(id, level, category, published_at, expires_at, title, body, link, received_at)
			 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(id) DO UPDATE SET level = excluded.level, category = excluded.category,
			   expires_at = excluded.expires_at, title = excluded.title, body = excluded.body, link = excluded.link`,
			it.ID, it.Level, it.Category, it.PublishedAt, it.ExpiresAt, string(title), string(body), it.Link, now)
		if err != nil {
			m.Log.Warn("cloud: store announcement", "id", it.ID, "err", err)
			continue
		}
		// New critical messages and security notices go out on the
		// notification channels as well (deduplicated per message).
		if n, _ := res.RowsAffected(); n > 0 && (it.Level == "critical" || it.Category == "security") && m.isNew(ctx, it.ID) {
			m.Events.Publish(events.Event{
				Type: events.CloudAnnouncement, Level: levelOf(it.Level), DedupeKey: "announcement:" + it.ID,
				Params: map[string]any{"id": it.ID, "titles": titlesParam(it.Title), "link": it.Link},
			})
		}
	}
	// Messages withdrawn by the publisher, or no longer applicable, disappear.
	rows, err := m.DB.QueryContext(ctx, `SELECT id FROM announcements`)
	if err == nil {
		var gone []string
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil && !keep[id] {
				gone = append(gone, id)
			}
		}
		rows.Close()
		for _, id := range gone {
			_, _ = m.DB.ExecContext(ctx, `DELETE FROM announcements WHERE id = ?`, id)
		}
	}
	m.kvSet(ctx, "cloud.announcements.generated", strconv.FormatInt(feed.GeneratedAt, 10))
}

// isNew records that an announcement was announced on the bus once.
func (m *Module) isNew(ctx context.Context, id string) bool {
	key := "cloud.announced." + id
	if m.kvGet(ctx, key) != "" {
		return false
	}
	m.kvSet(ctx, key, "1")
	return true
}

func levelOf(s string) events.Level {
	if events.ValidLevel(s) {
		return events.Level(s)
	}
	return events.Info
}

func (m *Module) setStatus(status, reason string, latest *cloud.Release) {
	m.mu.Lock()
	m.status, m.reason, m.latest, m.checkedAt = status, reason, latest, time.Now().Unix()
	m.mu.Unlock()
}

func (m *Module) refreshReleases(ctx context.Context) {
	cur := m.current()
	cfg := m.Settings.Get()
	switch {
	case cur == nil:
		m.setStatus("unavailable", "dev_build", nil)
		return
	case !cfg.Enabled:
		m.setStatus("unavailable", "cloud_disabled", nil)
		return
	case m.Format == "":
		m.setStatus("unavailable", "unsupported_platform", nil)
		return
	}
	c, err := m.client()
	if err != nil {
		m.setStatus("unavailable", "verification_failed", nil)
		return
	}
	last, _ := strconv.ParseInt(m.kvGet(ctx, "cloud.releases.generated"), 10, 64)
	idx, err := c.FetchReleases(ctx, last)
	switch {
	case errors.Is(err, cloud.ErrNoTrustedKey):
		m.setStatus("unavailable", "no_trusted_key", nil)
		return
	case errors.Is(err, cloud.ErrSignature), errors.Is(err, cloud.ErrRollback):
		m.Log.Warn("cloud: release index rejected", "err", err)
		m.setStatus("unavailable", "verification_failed", nil)
		return
	case err != nil:
		m.Log.Warn("cloud: release index", "err", err)
		m.setStatus("unavailable", "fetch_failed", nil)
		return
	}
	m.kvSet(ctx, "cloud.releases.generated", strconv.FormatInt(idx.GeneratedAt, 10))
	rel, _ := idx.Latest(*cur, cfg.Channel, m.Format, m.Arch)
	if rel == nil {
		m.setStatus("up_to_date", "", nil)
		return
	}
	m.setStatus("available", "", rel)
}

type announcement struct {
	ID          string            `json:"id"`
	Level       string            `json:"level"`
	Category    string            `json:"category"`
	PublishedAt int64             `json:"publishedAt"`
	ExpiresAt   int64             `json:"expiresAt"`
	Title       map[string]string `json:"title"`
	Body        map[string]string `json:"body"`
	Link        string            `json:"link"`
	Read        bool              `json:"read"`
}

type announcementsResponse struct {
	Announcements []announcement `json:"announcements"`
	Unread        int            `json:"unread"`
}

func (m *Module) listAnnouncements(w http.ResponseWriter, r *http.Request) error {
	p, _ := api.PrincipalFrom(r.Context())
	rows, err := m.DB.QueryContext(r.Context(),
		`SELECT a.id, a.level, a.category, a.published_at, a.expires_at, a.title, a.body, a.link, r.read_at IS NOT NULL
		 FROM announcements a LEFT JOIN announcement_reads r ON r.announcement_id = a.id AND r.user_id = ?
		 WHERE a.expires_at = 0 OR a.expires_at >= ?
		 ORDER BY a.published_at DESC, a.id`, p.UserID, time.Now().Unix())
	if err != nil {
		return err
	}
	defer rows.Close()
	resp := announcementsResponse{Announcements: []announcement{}}
	for rows.Next() {
		var a announcement
		var title, body string
		if err := rows.Scan(&a.ID, &a.Level, &a.Category, &a.PublishedAt, &a.ExpiresAt, &title, &body, &a.Link, &a.Read); err != nil {
			return err
		}
		_ = json.Unmarshal([]byte(title), &a.Title)
		_ = json.Unmarshal([]byte(body), &a.Body)
		if !a.Read {
			resp.Unread++
		}
		resp.Announcements = append(resp.Announcements, a)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return api.WriteJSON(w, http.StatusOK, resp)
}

func (m *Module) readOne(w http.ResponseWriter, r *http.Request) error {
	p, _ := api.PrincipalFrom(r.Context())
	res, err := m.DB.ExecContext(r.Context(),
		`INSERT OR IGNORE INTO announcement_reads(user_id, announcement_id, read_at)
		 SELECT ?, id, ? FROM announcements WHERE id = ?`, p.UserID, time.Now().Unix(), r.PathValue("id"))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var exists int
		_ = m.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM announcements WHERE id = ?`, r.PathValue("id")).Scan(&exists)
		if exists == 0 {
			return api.ErrNotFound
		}
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (m *Module) readAll(w http.ResponseWriter, r *http.Request) error {
	p, _ := api.PrincipalFrom(r.Context())
	if _, err := m.DB.ExecContext(r.Context(),
		`INSERT OR IGNORE INTO announcement_reads(user_id, announcement_id, read_at) SELECT ?, id, ? FROM announcements`,
		p.UserID, time.Now().Unix()); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type latestInfo struct {
	Version     string            `json:"version"`
	PublishedAt int64             `json:"publishedAt"`
	Security    bool              `json:"security"`
	Notes       map[string]string `json:"notes"`
}

type updatesResponse struct {
	Current   string              `json:"current"`
	Channel   string              `json:"channel"`
	Status    string              `json:"status"`
	Reason    string              `json:"reason"`
	Latest    *latestInfo         `json:"latest"`
	CheckedAt int64               `json:"checkedAt"`
	Job       helperapi.UpdateJob `json:"job"`
}

func (m *Module) job(ctx context.Context) helperapi.UpdateJob {
	var job helperapi.UpdateJob
	if err := m.Helper.Call(ctx, helperapi.ActionUpdateStatus, helperapi.Caller{Source: "web"}, nil, &job); err != nil {
		return helperapi.UpdateJob{State: "idle"}
	}
	return job
}

func (m *Module) updates(r *http.Request) updatesResponse {
	ctx := r.Context()
	job := m.job(ctx)
	m.auditFinished(r, job)
	m.mu.Lock()
	resp := updatesResponse{Current: m.Version, Channel: m.Settings.Get().Channel, Status: m.status, Reason: m.reason, CheckedAt: m.checkedAt, Job: job}
	if m.latest != nil {
		resp.Latest = &latestInfo{Version: m.latest.Version, PublishedAt: m.latest.PublishedAt, Security: m.latest.Security, Notes: m.latest.Notes}
	}
	m.mu.Unlock()
	if resp.Status == "" {
		resp.Status, resp.Reason = "unavailable", "fetch_failed"
		if m.current() == nil {
			resp.Reason = "dev_build"
		}
	}
	return resp
}

// auditFinished records the outcome of an update once, after the fact: the
// runner works in its own unit and cannot write the panel's audit log.
func (m *Module) auditFinished(r *http.Request, job helperapi.UpdateJob) {
	if job.State != "succeeded" && job.State != "failed" {
		return
	}
	// Inserting the marker is the check: of concurrent requests, only the
	// one that inserts it writes the entry.
	key := "update.audited." + strconv.FormatInt(job.StartedAt, 10)
	res, err := m.DB.ExecContext(r.Context(), `INSERT INTO kv(key, value) VALUES(?, '1') ON CONFLICT(key) DO NOTHING`, key)
	if err != nil {
		m.Log.Warn("cloud: kv write", "key", key, "err", err)
		return
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return
	}
	who := modutil.Actor{Username: m.kvGet(r.Context(), "update.requested_by")}
	result := "ok"
	if job.State == "failed" {
		result = "update.install_failed"
	}
	modutil.Audit(r, m.DB, m.Log, who, "update.finish", result, map[string]any{"version": job.Version})
}

func (m *Module) getUpdates(w http.ResponseWriter, r *http.Request) error {
	return api.WriteJSON(w, http.StatusOK, m.updates(r))
}

func (m *Module) checkUpdates(w http.ResponseWriter, r *http.Request) error {
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	m.refreshReleases(ctx)
	return api.WriteJSON(w, http.StatusOK, m.updates(r))
}

type applyRequest struct {
	Version string `json:"version"`
}

type jobResponse struct {
	Job helperapi.UpdateJob `json:"job"`
}

func (m *Module) applyUpdate(w http.ResponseWriter, r *http.Request) error {
	var req applyRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	if req.Version == "" {
		return api.NewError(http.StatusBadRequest, "validation.invalid", map[string]any{"field": "version", "rule": "required"})
	}
	who := modutil.ActorOf(r)
	caller := helperapi.Caller{UserID: who.UserID, Username: who.Username, Remote: api.ClientIP(r), Source: "web"}
	var job helperapi.UpdateJob
	// The helper re-fetches and verifies the signed index itself; only the
	// version travels from here.
	err := m.Helper.Call(r.Context(), helperapi.ActionUpdate, caller, helperapi.UpdateRequest{Version: req.Version}, &job)
	if he, ok := helperapi.AsError(err); ok {
		modutil.Audit(r, m.DB, m.Log, who, "update.apply", he.Code, map[string]any{"version": req.Version})
		status := http.StatusBadGateway
		switch he.Code {
		case helperapi.CodeUpdateNotAvailable, helperapi.CodeUpdateInProgress, helperapi.CodePkgBusy:
			status = http.StatusConflict
		}
		return api.NewError(status, he.Code, he.Params)
	}
	if err != nil {
		return err
	}
	m.kvSet(r.Context(), "update.requested_by", who.Username)
	modutil.Audit(r, m.DB, m.Log, who, "update.apply", "ok", map[string]any{"version": req.Version})
	return api.WriteJSON(w, http.StatusAccepted, jobResponse{Job: job})
}

type cloudSettings struct {
	Enabled bool   `json:"enabled"`
	BaseURL string `json:"baseUrl"`
	Channel string `json:"channel"`
}

func (m *Module) getCloud(w http.ResponseWriter, _ *http.Request) error {
	c := m.Settings.Get()
	return api.WriteJSON(w, http.StatusOK, cloudSettings{Enabled: c.Enabled, BaseURL: c.BaseURL, Channel: c.Channel})
}

func (m *Module) setCloud(w http.ResponseWriter, r *http.Request) error {
	var req cloudSettings
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	if err := (helperapi.SetCloudRequest{Enabled: req.Enabled, BaseURL: req.BaseURL, Channel: req.Channel}).Validate(); err != nil {
		he, _ := helperapi.AsError(err)
		return api.NewError(http.StatusBadRequest, he.Code, he.Params)
	}
	who := modutil.ActorOf(r)
	caller := helperapi.Caller{UserID: who.UserID, Username: who.Username, Remote: api.ClientIP(r), Source: "web"}
	err := m.Helper.Call(r.Context(), helperapi.ActionSetCloud, caller,
		helperapi.SetCloudRequest{Enabled: req.Enabled, BaseURL: req.BaseURL, Channel: req.Channel}, nil)
	// The base URL decides whom the panel trusts for messages and updates:
	// it is in the audit entry, also of a refused change.
	params := map[string]any{"enabled": req.Enabled, "baseUrl": req.BaseURL, "channel": req.Channel}
	if he, ok := helperapi.AsError(err); ok {
		modutil.Audit(r, m.DB, m.Log, who, "settings.cloud", he.Code, params)
		return api.NewError(http.StatusBadGateway, he.Code, he.Params)
	}
	if err != nil {
		modutil.Audit(r, m.DB, m.Log, who, "settings.cloud", api.ErrInternal.Code, params)
		return err
	}
	m.Settings.set(req.Enabled, req.BaseURL, req.Channel)
	modutil.Audit(r, m.DB, m.Log, who, "settings.cloud", "ok", params)
	go m.Refresh(context.WithoutCancel(r.Context()))
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// titlesParam turns localized titles into an event parameter; the notifier
// picks the channel's language.
func titlesParam(t map[string]string) map[string]any {
	out := make(map[string]any, len(t))
	for k, v := range t {
		out[k] = v
	}
	return out
}
