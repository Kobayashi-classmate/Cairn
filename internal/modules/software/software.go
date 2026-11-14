// SPDX-License-Identifier: AGPL-3.0-only

// Package software serves the software catalog as offered on this system
// (which lines can be installed, how, which one is recommended and which are
// installed) and starts package jobs: refreshing the indexes, installing and
// removing a line. The root helper resolves every request against its own
// copy of the catalog; this module only names an entry and a line.
package software

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"sort"
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
	r.Handle("POST /api/v1/software/{id}/install", m.install, api.Require(Manage), api.Body(lineRequest{}),
		doc("Install a line: the exact newest version of the line from its source (starts a job)"), api.Returns(202, jobResponse{}))
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

// startLineJob checks that the line can be handled on this system and
// starts the job that runs the helper operation.
func (m *Module) startLineJob(w http.ResponseWriter, r *http.Request, kind, action string) error {
	var req lineRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	id := r.PathValue("id")
	sw, ok := m.Catalog.Find(id)
	if !ok {
		return api.ErrNotFound
	}
	var offer *catalog.Offer
	for _, o := range sw.Offers(catalog.System{Chain: m.Chain, Arch: m.Arch, Now: time.Now()}) {
		if o.Line == req.Line {
			offer = &o
		}
	}
	params := map[string]any{"software": id, "line": req.Line}
	switch {
	case offer == nil:
		return api.NewError(http.StatusNotFound, "request.not_found", params)
	case offer.Method != catalog.MethodSystem && offer.Method != catalog.MethodVendor:
		// Prebuilt runtimes come with the next step of the install engine.
		params["reason"] = offer.Reason
		if offer.Method == catalog.MethodStandalone {
			params["reason"] = "standalone"
		}
		return api.NewError(http.StatusConflict, helperapi.CodePkgNotAvailable, params)
	}
	who := modutil.ActorOf(r)
	params["name"] = sw.Name
	job, err := m.Jobs.Start(r.Context(), jobs.Spec{
		Kind: kind, Params: params, Permission: Read, ManagePermission: Manage,
		Actor: jobs.Actor{UserID: who.UserID, Username: who.Username, Source: who.Source, Remote: api.ClientIP(r)},
	}, func(_ context.Context, j *jobs.Job) error {
		var res helperapi.InstallResult
		if err := j.RunOp(action, helperapi.SoftwareRequest{Software: id, Line: req.Line}, &res); err != nil {
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
	modutil.Audit(r, m.DB, m.Log, who, kind, "ok", map[string]any{"software": id, "name": sw.Name, "line": req.Line, "job": job.ID})
	return api.WriteJSON(w, http.StatusAccepted, jobResponse{Job: job})
}
