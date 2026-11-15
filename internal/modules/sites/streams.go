// SPDX-License-Identifier: AGPL-3.0-only

package sites

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/jobs"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/modutil"
)

// Four-layer proxies (nginx stream): kept like sites, changed by jobs.

type streamRequest struct {
	Code      string   `json:"code,omitempty"`
	Protocol  string   `json:"protocol"`
	Port      int      `json:"port"`
	Upstreams []string `json:"upstreams"`
	Balance   string   `json:"balance,omitempty"`
}

type streamView struct {
	helperapi.StreamSpec
	State     string `json:"state"`
	ErrorCode string `json:"errorCode,omitempty"`
	LastJob   string `json:"lastJob,omitempty"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
	Drift     bool   `json:"drift"`
}

type streamsResponse struct {
	Streams []streamView `json:"streams"`
}

func (m *Module) streamRoutes(r *api.Router, doc func(string) api.RouteOption) {
	r.Handle("GET /api/v1/streams", m.listStreams, api.Require(Read), doc("Four-layer (TCP/UDP) proxies"), api.Returns(200, streamsResponse{}))
	r.Handle("POST /api/v1/streams", m.createStream, api.Require(Manage), api.Body(streamRequest{}),
		doc("Create a four-layer proxy (starts a job)"), api.Returns(202, jobResponse{}))
	r.Handle("PUT /api/v1/streams/{code}", m.updateStream, api.Require(Manage), api.Body(streamRequest{}),
		doc("Change a four-layer proxy (starts a job)"), api.Returns(202, jobResponse{}))
	r.Handle("POST /api/v1/streams/{code}/start", m.startStream, api.Require(Manage), api.NoBody(),
		doc("Listen again (starts a job)"), api.Returns(202, jobResponse{}))
	r.Handle("POST /api/v1/streams/{code}/stop", m.stopStream, api.Require(Manage), api.NoBody(),
		doc("Stop listening (starts a job)"), api.Returns(202, jobResponse{}))
	r.Handle("DELETE /api/v1/streams/{code}", m.deleteStream, api.Require(Manage), api.StepUp(),
		doc("Delete a four-layer proxy (starts a job)"), api.Returns(202, jobResponse{}))
}

type streamRow struct {
	spec                      helperapi.StreamSpec
	state, errorCode, lastJob string
	createdAt, updatedAt      int64
}

func (m *Module) loadStream(ctx context.Context, code string) (streamRow, error) {
	var rw streamRow
	var spec string
	err := m.DB.QueryRowContext(ctx, `SELECT spec, state, error_code, last_job, created_at, updated_at FROM streams WHERE code = ?`, code).
		Scan(&spec, &rw.state, &rw.errorCode, &rw.lastJob, &rw.createdAt, &rw.updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return rw, api.NewError(http.StatusNotFound, helperapi.CodeStreamNotFound, map[string]any{"stream": code})
	}
	if err != nil {
		return rw, err
	}
	return rw, json.Unmarshal([]byte(spec), &rw.spec)
}

func (m *Module) listStreams(w http.ResponseWriter, r *http.Request) error {
	st, known := m.status(r.Context(), r)
	rows, err := m.DB.QueryContext(r.Context(), `SELECT code FROM streams ORDER BY code`)
	if err != nil {
		return err
	}
	var codes []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			rows.Close()
			return err
		}
		codes = append(codes, c)
	}
	rows.Close()
	resp := streamsResponse{Streams: []streamView{}}
	for _, c := range codes {
		rw, err := m.loadStream(r.Context(), c)
		if err != nil {
			return err
		}
		v := streamView{StreamSpec: rw.spec, State: rw.state, ErrorCode: rw.errorCode, LastJob: rw.lastJob, CreatedAt: rw.createdAt, UpdatedAt: rw.updatedAt}
		if known {
			a, ok := st.Streams[c]
			got, _ := json.Marshal(a)
			want, _ := json.Marshal(rw.spec)
			v.Drift = !ok || string(got) != string(want)
		}
		resp.Streams = append(resp.Streams, v)
	}
	return api.WriteJSON(w, http.StatusOK, resp)
}

// streamSpec checks a request and that its port is free.
func (m *Module) streamSpec(ctx context.Context, req streamRequest, code string, enabled bool) (helperapi.StreamSpec, error) {
	s := helperapi.StreamSpec{Code: code, Protocol: req.Protocol, Port: req.Port, Balance: req.Balance, Enabled: enabled}
	for _, u := range req.Upstreams {
		s.Upstreams = append(s.Upstreams, strings.ToLower(strings.TrimSpace(u)))
	}
	if err := s.Validate(); err != nil {
		if he, ok := helperapi.AsError(err); ok {
			return s, api.NewError(http.StatusBadRequest, he.Code, he.Params)
		}
		return s, err
	}
	if s.Port == m.PanelPort {
		return s, api.NewError(http.StatusConflict, helperapi.CodeStreamPortTaken, map[string]any{"port": s.Port, "panel": true})
	}
	rows, err := m.DB.QueryContext(ctx, `SELECT code, spec FROM streams WHERE code != ?`, code)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	for rows.Next() {
		var c, data string
		var other helperapi.StreamSpec
		if err := rows.Scan(&c, &data); err != nil {
			return s, err
		}
		if json.Unmarshal([]byte(data), &other) == nil && other.Port == s.Port && other.Protocol == s.Protocol {
			return s, api.NewError(http.StatusConflict, helperapi.CodeStreamPortTaken, map[string]any{"port": s.Port, "stream": c})
		}
	}
	return s, rows.Err()
}

func (m *Module) createStream(w http.ResponseWriter, r *http.Request) error {
	var req streamRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	code := req.Code
	if code == "" {
		code = req.Protocol + "-" + strconv.Itoa(req.Port)
	}
	s, err := m.streamSpec(r.Context(), req, code, true)
	if err != nil {
		return err
	}
	data, _ := json.Marshal(s)
	now := time.Now().Unix()
	if _, err := m.DB.ExecContext(r.Context(), `INSERT INTO streams (code, spec, state, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		s.Code, string(data), StateApplying, now, now); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return api.NewError(http.StatusConflict, "stream.code_taken", map[string]any{"code": s.Code})
		}
		return err
	}
	return m.streamJob(w, r, "stream.create", s)
}

func (m *Module) updateStream(w http.ResponseWriter, r *http.Request) error {
	var req streamRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	rw, err := m.loadStream(r.Context(), r.PathValue("code"))
	if err != nil {
		return err
	}
	s, err := m.streamSpec(r.Context(), req, rw.spec.Code, rw.spec.Enabled)
	if err != nil {
		return err
	}
	return m.saveStream(w, r, "stream.update", s)
}

func (m *Module) startStream(w http.ResponseWriter, r *http.Request) error {
	return m.toggleStream(w, r, true)
}
func (m *Module) stopStream(w http.ResponseWriter, r *http.Request) error {
	return m.toggleStream(w, r, false)
}

func (m *Module) toggleStream(w http.ResponseWriter, r *http.Request, enabled bool) error {
	rw, err := m.loadStream(r.Context(), r.PathValue("code"))
	if err != nil {
		return err
	}
	s := rw.spec
	s.Enabled = enabled
	return m.saveStream(w, r, map[bool]string{true: "stream.start", false: "stream.stop"}[enabled], s)
}

func (m *Module) saveStream(w http.ResponseWriter, r *http.Request, kind string, s helperapi.StreamSpec) error {
	data, _ := json.Marshal(s)
	if _, err := m.DB.ExecContext(r.Context(), `UPDATE streams SET spec = ?, state = ?, error_code = '', updated_at = ? WHERE code = ?`,
		string(data), StateApplying, time.Now().Unix(), s.Code); err != nil {
		return err
	}
	return m.streamJob(w, r, kind, s)
}

func (m *Module) streamJob(w http.ResponseWriter, r *http.Request, kind string, s helperapi.StreamSpec) error {
	who := modutil.ActorOf(r)
	job, err := m.Jobs.Start(r.Context(), jobs.Spec{
		Kind: kind, Params: map[string]any{"stream": s.Code, "protocol": s.Protocol, "port": s.Port}, Permission: Read, ManagePermission: Manage,
		Locks: []string{"stream:" + s.Code}, Actor: jobs.Actor{UserID: who.UserID, Username: who.Username, Source: who.Source, Remote: api.ClientIP(r)},
	}, func(ctx context.Context, j *jobs.Job) error {
		err := j.RunOp(helperapi.ActionStreamApply, s, nil)
		state, code := StateActive, ""
		if err != nil {
			state, code = StateFailed, api.ErrInternal.Code
			if he, ok := helperapi.AsError(err); ok {
				code = he.Code
			}
		}
		if _, dbErr := m.DB.ExecContext(context.WithoutCancel(ctx), `UPDATE streams SET state = ?, error_code = ?, last_job = ?, updated_at = ? WHERE code = ?`,
			state, code, j.ID(), time.Now().Unix(), s.Code); dbErr != nil {
			m.Log.Error("saving stream state failed", "stream", s.Code, "err", dbErr)
		}
		return err
	})
	if err != nil {
		return err
	}
	modutil.Audit(r, m.DB, m.Log, who, kind, "ok", map[string]any{"stream": s.Code, "protocol": s.Protocol, "port": s.Port, "upstreams": s.Upstreams, "job": job.ID})
	return api.WriteJSON(w, http.StatusAccepted, jobResponse{Job: job})
}

func (m *Module) deleteStream(w http.ResponseWriter, r *http.Request) error {
	rw, err := m.loadStream(r.Context(), r.PathValue("code"))
	if err != nil {
		return err
	}
	code := rw.spec.Code
	who := modutil.ActorOf(r)
	job, err := m.Jobs.Start(r.Context(), jobs.Spec{
		Kind: "stream.delete", Params: map[string]any{"stream": code, "protocol": rw.spec.Protocol, "port": rw.spec.Port},
		Permission: Read, ManagePermission: Manage, Locks: []string{"stream:" + code},
		Actor: jobs.Actor{UserID: who.UserID, Username: who.Username, Source: who.Source, Remote: api.ClientIP(r)},
	}, func(ctx context.Context, j *jobs.Job) error {
		err := j.RunOp(helperapi.ActionStreamRemove, helperapi.StreamRef{Code: code}, nil)
		if he, ok := helperapi.AsError(err); ok && he.Code == helperapi.CodeStreamNotFound {
			err = nil
		}
		if err != nil {
			return err
		}
		_, err = m.DB.ExecContext(context.WithoutCancel(ctx), `DELETE FROM streams WHERE code = ?`, code)
		return err
	})
	if err != nil {
		return err
	}
	modutil.Audit(r, m.DB, m.Log, who, "stream.delete", "ok", map[string]any{"stream": code, "job": job.ID})
	return api.WriteJSON(w, http.StatusAccepted, jobResponse{Job: job})
}
