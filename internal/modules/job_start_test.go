// SPDX-License-Identifier: AGPL-3.0-only

package modules_test

import (
	"os"
	"testing"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// A record stored before its job starts must not stay "applying" (or
// "issuing") forever when the job cannot start: no job would finish it.
func TestRecordsDoNotStayPendingWhenJobsCannotStart(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	admin.elevate("")
	e.helper.respond(helperapi.ActionTermUsers, helperapi.TermUsers{Users: []helperapi.TermUser{{Name: "cw-shop", Kind: helperapi.TermUserSite}}})
	// Jobs cannot start: their log directory is gone.
	if err := os.RemoveAll(e.jobs.Dir); err != nil {
		t.Fatal(err)
	}
	state := func(query string) string {
		t.Helper()
		var s string
		if err := e.db.QueryRow(query).Scan(&s); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return s
	}
	admin.expect(admin.do("POST", "/api/v1/sites", map[string]any{"type": "static", "domains": []string{"fail.example.com"}}), 500, "internal.error")
	if s := state(`SELECT state FROM sites WHERE code = 'fail-example-com'`); s != "failed" {
		t.Fatalf("site state %q", s)
	}
	admin.expect(admin.do("POST", "/api/v1/streams", map[string]any{"protocol": "tcp", "port": 4407, "upstreams": []string{"10.0.0.5:3306"}}), 500, "internal.error")
	if s := state(`SELECT state FROM streams WHERE code = 'tcp-4407'`); s != "failed" {
		t.Fatalf("stream state %q", s)
	}
	admin.expect(admin.do("POST", "/api/v1/cron", map[string]any{"name": "x", "type": "shell", "schedule": "daily", "user": "cw-shop", "script": "true", "enabled": true}),
		500, "internal.error")
	if s := state(`SELECT state FROM cron_tasks WHERE name = 'x'`); s != "failed" {
		t.Fatalf("task state %q", s)
	}
	admin.expect(admin.do("POST", "/api/v1/certs", map[string]any{"domains": []string{"fail.example.com"}, "challenge": "http-01"}), 500, "internal.error")
	if n := state(`SELECT count(*) FROM certificates`); n != "0" {
		t.Fatalf("certificates left issuing: %s", n)
	}
}
