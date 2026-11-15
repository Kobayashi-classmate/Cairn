// SPDX-License-Identifier: AGPL-3.0-only

package modules_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/events"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/jobs"
)

func TestCertsAndDNSAccounts(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	admin.elevate("")
	e.helper.respond(helperapi.ActionCertStatus, helperapi.CertStatus{Certs: map[string]helperapi.CertInfo{}})

	// DNS accounts keep their secrets to themselves.
	providers := admin.expect(admin.do("GET", "/api/v1/dns/providers", nil), 200, "").body["providers"].([]any)
	if len(providers) < 9 {
		t.Fatalf("providers: %d", len(providers))
	}
	admin.expect(admin.do("POST", "/api/v1/dns/accounts", map[string]any{"name": "cf", "provider": "cloudflare", "fields": map[string]string{}}), 400, "validation.invalid")
	admin.expect(admin.do("POST", "/api/v1/dns/accounts", map[string]any{"provider": "cloudflare", "fields": map[string]string{"api_token": "t1", "bogus": "x"}}), 400, "validation.invalid")
	acct := admin.expect(admin.do("POST", "/api/v1/dns/accounts", map[string]any{"name": "ns1", "provider": "rfc2136",
		"fields": map[string]string{"server": "10.0.0.53:53", "key_name": "cairn.", "key": "c2VjcmV0"}}), 201, "")
	id := acct.body["id"].(string)
	if strings.Contains(acct.raw, "c2VjcmV0") || acct.body["fields"].(map[string]any)["server"] != "10.0.0.53:53" || len(acct.body["secretSet"].([]any)) != 1 {
		t.Fatalf("account view: %s", acct.raw)
	}
	// An update without the secret keeps it.
	admin.expect(admin.do("PUT", "/api/v1/dns/accounts/"+id, map[string]any{"name": "ns1", "provider": "rfc2136",
		"fields": map[string]string{"server": "10.0.0.54:53", "key_name": "cairn."}}), 200, "")
	var stored string
	_ = e.db.QueryRow(`SELECT secret FROM dns_accounts WHERE id = ?`, id).Scan(&stored)
	if strings.Contains(stored, "10.0.0.54") || strings.Contains(stored, "c2VjcmV0") {
		t.Fatal("credentials stored in the clear")
	}

	// Issuing with DNS-01: the helper gets the CA and the credentials.
	e.helper.respond(helperapi.ActionCertIssue, helperapi.OpRef{ID: opID})
	info, _ := json.Marshal(helperapi.CertInfo{Domains: []string{"*.example.com", "example.com"}, NotBefore: time.Now().Unix(),
		NotAfter: time.Now().Add(90 * 24 * time.Hour).Unix(), Issuer: "CN=Test CA", Serial: "ab", Fingerprint: "ff", KeyType: "ec256"})
	e.helper.respond(helperapi.ActionOpGet, helperapi.OpGetResponse{Op: helperapi.Op{ID: opID, State: helperapi.OpSucceeded, Result: info}})
	admin.expect(admin.do("POST", "/api/v1/certs", map[string]any{"domains": []string{"*.example.com"}, "challenge": "http-01"}), 400, "validation.invalid")
	r := admin.expect(admin.do("POST", "/api/v1/certs", map[string]any{"domains": []string{"*.Example.com", "example.com"}, "challenge": "dns-01", "dnsAccount": id}), 202, "")
	job := r.obj("job")
	if final, _ := waitJob(t, admin, job["id"].(string)); final["state"] != jobs.Succeeded {
		t.Fatalf("issue job: %v", final)
	}
	var sent helperapi.CertIssueRequest
	_ = json.Unmarshal([]byte(e.helper.sent(helperapi.ActionCertIssue)), &sent)
	if sent.CA.Name != "letsencrypt" || sent.DNS == nil || sent.DNS.Fields["key"] != "c2VjcmV0" || sent.DNS.Fields["server"] != "10.0.0.54:53" || sent.Domains[0] != "*.example.com" {
		t.Fatalf("sent: %+v", sent)
	}
	certID := sent.ID
	list := admin.expect(admin.do("GET", "/api/v1/certs", nil), 200, "").body["certs"].([]any)
	if c := list[0].(map[string]any); len(list) != 1 || c["state"] != "valid" || c["issuer"] != "CN=Test CA" || c["autoRenew"] != true {
		t.Fatalf("list: %v", list)
	}
	// The account is in use now.
	admin.expect(admin.do("DELETE", "/api/v1/dns/accounts/"+id, nil), 409, "dns.account_in_use")

	// ZeroSSL needs EAB; the HMAC is write-only.
	admin.expect(admin.do("POST", "/api/v1/certs", map[string]any{"domains": []string{"a.example.com"}, "challenge": "http-01", "ca": "zerossl"}), 400, "validation.invalid")
	s := admin.expect(admin.do("PUT", "/api/v1/certs/settings", map[string]any{"email": "ops@example.com", "defaultCa": "zerossl",
		"cas": map[string]any{"zerossl": map[string]any{"eabKid": "kid1", "eabHmac": "aG1hYw"}}}), 200, "")
	if strings.Contains(s.raw, "aG1hYw") {
		t.Fatalf("settings leak the HMAC: %s", s.raw)
	}
	e.helper.respond(helperapi.ActionOpGet, helperapi.OpGetResponse{Op: helperapi.Op{ID: opID, State: helperapi.OpSucceeded, Result: info}})
	id2 := admin.expect(admin.do("POST", "/api/v1/certs", map[string]any{"domains": []string{"a.example.com"}, "challenge": "http-01"}), 202, "").obj("job")["id"].(string)
	waitJob(t, admin, id2)
	_ = json.Unmarshal([]byte(e.helper.sent(helperapi.ActionCertIssue)), &sent)
	if sent.CA.Name != "zerossl" || sent.CA.EABKid != "kid1" || sent.CA.EABHMAC != "aG1hYw" || sent.CA.Email != "ops@example.com" {
		t.Fatalf("zerossl request: %+v", sent.CA)
	}

	// Renewal: due certificates are renewed by the check; a failure is
	// reported as an event and the certificate stays valid.
	var mu sync.Mutex
	var seen []events.Event
	e.bus.Subscribe(func(ev events.Event) {
		mu.Lock()
		seen = append(seen, ev)
		mu.Unlock()
	})
	soon := time.Now().Add(10 * 24 * time.Hour).Unix()
	if _, err := e.db.Exec(`UPDATE certificates SET not_after = ?, renew_attempt_at = 0 WHERE id = ?`, soon, certID); err != nil {
		t.Fatal(err)
	}
	e.helper.respond(helperapi.ActionOpGet, helperapi.OpGetResponse{Op: helperapi.Op{ID: opID, State: helperapi.OpFailed,
		Error: &helperapi.Error{Code: helperapi.CodeCertACME}}})
	e.certs.Check(context.Background(), time.Now())
	deadline := time.Now().Add(5 * time.Second)
	for {
		var state, code string
		_ = e.db.QueryRow(`SELECT state, error_code FROM certificates WHERE id = ?`, certID).Scan(&state, &code)
		if code == helperapi.CodeCertACME {
			if state != "valid" {
				t.Fatalf("a failed renewal of a valid certificate made it %s", state)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("renewal did not run")
		}
		time.Sleep(50 * time.Millisecond)
	}
	waitFor := func(typ string) bool {
		for range 100 {
			mu.Lock()
			for _, ev := range seen {
				if ev.Type == typ {
					mu.Unlock()
					return true
				}
			}
			mu.Unlock()
			time.Sleep(20 * time.Millisecond)
		}
		return false
	}
	if !waitFor(events.CertRenewFailed) {
		t.Fatal("no renew_failed event")
	}
	// Not retried right away.
	before := strings.Count(strings.Join(e.helper.callsOf(), ","), helperapi.ActionCertIssue)
	e.certs.Check(context.Background(), time.Now())
	time.Sleep(200 * time.Millisecond)
	if after := strings.Count(strings.Join(e.helper.callsOf(), ","), helperapi.ActionCertIssue); after != before {
		t.Fatalf("renewal retried at once: %d -> %d", before, after)
	}

	// Expiring certificates in use are reported.
	e.helper.respond(helperapi.ActionCertStatus, helperapi.CertStatus{Certs: map[string]helperapi.CertInfo{certID: {ID: certID, Sites: []string{"shop"}}}})
	if _, err := e.db.Exec(`UPDATE certificates SET not_after = ? WHERE id = ?`, time.Now().Add(3*24*time.Hour).Unix(), certID); err != nil {
		t.Fatal(err)
	}
	e.certs.Check(context.Background(), time.Now())
	if !waitFor(events.CertExpiring) {
		t.Fatal("no expiring event")
	}

	// Deleting needs step-up and goes through the helper.
	e.helper.respond(helperapi.ActionCertRemove, helperapi.OpRef{ID: opID})
	e.helper.respond(helperapi.ActionOpGet, helperapi.OpGetResponse{Op: helperapi.Op{ID: opID, State: helperapi.OpSucceeded}})
	del := admin.expect(admin.do("DELETE", "/api/v1/certs/"+certID, nil), 202, "").obj("job")["id"].(string)
	if final, _ := waitJob(t, admin, del); final["state"] != jobs.Succeeded {
		t.Fatalf("delete: %v", final)
	}
	admin.expect(admin.do("GET", "/api/v1/certs/"+certID, nil), 404, helperapi.CodeCertNotFound)

	// Auditors see certificates but no secrets and change nothing.
	admin.expect(admin.do("POST", "/api/v1/users", map[string]any{"username": "aud", "password": pw, "role": "auditor"}), 201, "")
	aud := e.client()
	aud.expect(aud.login("aud", pw), 200, "")
	aud.expect(aud.do("GET", "/api/v1/certs", nil), 200, "")
	aud.expect(aud.do("GET", "/api/v1/dns/accounts", nil), 200, "")
	aud.expect(aud.do("POST", "/api/v1/certs/"+certID+"/renew", nil), 403, "auth.forbidden")
}
