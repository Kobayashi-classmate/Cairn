// SPDX-License-Identifier: AGPL-3.0-only

package modules_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/events"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/jobs"
	sitesmod "github.com/Kobayashi-classmate/Cairn/internal/modules/sites"
)

func TestAppSitesAndStreams(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	e.helper.respond(helperapi.ActionSiteStatus, helperapi.SiteStatus{Supported: true, Systemd: true, Sites: map[string]helperapi.SiteState{}})
	e.helper.respond(helperapi.ActionSiteApply, helperapi.OpRef{ID: opID})
	e.helper.respond(helperapi.ActionStreamApply, helperapi.OpRef{ID: opID})
	e.helper.respond(helperapi.ActionAppRestart, helperapi.OpRef{ID: opID})
	e.helper.respond(helperapi.ActionOpGet, helperapi.OpGetResponse{Op: helperapi.Op{ID: opID, State: helperapi.OpSucceeded, Result: json.RawMessage(`{}`)}})

	// An app gets the first free port of the app range.
	app := func(domain string, port int) map[string]any {
		return map[string]any{"type": "app", "domains": []string{domain},
			"app": map[string]any{"runtime": "nodejs", "line": "22", "command": "node server.js", "port": port, "healthPath": "/health"}}
	}
	id := admin.expect(admin.do("POST", "/api/v1/sites", app("api.example.com", 0)), 202, "").obj("job")["id"].(string)
	waitJob(t, admin, id)
	var sent helperapi.SiteSpec
	_ = json.Unmarshal([]byte(e.helper.sent(helperapi.ActionSiteApply)), &sent)
	if sent.App == nil || sent.App.Port != helperapi.AppPortMin || sent.App.Command != "node server.js" {
		t.Fatalf("sent: %+v", sent.App)
	}
	waitJob(t, admin, admin.expect(admin.do("POST", "/api/v1/sites", app("web.example.com", 0)), 202, "").obj("job")["id"].(string))
	_ = json.Unmarshal([]byte(e.helper.sent(helperapi.ActionSiteApply)), &sent)
	if sent.App.Port != helperapi.AppPortMin+1 {
		t.Fatalf("second port: %d", sent.App.Port)
	}
	// Taken ports and the panel's are refused; a change keeps the port.
	admin.expect(admin.do("POST", "/api/v1/sites", app("x.example.com", helperapi.AppPortMin)), 409, helperapi.CodeAppPortTaken)
	admin.expect(admin.do("POST", "/api/v1/sites", app("x.example.com", 28443)), 409, helperapi.CodeAppPortTaken)
	admin.expect(admin.do("POST", "/api/v1/sites", map[string]any{"type": "app", "domains": []string{"x.example.com"},
		"app": map[string]any{"command": "a\nb", "port": 0}}), 400, "validation.invalid")
	waitJob(t, admin, admin.expect(admin.do("PUT", "/api/v1/sites/api-example-com", app("api.example.com", 0)), 202, "").obj("job")["id"].(string))
	_ = json.Unmarshal([]byte(e.helper.sent(helperapi.ActionSiteApply)), &sent)
	if sent.App.Port != helperapi.AppPortMin {
		t.Fatalf("update changed the port to %d", sent.App.Port)
	}

	// Status and restart go through the helper.
	e.helper.respond(helperapi.ActionAppStatus, helperapi.AppStatus{Code: "api-example-com", ActiveState: "active", SubState: "running", MainPID: 42})
	if r := admin.expect(admin.do("GET", "/api/v1/sites/api-example-com/app", nil), 200, ""); r.body["mainPid"] != 42.0 || r.body["health"] != "ok" {
		t.Fatalf("app status: %v", r.body)
	}
	if final, _ := waitJob(t, admin, admin.expect(admin.do("POST", "/api/v1/sites/api-example-com/restart", nil), 202, "").obj("job")["id"].(string)); final["kind"] != "site.restart" || final["state"] != jobs.Succeeded {
		t.Fatalf("restart: %v", final)
	}

	// Health: three failed checks restart the app and publish an event.
	var mu sync.Mutex
	var seen []events.Event
	e.bus.Subscribe(func(ev events.Event) {
		mu.Lock()
		seen = append(seen, ev)
		mu.Unlock()
	})
	healthy := true
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" || !healthy {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv.Listener = ln
	srv.Start()
	defer srv.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	spec := sent
	spec.App.Port = port
	data, _ := json.Marshal(spec)
	if _, err := e.db.Exec(`UPDATE sites SET spec = ?, state = 'active' WHERE code = 'api-example-com'`, string(data)); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: time.Second}
	restarts := func() int { return strings.Count(strings.Join(e.helper.callsOf(), ","), helperapi.ActionAppRestart) }
	before := restarts()
	e.sites.CheckHealth(context.Background(), client)
	healthy = false
	for range 3 {
		e.sites.CheckHealth(context.Background(), client)
	}
	deadline := time.Now().Add(5 * time.Second)
	for restarts() == before && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if restarts() != before+1 {
		t.Fatalf("restarts after failing checks: %d -> %d", before, restarts())
	}
	mu.Lock()
	found := false
	for _, ev := range seen {
		found = found || (ev.Type == events.AppUnhealthy && ev.Params["site"] == "api-example-com")
	}
	mu.Unlock()
	if !found {
		t.Fatal("no app.unhealthy event")
	}
	// Checks pause after a restart.
	e.sites.CheckHealth(context.Background(), client)
	if r := admin.expect(admin.do("GET", "/api/v1/sites/api-example-com/app", nil), 200, ""); r.body["failures"] != nil && r.body["failures"] != 0.0 {
		t.Fatalf("failures counted while paused: %v", r.body)
	}

	// Streams: the code comes from protocol and port; ports are unique per
	// protocol and stay off the app range and the panel's port. Creating and
	// changing them takes a second verification.
	admin.expect(admin.do("POST", "/api/v1/streams", map[string]any{"protocol": "tcp", "port": 3307, "upstreams": []string{"10.0.0.5:3306"}}), 403, "auth.step_up_required")
	admin.elevate("")
	r := admin.expect(admin.do("POST", "/api/v1/streams", map[string]any{"protocol": "tcp", "port": 3307, "upstreams": []string{"10.0.0.5:3306"}}), 202, "")
	if final, _ := waitJob(t, admin, r.obj("job")["id"].(string)); final["state"] != jobs.Succeeded {
		t.Fatalf("stream: %v", final)
	}
	admin.expect(admin.do("POST", "/api/v1/streams", map[string]any{"code": "db2", "protocol": "tcp", "port": 3307, "upstreams": []string{"10.0.0.6:3306"}}), 409, helperapi.CodeStreamPortTaken)
	admin.expect(admin.do("POST", "/api/v1/streams", map[string]any{"code": "dns", "protocol": "udp", "port": 3307, "upstreams": []string{"10.0.0.53:53"}}), 202, "")
	// The panel's default port lies in the app range, which streams never get.
	admin.expect(admin.do("POST", "/api/v1/streams", map[string]any{"code": "x1", "protocol": "tcp", "port": 28443, "upstreams": []string{"10.0.0.6:1"}}), 400, "validation.invalid")
	admin.expect(admin.do("POST", "/api/v1/streams", map[string]any{"code": "x2", "protocol": "tcp", "port": helperapi.AppPortMin + 5, "upstreams": []string{"10.0.0.6:1"}}), 400, "validation.invalid")
	// An app may not take a stream's port either.
	admin.expect(admin.do("POST", "/api/v1/sites", app("y.example.com", 3307)), 409, helperapi.CodeAppPortTaken)
	waitJob(t, admin, admin.expect(admin.do("POST", "/api/v1/streams/tcp-3307/stop", nil), 202, "").obj("job")["id"].(string))
	var stopped helperapi.StreamSpec
	_ = json.Unmarshal([]byte(e.helper.sent(helperapi.ActionStreamApply)), &stopped)
	if stopped.Code != "tcp-3307" || stopped.Enabled {
		t.Fatalf("stop: %+v", stopped)
	}
	list := admin.expect(admin.do("GET", "/api/v1/streams", nil), 200, "").body["streams"].([]any)
	if len(list) != 2 {
		t.Fatalf("streams: %v", list)
	}
	e.helper.respond(helperapi.ActionStreamRemove, helperapi.OpRef{ID: opID})
	// An upstream on this host publishes a local service: that needs the
	// firewall permission (administrators). So does every name and every
	// address not written the canonical way: nginx resolves them itself,
	// and they can reach this host (0x7f.1 is 127.0.0.1 to the system).
	admin.expect(admin.do("POST", "/api/v1/users", map[string]any{"username": "oper", "password": pw, "role": "operator"}), 201, "")
	op := e.client()
	op.expect(op.login("oper", pw), 200, "")
	op.elevate("")
	for up, code := range map[string]string{
		"127.0.0.1:3306": sitesmod.CodeStreamLocalUpstream, "[::1]:5432": sitesmod.CodeStreamLocalUpstream,
		"0.0.0.0:3306": sitesmod.CodeStreamLocalUpstream, "[::ffff:127.0.0.1]:3306": sitesmod.CodeStreamLocalUpstream,
		"localhost:6379": sitesmod.CodeStreamUpstreamName, "db.internal.example:3306": sitesmod.CodeStreamUpstreamName,
		"0x7f.0x1:6379": sitesmod.CodeStreamUpstreamName, "[2001:db8:0::1]:6379": sitesmod.CodeStreamUpstreamName,
	} {
		r := op.expect(op.do("POST", "/api/v1/streams", map[string]any{"code": "local", "protocol": "tcp", "port": 4406, "upstreams": []string{up}}), 403, code)
		if r.params()["upstream"] != strings.ToLower(up) {
			t.Fatalf("refused upstream %v", r.params())
		}
	}
	op.expect(op.do("PUT", "/api/v1/streams/tcp-3307", map[string]any{"protocol": "tcp", "port": 3307, "upstreams": []string{"127.0.0.1:3306"}}), 403, sitesmod.CodeStreamLocalUpstream)
	// Nor a metadata service, nor an address of one of this host's
	// interfaces (a database listening on every address, kept from the
	// network by the firewall).
	op.expect(op.do("POST", "/api/v1/streams", map[string]any{"code": "local", "protocol": "tcp", "port": 4406, "upstreams": []string{"169.254.169.254:80"}}), 403, sitesmod.CodeStreamMetadataUpstream)
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() && !ipn.IP.IsLinkLocalUnicast() {
				up := net.JoinHostPort(ipn.IP.String(), "3306")
				op.expect(op.do("POST", "/api/v1/streams", map[string]any{"code": "local", "protocol": "tcp", "port": 4406, "upstreams": []string{up}}), 403, sitesmod.CodeStreamLocalUpstream)
			}
		}
	}
	sentStream := func() helperapi.StreamSpec {
		var s helperapi.StreamSpec
		_ = json.Unmarshal([]byte(e.helper.sent(helperapi.ActionStreamApply)), &s)
		return s
	}
	// The helper checks the upstreams again unless the caller may forward
	// anywhere.
	waitJob(t, op, op.expect(op.do("PUT", "/api/v1/streams/tcp-3307", map[string]any{"protocol": "tcp", "port": 3307, "upstreams": []string{"10.0.0.8:3306"}}), 202, "").obj("job")["id"].(string))
	if s := sentStream(); s.Code != "tcp-3307" || s.AnyUpstream {
		t.Fatalf("an operator's stream: %+v", s)
	}
	waitJob(t, admin, admin.expect(admin.do("POST", "/api/v1/streams", map[string]any{"code": "local", "protocol": "tcp", "port": 4406, "upstreams": []string{"127.0.0.1:3306"}}), 202, "").obj("job")["id"].(string))
	if s := sentStream(); s.Code != "local" || !s.AnyUpstream {
		t.Fatalf("an administrator's stream: %+v", s)
	}
	// An operator stops and starts it again as it is.
	for _, action := range []string{"stop", "start"} {
		waitJob(t, op, op.expect(op.do("POST", "/api/v1/streams/local/"+action, nil), 202, "").obj("job")["id"].(string))
	}
	if s := sentStream(); !s.Enabled || !s.AnyUpstream {
		t.Fatalf("started again by an operator: %+v", s)
	}
	// One saved by an earlier version, which did not record who set its
	// upstreams: starting it takes the firewall permission.
	if _, err := e.db.Exec(`UPDATE streams SET spec = ? WHERE code = 'local'`, `{"code":"local","protocol":"tcp","port":4406,"upstreams":["127.0.0.1:3306"],"enabled":false}`); err != nil {
		t.Fatal(err)
	}
	op.expect(op.do("POST", "/api/v1/streams/local/start", nil), 403, sitesmod.CodeStreamLocalUpstream)
	waitJob(t, admin, admin.expect(admin.do("POST", "/api/v1/streams/local/start", nil), 202, "").obj("job")["id"].(string))
	if s := sentStream(); !s.Enabled || !s.AnyUpstream {
		t.Fatalf("started by an administrator: %+v", s)
	}
	waitJob(t, admin, admin.expect(admin.do("DELETE", "/api/v1/streams/local", nil), 202, "").obj("job")["id"].(string))

	waitJob(t, admin, admin.expect(admin.do("DELETE", "/api/v1/streams/dns", nil), 202, "").obj("job")["id"].(string))
	if n := len(admin.expect(admin.do("GET", "/api/v1/streams", nil), 200, "").body["streams"].([]any)); n != 1 {
		t.Fatalf("after delete: %d", n)
	}
}
