// SPDX-License-Identifier: AGPL-3.0-only

package modutil

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/auth"
	"github.com/Kobayashi-classmate/Cairn/internal/store"
)

func TestBusyHashingIsServiceUnavailable(t *testing.T) {
	var ae *api.Error
	if err := MapError(fmt.Errorf("sign-in: %w", auth.ErrBusy), nil); !errors.As(err, &ae) || ae.Status != http.StatusServiceUnavailable || ae.Code != "auth.busy" {
		t.Fatalf("mapped to %v", err)
	}
}

// The client went away (or half-closed its connection) and its request's
// context is over: what it did is audited all the same.
func TestAuditOutlivesTheRequest(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "cairn.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/auth/login", nil)
	Audit(r, db, slog.New(slog.NewTextHandler(io.Discard, nil)), Actor{Username: "ghost"}, "auth.login", "auth.invalid_credentials", nil)
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM audit_log WHERE action = 'auth.login' AND username = 'ghost' AND remote_addr = '192.0.2.1'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("%d rows (%v)", n, err)
	}
}
