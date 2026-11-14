// SPDX-License-Identifier: AGPL-3.0-only

// Package module defines the contract every feature module implements and
// the registry that wires modules into the panel. Each module registers its
// own routes explicitly; modules talk to each other only through public
// interfaces, never through each other's internals.
//
// A module may describe itself (Describer): the permissions it adds, its
// menu entries, and whether it is part of the platform core. Optional
// modules can be turned off; their routes then answer module.disabled and
// their menu entries disappear. Database migrations and root helper actions
// stay in their central, ordered registries (internal/store, internal/helper)
// on purpose: migrations must apply in one deterministic order, and the
// helper's action table must stay fixed at compile time.
package module

import (
	"context"
	"database/sql"
	"net/http"
	"sort"
	"sync"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/authz"
)

// Module is a feature module of the panel.
type Module interface {
	// ID is a stable, lower-case identifier such as "core" or "system".
	ID() string
	// Routes registers the module's HTTP endpoints.
	Routes(r *api.Router)
}

// Describer is implemented by modules that add permissions or menu entries,
// or that can be turned off.
type Describer interface {
	Info() Info
}

// Info describes a module.
type Info struct {
	// Optional modules can be turned off by an administrator. Modules that
	// do not describe themselves are core modules.
	Optional bool
	// Permissions the module adds, with the roles that get them by default.
	Permissions []Permission
	// Nav lists the module's menu entries.
	Nav []NavItem
}

// Permission is a permission a module adds.
type Permission struct {
	Name  authz.Permission
	Roles []authz.Role
}

// NavItem is a menu entry. The UI maps Icon and Route to its own icon set
// and routes; unknown ones are skipped there.
type NavItem struct {
	Key        string           `json:"key"`
	LabelKey   string           `json:"labelKey"`
	Icon       string           `json:"icon"`
	Route      string           `json:"route"`
	Permission authz.Permission `json:"permission,omitempty"`
	// Order sorts the menu; lower comes first.
	Order int `json:"order"`
}

// State is a module as listed to administrators.
type State struct {
	ID       string `json:"id"`
	Optional bool   `json:"optional"`
	Enabled  bool   `json:"enabled"`
}

// Errors.
var (
	ErrDisabled    = api.NewError(http.StatusNotFound, "module.disabled", nil)
	ErrNotOptional = api.NewError(http.StatusConflict, "module.not_optional", nil)
)

type entry struct {
	m    Module
	info Info
}

// Registry holds the modules of the running panel.
type Registry struct {
	db      *sql.DB
	mods    []entry
	mu      sync.RWMutex
	enabled map[string]bool
}

// NewRegistry returns an empty registry storing module state in db.
func NewRegistry(db *sql.DB) *Registry {
	return &Registry{db: db, enabled: map[string]bool{}}
}

// Add registers modules and defines the permissions they declare. Call it
// before Mount and before serving requests.
func (r *Registry) Add(mods ...Module) {
	for _, m := range mods {
		var info Info
		if d, ok := m.(Describer); ok {
			info = d.Info()
		}
		for _, e := range r.mods {
			if e.m.ID() == m.ID() {
				panic("module: duplicate module " + m.ID())
			}
		}
		for _, p := range info.Permissions {
			authz.Define(p.Name, p.Roles...)
		}
		r.mods = append(r.mods, entry{m: m, info: info})
		r.enabled[m.ID()] = true
	}
}

// Load reads which optional modules are turned off.
func (r *Registry) Load(ctx context.Context) error {
	rows, err := r.db.QueryContext(ctx, `SELECT id, enabled FROM module_state`)
	if err != nil {
		return err
	}
	defer rows.Close()
	r.mu.Lock()
	defer r.mu.Unlock()
	for rows.Next() {
		var id string
		var on bool
		if err := rows.Scan(&id, &on); err != nil {
			return err
		}
		if e, ok := r.find(id); ok && e.info.Optional {
			r.enabled[id] = on
		}
	}
	return rows.Err()
}

func (r *Registry) find(id string) (entry, bool) {
	for _, e := range r.mods {
		if e.m.ID() == id {
			return e, true
		}
	}
	return entry{}, false
}

// Mount registers every module's routes. Routes of a module that is turned
// off answer module.disabled.
func (r *Registry) Mount(router *api.Router) {
	for _, e := range r.mods {
		id := e.m.ID()
		e.m.Routes(router.ForModule(id, func() error {
			if !r.Enabled(id) {
				return ErrDisabled
			}
			return nil
		}))
	}
}

// Enabled reports whether a module is on. Background work of optional
// modules checks it too.
func (r *Registry) Enabled(id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.enabled[id]
}

// SetEnabled turns an optional module on or off and stores the choice.
func (r *Registry) SetEnabled(ctx context.Context, id string, on bool) error {
	e, ok := r.find(id)
	if !ok {
		return api.ErrNotFound
	}
	if !e.info.Optional {
		return ErrNotOptional
	}
	if _, err := r.db.ExecContext(ctx, `INSERT INTO module_state (id, enabled) VALUES (?, ?)
		ON CONFLICT (id) DO UPDATE SET enabled = excluded.enabled`, id, on); err != nil {
		return err
	}
	r.mu.Lock()
	r.enabled[id] = on
	r.mu.Unlock()
	return nil
}

// States lists the modules in registration order.
func (r *Registry) States() []State {
	out := make([]State, 0, len(r.mods))
	for _, e := range r.mods {
		out = append(out, State{ID: e.m.ID(), Optional: e.info.Optional, Enabled: r.Enabled(e.m.ID())})
	}
	return out
}

// Nav returns the menu entries of enabled modules that can sees, sorted.
func (r *Registry) Nav(can func(authz.Permission) bool) []NavItem {
	out := []NavItem{}
	for _, e := range r.mods {
		if !r.Enabled(e.m.ID()) {
			continue
		}
		for _, n := range e.info.Nav {
			if n.Permission == "" || can(n.Permission) {
				out = append(out, n)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Order < out[j].Order })
	return out
}

// IDs returns the registered module IDs (for tests and diagnostics).
func (r *Registry) IDs() []string {
	ids := make([]string, 0, len(r.mods))
	for _, e := range r.mods {
		ids = append(ids, e.m.ID())
	}
	return ids
}
