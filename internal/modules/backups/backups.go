// SPDX-License-Identifier: AGPL-3.0-only

// Package backups manages backup storage (S3-compatible buckets) and
// backup plans: what to back up, where to, whom to encrypt for (age
// recipients; the panel never keeps a private key) and how many to keep.
// The root helper keeps the applied plans with their credentials and runs
// them, from a job here or from a scheduled task (cron module).
package backups

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/authz"
	"github.com/Kobayashi-classmate/Cairn/internal/events"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/jobs"
	"github.com/Kobayashi-classmate/Cairn/internal/module"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/modutil"
	"github.com/Kobayashi-classmate/Cairn/internal/secrets"
)

// Permissions of this module.
const (
	Read   authz.Permission = "backups.read"
	Manage authz.Permission = "backups.manage"
)

const (
	sealContext     = "backup-storage:"
	checkEvery      = 5 * time.Minute
	codeStorageUsed = "backup.storage_in_use"
	codePlanUsed    = "backup.plan_in_use"
	codeNotFound    = "backup.not_found"
)

// Module implements module.Module.
type Module struct {
	Jobs   *jobs.Manager
	Helper jobs.HelperClient
	DB     *sql.DB
	Box    *secrets.Box
	Events *events.Bus
	Log    *slog.Logger
	// DataDir holds the local copies (backups/plans/<plan>/).
	DataDir string
}

// ID implements module.Module.
func (m *Module) ID() string { return "backups" }

// Info implements module.Describer.
func (m *Module) Info() module.Info {
	return module.Info{
		Optional: true,
		Permissions: []module.Permission{
			{Name: Read, Roles: []authz.Role{authz.Operator, authz.Auditor}},
			{Name: Manage, Roles: []authz.Role{authz.Operator}},
		},
		Nav: []module.NavItem{
			{Key: "backups", LabelKey: "common.nav.backups", Icon: "archive", Route: "backups", Permission: Read, Group: module.GroupOps, Order: 22},
		},
	}
}

// Routes implements module.Module.
func (m *Module) Routes(r *api.Router) {
	doc := func(s string) api.RouteOption { return api.Doc("backups", s) }
	job := api.Returns(202, jobResponse{})
	r.Handle("GET /api/v1/backups/storages", m.storages, api.Require(Read), doc("Backup storage (never its secret key)"), api.Returns(200, storagesResponse{}))
	r.Handle("POST /api/v1/backups/storages", m.createStorage, api.Require(Manage), api.StepUp(), api.Body(storageRequest{}),
		doc("Add an S3-compatible bucket"), api.Returns(201, storageView{}))
	r.Handle("POST /api/v1/backups/storages/test", m.testNew, api.Require(Manage), api.Body(storageRequest{}),
		doc("Check a bucket's settings before saving them"), api.Returns(204, nil))
	r.Handle("PUT /api/v1/backups/storages/{id}", m.updateStorage, api.Require(Manage), api.StepUp(), api.Body(storageRequest{}),
		doc("Change a bucket; an empty secret key keeps it; plans using it are applied again"), api.Returns(200, storageView{}))
	r.Handle("DELETE /api/v1/backups/storages/{id}", m.deleteStorage, api.Require(Manage), api.StepUp(), doc("Delete a bucket no plan uses"), api.Returns(204, nil))
	r.Handle("POST /api/v1/backups/storages/{id}/test", m.testStorage, api.Require(Manage), api.NoBody(), doc("Check a saved bucket"), api.Returns(204, nil))

	r.Handle("GET /api/v1/backups/plans", m.plans, api.Require(Read), doc("Backup plans with their last run"), api.Returns(200, plansResponse{}))
	r.Handle("POST /api/v1/backups/plans", m.createPlan, api.Require(Manage), api.Body(planRequest{}), doc("Create a backup plan"), api.Returns(201, planView{}))
	r.Handle("PUT /api/v1/backups/plans/{id}", m.updatePlan, api.Require(Manage), api.Body(planRequest{}), doc("Change a backup plan"), api.Returns(200, planView{}))
	r.Handle("DELETE /api/v1/backups/plans/{id}", m.deletePlan, api.Require(Manage), api.StepUp(),
		doc("Delete a plan no task runs; ?files=true deletes its backups too"), api.Returns(204, nil))
	r.Handle("POST /api/v1/backups/plans/{id}/run", m.runPlan, api.Require(Manage), api.NoBody(), doc("Run a plan now (starts a job)"), job)
	r.Handle("GET /api/v1/backups/plans/{id}/runs", m.runs, api.Require(Read), doc("A plan's runs, newest first"), api.Returns(200, helperapi.BackupRuns{}))
	r.Handle("GET /api/v1/backups/plans/{id}/files", m.files, api.Require(Read), doc("A plan's backups, here and in the bucket"), api.Returns(200, helperapi.BackupList{}))
	r.Handle("GET /api/v1/backups/plans/{id}/files/{name}", m.download, api.Require(Manage), api.StepUp(),
		doc("Download a backup: the local copy, or a redirect to a short-lived link to the bucket's"), api.ReturnsFile(200, "application/octet-stream"))
	r.Handle("POST /api/v1/backups/plans/{id}/delete", m.deleteFiles, api.Require(Manage), api.StepUp(), api.Body(filesRequest{}),
		doc("Delete backups, here and with remote=true in the bucket (starts a job)"), job)
	r.Handle("POST /api/v1/backups/plans/{id}/restore", m.restore, api.Require(Manage), api.StepUp(), api.Body(restoreRequest{}),
		doc("Restore a backup (starts a job): files into a directory, a database into a dump of the databases page"), job)
}

// views and requests ----------------------------------------------------------

type storageRequest struct {
	Name string `json:"name"`
	// ID, when testing changed settings without a new secret key, takes
	// the saved bucket's secret key.
	ID string `json:"id,omitempty"`
	helperapi.S3Storage
}

type storageView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	helperapi.S3Storage
	SecretSet bool  `json:"secretSet"`
	CreatedAt int64 `json:"createdAt"`
	UpdatedAt int64 `json:"updatedAt"`
}

type storagesResponse struct {
	Storages []storageView `json:"storages"`
}

type planRequest struct {
	Name       string                   `json:"name"`
	Targets    []helperapi.BackupTarget `json:"targets"`
	StorageID  string                   `json:"storageId,omitempty"`
	KeepLocal  bool                     `json:"keepLocal"`
	Retention  *helperapi.Retention     `json:"retention,omitempty"`
	Recipients []string                 `json:"recipients,omitempty"`
}

type planView struct {
	ID         string                   `json:"id"`
	Name       string                   `json:"name"`
	Targets    []helperapi.BackupTarget `json:"targets"`
	StorageID  string                   `json:"storageId,omitempty"`
	KeepLocal  bool                     `json:"keepLocal"`
	Retention  helperapi.Retention      `json:"retention"`
	Recipients []string                 `json:"recipients"`
	LastRun    *helperapi.BackupRun     `json:"lastRun,omitempty"`
	// Tasks are the scheduled tasks running this plan.
	Tasks     []string `json:"tasks"`
	CreatedAt int64    `json:"createdAt"`
	UpdatedAt int64    `json:"updatedAt"`
}

type plansResponse struct {
	Plans []planView `json:"plans"`
}

type filesRequest struct {
	Names  []string `json:"names"`
	Remote bool     `json:"remote,omitempty"`
}

type restoreRequest struct {
	Name      string            `json:"name"`
	Identity  string            `json:"identity,omitempty"`
	Dest      helperapi.FileRef `json:"dest"`
	Overwrite bool              `json:"overwrite,omitempty"`
}

type jobResponse struct {
	Job jobs.Record `json:"job"`
}

func callerOf(r *http.Request) helperapi.Caller {
	who := modutil.ActorOf(r)
	return helperapi.Caller{UserID: who.UserID, Username: who.Username, Remote: api.ClientIP(r), Source: who.Source}
}

func badRequest(err error) error {
	var he *helperapi.Error
	if errors.As(err, &he) {
		return api.NewError(http.StatusBadRequest, he.Code, he.Params)
	}
	return err
}

func helperError(err error) error {
	if he, ok := helperapi.AsError(err); ok {
		status := http.StatusConflict
		switch he.Code {
		case helperapi.CodeBackupPlanNotFound, helperapi.CodeBackupNotFound:
			status = http.StatusNotFound
		case helperapi.CodeBackupStorage:
			status = http.StatusBadGateway
		}
		return api.NewError(status, he.Code, he.Params)
	}
	return err
}

func (m *Module) call(r *http.Request, action string, req, resp any) error {
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	return helperError(m.Helper.Call(ctx, action, callerOf(r), req, resp))
}

func newID(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)[:15]
}

func checkName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) > 64 || strings.ContainsAny(s, "\x00\r\n") {
		return "", api.NewError(http.StatusBadRequest, "validation.invalid", map[string]any{"field": "name", "rule": "max"})
	}
	return s, nil
}

// Storage -------------------------------------------------------------------

func (m *Module) storageRow(ctx context.Context, id string) (storageView, string, error) {
	var v storageView
	var config, sealed string
	err := m.DB.QueryRowContext(ctx, `SELECT id, name, config, secret, created_at, updated_at FROM backup_storages WHERE id = ?`, id).
		Scan(&v.ID, &v.Name, &config, &sealed, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return v, "", api.NewError(http.StatusNotFound, "backup.storage_not_found", map[string]any{"id": id})
	}
	if err != nil {
		return v, "", err
	}
	if err := json.Unmarshal([]byte(config), &v.S3Storage); err != nil {
		return v, "", err
	}
	v.SecretSet = sealed != ""
	return v, sealed, nil
}

// storage is a bucket with its secret key, for the helper.
func (m *Module) storage(ctx context.Context, id string) (*helperapi.S3Storage, error) {
	v, sealed, err := m.storageRow(ctx, id)
	if err != nil {
		return nil, err
	}
	secret, err := m.Box.Open(sealed, sealContext+id)
	if err != nil {
		return nil, err
	}
	s := v.S3Storage
	s.SecretKey = string(secret)
	return &s, nil
}

func (m *Module) storages(w http.ResponseWriter, r *http.Request) error {
	rows, err := m.DB.QueryContext(r.Context(), `SELECT id FROM backup_storages ORDER BY name, id`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	resp := storagesResponse{Storages: []storageView{}}
	for _, id := range ids {
		v, _, err := m.storageRow(r.Context(), id)
		if err != nil {
			return err
		}
		resp.Storages = append(resp.Storages, v)
	}
	return api.WriteJSON(w, http.StatusOK, resp)
}

func (m *Module) decodeStorage(r *http.Request) (string, helperapi.S3Storage, error) {
	var req storageRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return "", helperapi.S3Storage{}, err
	}
	name, err := checkName(req.Name)
	if err != nil {
		return "", helperapi.S3Storage{}, err
	}
	s := req.S3Storage
	s.Endpoint, s.Prefix = strings.TrimSpace(s.Endpoint), strings.Trim(s.Prefix, "/")
	if err := s.Validate(); err != nil {
		return "", s, badRequest(err)
	}
	return name, s, nil
}

func (m *Module) saveStorage(ctx context.Context, id, name string, s helperapi.S3Storage) error {
	secret := s.SecretKey
	s.SecretKey = ""
	config, _ := json.Marshal(s) //nolint:gosec // G117: the access key is an identifier; the secret key was taken out above.
	now := time.Now().Unix()
	if secret == "" {
		_, err := m.DB.ExecContext(ctx, `UPDATE backup_storages SET name = ?, config = ?, updated_at = ? WHERE id = ?`, name, string(config), now, id)
		return err
	}
	sealed, err := m.Box.Seal([]byte(secret), sealContext+id)
	if err != nil {
		return err
	}
	_, err = m.DB.ExecContext(ctx, `INSERT INTO backup_storages (id, name, config, secret, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET name = excluded.name, config = excluded.config, secret = excluded.secret, updated_at = excluded.updated_at`,
		id, name, string(config), sealed, now, now)
	return err
}

func (m *Module) createStorage(w http.ResponseWriter, r *http.Request) error {
	name, s, err := m.decodeStorage(r)
	if err != nil {
		return err
	}
	if s.SecretKey == "" {
		return api.NewError(http.StatusBadRequest, "validation.invalid", map[string]any{"field": "secretKey", "rule": "required"})
	}
	id := newID("s")
	if err := m.saveStorage(r.Context(), id, name, s); err != nil {
		return err
	}
	modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "backup.storage_create", "ok", map[string]any{"id": id, "name": name, "endpoint": s.Endpoint, "bucket": s.Bucket})
	v, _, err := m.storageRow(r.Context(), id)
	if err != nil {
		return err
	}
	return api.WriteJSON(w, http.StatusCreated, v)
}

func (m *Module) updateStorage(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	if _, _, err := m.storageRow(r.Context(), id); err != nil {
		return err
	}
	name, s, err := m.decodeStorage(r)
	if err != nil {
		return err
	}
	if err := m.saveStorage(r.Context(), id, name, s); err != nil {
		return err
	}
	// The plans using it get the new settings.
	rows, err := m.DB.QueryContext(r.Context(), `SELECT id FROM backup_plans WHERE storage_id = ?`, id)
	if err != nil {
		return err
	}
	var plans []string
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil {
			plans = append(plans, p)
		}
	}
	_ = rows.Close()
	for _, p := range plans {
		if err := m.apply(r, p); err != nil {
			return err
		}
	}
	modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "backup.storage_update", "ok", map[string]any{"id": id, "name": name, "secretChanged": s.SecretKey != ""})
	v, _, err := m.storageRow(r.Context(), id)
	if err != nil {
		return err
	}
	return api.WriteJSON(w, http.StatusOK, v)
}

func (m *Module) deleteStorage(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	v, _, err := m.storageRow(r.Context(), id)
	if err != nil {
		return err
	}
	var n int
	_ = m.DB.QueryRowContext(r.Context(), `SELECT count(*) FROM backup_plans WHERE storage_id = ?`, id).Scan(&n)
	if n > 0 {
		return api.NewError(http.StatusConflict, codeStorageUsed, map[string]any{"plans": n})
	}
	if _, err := m.DB.ExecContext(r.Context(), `DELETE FROM backup_storages WHERE id = ?`, id); err != nil {
		return err
	}
	modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "backup.storage_delete", "ok", map[string]any{"id": id, "name": v.Name})
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// testNew checks settings before they are saved (no name needed); with
// the id of a saved bucket and no secret key, that bucket's key is used.
func (m *Module) testNew(w http.ResponseWriter, r *http.Request) error {
	var req storageRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	s := req.S3Storage
	s.Endpoint, s.Prefix = strings.TrimSpace(s.Endpoint), strings.Trim(s.Prefix, "/")
	if s.SecretKey == "" && req.ID != "" {
		saved, err := m.storage(r.Context(), req.ID)
		if err != nil {
			return err
		}
		s.SecretKey = saved.SecretKey
	}
	if err := s.Validate(); err != nil {
		return badRequest(err)
	}
	if err := m.call(r, helperapi.ActionBackupTest, helperapi.StorageTestRequest{Storage: s}, nil); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (m *Module) testStorage(w http.ResponseWriter, r *http.Request) error {
	s, err := m.storage(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	if err := m.call(r, helperapi.ActionBackupTest, helperapi.StorageTestRequest{Storage: *s}, nil); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// Plans ---------------------------------------------------------------------

type planRow struct {
	name, storageID  string
	spec             helperapi.BackupPlan
	notified         int64
	created, updated int64
}

func (m *Module) planRow(ctx context.Context, id string) (planRow, error) {
	var p planRow
	var spec string
	err := m.DB.QueryRowContext(ctx, `SELECT name, spec, storage_id, notified_run, created_at, updated_at FROM backup_plans WHERE id = ?`, id).
		Scan(&p.name, &spec, &p.storageID, &p.notified, &p.created, &p.updated)
	if errors.Is(err, sql.ErrNoRows) {
		return p, api.NewError(http.StatusNotFound, helperapi.CodeBackupPlanNotFound, map[string]any{"plan": id})
	}
	if err != nil {
		return p, err
	}
	return p, json.Unmarshal([]byte(spec), &p.spec)
}

// apply hands a plan, with its bucket's credentials, to the helper.
func (m *Module) apply(r *http.Request, id string) error {
	p, err := m.planRow(r.Context(), id)
	if err != nil {
		return err
	}
	spec := p.spec
	if p.storageID != "" {
		if spec.Storage, err = m.storage(r.Context(), p.storageID); err != nil {
			return err
		}
	}
	return m.call(r, helperapi.ActionBackupPlanApply, spec, nil)
}

func (m *Module) decodePlan(r *http.Request, id string) (string, helperapi.BackupPlan, string, error) {
	var req planRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return "", helperapi.BackupPlan{}, "", err
	}
	name, err := checkName(req.Name)
	if err != nil {
		return "", helperapi.BackupPlan{}, "", err
	}
	ret := helperapi.DefaultRetention
	if req.Retention != nil {
		ret = *req.Retention
	}
	spec := helperapi.BackupPlan{ID: id, Targets: req.Targets, KeepLocal: req.KeepLocal || req.StorageID == "", Retention: ret, Recipients: req.Recipients}
	if spec.Recipients == nil {
		spec.Recipients = []string{}
	}
	if err := spec.Validate(); err != nil {
		return "", spec, "", badRequest(err)
	}
	if req.StorageID != "" {
		if _, _, err := m.storageRow(r.Context(), req.StorageID); err != nil {
			return "", spec, "", api.NewError(http.StatusBadRequest, "backup.storage_not_found", map[string]any{"id": req.StorageID})
		}
	}
	return name, spec, req.StorageID, nil
}

func (m *Module) savePlan(ctx context.Context, name string, spec helperapi.BackupPlan, storageID string) error {
	data, _ := json.Marshal(spec)
	now := time.Now().Unix()
	_, err := m.DB.ExecContext(ctx, `INSERT INTO backup_plans (id, name, spec, storage_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET name = excluded.name, spec = excluded.spec, storage_id = excluded.storage_id, updated_at = excluded.updated_at`,
		spec.ID, name, string(data), storageID, now, now)
	return err
}

func (m *Module) createPlan(w http.ResponseWriter, r *http.Request) error {
	id := newID("p")
	name, spec, storageID, err := m.decodePlan(r, id)
	if err != nil {
		return err
	}
	if err := m.savePlan(r.Context(), name, spec, storageID); err != nil {
		return err
	}
	if err := m.apply(r, id); err != nil {
		_, _ = m.DB.ExecContext(context.WithoutCancel(r.Context()), `DELETE FROM backup_plans WHERE id = ?`, id)
		return err
	}
	modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "backup.plan_create", "ok", map[string]any{"plan": id, "name": name, "targets": len(spec.Targets),
		"storage": storageID, "encrypted": len(spec.Recipients) > 0})
	return m.writePlan(w, r, id, http.StatusCreated)
}

func (m *Module) updatePlan(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	old, err := m.planRow(r.Context(), id)
	if err != nil {
		return err
	}
	name, spec, storageID, err := m.decodePlan(r, id)
	if err != nil {
		return err
	}
	if err := m.savePlan(r.Context(), name, spec, storageID); err != nil {
		return err
	}
	if err := m.apply(r, id); err != nil {
		_ = m.savePlan(context.WithoutCancel(r.Context()), old.name, old.spec, old.storageID)
		return err
	}
	modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "backup.plan_update", "ok", map[string]any{"plan": id, "name": name, "targets": len(spec.Targets),
		"storage": storageID, "encrypted": len(spec.Recipients) > 0})
	return m.writePlan(w, r, id, http.StatusOK)
}

// tasksOf lists the scheduled tasks running a plan.
func (m *Module) tasksOf(ctx context.Context, plan string) []string {
	rows, err := m.DB.QueryContext(ctx, `SELECT id FROM cron_tasks WHERE json_extract(spec, '$.type') = 'backup' AND json_extract(spec, '$.plan') = ?`, plan)
	out := []string{}
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			out = append(out, id)
		}
	}
	return out
}

func (m *Module) view(ctx context.Context, r *http.Request, id string, withRun bool) (planView, error) {
	p, err := m.planRow(ctx, id)
	if err != nil {
		return planView{}, err
	}
	v := planView{ID: id, Name: p.name, Targets: p.spec.Targets, StorageID: p.storageID, KeepLocal: p.spec.KeepLocal, Retention: p.spec.Retention,
		Recipients: p.spec.Recipients, Tasks: m.tasksOf(ctx, id), CreatedAt: p.created, UpdatedAt: p.updated}
	if v.Recipients == nil {
		v.Recipients = []string{}
	}
	if withRun {
		var runs helperapi.BackupRuns
		if m.call(r, helperapi.ActionBackupRuns, helperapi.PlanRef{Plan: id}, &runs) == nil && len(runs.Runs) > 0 {
			v.LastRun = &runs.Runs[0]
		}
	}
	return v, nil
}

func (m *Module) writePlan(w http.ResponseWriter, r *http.Request, id string, status int) error {
	v, err := m.view(r.Context(), r, id, false)
	if err != nil {
		return err
	}
	return api.WriteJSON(w, status, v)
}

func (m *Module) plans(w http.ResponseWriter, r *http.Request) error {
	rows, err := m.DB.QueryContext(r.Context(), `SELECT id FROM backup_plans ORDER BY name, id`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	_ = rows.Close()
	resp := plansResponse{Plans: []planView{}}
	for _, id := range ids {
		v, err := m.view(r.Context(), r, id, true)
		if err != nil {
			return err
		}
		resp.Plans = append(resp.Plans, v)
	}
	return api.WriteJSON(w, http.StatusOK, resp)
}

func (m *Module) deletePlan(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	p, err := m.planRow(r.Context(), id)
	if err != nil {
		return err
	}
	if tasks := m.tasksOf(r.Context(), id); len(tasks) > 0 {
		return api.NewError(http.StatusConflict, codePlanUsed, map[string]any{"tasks": len(tasks)})
	}
	files := r.URL.Query().Get("files") == "true"
	err = m.call(r, helperapi.ActionBackupPlanRemove, helperapi.PlanRemoveRequest{Plan: id, Files: files}, nil)
	// A plan the helper never kept can go.
	var ae *api.Error
	if errors.As(err, &ae) && ae.Code == helperapi.CodeBackupPlanNotFound {
		err = nil
	}
	if err != nil {
		return err
	}
	if _, err := m.DB.ExecContext(r.Context(), `DELETE FROM backup_plans WHERE id = ?`, id); err != nil {
		return err
	}
	modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "backup.plan_delete", "ok", map[string]any{"plan": id, "name": p.name, "files": files})
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (m *Module) start(r *http.Request, kind string, params map[string]any, fn jobs.Func) (jobs.Record, error) {
	who := modutil.ActorOf(r)
	job, err := m.Jobs.Start(r.Context(), jobs.Spec{
		Kind: kind, Params: params, Permission: Read, ManagePermission: Manage, Locks: []string{"backup:" + params["plan"].(string)},
		Actor: jobs.Actor{UserID: who.UserID, Username: who.Username, Source: who.Source, Remote: api.ClientIP(r)},
	}, fn)
	if err != nil {
		return job, err
	}
	audit := map[string]any{"job": job.ID}
	for k, v := range params {
		audit[k] = v
	}
	modutil.Audit(r, m.DB, m.Log, who, kind, "ok", audit)
	return job, nil
}

func (m *Module) runPlan(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	p, err := m.planRow(r.Context(), id)
	if err != nil {
		return err
	}
	job, err := m.start(r, "backup.run", map[string]any{"plan": id, "name": p.name}, func(_ context.Context, j *jobs.Job) error {
		var run helperapi.BackupRun
		if err := j.RunOp(helperapi.ActionBackupRun, helperapi.PlanRef{Plan: id}, &run); err != nil {
			return err
		}
		j.Logf("%d backups, %d bytes; %d old ones removed.\n", len(run.Files), run.Size, run.Deleted)
		return nil
	})
	if err != nil {
		return err
	}
	return api.WriteJSON(w, http.StatusAccepted, jobResponse{Job: job})
}

func (m *Module) runs(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	if _, err := m.planRow(r.Context(), id); err != nil {
		return err
	}
	var resp helperapi.BackupRuns
	if err := m.call(r, helperapi.ActionBackupRuns, helperapi.PlanRef{Plan: id}, &resp); err != nil {
		return err
	}
	if resp.Runs == nil {
		resp.Runs = []helperapi.BackupRun{}
	}
	return api.WriteJSON(w, http.StatusOK, resp)
}

func (m *Module) files(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	if _, err := m.planRow(r.Context(), id); err != nil {
		return err
	}
	var resp helperapi.BackupList
	if err := m.call(r, helperapi.ActionBackupList, helperapi.PlanRef{Plan: id}, &resp); err != nil {
		return err
	}
	if resp.Files == nil {
		resp.Files = []helperapi.BackupFile{}
	}
	return api.WriteJSON(w, http.StatusOK, resp)
}

// download serves the local copy, which belongs to the panel, or sends the
// browser to a short-lived link to the bucket's copy.
func (m *Module) download(w http.ResponseWriter, r *http.Request) error {
	id, name := r.PathValue("id"), r.PathValue("name")
	p, err := m.planRow(r.Context(), id)
	if err != nil {
		return err
	}
	if !helperapi.ValidBackupName(name) {
		return api.NewError(http.StatusBadRequest, "validation.invalid", map[string]any{"field": "name", "rule": "backup_name"})
	}
	who := modutil.ActorOf(r)
	if root, err := os.OpenRoot(m.DataDir); err == nil {
		defer func() { _ = root.Close() }()
		rel := path.Join("backups/plans", id, name)
		if fi, err := root.Lstat(rel); err == nil && fi.Mode().IsRegular() {
			f, err := root.Open(rel)
			if err != nil {
				return err
			}
			defer func() { _ = f.Close() }()
			modutil.Audit(r, m.DB, m.Log, who, "backup.download", "ok", map[string]any{"plan": id, "name": p.name, "file": name})
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
			http.ServeContent(w, r, "", fi.ModTime(), f)
			return nil
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if p.storageID == "" {
		return api.NewError(http.StatusNotFound, codeNotFound, map[string]any{"name": name})
	}
	var link helperapi.PresignResponse
	if err := m.call(r, helperapi.ActionBackupPresign, helperapi.BackupFilesRequest{Plan: id, Names: []string{name}}, &link); err != nil {
		return err
	}
	modutil.Audit(r, m.DB, m.Log, who, "backup.download", "ok", map[string]any{"plan": id, "name": p.name, "file": name, "remote": true})
	http.Redirect(w, r, link.URL, http.StatusFound)
	return nil
}

func (m *Module) deleteFiles(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	p, err := m.planRow(r.Context(), id)
	if err != nil {
		return err
	}
	var req filesRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	q := helperapi.BackupFilesRequest{Plan: id, Names: req.Names, Remote: req.Remote}
	if err := q.Validate(); err != nil {
		return badRequest(err)
	}
	job, err := m.start(r, "backup.delete", map[string]any{"plan": id, "name": p.name, "count": len(req.Names), "remote": req.Remote},
		func(_ context.Context, j *jobs.Job) error { return j.RunOp(helperapi.ActionBackupDelete, q, nil) })
	if err != nil {
		return err
	}
	return api.WriteJSON(w, http.StatusAccepted, jobResponse{Job: job})
}

func (m *Module) restore(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	p, err := m.planRow(r.Context(), id)
	if err != nil {
		return err
	}
	var req restoreRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	q := helperapi.BackupRestoreRequest{Plan: id, Name: req.Name, Identity: strings.TrimSpace(req.Identity), Dest: req.Dest, Overwrite: req.Overwrite}
	if err := q.Validate(); err != nil {
		return badRequest(err)
	}
	// The private key goes to the helper for this job only: it is in no
	// parameter, log or audit entry.
	params := map[string]any{"plan": id, "name": p.name, "file": req.Name}
	if engine, database, ok := helperapi.DatabaseOfBackup(req.Name); ok {
		// Where the dump will be: the databases page of this engine.
		params["engine"], params["database"] = engine, database
	} else {
		params["root"], params["dest"] = req.Dest.Root, req.Dest.Path
	}
	job, err := m.start(r, "backup.restore", params, func(_ context.Context, j *jobs.Job) error {
		var res helperapi.BackupRestoreResult
		if err := j.RunOp(helperapi.ActionBackupRestore, q, &res); err != nil {
			return err
		}
		if res.Dump != "" {
			j.Logf("The database backup is now the dump %s of %s: restore it from the databases page, as a user of the database.\n", res.Dump, res.Engine)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return api.WriteJSON(w, http.StatusAccepted, jobResponse{Job: job})
}

// Run reports failed runs: scheduled ones finish without the panel.
func (m *Module) Run(ctx context.Context) {
	t := time.NewTicker(checkEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.Check(ctx)
		}
	}
}

// Check publishes backup.failed for each plan whose last run failed and
// was not reported yet.
func (m *Module) Check(ctx context.Context) {
	rows, err := m.DB.QueryContext(ctx, `SELECT id, name, notified_run FROM backup_plans`)
	if err != nil {
		return
	}
	type plan struct {
		id, name string
		notified int64
	}
	var plans []plan
	for rows.Next() {
		var p plan
		if rows.Scan(&p.id, &p.name, &p.notified) == nil {
			plans = append(plans, p)
		}
	}
	_ = rows.Close()
	for _, p := range plans {
		var runs helperapi.BackupRuns
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := m.Helper.Call(cctx, helperapi.ActionBackupRuns, helperapi.Caller{Source: "system", Username: "cairn"}, helperapi.PlanRef{Plan: p.id}, &runs)
		cancel()
		if err != nil || len(runs.Runs) == 0 {
			continue
		}
		last := runs.Runs[0]
		if last.OK || last.Started <= p.notified {
			continue
		}
		if _, err := m.DB.ExecContext(ctx, `UPDATE backup_plans SET notified_run = ? WHERE id = ?`, last.Started, p.id); err != nil {
			continue
		}
		code := ""
		if last.Error != nil {
			code = last.Error.Code
		}
		m.Events.Publish(events.Event{Type: events.BackupFailed, Level: events.Warning, DedupeKey: "backup-failed:" + p.id + ":" + time.Unix(last.Started, 0).UTC().Format(time.RFC3339),
			Params: map[string]any{"plan": p.id, "name": p.name, "code": code}})
	}
}

// overview and sites ---------------------------------------------------------

type planRuns struct {
	id      string
	targets []helperapi.BackupTarget
	runs    []helperapi.BackupRun // newest first
}

// allRuns lists every plan with its runs; the runs of a plan the helper
// cannot report stay empty.
func (m *Module) allRuns(r *http.Request) ([]planRuns, error) {
	rows, err := m.DB.QueryContext(r.Context(), `SELECT id, spec FROM backup_plans ORDER BY id`)
	if err != nil {
		return nil, err
	}
	var plans []planRuns
	for rows.Next() {
		var p planRuns
		var spec string
		if err := rows.Scan(&p.id, &spec); err != nil {
			rows.Close()
			return nil, err
		}
		var s helperapi.BackupPlan
		if json.Unmarshal([]byte(spec), &s) == nil {
			p.targets = s.Targets
		}
		plans = append(plans, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range plans {
		var runs helperapi.BackupRuns
		if err := m.call(r, helperapi.ActionBackupRuns, helperapi.PlanRef{Plan: plans[i].id}, &runs); err == nil {
			plans[i].runs = runs.Runs
		} else if r.Context().Err() != nil {
			return nil, r.Context().Err()
		}
	}
	return plans, nil
}

func runFailed(run helperapi.BackupRun) bool {
	return run.Finished > 0 && !run.OK
}

// Summarize implements module.Summarizer: plans whose last run failed.
func (m *Module) Summarize(r *http.Request, can func(authz.Permission) bool) (module.Summary, error) {
	if !can(Read) {
		return module.Summary{}, nil
	}
	plans, err := m.allRuns(r)
	if err != nil {
		return module.Summary{}, err
	}
	failed := 0
	for _, p := range plans {
		if len(p.runs) > 0 && runFailed(p.runs[0]) {
			failed++
		}
	}
	if failed == 0 {
		return module.Summary{}, nil
	}
	return module.Summary{Notices: []module.Check{{Code: "backups.failed", Status: module.StatusBad, Route: "backups",
		Params: map[string]any{"count": failed}}}}, nil
}

// SiteBackups implements module.BackupLookup.
func (m *Module) SiteBackups(r *http.Request) (map[string]module.SiteBackup, error) {
	plans, err := m.allRuns(r)
	if err != nil {
		return nil, err
	}
	out := map[string]module.SiteBackup{}
	lastFailed := map[string]int64{}
	for _, p := range plans {
		var okAt, failedAt int64
		for _, run := range p.runs {
			switch {
			case run.OK && run.Finished > okAt:
				okAt = run.Finished
			case runFailed(run) && run.Started > failedAt:
				failedAt = run.Started
			}
		}
		for _, t := range p.targets {
			if t.Kind != helperapi.BackupSite {
				continue
			}
			b := out[t.Site]
			b.Plans = append(b.Plans, p.id)
			b.LastOKAt = max(b.LastOKAt, okAt)
			lastFailed[t.Site] = max(lastFailed[t.Site], failedAt)
			out[t.Site] = b
		}
	}
	for site, b := range out {
		if f := lastFailed[site]; f > b.LastOKAt {
			b.LastFailedAt = f
			out[site] = b
		}
	}
	return out, nil
}
