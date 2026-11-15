// SPDX-License-Identifier: AGPL-3.0-only

package sites

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/events"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/jobs"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/modutil"
)

// Health checks of app sites: every checkEvery, an HTTP GET of the app's
// health path on 127.0.0.1; after failLimit failures in a row the app is
// restarted and an app.unhealthy event published.
const (
	checkEvery  = 30 * time.Second
	checkWait   = 5 * time.Second
	failLimit   = 3
	restartRest = 2 * time.Minute // checks pause after a restart
)

type healthState struct {
	mu       sync.Mutex
	failures map[string]int
	paused   map[string]time.Time
}

type appStatusView struct {
	helperapi.AppStatus
	// Health is the last check: "ok", "failing" or "" (no check).
	Health   string `json:"health,omitempty"`
	Failures int    `json:"failures,omitempty"`
}

// usedPorts returns the ports other sites' apps and streams use, with the
// error parameters naming the user ({"site": code}, {"stream": code} or
// {"panel": true}).
func (m *Module) usedPorts(ctx context.Context, except string) (map[int]map[string]any, error) {
	used := map[int]map[string]any{}
	rows, err := m.DB.QueryContext(ctx, `SELECT code, spec FROM sites WHERE type = 'app' AND code != ?`, except)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var code, spec string
		var s helperapi.SiteSpec
		if err := rows.Scan(&code, &spec); err != nil {
			rows.Close()
			return nil, err
		}
		if json.Unmarshal([]byte(spec), &s) == nil && s.App != nil {
			used[s.App.Port] = map[string]any{"site": code}
		}
	}
	rows.Close()
	srows, err := m.DB.QueryContext(ctx, `SELECT code, spec FROM streams`)
	if err != nil {
		return nil, err
	}
	defer srows.Close()
	for srows.Next() {
		var code, spec string
		var s helperapi.StreamSpec
		if err := srows.Scan(&code, &spec); err != nil {
			return nil, err
		}
		if json.Unmarshal([]byte(spec), &s) == nil {
			used[s.Port] = map[string]any{"stream": code}
		}
	}
	if m.PanelPort > 0 {
		used[m.PanelPort] = map[string]any{"panel": true}
	}
	return used, srows.Err()
}

// assignPort gives an app request without a port the first free one of
// the app range, and refuses a port another app or stream uses.
func (m *Module) assignPort(ctx context.Context, req *siteRequest, code string) error {
	if req.Type != helperapi.SiteApp || req.App == nil {
		return nil
	}
	used, err := m.usedPorts(ctx, code)
	if err != nil {
		return err
	}
	if req.App.Port != 0 {
		if who, ok := used[req.App.Port]; ok {
			params := map[string]any{"port": req.App.Port}
			for k, v := range who {
				params[k] = v
			}
			return api.NewError(http.StatusConflict, helperapi.CodeAppPortTaken, params)
		}
		return nil
	}
	for p := helperapi.AppPortMin; p <= helperapi.AppPortMax; p++ {
		if _, ok := used[p]; !ok {
			req.App.Port = p
			return nil
		}
	}
	return api.NewError(http.StatusConflict, helperapi.CodeAppPortTaken, map[string]any{"range": "full"})
}

func (m *Module) appRow(r *http.Request) (row, error) {
	rw, err := m.load(r.Context(), r.PathValue("code"))
	if err != nil {
		return rw, err
	}
	if rw.spec.Type != helperapi.SiteApp {
		return rw, api.NewError(http.StatusConflict, helperapi.CodeAppNotFound, map[string]any{"site": rw.spec.Code})
	}
	return rw, nil
}

func helperError(err error) error {
	if he, ok := helperapi.AsError(err); ok {
		return api.NewError(http.StatusConflict, he.Code, he.Params)
	}
	return err
}

func (m *Module) appStatus(w http.ResponseWriter, r *http.Request) error {
	rw, err := m.appRow(r)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	var st helperapi.AppStatus
	if err := m.Helper.Call(ctx, helperapi.ActionAppStatus, callerOf(r), helperapi.AppRef{Code: rw.spec.Code}, &st); err != nil {
		return helperError(err)
	}
	v := appStatusView{AppStatus: st}
	if rw.spec.App != nil && rw.spec.App.HealthPath != "" {
		m.health.mu.Lock()
		v.Failures = m.health.failures[rw.spec.Code]
		m.health.mu.Unlock()
		v.Health = map[bool]string{true: "ok", false: "failing"}[v.Failures == 0]
	}
	return api.WriteJSON(w, http.StatusOK, v)
}

func (m *Module) appLog(w http.ResponseWriter, r *http.Request) error {
	rw, err := m.appRow(r)
	if err != nil {
		return err
	}
	req := helperapi.AppLogRequest{Code: rw.spec.Code, Cursor: r.URL.Query().Get("cursor")}
	if err := req.Validate(); err != nil {
		if he, ok := helperapi.AsError(err); ok {
			return api.NewError(http.StatusBadRequest, he.Code, he.Params)
		}
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	var resp helperapi.AppLogResponse
	if err := m.Helper.Call(ctx, helperapi.ActionAppLog, callerOf(r), req, &resp); err != nil {
		return helperError(err)
	}
	return api.WriteJSON(w, http.StatusOK, resp)
}

func (m *Module) restart(w http.ResponseWriter, r *http.Request) error {
	rw, err := m.appRow(r)
	if err != nil {
		return err
	}
	who := modutil.ActorOf(r)
	job, err := m.restartJob(r.Context(), rw.spec, jobs.Actor{UserID: who.UserID, Username: who.Username, Source: who.Source, Remote: api.ClientIP(r)}, "")
	if err != nil {
		return err
	}
	modutil.Audit(r, m.DB, m.Log, who, "site.restart", "ok", map[string]any{"site": rw.spec.Code, "job": job.ID})
	return api.WriteJSON(w, http.StatusAccepted, jobResponse{Job: job})
}

func (m *Module) restartJob(ctx context.Context, s helperapi.SiteSpec, actor jobs.Actor, reason string) (jobs.Record, error) {
	params := map[string]any{"site": s.Code, "domain": s.Domains[0]}
	if reason != "" {
		params["reason"] = reason
	}
	return m.Jobs.Start(ctx, jobs.Spec{
		Kind: "site.restart", Params: params, Permission: Read, ManagePermission: Manage, Locks: []string{"site:" + s.Code}, Actor: actor,
	}, func(_ context.Context, j *jobs.Job) error {
		var st helperapi.AppStatus
		if err := j.RunOp(helperapi.ActionAppRestart, helperapi.AppRef{Code: s.Code}, &st); err != nil {
			return err
		}
		j.Logf("The app is %s (%s).\n", st.ActiveState, st.SubState)
		return nil
	})
}

// Run checks the health of app sites until ctx ends.
func (m *Module) Run(ctx context.Context) {
	client := &http.Client{Timeout: checkWait, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	t := time.NewTicker(checkEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		m.CheckHealth(ctx, client)
	}
}

// CheckHealth runs one round of health checks (exported for tests).
func (m *Module) CheckHealth(ctx context.Context, client *http.Client) {
	rows, err := m.DB.QueryContext(ctx, `SELECT spec FROM sites WHERE type = 'app' AND state = 'active'`)
	if err != nil {
		return
	}
	var specs []helperapi.SiteSpec
	for rows.Next() {
		var data string
		var s helperapi.SiteSpec
		if rows.Scan(&data) == nil && json.Unmarshal([]byte(data), &s) == nil && s.Enabled && s.App != nil && s.App.HealthPath != "" {
			specs = append(specs, s)
		}
	}
	rows.Close()
	m.health.mu.Lock()
	if m.health.failures == nil {
		m.health.failures, m.health.paused = map[string]int{}, map[string]time.Time{}
	}
	m.health.mu.Unlock()
	for _, s := range specs {
		m.health.mu.Lock()
		paused := time.Now().Before(m.health.paused[s.Code])
		m.health.mu.Unlock()
		if paused {
			continue
		}
		ok := probe(ctx, client, s.App.Port, s.App.HealthPath)
		m.health.mu.Lock()
		if ok {
			m.health.failures[s.Code] = 0
			m.health.mu.Unlock()
			continue
		}
		m.health.failures[s.Code]++
		n := m.health.failures[s.Code]
		if n >= failLimit {
			m.health.failures[s.Code], m.health.paused[s.Code] = 0, time.Now().Add(restartRest)
		}
		m.health.mu.Unlock()
		if n < failLimit {
			continue
		}
		m.Log.Warn("app unhealthy; restarting", "site", s.Code, "path", s.App.HealthPath)
		m.Events.Publish(events.Event{Type: events.AppUnhealthy, Level: events.Warning,
			DedupeKey: "app-unhealthy:" + s.Code + ":" + time.Now().UTC().Format("2006-01-02T15"),
			Params:    map[string]any{"site": s.Code, "domain": s.Domains[0], "path": s.App.HealthPath, "failures": n}})
		if _, err := m.restartJob(ctx, s, jobs.Actor{Source: "system", Username: "cairn"}, "health"); err != nil {
			m.Log.Warn("restart after failed health checks not started", "site", s.Code, "err", err)
		}
	}
}

// probe reports whether the app answers its health path below status 500.
func probe(ctx context.Context, client *http.Client, port int, path string) bool {
	ctx, cancel := context.WithTimeout(ctx, checkWait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+path, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", "Cairn health check")
	res, err := client.Do(req)
	if err != nil {
		return false
	}
	_ = res.Body.Close()
	return res.StatusCode < 500
}
