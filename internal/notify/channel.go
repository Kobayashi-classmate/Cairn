// SPDX-License-Identifier: AGPL-3.0-only

// Package notify is the notification center: channels (e-mail, webhook, more
// later), matching of events to channels, a durable outbox with retries, and
// rendering of messages in each channel's language.
package notify

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Kobayashi-classmate/Cairn/internal/auth"
	"github.com/Kobayashi-classmate/Cairn/internal/events"
	"github.com/Kobayashi-classmate/Cairn/internal/i18n"
	"github.com/Kobayashi-classmate/Cairn/internal/secrets"
)

// Channel types.
const (
	TypeEmail   = "email"
	TypeWebhook = "webhook"
)

// ErrNotFound is returned for unknown channels.
var ErrNotFound = errors.New("notify: channel not found")

// EmailConfig configures SMTP delivery.
type EmailConfig struct {
	Host     string   `json:"host"`
	Port     int      `json:"port"`
	Security string   `json:"security"` // starttls | tls | none
	Username string   `json:"username"`
	Password string   `json:"password,omitempty"`
	From     string   `json:"from"`
	To       []string `json:"to"`
}

// WebhookConfig configures HTTP delivery.
type WebhookConfig struct {
	URL    string `json:"url"`
	Secret string `json:"secret,omitempty"`
}

// Config is the stored configuration of a channel; exactly one is set.
type Config struct {
	Email   *EmailConfig   `json:"email,omitempty"`
	Webhook *WebhookConfig `json:"webhook,omitempty"`
}

// Channel is a configured destination.
type Channel struct {
	ID        int64
	Type      string
	Name      string
	Locale    string
	Enabled   bool
	Events    []string
	MinLevel  string
	Config    Config
	CreatedAt int64
}

// Matches reports whether e should be delivered on c.
func (c *Channel) Matches(e events.Event) bool {
	if !c.Enabled || e.Level.Rank() < events.Level(c.MinLevel).Rank() {
		return false
	}
	for _, p := range c.Events {
		if strings.HasPrefix(e.Type, p) {
			return true
		}
	}
	return false
}

// Public is the API view of a channel: credentials are replaced by flags.
type Public struct {
	ID        int64    `json:"id"`
	Type      string   `json:"type"`
	Name      string   `json:"name"`
	Locale    string   `json:"locale"`
	Enabled   bool     `json:"enabled"`
	Events    []string `json:"events"`
	MinLevel  string   `json:"minLevel"`
	Config    any      `json:"config"`
	CreatedAt int64    `json:"createdAt"`
}

type publicEmail struct {
	Host        string   `json:"host"`
	Port        int      `json:"port"`
	Security    string   `json:"security"`
	Username    string   `json:"username"`
	HasPassword bool     `json:"hasPassword"`
	From        string   `json:"from"`
	To          []string `json:"to"`
}

type publicWebhook struct {
	URL       string `json:"url"`
	HasSecret bool   `json:"hasSecret"`
}

// Public returns the redacted view of c.
func (c *Channel) Public() Public {
	p := Public{ID: c.ID, Type: c.Type, Name: c.Name, Locale: c.Locale, Enabled: c.Enabled,
		Events: c.Events, MinLevel: c.MinLevel, CreatedAt: c.CreatedAt}
	switch {
	case c.Config.Email != nil:
		e := c.Config.Email
		p.Config = publicEmail{Host: e.Host, Port: e.Port, Security: e.Security, Username: e.Username,
			HasPassword: e.Password != "", From: e.From, To: e.To}
	case c.Config.Webhook != nil:
		p.Config = publicWebhook{URL: c.Config.Webhook.URL, HasSecret: c.Config.Webhook.Secret != ""}
	}
	return p
}

// Validate checks every field and returns an *auth.FieldError on failure.
func (c *Channel) Validate() error {
	fe := func(field, rule string) error { return &auth.FieldError{Field: field, Rule: rule} }
	switch n := utf8.RuneCountInString(strings.TrimSpace(c.Name)); {
	case n == 0:
		return fe("name", "required")
	case n > 64:
		return &auth.FieldError{Field: "name", Rule: "max_length", Max: 64}
	}
	if !i18n.IsSupported(c.Locale) {
		return fe("locale", "enum")
	}
	if !events.ValidLevel(c.MinLevel) {
		return fe("minLevel", "enum")
	}
	if len(c.Events) == 0 {
		return fe("events", "required")
	}
	for _, e := range c.Events {
		if !validCategory(e) {
			return fe("events", "enum")
		}
	}
	switch c.Type {
	case TypeEmail:
		e := c.Config.Email
		if e == nil {
			return fe("type", "required")
		}
		if e.Host == "" {
			return fe("host", "required")
		}
		if strings.ContainsAny(e.Host, " /:@") && net.ParseIP(e.Host) == nil {
			return fe("host", "pattern")
		}
		if e.Port < 1 || e.Port > 65535 {
			return &auth.FieldError{Field: "port", Rule: "range", Min: 1, Max: 65535}
		}
		if e.Security != "starttls" && e.Security != "tls" && e.Security != "none" {
			return fe("security", "enum")
		}
		if _, err := mail.ParseAddress(e.From); err != nil {
			return fe("from", "email")
		}
		if len(e.To) == 0 {
			return fe("to", "required")
		}
		if len(e.To) > 20 {
			return &auth.FieldError{Field: "to", Rule: "max_length", Max: 20}
		}
		for _, t := range e.To {
			if _, err := mail.ParseAddress(t); err != nil {
				return fe("to", "email")
			}
		}
	case TypeWebhook:
		w := c.Config.Webhook
		if w == nil || w.URL == "" {
			return fe("url", "required")
		}
		u, err := url.Parse(w.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
			return fe("url", "url")
		}
		if len(w.Secret) > 256 {
			return &auth.FieldError{Field: "secret", Rule: "max_length", Max: 256}
		}
	default:
		return fe("type", "enum")
	}
	return nil
}

func validCategory(p string) bool {
	for _, c := range events.Categories {
		if c == p {
			return true
		}
	}
	return false
}

func configContext(id int64) string { return "notify:channel:" + strconv.FormatInt(id, 10) }

func sealConfig(box *secrets.Box, id int64, cfg Config) (string, error) {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	return box.Seal(raw, configContext(id))
}

// Store persists channels.
type Store struct {
	DB  *sql.DB
	Box *secrets.Box
}

const channelColumns = `id, type, name, locale, enabled, events, min_level, config, created_at`

func (s *Store) scan(row interface{ Scan(...any) error }) (*Channel, error) {
	var c Channel
	var evs, sealed string
	if err := row.Scan(&c.ID, &c.Type, &c.Name, &c.Locale, &c.Enabled, &evs, &c.MinLevel, &sealed, &c.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if err := json.Unmarshal([]byte(evs), &c.Events); err != nil {
		return nil, err
	}
	if sealed != "" {
		raw, err := s.Box.Open(sealed, configContext(c.ID))
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &c.Config); err != nil {
			return nil, err
		}
	}
	return &c, nil
}

// List returns all channels ordered by id.
func (s *Store) List(ctx context.Context) ([]*Channel, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+channelColumns+` FROM notify_channels ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Channel{}
	for rows.Next() {
		c, err := s.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Get loads one channel.
func (s *Store) Get(ctx context.Context, id int64) (*Channel, error) {
	return s.scan(s.DB.QueryRowContext(ctx, `SELECT `+channelColumns+` FROM notify_channels WHERE id = ?`, id))
}

// Create validates and inserts c, filling its ID and CreatedAt.
func (s *Store) Create(ctx context.Context, c *Channel) error {
	if err := c.Validate(); err != nil {
		return err
	}
	evs, _ := json.Marshal(c.Events)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO notify_channels(type, name, locale, enabled, events, min_level, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		c.Type, strings.TrimSpace(c.Name), c.Locale, c.Enabled, string(evs), c.MinLevel, now, now)
	if err != nil {
		return err
	}
	if c.ID, err = res.LastInsertId(); err != nil {
		return err
	}
	sealed, err := sealConfig(s.Box, c.ID, c.Config)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE notify_channels SET config = ? WHERE id = ?`, sealed, c.ID); err != nil {
		return err
	}
	c.CreatedAt = now
	return tx.Commit()
}

// Update validates and stores every field of c.
func (s *Store) Update(ctx context.Context, c *Channel) error {
	if err := c.Validate(); err != nil {
		return err
	}
	evs, _ := json.Marshal(c.Events)
	sealed, err := sealConfig(s.Box, c.ID, c.Config)
	if err != nil {
		return err
	}
	res, err := s.DB.ExecContext(ctx,
		`UPDATE notify_channels SET name = ?, locale = ?, enabled = ?, events = ?, min_level = ?, config = ?, updated_at = ? WHERE id = ?`,
		strings.TrimSpace(c.Name), c.Locale, c.Enabled, string(evs), c.MinLevel, sealed, time.Now().Unix(), c.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes a channel and its queued deliveries.
func (s *Store) Delete(ctx context.Context, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM notify_channels WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
