// SPDX-License-Identifier: AGPL-3.0-only

package helperapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// Client calls the root helper over its Unix socket.
type Client struct {
	hc *http.Client
}

// NewClient returns a client for the helper listening on socket.
func NewClient(socket string) *Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
		MaxIdleConns:    4,
		IdleConnTimeout: 30 * time.Second,
	}
	return &Client{hc: &http.Client{Transport: tr, Timeout: 10 * time.Minute}}
}

// Call invokes action with req and decodes the result into resp (which may be
// nil). Helper-side failures come back as *Error; an unreachable helper is
// reported as *Error with CodeUnavailable.
func (c *Client) Call(ctx context.Context, action string, caller Caller, req, resp any) error {
	if req == nil {
		req = Empty{}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	// The host part is ignored by the Unix dialer.
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://helper/v1/"+action, bytes.NewReader(body))
	if err != nil {
		return err
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set(CallerHeader, caller.Encode())

	hres, err := c.hc.Do(hreq)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &Error{Code: CodeUnavailable}
	}
	defer hres.Body.Close()
	data, err := io.ReadAll(io.LimitReader(hres.Body, 8<<20))
	if err != nil {
		return err
	}
	if hres.StatusCode != http.StatusOK {
		var he Error
		if json.Unmarshal(data, &he) == nil && he.Code != "" {
			return &he
		}
		return fmt.Errorf("helperapi: %s: unexpected status %d", action, hres.StatusCode)
	}
	if resp == nil {
		return nil
	}
	if err := json.Unmarshal(data, resp); err != nil {
		return fmt.Errorf("helperapi: %s: decode response: %w", action, err)
	}
	return nil
}

// AsError extracts a *Error from err, if any.
func AsError(err error) (*Error, bool) {
	var he *Error
	ok := errors.As(err, &he)
	return he, ok
}
