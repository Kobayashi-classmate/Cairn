// SPDX-License-Identifier: AGPL-3.0-only

// Package software serves the software catalog as offered on this system
// (which lines can be installed, how, which one is recommended and which are
// installed), probes the download sources of lines before they are
// installed, and starts package jobs: refreshing the indexes, installing and
// removing a line. The root helper resolves every request against its own
// copy of the catalog; this module only names an entry, a line and, for
// runtime downloads, one of the sources the probe offered.
package software

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Kobayashi-classmate/Cairn/catalog"
	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/authz"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/jobs"
	"github.com/Kobayashi-classmate/Cairn/internal/module"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/modutil"
)

// Permissions of this module.
const (
	Read   authz.Permission = "software.read"
	Manage authz.Permission = "software.manage"
)

// Module implements module.Module.
type Module struct {
	Catalog *catalog.Catalog
	Jobs    *jobs.Manager
	Helper  jobs.HelperClient
	DB      *sql.DB
	Log     *slog.Logger
	// Release is the platform profile name ("" when unsupported); Chain and
	// Arch describe the system offers are computed for.
	Release string
	Chain   []string
	Arch    string
}

// ID implements module.Module.
func (m *Module) ID() string { return "software" }

// Info implements module.Describer.
func (m *Module) Info() module.Info {
	return module.Info{
		Permissions: []module.Permission{
			{Name: Read, Roles: []authz.Role{authz.Operator, authz.Auditor}},
			{Name: Manage, Roles: []authz.Role{authz.Operator}},
		},
		Nav: []module.NavItem{
			{Key: "software", LabelKey: "common.nav.software", Icon: "apps", Route: "software", Permission: Read, Order: 20},
		},
	}
}

// Routes implements module.Module.
func (m *Module) Routes(r *api.Router) {
	doc := func(s string) api.RouteOption { return api.Doc("software", s) }
	r.Handle("GET /api/v1/software", m.list, api.Require(Read),
		doc("Software catalog as offered on this system: lines, install method, recommended line"), api.Returns(200, listResponse{}))
	r.Handle("POST /api/v1/software/refresh", m.refresh, api.Require(Manage), api.NoBody(),
		doc("Refresh the system package indexes (starts a job)"), api.Returns(202, jobResponse{}))
	r.Handle("POST /api/v1/software/probe", m.probe, api.Require(Manage), api.Body(probeRequest{}),
		doc("Check the download sources of lines before installing them: which candidates serve exactly the official file or index, how far behind they are, how fast they are from this machine (a few seconds)"),
		api.Returns(200, probeResponse{}))
	r.Handle("GET /api/v1/software/suites", m.suites, api.Require(Read),
		doc("Suites of the first-login wizard, resolved for this system: each item's software and recommended line"),
		api.Returns(200, suitesResponse{}))
	r.Handle("POST /api/v1/software/install", m.installSet, api.Require(Manage), api.Body(installSetRequest{}),
		doc("Install several lines as one job: the lines from the package manager in one transaction, prebuilt runtimes downloading meanwhile (starts a job)"),
		api.Returns(202, jobResponse{}))
	r.Handle("GET /api/v1/software/settings", m.getSettings, api.Require(Read),
		doc("Software settings: the download source policy"), api.Returns(200, settingsBody{}))
	r.Handle("PUT /api/v1/software/settings", m.putSettings, api.Require(Manage), api.Body(settingsBody{}),
		doc("Change the download source policy"), api.Returns(200, settingsBody{}))
	r.Handle("POST /api/v1/software/{id}/install", m.install, api.Require(Manage), api.Body(lineRequest{}),
		doc("Install a line: the exact newest version of the line from its source (starts a job). source picks a candidate of the line's runtime download slot (see probe); empty picks the recommended one"),
		api.Returns(202, jobResponse{}))
	r.Handle("POST /api/v1/software/{id}/remove", m.remove, api.Require(Manage), api.StepUp(), api.Body(lineRequest{}),
		doc("Remove an installed line; configuration and data are kept (starts a job)"), api.Returns(202, jobResponse{}))
}

type offerView struct {
	catalog.Offer
	Installed         bool                         `json:"installed"`
	InstalledPackages []helperapi.InstalledPackage `json:"installedPackages,omitempty"`
}

type entry struct {
	catalog.Software
	Offers []offerView `json:"offers"`
}

type listResponse struct {
	Release   string `json:"release"` // platform profile, "" when unsupported
	Arch      string `json:"arch"`
	Supported bool   `json:"supported"`
	// StatusAvailable is false when the root helper could not report what
	// is installed; "installed" is then false everywhere.
	StatusAvailable bool           `json:"statusAvailable"`
	Software        []entry        `json:"software"`
	Repos           []catalog.Repo `json:"repos"`
}

type lineRequest struct {
	Line string `json:"line"`
	// Source is a candidate ID of the line's runtime download slot.
	Source string `json:"source,omitempty"`
}

type suiteView struct {
	ID    string              `json:"id"`
	Items []catalog.SuiteItem `json:"items"`
}

type suitesResponse struct {
	Suites []suiteView `json:"suites"`
}

type installSetItem struct {
	Software string `json:"software"`
	Line     string `json:"line"`
	Source   string `json:"source,omitempty"`
}

type installSetRequest struct {
	Items []installSetItem `json:"items"`
}

type probeItem struct {
	Software string `json:"software"`
	Line     string `json:"line"`
}

type probeRequest struct {
	Items []probeItem `json:"items"`
}

type probeResponse struct {
	Policy string `json:"policy"` // recommended | official
	helperapi.ProbeResult
}

// Download source policies as the API names them.
const (
	policyRecommended = "recommended"
	policyOfficial    = "official"
	policyKey         = "software.download_policy"
)

type settingsBody struct {
	// DownloadPolicy is "recommended" (the fastest source that serves the
	// official file) or "official" (the upstream's own addresses only).
	DownloadPolicy string `json:"downloadPolicy"`
}

type jobResponse struct {
	Job jobs.Record `json:"job"`
}

func (m *Module) list(w http.ResponseWriter, r *http.Request) error {
	sys := catalog.System{Chain: m.Chain, Arch: m.Arch, Now: time.Now()}
	resp := listResponse{Release: m.Release, Arch: m.Arch, Supported: m.Release != "", Software: []entry{}, Repos: []catalog.Repo{}}
	var st helperapi.SoftwareStatus
	if resp.Supported {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		err := m.Helper.Call(ctx, helperapi.ActionPkgStatus, callerOf(r), nil, &st)
		cancel()
		if err != nil {
			m.Log.Warn("software status unavailable", "err", err)
		} else {
			resp.StatusAvailable = true
		}
	}
	for i := range m.Catalog.Software {
		sw := &m.Catalog.Software[i]
		e := entry{Software: *sw, Offers: []offerView{}}
		for _, o := range sw.Offers(sys) {
			pkgs := st.Installed[sw.ID][o.Line]
			e.Offers = append(e.Offers, offerView{Offer: o, Installed: len(pkgs) > 0, InstalledPackages: pkgs})
		}
		resp.Software = append(resp.Software, e)
	}
	for _, rp := range m.Catalog.Repos {
		resp.Repos = append(resp.Repos, rp)
	}
	sort.Slice(resp.Repos, func(i, j int) bool { return resp.Repos[i].ID < resp.Repos[j].ID })
	return api.WriteJSON(w, http.StatusOK, resp)
}

func (m *Module) refresh(w http.ResponseWriter, r *http.Request) error {
	who := modutil.ActorOf(r)
	job, err := m.Jobs.Start(r.Context(), jobs.Spec{
		Kind: "software.refresh", Permission: Read, ManagePermission: Manage,
		Actor: jobs.Actor{UserID: who.UserID, Username: who.Username, Source: who.Source, Remote: api.ClientIP(r)},
	}, func(_ context.Context, j *jobs.Job) error {
		j.Logf("Refreshing the package indexes of this system.\n")
		return j.RunOp(helperapi.ActionPkgRefresh, nil, nil)
	})
	if err != nil {
		return err
	}
	modutil.Audit(r, m.DB, m.Log, who, "software.refresh", "ok", map[string]any{"job": job.ID})
	return api.WriteJSON(w, http.StatusAccepted, jobResponse{Job: job})
}

// policy returns the stored download source policy.
func (m *Module) policy(ctx context.Context) string {
	var v string
	if err := m.DB.QueryRowContext(ctx, `SELECT value FROM kv WHERE key = ?`, policyKey).Scan(&v); err != nil || v != policyOfficial {
		return policyRecommended
	}
	return v
}

func helperPolicy(p string) string {
	if p == policyOfficial {
		return helperapi.PolicyOfficial
	}
	return helperapi.PolicyRecommended
}

func (m *Module) getSettings(w http.ResponseWriter, r *http.Request) error {
	return api.WriteJSON(w, http.StatusOK, settingsBody{DownloadPolicy: m.policy(r.Context())})
}

func (m *Module) putSettings(w http.ResponseWriter, r *http.Request) error {
	var req settingsBody
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	if req.DownloadPolicy != policyRecommended && req.DownloadPolicy != policyOfficial {
		return api.NewError(http.StatusBadRequest, "validation.invalid", map[string]any{"field": "downloadPolicy", "rule": "oneof"})
	}
	if _, err := m.DB.ExecContext(r.Context(), `INSERT INTO kv(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		policyKey, req.DownloadPolicy); err != nil {
		return err
	}
	modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "software.settings", "ok", map[string]any{"downloadPolicy": req.DownloadPolicy})
	return api.WriteJSON(w, http.StatusOK, req)
}

func (m *Module) suites(w http.ResponseWriter, _ *http.Request) error {
	sys := catalog.System{Chain: m.Chain, Arch: m.Arch, Now: time.Now()}
	resp := suitesResponse{Suites: []suiteView{}}
	for _, su := range m.Catalog.Suites {
		resp.Suites = append(resp.Suites, suiteView{ID: su.ID, Items: m.Catalog.Resolve(su, sys)})
	}
	return api.WriteJSON(w, http.StatusOK, resp)
}

// installSet starts one job installing several lines (the wizard's choice).
func (m *Module) installSet(w http.ResponseWriter, r *http.Request) error {
	var req installSetRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	policy := helperPolicy(m.policy(r.Context()))
	hreq := helperapi.InstallSetRequest{}
	var listed []map[string]any
	names := map[string]string{}
	for _, it := range req.Items {
		sw, err := m.availableOffer(it.Software, it.Line)
		if err != nil {
			return err
		}
		names[it.Software+" "+it.Line] = sw.Name + " " + it.Line
		hreq.Items = append(hreq.Items, helperapi.SoftwareRequest{Software: it.Software, Line: it.Line, Source: it.Source, Policy: policy})
		entry := map[string]any{"software": it.Software, "name": sw.Name, "line": it.Line}
		if it.Source != "" {
			entry["source"] = it.Source
		}
		listed = append(listed, entry)
	}
	if err := hreq.Validate(); err != nil {
		var he *helperapi.Error
		if errors.As(err, &he) {
			return api.NewError(http.StatusBadRequest, he.Code, he.Params)
		}
		return err
	}
	who := modutil.ActorOf(r)
	params := map[string]any{"items": listed, "count": len(listed)}
	job, err := m.Jobs.Start(r.Context(), jobs.Spec{
		Kind: "software.install_set", Params: params, Permission: Read, ManagePermission: Manage,
		Actor: jobs.Actor{UserID: who.UserID, Username: who.Username, Source: who.Source, Remote: api.ClientIP(r)},
	}, func(_ context.Context, j *jobs.Job) error {
		var res helperapi.InstallSetResult
		if err := j.RunOp(helperapi.ActionPkgInstallSet, hreq, &res); err != nil {
			return err
		}
		var failed []string
		for _, it := range res.Items {
			name := names[it.Software+" "+it.Line]
			if it.Error != nil {
				failed = append(failed, name)
				j.Logf("%s: not installed (%s)\n", name, it.Error.Code)
				continue
			}
			for _, p := range it.Result.Packages {
				j.Logf("%s: %s %s\n", name, p.Name, p.Version)
			}
		}
		if len(failed) > 0 {
			return &helperapi.Error{Code: CodeSetIncomplete, Params: map[string]any{
				"failed": strings.Join(failed, ", "), "count": len(failed), "total": len(res.Items)}}
		}
		return nil
	})
	if err != nil {
		return err
	}
	modutil.Audit(r, m.DB, m.Log, who, "software.install_set", "ok", map[string]any{"items": listed, "job": job.ID})
	return api.WriteJSON(w, http.StatusAccepted, jobResponse{Job: job})
}

// CodeSetIncomplete ends an install-set job in which some lines failed.
const CodeSetIncomplete = "software.install_incomplete"

// probe asks the root helper to check the download sources of the lines.
func (m *Module) probe(w http.ResponseWriter, r *http.Request) error {
	var req probeRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	if len(req.Items) == 0 || len(req.Items) > helperapi.MaxProbeItems {
		return api.NewError(http.StatusBadRequest, "validation.invalid", map[string]any{"field": "items", "rule": "count"})
	}
	policy := m.policy(r.Context())
	hreq := helperapi.ProbeRequest{Policy: helperPolicy(policy)}
	for _, it := range req.Items {
		if _, err := m.availableOffer(it.Software, it.Line); err != nil {
			return err
		}
		hreq.Items = append(hreq.Items, helperapi.SoftwareRequest{Software: it.Software, Line: it.Line})
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	resp := probeResponse{Policy: policy}
	if err := m.Helper.Call(ctx, helperapi.ActionPkgProbe, callerOf(r), hreq, &resp.ProbeResult); err != nil {
		return err
	}
	return api.WriteJSON(w, http.StatusOK, resp)
}

func callerOf(r *http.Request) helperapi.Caller {
	who := modutil.ActorOf(r)
	return helperapi.Caller{UserID: who.UserID, Username: who.Username, Remote: api.ClientIP(r), Source: who.Source}
}

func (m *Module) install(w http.ResponseWriter, r *http.Request) error {
	return m.startLineJob(w, r, "software.install", helperapi.ActionPkgInstall)
}

func (m *Module) remove(w http.ResponseWriter, r *http.Request) error {
	return m.startLineJob(w, r, "software.remove", helperapi.ActionPkgRemove)
}

// availableOffer returns the offer of a line that can be installed here.
func (m *Module) availableOffer(id, line string) (*catalog.Software, error) {
	sw, ok := m.Catalog.Find(id)
	if !ok {
		return nil, api.ErrNotFound
	}
	params := map[string]any{"software": id, "line": line}
	for _, o := range sw.Offers(catalog.System{Chain: m.Chain, Arch: m.Arch, Now: time.Now()}) {
		if o.Line != line {
			continue
		}
		if o.Method == "" {
			params["reason"] = o.Reason
			return nil, api.NewError(http.StatusConflict, helperapi.CodePkgNotAvailable, params)
		}
		return sw, nil
	}
	return nil, api.NewError(http.StatusNotFound, "request.not_found", params)
}

// startLineJob checks that the line can be handled on this system and
// starts the job that runs the helper operation.
func (m *Module) startLineJob(w http.ResponseWriter, r *http.Request, kind, action string) error {
	var req lineRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	id := r.PathValue("id")
	sw, err := m.availableOffer(id, req.Line)
	if err != nil {
		return err
	}
	hreq := helperapi.SoftwareRequest{Software: id, Line: req.Line}
	params := map[string]any{"software": id, "line": req.Line, "name": sw.Name}
	if action == helperapi.ActionPkgInstall {
		hreq.Source, hreq.Policy = req.Source, helperPolicy(m.policy(r.Context()))
		if req.Source != "" {
			params["source"] = req.Source
		}
	} else if req.Source != "" {
		return api.NewError(http.StatusBadRequest, "validation.invalid", map[string]any{"field": "source", "rule": "unexpected"})
	}
	if err := hreq.Validate(); err != nil {
		return api.NewError(http.StatusBadRequest, "validation.invalid", map[string]any{"field": "source", "rule": "id"})
	}
	who := modutil.ActorOf(r)
	job, err := m.Jobs.Start(r.Context(), jobs.Spec{
		Kind: kind, Params: params, Permission: Read, ManagePermission: Manage,
		Actor: jobs.Actor{UserID: who.UserID, Username: who.Username, Source: who.Source, Remote: api.ClientIP(r)},
	}, func(_ context.Context, j *jobs.Job) error {
		var res helperapi.InstallResult
		if err := j.RunOp(action, hreq, &res); err != nil {
			return err
		}
		for _, p := range res.Packages {
			j.Logf("%s %s\n", p.Name, p.Version)
		}
		return nil
	})
	if err != nil {
		return err
	}
	audit := map[string]any{"software": id, "name": sw.Name, "line": req.Line, "job": job.ID}
	if req.Source != "" {
		audit["source"] = req.Source
	}
	modutil.Audit(r, m.DB, m.Log, who, kind, "ok", audit)
	return api.WriteJSON(w, http.StatusAccepted, jobResponse{Job: job})
}
