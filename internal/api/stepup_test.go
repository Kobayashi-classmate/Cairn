// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/authz"
)

// The step-up rule, on a route registered with StepUp and inside a handler
// that calls CheckStepUp, for each state of the switch: browser sessions
// pass when re-authenticated or when the switch is off; API tokens never.
func TestStepUpRule(t *testing.T) {
	var principal atomic.Pointer[Principal]
	rt := NewRouter(func(*http.Request) (*Principal, error) { return principal.Load(), nil }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ok := func(w http.ResponseWriter, _ *http.Request) error {
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	rt.Handle("GET /api/v1/guarded", ok, Require(authz.Self), StepUp())
	rt.Handle("GET /api/v1/partly", func(w http.ResponseWriter, r *http.Request) error {
		if err := CheckStepUp(r); err != nil {
			return err
		}
		return ok(w, r)
	}, Require(authz.Self))

	now := time.Now().Unix()
	elevated := &Principal{UserID: 1, Role: string(authz.Admin), SessionID: "s1", ElevatedUntil: now + 60, Source: "web"}
	plain := &Principal{UserID: 1, Role: string(authz.Admin), SessionID: "s2", ElevatedUntil: now - 1, Source: "web"}
	// A token never carries an elevation; one that did must not pass either.
	token := &Principal{UserID: 1, Role: string(authz.Admin), Scopes: []string{}, ElevatedUntil: now + 60, Source: "api"}
	noSession := &Principal{UserID: 1, Role: string(authz.Admin), ElevatedUntil: now + 60, Source: "web"}

	var off atomic.Bool
	cases := []struct {
		name    string
		install bool // set a switch (otherwise none: always on)
		off     bool
		who     *Principal
		passes  bool
	}{
		{"no switch, elevated", false, false, elevated, true},
		{"no switch, plain", false, false, plain, false},
		{"no switch, token", false, false, token, false},
		{"on, elevated", true, false, elevated, true},
		{"on, plain", true, false, plain, false},
		{"on, token", true, false, token, false},
		{"off, elevated", true, true, elevated, true},
		{"off, plain", true, true, plain, true},
		{"off, token", true, true, token, false},
		{"off, no session", true, true, noSession, false},
	}
	for _, c := range cases {
		if c.install {
			rt.SetStepUpRequired(func() bool { return !off.Load() })
		} else {
			rt.SetStepUpRequired(nil)
		}
		off.Store(c.off)
		principal.Store(c.who)
		for _, path := range []string{"/api/v1/guarded", "/api/v1/partly"} {
			rec := httptest.NewRecorder()
			rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if c.passes {
				if rec.Code != http.StatusNoContent {
					t.Fatalf("%s %s: %d %s", c.name, path, rec.Code, rec.Body)
				}
				continue
			}
			var body errorBody
			_ = json.Unmarshal(rec.Body.Bytes(), &body)
			if rec.Code != http.StatusForbidden || body.Error.Code != ErrStepUpRequired.Code {
				t.Fatalf("%s %s: %d %s", c.name, path, rec.Code, rec.Body)
			}
		}
	}

	// The switch is asked on every request, not once.
	rt.SetStepUpRequired(func() bool { return !off.Load() })
	principal.Store(plain)
	for _, o := range []bool{true, false, true} {
		off.Store(o)
		rec := httptest.NewRecorder()
		rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/guarded", nil))
		if (rec.Code == http.StatusNoContent) != o {
			t.Fatalf("off=%v: %d", o, rec.Code)
		}
	}

	// Outside an authenticated route there is nobody to have stepped up.
	if err := CheckStepUp(httptest.NewRequest(http.MethodGet, "/", nil)); !errors.Is(err, ErrStepUpRequired) {
		t.Fatalf("no principal: %v", err)
	}
}
