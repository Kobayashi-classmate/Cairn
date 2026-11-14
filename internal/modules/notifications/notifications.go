// SPDX-License-Identifier: AGPL-3.0-only

// Package notifications exposes the notification center: channel management
// (credentials are write-only), test messages and the delivery log.
package notifications

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/authz"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/modutil"
	"github.com/Kobayashi-classmate/Cairn/internal/notify"
)

// Module implements module.Module.
type Module struct {
	DB       *sql.DB
	Store    *notify.Store
	Notifier *notify.Notifier
	Log      *slog.Logger
}

// ID implements module.Module.
func (m *Module) ID() string { return "notifications" }

// Routes implements module.Module.
func (m *Module) Routes(r *api.Router) {
	manage := api.Require(authz.NotifyManage)
	doc := func(s string) api.RouteOption { return api.Doc("notify", s) }
	r.Handle("GET /api/v1/notify/channels", m.list, manage, doc("List notification channels"), api.Returns(200, listResponse{}))
	r.Handle("POST /api/v1/notify/channels", m.create, manage, api.StepUp(), doc("Create a channel"),
		api.Body(channelRequest{}), api.Returns(201, channelResponse{}))
	r.Handle("PATCH /api/v1/notify/channels/{id}", m.update, manage, api.StepUp(), doc("Update a channel; omitted fields are kept"),
		api.Body(channelRequest{}), api.Returns(200, channelResponse{}))
	r.Handle("DELETE /api/v1/notify/channels/{id}", m.delete, manage, api.StepUp(), doc("Delete a channel"), api.Returns(204, nil))
	r.Handle("POST /api/v1/notify/channels/{id}/test", m.test, manage, doc("Send a test message now"), api.Returns(204, nil))
	r.Handle("GET /api/v1/notify/deliveries", m.deliveries, manage, doc("Recent deliveries, newest first"), api.Returns(200, deliveriesResponse{}))
}

type listResponse struct {
	Channels []notify.Public `json:"channels"`
}

type channelResponse struct {
	Channel notify.Public `json:"channel"`
}

// configRequest carries the fields of both channel types; pointers tell
// "not sent" (keep) from "sent empty" (clear) for the credentials.
type configRequest struct {
	Host     *string   `json:"host,omitempty"`
	Port     *int      `json:"port,omitempty"`
	Security *string   `json:"security,omitempty"`
	Username *string   `json:"username,omitempty"`
	Password *string   `json:"password,omitempty"`
	From     *string   `json:"from,omitempty"`
	To       *[]string `json:"to,omitempty"`
	URL      *string   `json:"url,omitempty"`
	Secret   *string   `json:"secret,omitempty"`
}

type channelRequest struct {
	Type     *string        `json:"type,omitempty"`
	Name     *string        `json:"name,omitempty"`
	Locale   *string        `json:"locale,omitempty"`
	Enabled  *bool          `json:"enabled,omitempty"`
	Events   *[]string      `json:"events,omitempty"`
	MinLevel *string        `json:"minLevel,omitempty"`
	Config   *configRequest `json:"config,omitempty"`
}

// apply merges req into c. Only fields present in the request change.
func (req *channelRequest) apply(c *notify.Channel) {
	set := func(dst *string, src *string) {
		if src != nil {
			*dst = *src
		}
	}
	set(&c.Name, req.Name)
	set(&c.Locale, req.Locale)
	set(&c.MinLevel, req.MinLevel)
	if req.Enabled != nil {
		c.Enabled = *req.Enabled
	}
	if req.Events != nil {
		c.Events = *req.Events
	}
	cfg := req.Config
	if cfg == nil {
		return
	}
	switch c.Type {
	case notify.TypeEmail:
		if c.Config.Email == nil {
			c.Config.Email = &notify.EmailConfig{}
		}
		e := c.Config.Email
		set(&e.Host, cfg.Host)
		set(&e.Security, cfg.Security)
		set(&e.Username, cfg.Username)
		set(&e.Password, cfg.Password)
		set(&e.From, cfg.From)
		if cfg.Port != nil {
			e.Port = *cfg.Port
		}
		if cfg.To != nil {
			e.To = *cfg.To
		}
	case notify.TypeWebhook:
		if c.Config.Webhook == nil {
			c.Config.Webhook = &notify.WebhookConfig{}
		}
		set(&c.Config.Webhook.URL, cfg.URL)
		set(&c.Config.Webhook.Secret, cfg.Secret)
	}
}

func (m *Module) list(w http.ResponseWriter, r *http.Request) error {
	channels, err := m.Store.List(r.Context())
	if err != nil {
		return err
	}
	out := make([]notify.Public, 0, len(channels))
	for _, c := range channels {
		out = append(out, c.Public())
	}
	return api.WriteJSON(w, http.StatusOK, listResponse{Channels: out})
}

func (m *Module) create(w http.ResponseWriter, r *http.Request) error {
	var req channelRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	c := &notify.Channel{Enabled: true}
	if req.Type != nil {
		c.Type = *req.Type
	}
	req.apply(c)
	if err := m.Store.Create(r.Context(), c); err != nil {
		return modutil.MapError(err, nil)
	}
	modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "notify.channel_create", "ok", map[string]any{"name": c.Name, "type": c.Type})
	return api.WriteJSON(w, http.StatusCreated, channelResponse{Channel: c.Public()})
}

func (m *Module) load(r *http.Request) (*notify.Channel, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return nil, api.ErrNotFound
	}
	c, err := m.Store.Get(r.Context(), id)
	if errors.Is(err, notify.ErrNotFound) {
		return nil, api.ErrNotFound
	}
	return c, err
}

func (m *Module) update(w http.ResponseWriter, r *http.Request) error {
	var req channelRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	c, err := m.load(r)
	if err != nil {
		return err
	}
	if req.Type != nil && *req.Type != c.Type {
		return api.NewError(http.StatusBadRequest, "validation.invalid", map[string]any{"field": "type", "rule": "immutable"})
	}
	req.apply(c)
	if err := m.Store.Update(r.Context(), c); err != nil {
		return modutil.MapError(err, nil)
	}
	modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "notify.channel_update", "ok", map[string]any{"name": c.Name, "type": c.Type})
	return api.WriteJSON(w, http.StatusOK, channelResponse{Channel: c.Public()})
}

func (m *Module) delete(w http.ResponseWriter, r *http.Request) error {
	c, err := m.load(r)
	if err != nil {
		return err
	}
	if err := m.Store.Delete(r.Context(), c.ID); err != nil {
		return err
	}
	modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "notify.channel_delete", "ok", map[string]any{"name": c.Name, "type": c.Type})
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (m *Module) test(w http.ResponseWriter, r *http.Request) error {
	c, err := m.load(r)
	if err != nil {
		return err
	}
	who := modutil.ActorOf(r)
	if err := m.Notifier.Test(r.Context(), c.ID, who.Username); err != nil {
		modutil.Audit(r, m.DB, m.Log, who, "notify.test", "notify.delivery_failed", map[string]any{"name": c.Name, "type": c.Type})
		return api.NewError(http.StatusBadGateway, "notify.delivery_failed", map[string]any{"reason": err.Error()})
	}
	modutil.Audit(r, m.DB, m.Log, who, "notify.test", "ok", map[string]any{"name": c.Name, "type": c.Type})
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type deliveriesResponse struct {
	Deliveries []notify.Delivery `json:"deliveries"`
}

func (m *Module) deliveries(w http.ResponseWriter, r *http.Request) error {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := m.Notifier.Deliveries(r.Context(), limit)
	if err != nil {
		return err
	}
	return api.WriteJSON(w, http.StatusOK, deliveriesResponse{Deliveries: list})
}
