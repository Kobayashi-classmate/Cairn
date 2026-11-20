// SPDX-License-Identifier: AGPL-3.0-only

// Package users lets administrators manage panel accounts. Every change is
// protected by step-up re-authentication, and the last active administrator
// can never be removed, disabled or demoted.
package users

import (
	"database/sql"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/auth"
	"github.com/Kobayashi-classmate/Cairn/internal/authz"
	"github.com/Kobayashi-classmate/Cairn/internal/events"
	"github.com/Kobayashi-classmate/Cairn/internal/module"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/modutil"
)

// Module implements module.Module.
type Module struct {
	DB     *sql.DB
	Events *events.Bus // may be nil
	Log    *slog.Logger
}

// ID implements module.Module.
func (m *Module) ID() string { return "users" }

// Info implements module.Describer.
func (m *Module) Info() module.Info {
	return module.Info{Nav: []module.NavItem{
		{Key: "users", LabelKey: "common.nav.users", Icon: "people", Route: "users", Permission: authz.UsersManage, Group: module.GroupSystem, Order: 80},
	}}
}

// Routes implements module.Module.
func (m *Module) Routes(r *api.Router) {
	manage := api.Require(authz.UsersManage)
	doc := func(s string) api.RouteOption { return api.Doc("users", s) }
	noContent := api.Returns(http.StatusNoContent, nil)
	r.Handle("GET /api/v1/users", m.list, manage, doc("List accounts"), api.Returns(200, listResponse{}))
	r.Handle("POST /api/v1/users", m.create, manage, api.StepUp(), doc("Create an account"),
		api.Body(createRequest{}), api.Returns(201, userResponse{}))
	r.Handle("PATCH /api/v1/users/{id}", m.update, manage, api.StepUp(), doc("Change role or disable/enable an account"),
		api.Body(updateRequest{}), api.Returns(200, userResponse{}))
	r.Handle("PUT /api/v1/users/{id}/password", m.setPassword, manage, api.StepUp(), doc("Set a new password and sign the user out"),
		api.Body(passwordRequest{}), noContent)
	r.Handle("DELETE /api/v1/users/{id}/totp", m.resetTOTP, manage, api.StepUp(), doc("Turn off an account's two-step sign-in"), noContent)
	r.Handle("DELETE /api/v1/users/{id}", m.delete, manage, api.StepUp(), doc("Delete an account"), noContent)
}

// changed announces an account change made by the current user.
func (m *Module) changed(r *http.Request, action, username string) {
	actor := modutil.ActorOf(r).Username
	m.Events.Publish(events.Event{Type: events.UsersChange, Level: events.Info,
		Params: map[string]any{"actor": actor, "action": action, "username": username}})
	if action == "reset_totp" {
		m.Events.Publish(events.Event{Type: events.SecurityTOTPDisabled, Level: events.Warning,
			Params: map[string]any{"actor": actor, "username": username}})
	}
}

var (
	errLastAdmin = api.NewError(http.StatusConflict, "users.last_admin", nil)
	errSelf      = api.NewError(http.StatusConflict, "users.cannot_modify_self", nil)
)

type listResponse struct {
	Users []*auth.User `json:"users"`
}

type userResponse struct {
	User *auth.User `json:"user"`
}

func (m *Module) list(w http.ResponseWriter, r *http.Request) error {
	list, err := auth.ListUsers(r.Context(), m.DB)
	if err != nil {
		return err
	}
	if list == nil {
		list = []*auth.User{}
	}
	return api.WriteJSON(w, http.StatusOK, listResponse{Users: list})
}

type createRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

func (m *Module) create(w http.ResponseWriter, r *http.Request) error {
	var req createRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	u, err := auth.CreateUser(r.Context(), m.DB, req.Username, req.Password, "", req.Role)
	if err != nil {
		return modutil.MapError(err, nil)
	}
	modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "users.create", "ok", map[string]any{"username": u.Username, "role": u.Role})
	m.changed(r, "create", u.Username)
	return api.WriteJSON(w, http.StatusCreated, userResponse{User: u})
}

// pathID parses the {id} path segment.
func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, api.ErrNotFound
	}
	return id, nil
}

type updateRequest struct {
	Role     *string `json:"role"`
	Disabled *bool   `json:"disabled"`
}

// update changes role and/or disabled state atomically with the safeguards:
// no change to oneself, and at least one active administrator remains.
func (m *Module) update(w http.ResponseWriter, r *http.Request) error {
	var req updateRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	id, err := pathID(r)
	if err != nil {
		return err
	}
	if req.Role != nil {
		if err := auth.ValidateRole(*req.Role); err != nil {
			return modutil.MapError(err, nil)
		}
	}
	ctx := r.Context()
	p, _ := api.PrincipalFrom(ctx)

	tx, err := m.DB.BeginTx(ctx, nil) // BEGIN IMMEDIATE: check and change cannot interleave
	if err != nil {
		return err
	}
	defer tx.Rollback()
	u, err := auth.UserByID(ctx, tx, id)
	if err != nil {
		return modutil.MapError(err, nil)
	}
	newRole := u.Role
	if req.Role != nil {
		newRole = *req.Role
	}
	newDisabled := u.Disabled
	if req.Disabled != nil {
		newDisabled = *req.Disabled
	}
	if u.ID == p.UserID && (newRole != u.Role || newDisabled != u.Disabled) {
		return errSelf
	}
	wasActiveAdmin := u.Role == string(authz.Admin) && !u.Disabled
	staysActiveAdmin := newRole == string(authz.Admin) && !newDisabled
	if wasActiveAdmin && !staysActiveAdmin {
		if n, err := auth.CountActiveAdmins(ctx, tx); err != nil {
			return err
		} else if n <= 1 {
			return errLastAdmin
		}
	}
	params := map[string]any{"username": u.Username}
	if newRole != u.Role {
		if err := auth.SetRole(ctx, tx, u.ID, newRole); err != nil {
			return modutil.MapError(err, nil)
		}
		params["role"] = newRole
	}
	if newDisabled != u.Disabled {
		if err := auth.SetDisabled(ctx, tx, u.ID, newDisabled); err != nil {
			return err
		}
		params["disabled"] = newDisabled
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "users.update", "ok", params)
	m.changed(r, "update", u.Username)
	u, err = auth.UserByID(ctx, m.DB, id)
	if err != nil {
		return err
	}
	return api.WriteJSON(w, http.StatusOK, userResponse{User: u})
}

type passwordRequest struct {
	Password string `json:"password"`
}

func (m *Module) setPassword(w http.ResponseWriter, r *http.Request) error {
	var req passwordRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	id, err := pathID(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	u, err := auth.UserByID(ctx, m.DB, id)
	if err != nil {
		return modutil.MapError(err, nil)
	}
	if err := auth.SetPassword(ctx, m.DB, u.ID, req.Password); err != nil {
		return modutil.MapError(err, nil)
	}
	if err := auth.DeleteUserSessions(ctx, m.DB, u.ID); err != nil {
		return err
	}
	if err := auth.ResetFailures(ctx, m.DB, auth.UserKey(u.Username)); err != nil {
		return err
	}
	modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "users.set_password", "ok", map[string]any{"username": u.Username})
	m.changed(r, "set_password", u.Username)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (m *Module) resetTOTP(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	u, err := auth.UserByID(r.Context(), m.DB, id)
	if err != nil {
		return modutil.MapError(err, nil)
	}
	if err := auth.DisableTOTP(r.Context(), m.DB, u.ID); err != nil {
		return err
	}
	modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "users.reset_totp", "ok", map[string]any{"username": u.Username})
	m.changed(r, "reset_totp", u.Username)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (m *Module) delete(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	p, _ := api.PrincipalFrom(ctx)
	if id == p.UserID {
		return errSelf
	}
	tx, err := m.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	u, err := auth.UserByID(ctx, tx, id)
	if err != nil {
		return modutil.MapError(err, nil)
	}
	if u.Role == string(authz.Admin) && !u.Disabled {
		if n, err := auth.CountActiveAdmins(ctx, tx); err != nil {
			return err
		} else if n <= 1 {
			return errLastAdmin
		}
	}
	if err := auth.DeleteUser(ctx, tx, u.ID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "users.delete", "ok", map[string]any{"username": u.Username})
	m.changed(r, "delete", u.Username)
	w.WriteHeader(http.StatusNoContent)
	return nil
}
