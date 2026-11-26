// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/config"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// After an installation the package manager's exit status says only that
// the files are in place: cairn-web may still fail its migrations or crash
// as it starts, and with Type=simple systemd reports a start as soon as the
// process exists. So a run counts as done when both services of the
// version answer, the helper on its socket and cairn-web on its listening
// address, and keep answering without cairn-web restarting in between.

// panelUnit is the service an update stops, starts and checks.
const panelUnit = "cairn-web.service"

const (
	// healthTimeout is how long a version has to answer after cairn-web
	// was started; healthSettle how long it must then keep answering.
	healthTimeout  = 2 * time.Minute
	healthSettle   = 10 * time.Second
	healthInterval = 2 * time.Second
	// probeTimeout bounds one question to one service.
	probeTimeout = 5 * time.Second
)

// checkHealth waits until both services answer as version. Tests replace
// it; they must never reach the services of the machine they run on.
var checkHealth = func(ctx context.Context, cfg config.Config, version string) error {
	p := newPanelProbe(cfg)
	return healthWait{check: p.check, restarts: webRestarts, timeout: healthTimeout, settle: healthSettle, interval: healthInterval}.
		wait(ctx, version)
}

// healthWait asks check until it has held for settle with the same
// restart count, or gives up after timeout (plus a settle period begun
// before it).
type healthWait struct {
	check    func(ctx context.Context, version string) error
	restarts func(ctx context.Context) (int, error)
	timeout  time.Duration
	settle   time.Duration
	interval time.Duration
}

func (h healthWait) wait(ctx context.Context, version string) error {
	start := time.Now()
	var since time.Time // the start of the current run of answers
	count := 0
	var last error
	for {
		err := h.check(ctx, version)
		if err == nil {
			var n int
			n, err = h.restarts(ctx)
			switch {
			case err != nil:
			case since.IsZero():
				since, count = time.Now(), n
			case n != count:
				// It answered both times but was restarted in between.
				last = fmt.Errorf("%s restarted while it was checked", panelUnit)
				since, count = time.Now(), n
			case time.Since(since) >= h.settle:
				return nil
			}
		}
		if err != nil {
			since, last = time.Time{}, err
		}
		elapsed := time.Since(start)
		if (since.IsZero() && elapsed >= h.timeout) || elapsed >= h.timeout+h.settle {
			if last == nil {
				last = errors.New("no answer")
			}
			return fmt.Errorf("after %s: %w", h.timeout.Round(time.Second), last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(h.interval):
		}
	}
}

// panelProbe asks the panel's services which version they run.
type panelProbe struct {
	helper *helperapi.Client
	web    *http.Client
	meta   string // URL of cairn-web's /api/v1/meta
}

func newPanelProbe(cfg config.Config) *panelProbe {
	return &panelProbe{
		helper: helperapi.NewClient(cfg.Paths.HelperSocket),
		meta:   metaURL(cfg.Server.Listen),
		web: &http.Client{Timeout: probeTimeout, Transport: &http.Transport{
			// The panel itself, never through a proxy.
			Proxy:             nil,
			DisableKeepAlives: true,
			// The panel's certificate is often self-signed or made out to
			// a name other than the address asked here. Nothing is sent
			// but this request, and its answer only decides whether the
			// update is rolled back.
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}, //nolint:gosec // G402: see above.
		}},
	}
}

// metaURL is where cairn-web answers on this machine for listen.
func metaURL(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return ""
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
		if ip != nil && ip.To4() == nil {
			host = "::1"
		}
	}
	return "https://" + net.JoinHostPort(host, port) + "/api/v1/meta"
}

func (p *panelProbe) check(ctx context.Context, version string) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	var pong helperapi.PingResponse
	if err := p.helper.Call(ctx, helperapi.ActionPing, helperapi.Caller{Source: "update"}, nil, &pong); err != nil {
		return fmt.Errorf("the helper does not answer: %w", err)
	}
	if pong.Version != version {
		return fmt.Errorf("the helper runs version %q", pong.Version)
	}
	got, err := p.webVersion(ctx)
	if err != nil {
		return fmt.Errorf("%s does not answer: %w", panelUnit, err)
	}
	if got != version {
		return fmt.Errorf("%s runs version %q", panelUnit, got)
	}
	return nil
}

func (p *panelProbe) webVersion(ctx context.Context) (string, error) {
	if p.meta == "" {
		return "", errors.New("no listening address in the configuration")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.meta, nil)
	if err != nil {
		return "", err
	}
	res, err := p.web.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", res.StatusCode)
	}
	var meta struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&meta); err != nil {
		return "", err
	}
	return meta.Version, nil
}

// webRestarts is how often systemd has restarted cairn-web since it was
// last started by hand.
func webRestarts(ctx context.Context) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	out, err := queryCommand(ctx, "systemctl", "show", "--property=NRestarts", "--value", panelUnit)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(out))
}
