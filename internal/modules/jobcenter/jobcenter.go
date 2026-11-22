// SPDX-License-Identifier: AGPL-3.0-only

// Package jobcenter serves background jobs: the list, one job with its log
// (followed by long polling), and cancellation. Every job carries the
// permission needed to see it and the one needed to cancel it; users only
// ever see jobs they are allowed to see.
package jobcenter

import (
	"database/sql"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/authz"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/jobs"
	"github.com/Kobayashi-classmate/Cairn/internal/module"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/modutil"
)

// maxWait bounds a long-poll request.
const maxWait = 25 * time.Second

// Module implements module.Module.
type Module struct {
	Jobs *jobs.Manager
	DB   *sql.DB
	Log  *slog.Logger
}

// ID implements module.Module.
func (m *Module) ID() string { return "jobs" }

// Info implements module.Describer.
func (m *Module) Info() module.Info {
	return module.Info{Nav: []module.NavItem{
		{Key: "jobs", LabelKey: "common.nav.jobs", Icon: "list", Route: "jobs", Group: module.GroupSystem, Order: 70},
	}}
}

// Routes implements module.Module. The routes need only Self: which jobs a
// user may see or cancel is decided per job.
func (m *Module) Routes(r *api.Router) {
	self := api.Require(authz.Self)
	doc := func(s string) api.RouteOption { return api.Doc("jobs", s) }
	r.Handle("GET /api/v1/jobs", m.list, self,
		doc("Background jobs visible to the caller, newest first (query: limit, before = nextBefore of the previous page, state (or \"active\": queued or running), kind or kind prefix such as \"software.\")"),
		api.Returns(200, listResponse{}))
	r.Handle("GET /api/v1/jobs/{id}", m.get, self, doc("One job"), api.Returns(200, jobResponse{}))
	r.Handle("GET /api/v1/jobs/{id}/log", m.log, self,
		doc("Job state and its log from offset; waits up to wait seconds (max 25) for news (query: offset, wait)"),
		api.Returns(200, logResponse{}))
	r.Handle("POST /api/v1/jobs/{id}/cancel", m.cancel, self, doc("Cancel a job (installations in progress cannot be canceled)"),
		api.NoBody(), api.Returns(202, jobResponse{}))
}

// jobView is a job as the caller sees it.
type jobView struct {
	jobs.Record
	// CanCancel reports whether the caller holds the job's manage
	// permission; Cancelable says whether the job can be canceled now.
	CanCancel bool `json:"canCancel"`
}

func viewOf(p *api.Principal, j jobs.Record) jobView {
	return jobView{Record: j, CanCancel: p.Can(j.ManagePermission)}
}

type listResponse struct {
	Jobs []jobView `json:"jobs"`
	// NextBefore is an opaque cursor for the next page; null on the last.
	NextBefore *int64 `json:"nextBefore"`
}

type jobResponse struct {
	Job jobView `json:"job"`
}

type logResponse struct {
	Job     jobView `json:"job"`
	Log     string  `json:"log"`
	Offset  int64   `json:"offset"`  // where the next request continues
	LogSize int64   `json:"logSize"` // current length of the log
}

func principal(r *http.Request) *api.Principal {
	p, _ := api.PrincipalFrom(r.Context())
	return p
}

func (m *Module) list(w http.ResponseWriter, r *http.Request) error {
	p := principal(r)
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	state, kind := q.Get("state"), q.Get("kind")
	if state != "" && !slices.Contains(jobStates, state) {
		return api.NewError(http.StatusBadRequest, "validation.invalid", map[string]any{"field": "state", "rule": "oneof"})
	}
	if kind != "" && !jobKind.MatchString(kind) {
		return api.NewError(http.StatusBadRequest, "validation.invalid", map[string]any{"field": "kind", "rule": "pattern"})
	}
	list, next, err := m.Jobs.List(r.Context(), jobs.ListFilter{
		Limit: limit, Before: before,
		Visible: func(j jobs.Record) bool {
			// A kind ending in "." is a prefix ("software."), otherwise exact.
			return p.Can(j.Permission) && matchesState(j, state) &&
				(kind == "" || j.Kind == kind || (strings.HasSuffix(kind, ".") && strings.HasPrefix(j.Kind, kind)))
		},
	})
	if err != nil {
		return err
	}
	resp := listResponse{Jobs: make([]jobView, 0, len(list))}
	for _, j := range list {
		resp.Jobs = append(resp.Jobs, viewOf(p, j))
	}
	if next > 0 {
		resp.NextBefore = &next
	}
	return api.WriteJSON(w, http.StatusOK, resp)
}

// matchesState reports whether a job is in the state asked for: one of the
// job states, "active" (queued or running) or "" (any).
func matchesState(j jobs.Record, state string) bool {
	switch state {
	case "":
		return true
	case "active":
		return !jobs.Done(j.State)
	}
	return j.State == state
}

var (
	jobStates = []string{"active", jobs.Queued, jobs.Running, jobs.Succeeded, jobs.Failed, jobs.Canceled, jobs.Interrupted}
	jobKind   = regexp.MustCompile(`^[a-z0-9_]+(\.[a-z0-9_]*)*$`)
)

// visible loads a job the caller may see; others look like they do not exist.
func (m *Module) visible(r *http.Request) (jobs.Record, error) {
	j, err := m.Jobs.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		return j, err
	}
	if !principal(r).Can(j.Permission) {
		return j, api.ErrNotFound
	}
	return j, nil
}

func (m *Module) get(w http.ResponseWriter, r *http.Request) error {
	j, err := m.visible(r)
	if err != nil {
		return err
	}
	return api.WriteJSON(w, http.StatusOK, jobResponse{Job: viewOf(principal(r), j)})
}

func (m *Module) log(w http.ResponseWriter, r *http.Request) error {
	if _, err := m.visible(r); err != nil {
		return err
	}
	q := r.URL.Query()
	offset, _ := strconv.ParseInt(q.Get("offset"), 10, 64)
	secs, _ := strconv.Atoi(q.Get("wait"))
	wait := min(time.Duration(max(secs, 0))*time.Second, maxWait)
	j, chunk, err := m.Jobs.Follow(r.Context(), r.PathValue("id"), offset, wait)
	if err != nil {
		return err
	}
	return api.WriteJSON(w, http.StatusOK, logResponse{Job: viewOf(principal(r), j), Log: chunk.Log, Offset: chunk.Offset, LogSize: chunk.LogSize})
}

func (m *Module) cancel(w http.ResponseWriter, r *http.Request) error {
	j, err := m.visible(r)
	if err != nil {
		return err
	}
	p := principal(r)
	if !p.Can(j.ManagePermission) {
		return api.ErrForbidden
	}
	caller := helperapi.Caller{UserID: p.UserID, Username: p.Username, Remote: api.ClientIP(r), Source: p.Source}
	params := map[string]any{"id": j.ID, "kind": j.Kind}
	updated, err := m.Jobs.Cancel(r.Context(), j.ID, caller)
	if err != nil {
		modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "job.cancel", modutil.ErrorCode(err), params)
		return err
	}
	modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "job.cancel", "ok", params)
	return api.WriteJSON(w, http.StatusAccepted, jobResponse{Job: viewOf(p, updated)})
}
