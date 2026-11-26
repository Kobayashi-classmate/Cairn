// SPDX-License-Identifier: AGPL-3.0-only

package modules_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/cloud"
	"github.com/Kobayashi-classmate/Cairn/internal/config"
	"github.com/Kobayashi-classmate/Cairn/internal/events"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/cloudsvc"
)

// signedFeed serves announcements.json and releases.json signed by priv.
func signedFeed(t *testing.T, priv ed25519.PrivateKey, files map[string]any) *httptest.Server {
	t.Helper()
	blobs := map[string][]byte{}
	for name, v := range files {
		b, _ := json.Marshal(v)
		blobs["/"+name] = b
		blobs["/"+name+".sig"] = cloud.Sign(b, priv)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b, ok := blobs[r.URL.Path]; ok {
			_, _ = w.Write(b)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func useFeed(e *env, url string, keys ...string) {
	c := config.Default().Cloud
	c.BaseURL, c.TrustedKeys = url, keys
	e.cloud.Settings = cloudsvc.NewSettings(c)
}

func TestCloudMessagesAndUpdates(t *testing.T) {
	e := newEnv(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	pubB64 := base64.StdEncoding.EncodeToString(pub)
	now := time.Now().Unix()
	feed := signedFeed(t, priv, map[string]any{
		cloud.AnnouncementsFile: cloud.Announcements{Type: "cairn.announcements", GeneratedAt: now, Items: []cloud.Announcement{
			{ID: "welcome", Level: "info", Category: "general", PublishedAt: now - 100,
				Title: map[string]string{"en": "Welcome", "zh-CN": "WELCOME-ZH"}, Body: map[string]string{"en": "Hello **world**"}},
			{ID: "sec-1", Level: "critical", Category: "security", PublishedAt: now,
				Title: map[string]string{"en": "Patch now", "zh-CN": "PATCH-ZH"}, Link: "https://example.com/advisory"},
			{ID: "other-version", Level: "info", Category: "general", Versions: ">=9.0.0", PublishedAt: now},
			{ID: "el-only", Level: "info", Category: "general", Families: []string{"el"}, PublishedAt: now},
		}},
		cloud.ReleasesFile: cloud.Releases{Type: "cairn.releases", GeneratedAt: now, Releases: []cloud.Release{
			{Version: "0.2.0", Channel: "stable", Security: true, Notes: map[string]string{"en": "Fixes"},
				Packages: []cloud.Package{{Format: "deb", Arch: "amd64", URL: "https://example.com/cairn_0.2.0_amd64.deb", SHA256: strings.Repeat("a", 64)}}},
		}},
	})

	// Capture what reaches the event bus.
	var mu sync.Mutex
	var seen []events.Event
	e.bus.Subscribe(func(ev events.Event) { mu.Lock(); seen = append(seen, ev); mu.Unlock() })

	admin := e.setupAdmin("admin")

	// Without a trusted key nothing is accepted.
	useFeed(e, feed.URL)
	e.cloud.Refresh(context.Background())
	if r := admin.expect(admin.do("GET", "/api/v1/updates", nil), 200, ""); r.body["reason"] != "no_trusted_key" {
		t.Fatalf("without key: %v", r.body)
	}
	if r := admin.do("GET", "/api/v1/announcements", nil); len(r.body["announcements"].([]any)) != 0 {
		t.Fatalf("unsigned-by-trusted-key messages shown: %v", r.body)
	}

	useFeed(e, feed.URL, pubB64)
	e.cloud.Refresh(context.Background())
	list := admin.expect(admin.do("GET", "/api/v1/announcements", nil), 200, "")
	items := list.body["announcements"].([]any)
	ids := map[string]bool{}
	for _, it := range items {
		ids[it.(map[string]any)["id"].(string)] = true
	}
	if len(items) != 2 || !ids["welcome"] || !ids["sec-1"] || list.body["unread"].(float64) != 2 {
		t.Fatalf("applicable messages: %v", list.body)
	}
	admin.expect(admin.do("POST", "/api/v1/announcements/sec-1/read", nil), 204, "")
	admin.expect(admin.do("POST", "/api/v1/announcements/nope/read", nil), 404, "request.not_found")
	if r := admin.do("GET", "/api/v1/announcements", nil); r.body["unread"].(float64) != 1 {
		t.Fatalf("unread after reading one: %v", r.body["unread"])
	}
	admin.expect(admin.do("POST", "/api/v1/announcements/read-all", nil), 204, "")
	if r := admin.do("GET", "/api/v1/announcements", nil); r.body["unread"].(float64) != 0 {
		t.Fatalf("unread after read-all: %v", r.body["unread"])
	}

	// The critical security message went to the bus once, with all titles.
	e.cloud.Refresh(context.Background())
	mu.Lock()
	var forwarded []events.Event
	for _, ev := range seen {
		if ev.Type == events.CloudAnnouncement {
			forwarded = append(forwarded, ev)
		}
	}
	mu.Unlock()
	if len(forwarded) != 1 || forwarded[0].Params["id"] != "sec-1" || forwarded[0].Level != events.Critical {
		t.Fatalf("forwarded events: %+v", forwarded)
	}
	if titles, _ := forwarded[0].Params["titles"].(map[string]any); titles["zh-CN"] != "PATCH-ZH" {
		t.Fatalf("titles not forwarded for per-channel language: %v", forwarded[0].Params)
	}

	// Updates: 0.2.0 is available for 0.1.0 on deb/amd64.
	r := admin.expect(admin.do("GET", "/api/v1/updates", nil), 200, "")
	latest, _ := r.body["latest"].(map[string]any)
	if r.body["status"] != "available" || latest["version"] != "0.2.0" || latest["security"] != true {
		t.Fatalf("updates: %v", r.body)
	}

	// Apply needs step-up, then sends only the version to the helper.
	admin.expect(admin.do("POST", "/api/v1/updates/apply", map[string]any{"version": "0.2.0"}), 403, "auth.step_up_required")
	admin.elevate("")
	e.helper.responses = map[string]any{
		helperapi.ActionUpdate:       helperapi.UpdateJob{State: "running", Version: "0.2.0", StartedAt: now},
		helperapi.ActionUpdateStatus: helperapi.UpdateJob{State: "running", Version: "0.2.0", StartedAt: now},
	}
	admin.expect(admin.do("POST", "/api/v1/updates/apply", map[string]any{"version": "0.2.0"}), 202, "")
	if req := string(e.helper.requests[helperapi.ActionUpdate]); req != `{"version":"0.2.0"}` {
		t.Fatalf("helper received more than the version: %s", req)
	}

	// The finished job is audited exactly once.
	e.helper.responses[helperapi.ActionUpdateStatus] = helperapi.UpdateJob{State: "succeeded", Version: "0.2.0", StartedAt: now, FinishedAt: now + 30}
	for i := 0; i < 3; i++ {
		admin.expect(admin.do("GET", "/api/v1/updates", nil), 200, "")
	}
	var finished int
	_ = e.db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'update.finish' AND result = 'ok'`).Scan(&finished)
	if finished != 1 {
		t.Fatalf("update.finish audited %d times", finished)
	}
	// A later run that a restart cut short is an outcome of its own.
	e.helper.responses[helperapi.ActionUpdateStatus] = helperapi.UpdateJob{State: "interrupted", Version: "0.2.0", StartedAt: now + 60, FinishedAt: now + 90}
	for i := 0; i < 2; i++ {
		if r := admin.expect(admin.do("GET", "/api/v1/updates", nil), 200, ""); r.body["job"].(map[string]any)["state"] != "interrupted" {
			t.Fatalf("interrupted job not passed on: %v", r.body["job"])
		}
	}
	var interrupted int
	_ = e.db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'update.finish' AND result = 'update.interrupted'`).Scan(&interrupted)
	if interrupted != 1 {
		t.Fatalf("interrupted update audited %d times", interrupted)
	}

	// Helper refusals map to conflict.
	e.helper.err = &helperapi.Error{Code: helperapi.CodeUpdateInProgress}
	admin.expect(admin.do("POST", "/api/v1/updates/apply", map[string]any{"version": "0.2.0"}), 409, helperapi.CodeUpdateInProgress)
	e.helper.err = nil

	// Development builds never offer updates.
	e.cloud.Version = "8772440-dirty"
	if r := admin.expect(admin.do("POST", "/api/v1/updates/check", nil), 200, ""); r.body["reason"] != "dev_build" || r.body["latest"] != nil {
		t.Fatalf("dev build: %v", r.body)
	}
}

func TestCloudSettings(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	if r := admin.expect(admin.do("GET", "/api/v1/settings/cloud", nil), 200, ""); r.body["baseUrl"] != config.DefaultCloudURL || r.body["enabled"] != true {
		t.Fatalf("defaults: %v", r.body)
	}
	admin.elevate("")
	bad := admin.expect(admin.do("PUT", "/api/v1/settings/cloud", map[string]any{"enabled": true, "baseUrl": "http://insecure.example", "channel": "stable"}), 400, "validation.invalid")
	if bad.params()["field"] != "baseUrl" {
		t.Fatalf("http URL: %v", bad.params())
	}
	admin.expect(admin.do("PUT", "/api/v1/settings/cloud", map[string]any{"enabled": true, "baseUrl": "https://mirror.example/feed", "channel": "nightly"}), 400, "validation.invalid")
	admin.expect(admin.do("PUT", "/api/v1/settings/cloud", map[string]any{"enabled": false, "baseUrl": "https://mirror.example/feed", "channel": "beta"}), 204, "")
	if r := admin.do("GET", "/api/v1/settings/cloud", nil); r.body["channel"] != "beta" || r.body["enabled"] != false {
		t.Fatalf("after update: %v", r.body)
	}
	if r := admin.do("POST", "/api/v1/updates/check", nil); r.body["reason"] != "cloud_disabled" {
		t.Fatalf("disabled cloud: %v", r.body)
	}
}
