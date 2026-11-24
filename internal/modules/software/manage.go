// SPDX-License-Identifier: AGPL-3.0-only

package software

import (
	"context"
	"net/http"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/jobs"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/modutil"
)

// Installed lines: services, PHP extensions and configuration files. The
// helper resolves everything from its own catalog; these routes only name
// an entry, a line and catalog IDs.

func (m *Module) manageRoutes(r *api.Router, doc func(string) api.RouteOption) {
	job := api.Returns(202, jobResponse{})
	r.Handle("POST /api/v1/software/{id}/service", m.service, api.Require(Manage), api.Body(serviceRequest{}),
		doc("Start, stop, restart or reload the services of an installed line (starts a job)"), job)
	r.Handle("GET /api/v1/software/{id}/lines/{line}/extensions", m.extensions, api.Require(Read),
		doc("PHP extensions of an installed line: which can be installed, which are installed and loaded"),
		api.Returns(200, helperapi.ExtStatus{}))
	r.Handle("POST /api/v1/software/{id}/lines/{line}/extensions", m.applyExtensions, api.Require(Manage), api.Body(extensionsRequest{}),
		doc("Install and remove PHP extensions of a line, then reload its PHP-FPM (starts a job)"), job)
	r.Handle("GET /api/v1/software/{id}/lines/{line}/configs/{config}", m.config, api.Require(Config), api.StepUp(),
		doc("A configuration file of an installed line, with the versions kept from earlier saves"),
		api.Returns(200, helperapi.ConfigFile{}))
	r.Handle("GET /api/v1/software/{id}/lines/{line}/configs/{config}/versions/{version}", m.configVersion, api.Require(Config), api.StepUp(),
		doc("A kept earlier version of a configuration file"), api.Returns(200, helperapi.ConfigFile{}))
	r.Handle("PUT /api/v1/software/{id}/lines/{line}/configs/{config}", m.saveConfig, api.Require(Config), api.StepUp(), api.Body(configRequest{}),
		doc("Save a configuration file: it is checked and the service reloaded or restarted; on failure the previous file comes back (starts a job). base is the sha256 the file was read with"),
		job)
}

type serviceRequest struct {
	Line   string `json:"line"`
	Action string `json:"action"`
}

type extensionsRequest struct {
	Install []string `json:"install,omitempty"`
	Remove  []string `json:"remove,omitempty"`
}

type configRequest struct {
	Content string `json:"content"`
	Base    string `json:"base"`
}

// statusOf is the HTTP status of a helper error answered synchronously.
func statusOf(code string) int {
	switch code {
	case "validation.invalid", helperapi.CodePkgConfigTooLarge, helperapi.CodePkgConfigNotText,
		helperapi.CodePkgExtUnknown, helperapi.CodePkgExtUnavailable:
		return http.StatusBadRequest
	case helperapi.CodePkgConfigLocked:
		return http.StatusForbidden
	case helperapi.CodePkgExtUnsupported, helperapi.CodePkgConfigUnknown, helperapi.CodePkgConfigMissing,
		helperapi.CodePkgConfigVersion:
		return http.StatusNotFound
	}
	return http.StatusConflict
}

func helperError(err error) error {
	if he, ok := helperapi.AsError(err); ok {
		return api.NewError(statusOf(he.Code), he.Code, he.Params)
	}
	return err
}

func validationError(err error) error {
	if he, ok := helperapi.AsError(err); ok {
		return api.NewError(statusOf(he.Code), he.Code, he.Params)
	}
	return api.NewError(http.StatusBadRequest, "validation.invalid", nil)
}

func (m *Module) call(r *http.Request, action string, req, resp any) error {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	return helperError(m.Helper.Call(ctx, action, callerOf(r), req, resp))
}

// lineJob starts a job running one helper operation on a line, and audits it.
func (m *Module) lineJob(w http.ResponseWriter, r *http.Request, kind, action string, hreq any, params map[string]any) error {
	who := modutil.ActorOf(r)
	job, err := m.Jobs.Start(r.Context(), jobs.Spec{
		Kind: kind, Params: params, Permission: Read, ManagePermission: Manage,
		Actor: jobs.Actor{UserID: who.UserID, Username: who.Username, Source: who.Source, Remote: api.ClientIP(r)},
	}, func(_ context.Context, j *jobs.Job) error {
		return j.RunOp(action, hreq, nil)
	})
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

func (m *Module) service(w http.ResponseWriter, r *http.Request) error {
	var req serviceRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	id := r.PathValue("id")
	sw, _, err := m.knownLine(id, req.Line)
	if err != nil {
		return err
	}
	hreq := helperapi.ServiceRequest{Software: id, Line: req.Line, Action: req.Action}
	if err := hreq.Validate(); err != nil {
		return validationError(err)
	}
	params := map[string]any{"software": id, "name": sw.Name, "line": req.Line, "action": req.Action}
	return m.lineJob(w, r, "software.service", helperapi.ActionPkgService, hreq, params)
}

func (m *Module) lineRef(r *http.Request) (helperapi.LineRef, string, error) {
	ref := helperapi.LineRef{Software: r.PathValue("id"), Line: r.PathValue("line")}
	sw, _, err := m.knownLine(ref.Software, ref.Line)
	if err != nil {
		return ref, "", err
	}
	if err := ref.Validate(); err != nil {
		return ref, "", validationError(err)
	}
	return ref, sw.Name, nil
}

func (m *Module) extensions(w http.ResponseWriter, r *http.Request) error {
	ref, _, err := m.lineRef(r)
	if err != nil {
		return err
	}
	var st helperapi.ExtStatus
	if err := m.call(r, helperapi.ActionPkgExtStatus, ref, &st); err != nil {
		return err
	}
	if st.Modules == nil {
		st.Modules = []string{}
	}
	return api.WriteJSON(w, http.StatusOK, st)
}

func (m *Module) applyExtensions(w http.ResponseWriter, r *http.Request) error {
	ref, name, err := m.lineRef(r)
	if err != nil {
		return err
	}
	var req extensionsRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	hreq := helperapi.ExtApplyRequest{Software: ref.Software, Line: ref.Line, Install: req.Install, Remove: req.Remove}
	if err := hreq.Validate(); err != nil {
		return validationError(err)
	}
	// Unknown or unavailable extensions fail here rather than in the job.
	var st helperapi.ExtStatus
	if err := m.call(r, helperapi.ActionPkgExtStatus, ref, &st); err != nil {
		return err
	}
	known := map[string]helperapi.ExtensionInfo{}
	for _, e := range st.Extensions {
		known[e.ID] = e
	}
	for _, id := range append(append([]string{}, req.Install...), req.Remove...) {
		e, ok := known[id]
		switch {
		case !ok:
			return api.NewError(http.StatusBadRequest, helperapi.CodePkgExtUnknown, map[string]any{"extension": id})
		case !e.Available || e.Builtin:
			return api.NewError(http.StatusBadRequest, helperapi.CodePkgExtUnavailable, map[string]any{"extension": id})
		}
	}
	params := map[string]any{"software": ref.Software, "name": name, "line": ref.Line}
	if len(req.Install) > 0 {
		params["install"] = req.Install
	}
	if len(req.Remove) > 0 {
		params["remove"] = req.Remove
	}
	return m.lineJob(w, r, "software.extensions", helperapi.ActionPkgExtApply, hreq, params)
}

func (m *Module) configRef(r *http.Request) (helperapi.ConfigRequest, string, error) {
	ref, name, err := m.lineRef(r)
	if err != nil {
		return helperapi.ConfigRequest{}, "", err
	}
	q := helperapi.ConfigRequest{Software: ref.Software, Line: ref.Line, Config: r.PathValue("config"), Version: r.PathValue("version")}
	if err := q.Validate(); err != nil {
		return q, "", validationError(err)
	}
	return q, name, nil
}

func (m *Module) config(w http.ResponseWriter, r *http.Request) error {
	q, _, err := m.configRef(r)
	if err != nil {
		return err
	}
	var f helperapi.ConfigFile
	if err := m.call(r, helperapi.ActionPkgConfigRead, q, &f); err != nil {
		return err
	}
	return api.WriteJSON(w, http.StatusOK, f)
}

func (m *Module) configVersion(w http.ResponseWriter, r *http.Request) error {
	q, _, err := m.configRef(r)
	if err != nil {
		return err
	}
	if q.Version == "" {
		return api.ErrNotFound
	}
	var f helperapi.ConfigFile
	if err := m.call(r, helperapi.ActionPkgConfigRead, q, &f); err != nil {
		return err
	}
	return api.WriteJSON(w, http.StatusOK, f)
}

func (m *Module) saveConfig(w http.ResponseWriter, r *http.Request) error {
	q, name, err := m.configRef(r)
	if err != nil {
		return err
	}
	var req configRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	hreq := helperapi.ConfigWriteRequest{Software: q.Software, Line: q.Line, Config: q.Config, Content: req.Content, Base: req.Base}
	if err := hreq.Validate(); err != nil {
		return validationError(err)
	}
	// A file changed since it was read, or a server that keeps files
	// read-only, fails now rather than in the job.
	var cur helperapi.ConfigFile
	if err := m.call(r, helperapi.ActionPkgConfigRead, q, &cur); err != nil {
		return err
	}
	if !cur.Editable {
		return api.NewError(http.StatusForbidden, helperapi.CodePkgConfigLocked, nil)
	}
	if cur.SHA256 != req.Base {
		return api.NewError(http.StatusConflict, helperapi.CodePkgConfigChanged, map[string]any{"path": cur.Path})
	}
	params := map[string]any{"software": q.Software, "name": name, "line": q.Line, "config": q.Config, "path": cur.Path}
	return m.lineJob(w, r, "software.config", helperapi.ActionPkgConfigWrite, hreq, params)
}
