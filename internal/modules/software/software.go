// SPDX-License-Identifier: AGPL-3.0-only

// Package software serves the software catalog as offered on this system
// (which lines can be installed, how, and which one is recommended) and
// starts package jobs. Installation itself arrives with the install engine;
// this module already refreshes the package indexes as a job.
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
}

type entry struct {
	catalog.Software
	Offers []catalog.Offer `json:"offers"`
}

type listResponse struct {
	Release   string         `json:"release"` // platform profile, "" when unsupported
	Arch      string         `json:"arch"`
	Supported bool           `json:"supported"`
	Software  []entry        `json:"software"`
	Repos     []catalog.Repo `json:"repos"`
}

type jobResponse struct {
	Job jobs.Record `json:"job"`
}

func (m *Module) list(w http.ResponseWriter, r *http.Request) error {
	sys := catalog.System{Chain: m.Chain, Arch: m.Arch, Now: time.Now()}
	resp := listResponse{Release: m.Release, Arch: m.Arch, Supported: m.Release != "", Software: []entry{}, Repos: []catalog.Repo{}}
	for i := range m.Catalog.Software {
		sw := &m.Catalog.Software[i]
		resp.Software = append(resp.Software, entry{Software: *sw, Offers: sw.Offers(sys)})
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
