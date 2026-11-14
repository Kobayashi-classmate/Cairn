// SPDX-License-Identifier: AGPL-3.0-only

package notify

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/auth"
	"github.com/Kobayashi-classmate/Cairn/internal/events"
	"github.com/Kobayashi-classmate/Cairn/internal/i18n"
	"github.com/Kobayashi-classmate/Cairn/internal/secrets"
	"github.com/Kobayashi-classmate/Cairn/internal/store"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "cairn.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	box, err := secrets.LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	return &Store{DB: db, Box: box}
}

func webhookChannel(url, secret string) *Channel {
	return &Channel{Type: TypeWebhook, Name: "hook", Locale: "en", Enabled: true,
		Events: []string{"security."}, MinLevel: "info", Config: Config{Webhook: &WebhookConfig{URL: url, Secret: secret}}}
}

func TestValidation(t *testing.T) {
	ok := &Channel{Type: TypeEmail, Name: "ops mail", Locale: "zh-CN", Enabled: true, Events: []string{"security.", "users."},
		MinLevel: "warning", Config: Config{Email: &EmailConfig{Host: "smtp.example.com", Port: 587, Security: "starttls", From: "Cairn <cairn@example.com>", To: []string{"ops@example.com"}}}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid channel rejected: %v", err)
	}
	cases := map[string]func(c *Channel){
		"name:required":   func(c *Channel) { c.Name = "  " },
		"locale:enum":     func(c *Channel) { c.Locale = "fr" },
		"minLevel:enum":   func(c *Channel) { c.MinLevel = "debug" },
		"events:enum":     func(c *Channel) { c.Events = []string{"everything"} },
		"events:required": func(c *Channel) { c.Events = nil },
		"port:range":      func(c *Channel) { c.Config.Email.Port = 70000 },
		"security:enum":   func(c *Channel) { c.Config.Email.Security = "ssl3" },
		"from:email":      func(c *Channel) { c.Config.Email.From = "not an address" },
		"to:email":        func(c *Channel) { c.Config.Email.To = []string{"x@"} },
		"host:pattern":    func(c *Channel) { c.Config.Email.Host = "user@host:25" },
	}
	for want, mutate := range cases {
		c := *ok
		e := *ok.Config.Email
		c.Config = Config{Email: &e}
		mutate(&c)
		var fe *auth.FieldError
		if err := c.Validate(); !errors.As(err, &fe) || fe.Field+":"+fe.Rule != want {
			t.Errorf("%s: got %v", want, err)
		}
	}
	for _, u := range []string{"ftp://x", "javascript:alert(1)", "https://user:pw@example.com/", "//nohost"} {
		var fe *auth.FieldError
		if err := webhookChannel(u, "").Validate(); !errors.As(err, &fe) || fe.Field != "url" {
			t.Errorf("url %q accepted: %v", u, err)
		}
	}
}

func TestStoreSealsCredentials(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	c := &Channel{Type: TypeEmail, Name: "mail", Locale: "en", Enabled: true, Events: []string{"security."}, MinLevel: "info",
		Config: Config{Email: &EmailConfig{Host: "smtp.example.com", Port: 465, Security: "tls", Username: "u", Password: "smtp-secret-pw", From: "a@example.com", To: []string{"b@example.com"}}}}
	if err := s.Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	var raw string
	_ = s.DB.QueryRow(`SELECT config FROM notify_channels WHERE id = ?`, c.ID).Scan(&raw)
	if strings.Contains(raw, "smtp-secret-pw") || strings.Contains(raw, "smtp.example.com") {
		t.Fatal("channel config stored in plaintext")
	}
	got, err := s.Get(ctx, c.ID)
	if err != nil || got.Config.Email.Password != "smtp-secret-pw" {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	pub, _ := json.Marshal(got.Public())
	if strings.Contains(string(pub), "smtp-secret-pw") || !strings.Contains(string(pub), `"hasPassword":true`) {
		t.Fatalf("public view leaks or lacks flag: %s", pub)
	}
}

func TestWebhookIsSignedAndChecksStatus(t *testing.T) {
	var mu sync.Mutex
	var gotSig, gotTS, gotBody string
	status := http.StatusNoContent
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotSig, gotTS, gotBody = r.Header.Get("X-Cairn-Signature"), r.Header.Get("X-Cairn-Timestamp"), string(b)
		mu.Unlock()
		w.WriteHeader(status)
	}))
	defer srv.Close()
	c := webhookChannel(srv.URL, "hook-secret")
	m := Message{Event: events.SecurityLogin, Level: "info", Title: "t", Body: "b", Params: map[string]any{"username": "admin"}}
	if err := (WebhookSender{}).Send(context.Background(), c, m); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if want := Sign("hook-secret", gotTS, []byte(gotBody)); gotSig != want || !strings.Contains(gotBody, `"username":"admin"`) {
		t.Fatalf("signature %q want %q body %s", gotSig, want, gotBody)
	}
	mu.Unlock()
	status = http.StatusInternalServerError
	if err := (WebhookSender{}).Send(context.Background(), c, m); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("non-2xx must fail: %v", err)
	}
}

// fakeSMTP accepts one message without TLS or authentication.
func fakeSMTP(t *testing.T) (addr string, data <-chan string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	out := make(chan string, 1)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		say := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
		say("220 localhost ESMTP test")
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			cmd := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(cmd, "EHLO"):
				say("250-localhost")
				say("250 8BITMIME")
			case strings.HasPrefix(cmd, "MAIL"), strings.HasPrefix(cmd, "RCPT"):
				say("250 OK")
			case cmd == "DATA":
				say("354 go ahead")
				var b strings.Builder
				for {
					l, err := r.ReadString('\n')
					if err != nil || l == ".\r\n" {
						break
					}
					b.WriteString(l)
				}
				out <- b.String()
				say("250 queued")
			case cmd == "QUIT":
				say("221 bye")
				return
			default:
				say("250 OK")
			}
		}
	}()
	return l.Addr().String(), out
}

func TestEmailSender(t *testing.T) {
	addr, data := fakeSMTP(t)
	host, port, _ := net.SplitHostPort(addr)
	p, _ := strconv.Atoi(port)
	c := &Channel{Type: TypeEmail, Config: Config{Email: &EmailConfig{Host: host, Port: p, Security: "none", From: "Cairn <cairn@example.com>", To: []string{"ops@example.com"}}}}
	// Non-ASCII subject exercises RFC 2047 encoding.
	m := Message{Event: events.SystemTest, TS: time.Now().Unix(), Title: "[Cairn] Prüfung subject", Body: "hello body", Host: "web1"}
	if err := (EmailSender{}).Send(context.Background(), c, m); err != nil {
		t.Fatal(err)
	}
	msg := <-data
	var subject, body string
	parts := strings.SplitN(msg, "\r\n\r\n", 2)
	for _, h := range strings.Split(parts[0], "\r\n") {
		if v, ok := strings.CutPrefix(h, "Subject: "); ok {
			subject, _ = new(mime.WordDecoder).DecodeHeader(v)
		}
	}
	raw, _ := base64.StdEncoding.DecodeString(strings.ReplaceAll(parts[1], "\r\n", ""))
	body = string(raw)
	if subject != m.Title || !strings.Contains(body, "hello body") || !strings.Contains(body, "web1") {
		t.Fatalf("subject %q body %q", subject, body)
	}

	c.Config.Email.Username = "u"
	if err := (EmailSender{}).Send(context.Background(), c, m); err == nil || !strings.Contains(err.Error(), "without TLS") {
		t.Fatalf("credentials over plaintext must be refused: %v", err)
	}
}

type recordingSender struct {
	mu   sync.Mutex
	fail error
	got  []Message
}

func (r *recordingSender) Send(_ context.Context, _ *Channel, m Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, m)
	return r.fail
}

func TestNotifierMatchingRetriesAndDedupe(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	catalog, err := i18n.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	n := New(s, catalog, slog.New(slog.NewTextHandler(io.Discard, nil)))
	n.Host = "web1"
	rec := &recordingSender{}
	n.Senders[TypeWebhook] = rec
	clock := time.Now()
	n.now = func() time.Time { return clock }

	security := webhookChannel("https://example.com/hook", "")
	security.Locale = "zh-CN"
	security.MinLevel = "warning"
	usersOnly := webhookChannel("https://example.com/users", "")
	usersOnly.Events = []string{"users."}
	for _, c := range []*Channel{security, usersOnly} {
		if err := s.Create(ctx, c); err != nil {
			t.Fatal(err)
		}
	}

	// Below the minimum level: not queued. Matching: queued once per channel.
	n.Handle(events.Event{Type: events.SecurityLogin, Level: events.Info, Params: map[string]any{"username": "a", "ip": "1.2.3.4"}})
	lock := events.Event{Type: events.SecurityLockout, Level: events.Warning, DedupeKey: "lockout:a:1.2.3.4",
		Params: map[string]any{"username": "a", "ip": "1.2.3.4", "minutes": 1}, TS: clock.Unix()}
	n.Handle(lock)
	n.Handle(lock) // duplicate within the window: suppressed
	n.Handle(events.Event{Type: events.UsersChange, Level: events.Info, Params: map[string]any{"actor": "root", "action": "delete", "username": "bob"}})
	n.drain(ctx)

	if len(rec.got) != 2 {
		t.Fatalf("want 2 deliveries, got %d: %+v", len(rec.got), rec.got)
	}
	if !strings.Contains(rec.got[0].Title, "web1") || strings.Contains(rec.got[0].Body, "{{") || rec.got[0].Body == "notify.security.lockout.body" {
		t.Fatalf("zh-CN lockout not rendered: %+v", rec.got[0])
	}
	if !strings.Contains(rec.got[1].Body, "account deleted") || !strings.Contains(rec.got[1].Body, "bob") {
		t.Fatalf("action text not translated: %q", rec.got[1].Body)
	}

	// Failures are retried with backoff, then marked failed.
	rec.fail = errors.New("receiver down")
	n.Handle(events.Event{Type: events.UsersChange, Level: events.Info, Params: map[string]any{"actor": "root", "action": "create", "username": "carol"}})
	for i := 0; i < maxAttempts; i++ {
		n.drain(ctx)
		clock = clock.Add(2 * time.Hour)
	}
	ds, err := n.Deliveries(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if ds[0].Status != "failed" || ds[0].Attempts != maxAttempts || ds[0].LastError != "receiver down" || ds[0].ChannelName != "hook" {
		t.Fatalf("latest delivery: %+v", ds[0])
	}
	if ds[1].Status != "sent" {
		t.Fatalf("earlier delivery: %+v", ds[1])
	}

	// Test sends synchronously and records the attempt.
	if err := n.Test(ctx, usersOnly.ID, "admin"); err == nil {
		t.Fatal("test should report the sender error")
	}
	rec.fail = nil
	if err := n.Test(ctx, usersOnly.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := n.Test(ctx, 9999, "admin"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown channel: %v", err)
	}
}
