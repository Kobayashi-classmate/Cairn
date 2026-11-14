// SPDX-License-Identifier: AGPL-3.0-only

package notify

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/events"
	"github.com/Kobayashi-classmate/Cairn/internal/i18n"
)

// Delivery policy.
const (
	maxAttempts  = 6
	dedupeWindow = 10 * time.Minute
	sendTimeout  = 30 * time.Second
	batchSize    = 20
	pollInterval = 5 * time.Second
)

// backoff is the wait after the n-th failed attempt (1-based).
var backoff = []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute, time.Hour}

// Notifier turns events into deliveries and works through the outbox.
type Notifier struct {
	Store   *Store
	Catalog *i18n.Catalog
	Senders map[string]Sender
	Log     *slog.Logger
	Host    string

	wake chan struct{}
	now  func() time.Time
}

// New returns a notifier with the default e-mail and webhook senders.
func New(store *Store, catalog *i18n.Catalog, log *slog.Logger) *Notifier {
	host, _ := os.Hostname()
	return &Notifier{
		Store: store, Catalog: catalog, Log: log, Host: host,
		Senders: map[string]Sender{TypeEmail: EmailSender{}, TypeWebhook: WebhookSender{}},
		wake:    make(chan struct{}, 1),
		now:     time.Now,
	}
}

// Handle queues e on every matching channel. It is the event-bus subscriber
// and only writes to the database; sending happens in Run.
func (n *Notifier) Handle(e events.Event) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	channels, err := n.Store.List(ctx)
	if err != nil {
		n.Log.Error("notify: list channels", "err", err)
		return
	}
	params, _ := json.Marshal(e.Params)
	now := n.now()
	queued := false
	for _, c := range channels {
		if !c.Matches(e) {
			continue
		}
		if e.DedupeKey != "" {
			var dup int
			err := n.Store.DB.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM notify_outbox WHERE channel_id = ? AND dedupe_key = ? AND created_at > ?`,
				c.ID, e.DedupeKey, now.Add(-dedupeWindow).Unix()).Scan(&dup)
			if err == nil && dup > 0 {
				continue
			}
		}
		if _, err := n.Store.DB.ExecContext(ctx,
			`INSERT INTO notify_outbox(channel_id, event_type, level, params, dedupe_key, status, next_attempt_at, created_at)
			 VALUES(?, ?, ?, ?, ?, 'pending', ?, ?)`,
			c.ID, e.Type, string(e.Level), string(params), e.DedupeKey, now.Unix(), e.TS); err != nil {
			n.Log.Error("notify: enqueue", "channel", c.ID, "err", err)
			continue
		}
		queued = true
	}
	if queued {
		n.poke()
	}
}

func (n *Notifier) poke() {
	select {
	case n.wake <- struct{}{}:
	default:
	}
}

// Run delivers due outbox entries until ctx is cancelled.
func (n *Notifier) Run(ctx context.Context) {
	t := time.NewTicker(pollInterval)
	defer t.Stop()
	for {
		n.drain(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-n.wake:
		}
	}
}

type outboxRow struct {
	id, channelID int64
	event, level  string
	params        string
	attempts      int
	createdAt     int64
}

func (n *Notifier) drain(ctx context.Context) {
	rows, err := n.Store.DB.QueryContext(ctx,
		`SELECT id, channel_id, event_type, level, params, attempts, created_at FROM notify_outbox
		 WHERE status = 'pending' AND next_attempt_at <= ? ORDER BY id LIMIT ?`, n.now().Unix(), batchSize)
	if err != nil {
		if ctx.Err() == nil {
			n.Log.Error("notify: read outbox", "err", err)
		}
		return
	}
	var due []outboxRow
	for rows.Next() {
		var r outboxRow
		if err := rows.Scan(&r.id, &r.channelID, &r.event, &r.level, &r.params, &r.attempts, &r.createdAt); err == nil {
			due = append(due, r)
		}
	}
	rows.Close()
	for _, r := range due {
		if ctx.Err() != nil {
			return
		}
		n.deliver(ctx, r)
	}
}

func (n *Notifier) deliver(ctx context.Context, r outboxRow) {
	c, err := n.Store.Get(ctx, r.channelID)
	if err != nil {
		n.finish(ctx, r, err)
		return
	}
	var params map[string]any
	_ = json.Unmarshal([]byte(r.params), &params)
	err = n.send(ctx, c, events.Event{Type: r.event, Level: events.Level(r.level), Params: params, TS: r.createdAt})
	n.finish(ctx, r, err)
}

// finish records the outcome of one attempt and schedules a retry if needed.
func (n *Notifier) finish(ctx context.Context, r outboxRow, sendErr error) {
	now := n.now()
	var err error
	if sendErr == nil {
		_, err = n.Store.DB.ExecContext(ctx,
			`UPDATE notify_outbox SET status = 'sent', attempts = attempts + 1, sent_at = ?, last_error = '' WHERE id = ?`, now.Unix(), r.id)
	} else {
		attempt := r.attempts + 1
		status, next := "pending", now
		if attempt >= maxAttempts || errors.Is(sendErr, ErrNotFound) {
			status = "failed"
		} else {
			next = now.Add(backoff[min(attempt, len(backoff))-1])
		}
		_, err = n.Store.DB.ExecContext(ctx,
			`UPDATE notify_outbox SET status = ?, attempts = ?, next_attempt_at = ?, last_error = ? WHERE id = ?`,
			status, attempt, next.Unix(), truncate(sendErr.Error(), 300), r.id)
		n.Log.Warn("notify: delivery failed", "outbox", r.id, "channel", r.channelID, "attempt", attempt, "err", sendErr)
	}
	if err != nil && ctx.Err() == nil {
		n.Log.Error("notify: update outbox", "err", err)
	}
}

func (n *Notifier) send(ctx context.Context, c *Channel, e events.Event) error {
	s, ok := n.Senders[c.Type]
	if !ok {
		return errors.New("no sender for channel type " + c.Type)
	}
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	return s.Send(ctx, c, n.Render(c.Locale, e))
}

// Render builds the message for e in locale. Texts come from the backend
// catalog: notify.<event>.title and notify.<event>.body.
func (n *Notifier) Render(locale string, e events.Event) Message {
	data := map[string]any{}
	for k, v := range e.Params {
		data[k] = v
	}
	data["host"] = n.Host
	// Codes inside parameters are translated too, so a message never shows
	// a raw identifier in another language.
	if a, ok := e.Params["action"].(string); ok {
		data["actionText"] = n.Catalog.T(locale, "notify.action."+a, nil)
	}
	// Multilingual texts (official messages) are reduced to the channel's
	// language, falling back to English.
	if titles, ok := e.Params["titles"].(map[string]any); ok {
		title, _ := titles[locale].(string)
		if title == "" {
			title, _ = titles["en"].(string)
		}
		data["title"] = title
	}
	return Message{
		Event: e.Type, Level: string(e.Level), TS: e.TS, Params: e.Params, Host: n.Host,
		Title: n.Catalog.T(locale, "notify."+e.Type+".title", data),
		Body:  n.Catalog.T(locale, "notify."+e.Type+".body", data),
	}
}

// Test sends a system.test message on one channel right away and records
// the attempt in the outbox. The error, if any, is the sender's.
func (n *Notifier) Test(ctx context.Context, channelID int64, actor string) error {
	c, err := n.Store.Get(ctx, channelID)
	if err != nil {
		return err
	}
	e := events.Event{Type: events.SystemTest, Level: events.Info, Params: map[string]any{"actor": actor}, TS: n.now().Unix()}
	sendErr := n.send(ctx, c, e)
	params, _ := json.Marshal(e.Params)
	status, sentAt, lastErr := "sent", e.TS, ""
	if sendErr != nil {
		status, sentAt, lastErr = "failed", 0, truncate(sendErr.Error(), 300)
	}
	if _, err := n.Store.DB.ExecContext(ctx,
		`INSERT INTO notify_outbox(channel_id, event_type, level, params, status, attempts, next_attempt_at, last_error, created_at, sent_at)
		 VALUES(?, ?, ?, ?, ?, 1, ?, ?, ?, ?)`,
		c.ID, e.Type, string(e.Level), string(params), status, e.TS, lastErr, e.TS, sentAt); err != nil {
		return err
	}
	return sendErr
}

// Delivery is one outbox entry as shown to administrators.
type Delivery struct {
	ID          int64  `json:"id"`
	ChannelID   int64  `json:"channelId"`
	ChannelName string `json:"channelName"`
	Event       string `json:"event"`
	Level       string `json:"level"`
	Status      string `json:"status"`
	Attempts    int    `json:"attempts"`
	LastError   string `json:"lastError"`
	CreatedAt   int64  `json:"createdAt"`
	SentAt      int64  `json:"sentAt"`
}

// Deliveries lists recent outbox entries, newest first.
func (n *Notifier) Deliveries(ctx context.Context, limit int) ([]Delivery, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := n.Store.DB.QueryContext(ctx,
		`SELECT o.id, o.channel_id, COALESCE(c.name, ''), o.event_type, o.level, o.status, o.attempts, o.last_error, o.created_at, o.sent_at
		 FROM notify_outbox o LEFT JOIN notify_channels c ON c.id = o.channel_id ORDER BY o.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Delivery{}
	for rows.Next() {
		var d Delivery
		if err := rows.Scan(&d.ID, &d.ChannelID, &d.ChannelName, &d.Event, &d.Level, &d.Status, &d.Attempts, &d.LastError, &d.CreatedAt, &d.SentAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
