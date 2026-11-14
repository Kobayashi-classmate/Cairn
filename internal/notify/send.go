// SPDX-License-Identifier: AGPL-3.0-only

package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/buildinfo"
)

// Message is a rendered notification.
type Message struct {
	Event  string         `json:"event"`
	Level  string         `json:"level"`
	TS     int64          `json:"ts"`
	Title  string         `json:"title"`
	Body   string         `json:"body"`
	Params map[string]any `json:"params"`
	Host   string         `json:"host"` // panel host name, to tell servers apart
}

// Sender delivers a message on one channel. Errors are short technical
// English strings; they are shown to administrators as-is.
type Sender interface {
	Send(ctx context.Context, c *Channel, m Message) error
}

// EmailSender delivers over SMTP. TLS certificates are always verified;
// credentials are never sent over an unencrypted connection.
type EmailSender struct {
	// Dial is replaceable in tests.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

// Send implements Sender.
func (s EmailSender) Send(ctx context.Context, c *Channel, m Message) error {
	cfg := c.Config.Email
	if cfg == nil {
		return errors.New("channel has no e-mail configuration")
	}
	if cfg.Security == "none" && cfg.Username != "" {
		return errors.New("refusing to send credentials without TLS")
	}
	dial := s.Dial
	if dial == nil {
		var d net.Dialer
		dial = d.DialContext
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	conn, err := dial(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("connect %s: %w", addr, err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	tlsCfg := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}
	if cfg.Security == "tls" {
		conn = tls.Client(conn, tlsCfg)
	}
	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp greeting: %w", err)
	}
	defer client.Close()
	if cfg.Security == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("server does not offer STARTTLS")
		}
		if err := client.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}
	if cfg.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	from, err := mail.ParseAddress(cfg.From)
	if err != nil {
		return err
	}
	if err := client.Mail(from.Address); err != nil {
		return fmt.Errorf("smtp MAIL FROM: %w", err)
	}
	for _, t := range cfg.To {
		to, err := mail.ParseAddress(t)
		if err != nil {
			return err
		}
		if err := client.Rcpt(to.Address); err != nil {
			return fmt.Errorf("smtp RCPT TO %s: %w", to.Address, err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	if _, err := w.Write(buildMail(from, cfg.To, m)); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp end of data: %w", err)
	}
	return client.Quit()
}

// buildMail renders a plain-text UTF-8 message (base64 body, RFC 2047 subject).
func buildMail(from *mail.Address, to []string, m Message) []byte {
	var b bytes.Buffer
	idBytes := make([]byte, 12)
	_, _ = rand.Read(idBytes)
	domain := from.Address[strings.LastIndexByte(from.Address, '@')+1:]
	hdr := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	hdr("From", from.String())
	hdr("To", strings.Join(to, ", "))
	hdr("Subject", mime.QEncoding.Encode("utf-8", m.Title))
	hdr("Date", time.Unix(m.TS, 0).UTC().Format(time.RFC1123Z))
	hdr("Message-ID", fmt.Sprintf("<%s@%s>", hex.EncodeToString(idBytes), domain))
	hdr("MIME-Version", "1.0")
	hdr("Content-Type", `text/plain; charset="utf-8"`)
	hdr("Content-Transfer-Encoding", "base64")
	hdr("X-Cairn-Event", m.Event)
	b.WriteString("\r\n")
	body := base64.StdEncoding.EncodeToString([]byte(m.Body + "\n\n-- \nCairn @ " + m.Host + "\n"))
	for len(body) > 76 {
		b.WriteString(body[:76] + "\r\n")
		body = body[76:]
	}
	b.WriteString(body + "\r\n")
	return b.Bytes()
}

// WebhookSender POSTs the message as JSON. With a secret, the request is
// signed: X-Cairn-Signature = "sha256=" + hex(HMAC-SHA256(secret, ts + "." + body)).
type WebhookSender struct {
	Client *http.Client
}

// Sign computes the webhook signature (exported for receivers' tests).
func Sign(secret, ts string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// Send implements Sender.
func (s WebhookSender) Send(ctx context.Context, c *Channel, m Message) error {
	cfg := c.Config.Webhook
	if cfg == nil {
		return errors.New("channel has no webhook configuration")
	}
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Cairn/"+buildinfo.Version)
	req.Header.Set("X-Cairn-Event", m.Event)
	req.Header.Set("X-Cairn-Timestamp", ts)
	if cfg.Secret != "" {
		req.Header.Set("X-Cairn-Signature", Sign(cfg.Secret, ts, body))
	}
	hc := s.Client
	if hc == nil {
		hc = &http.Client{
			Timeout: 15 * time.Second,
			// Do not follow redirects: the configured URL is the destination.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	res, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("post: %w", err)
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return fmt.Errorf("receiver answered HTTP %d", res.StatusCode)
	}
	return nil
}
