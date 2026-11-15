// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libdns/libdns"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// testCA signs leaf certificates for tests.
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Cairn Test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return &testCA{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// leaf returns a chain (leaf, CA) and the leaf's key, both PEM.
func (ca *testCA) leaf(t *testing.T, notAfter time.Time, domains ...string) (string, string) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: domains[0]}, DNSNames: domains,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: notAfter, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	kd, _ := x509.MarshalECPrivateKey(key)
	chain := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})) + string(ca.pem)
	return chain, string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kd}))
}

func TestParseCertChecks(t *testing.T) {
	ca := newTestCA(t)
	chain, key := ca.leaf(t, time.Now().Add(90*24*time.Hour), "shop.example.com", "*.shop.example.com")
	info, err := parseCert([]byte(chain), []byte(key))
	if err != nil || len(info.Chain) != 2 || info.KeyType != "ec256" || len(info.Fingerprint) != 64 || info.Domains[1] != "*.shop.example.com" {
		t.Fatalf("%+v %v", info, err)
	}
	_, otherKey := ca.leaf(t, time.Now().Add(time.Hour), "other.example.com")
	for name, tc := range map[string][2]string{
		"key of another certificate": {chain, otherKey},
		"no certificate":             {"junk", key},
		"chain out of order":         {string(ca.pem) + chain, key},
		"encrypted key":              {chain, "-----BEGIN ENCRYPTED PRIVATE KEY-----\nAAAA\n-----END ENCRYPTED PRIVATE KEY-----\n"},
	} {
		if _, err := parseCert([]byte(tc[0]), []byte(tc[1])); code(err) != helperapi.CodeCertInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}
	if got := uncovered([]string{"shop.example.com", "a.shop.example.com", "a.b.shop.example.com", "example.com"}, info.Domains); strings.Join(got, " ") != "a.b.shop.example.com example.com" {
		t.Errorf("uncovered: %v", got)
	}
}

// certEnv adds a certificate engine to a site environment.
func newCertEnv(t *testing.T) (*siteEnv, *Certs) {
	t.Helper()
	e := newSiteEnv(t)
	c := &Certs{Sites: e.st, Root: e.st.Root, PanelUser: "cairn"}
	// The panel's user exists on a real system from the package install.
	e.f.mu.Lock()
	e.f.cmds = append(e.f.cmds, "useradd --system cairn")
	e.f.mu.Unlock()
	RegisterCerts(e.r, e.o, c)
	old := nginxVersion
	nginxVersion = func(string) string { return "1.26.3" }
	t.Cleanup(func() { nginxVersion = old })
	return e, c
}

func TestCertImportServeAndRemove(t *testing.T) {
	e, c := newCertEnv(t)
	ca := newTestCA(t)
	const id = "c0123456789abcde"
	chain, key := ca.leaf(t, time.Now().Add(90*24*time.Hour), "shop.example.com", "www.shop.example.com")
	op, log := e.run(helperapi.ActionCertImport, helperapi.CertImportRequest{ID: id, CertPEM: chain, KeyPEM: key})
	if op.State != helperapi.OpSucceeded {
		t.Fatalf("import: %+v\n%s", op.Error, log)
	}
	if info, err := os.Stat(filepath.Join(c.Root, certStateDir, id, "key.pem")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("stored key: %v %v", info, err)
	}

	// A site served over HTTPS, plain HTTP redirecting to it.
	spec := helperapi.SiteSpec{Code: "shop", Type: helperapi.SiteStatic, Domains: []string{"shop.example.com", "www.shop.example.com"},
		TLS: &helperapi.SiteTLS{Cert: id, Redirect: true, HSTS: true}, Enabled: true}
	if op, log := e.run(helperapi.ActionSiteApply, spec); op.State != helperapi.OpSucceeded {
		t.Fatalf("https site: %+v\n%s", op.Error, log)
	}
	conf := e.read("/etc/nginx/cairn/sites/shop.conf")
	for _, want := range []string{"listen 443 ssl;", "http2 on;", "ssl_certificate /etc/nginx/cairn/certs/" + id + ".crt;",
		"return 301 https://$host$request_uri;", "location ^~ /.well-known/acme-challenge/", "Strict-Transport-Security"} {
		if !strings.Contains(conf, want) {
			t.Errorf("site conf lacks %q:\n%s", want, conf)
		}
	}
	if info, err := os.Stat(filepath.Join(c.Root, nginxCertDir, id+".key")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("nginx key copy: %v %v", info, err)
	}
	// Older nginx takes http2 on the listen line.
	nginxVersion = func(string) string { return "1.22.1" }
	spec.TLS.Redirect = false
	e.run(helperapi.ActionSiteApply, spec)
	conf = e.read("/etc/nginx/cairn/sites/shop.conf")
	if !strings.Contains(conf, "listen 443 ssl http2;") || strings.Contains(conf, "http2 on;") || strings.Contains(conf, "return 301") {
		t.Fatalf("old nginx:\n%s", conf)
	}

	// The certificate must cover every domain of the site.
	spec.Domains = append(spec.Domains, "blog.example.com")
	if op, _ := e.run(helperapi.ActionSiteApply, spec); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeSiteCertCoverage {
		t.Fatalf("coverage: %+v", op.Error)
	}
	// A replacement that no longer covers the site is refused too.
	narrow, narrowKey := ca.leaf(t, time.Now().Add(time.Hour), "shop.example.com")
	if op, _ := e.run(helperapi.ActionCertImport, helperapi.CertImportRequest{ID: id, CertPEM: narrow, KeyPEM: narrowKey}); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeCertCoverage {
		t.Fatalf("narrow replacement: %+v", op.Error)
	}
	// Expired uploads are refused.
	old, oldKey := ca.leaf(t, time.Now().Add(-time.Minute), "shop.example.com")
	if op, _ := e.run(helperapi.ActionCertImport, helperapi.CertImportRequest{ID: "c1111111111111111"[:16], CertPEM: old, KeyPEM: oldKey}); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeCertExpired {
		t.Fatalf("expired: %+v", op.Error)
	}

	// The panel serves it from its own copy.
	if op, log := e.run(helperapi.ActionPanelCert, helperapi.PanelCertRequest{ID: id}); op.State != helperapi.OpSucceeded {
		t.Fatalf("panel: %+v\n%s", op.Error, log)
	}
	if info, err := os.Stat(filepath.Join(c.Root, panelTLSDir, "panel.key")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("panel key: %v %v", info, err)
	}
	st := c.Status()
	if got := st.Certs[id]; st.Panel != id || !got.Panel || strings.Join(got.Sites, ",") != "shop" || got.Source != "upload" {
		t.Fatalf("status: %+v", st)
	}

	// In use: not removable until the site and the panel let go.
	if op, _ := e.run(helperapi.ActionCertRemove, helperapi.CertRef{ID: id}); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeCertInUse {
		t.Fatalf("remove in use: %+v", op.Error)
	}
	spec.Domains, spec.TLS = spec.Domains[:2], nil
	e.run(helperapi.ActionSiteApply, spec)
	e.run(helperapi.ActionPanelCert, helperapi.PanelCertRequest{})
	if e.exists(panelTLSDir + "/panel.crt") {
		t.Fatal("panel copy left after going back to self-signed")
	}
	if op, _ := e.run(helperapi.ActionCertRemove, helperapi.CertRef{ID: id}); op.State != helperapi.OpSucceeded {
		t.Fatalf("remove: %+v", op.Error)
	}
	if e.exists(certStateDir+"/"+id) || e.exists(nginxCertDir+"/"+id+".key") {
		t.Fatal("certificate files left")
	}
	var got helperapi.CertInfo
	_ = json.Unmarshal(op.Result, &got)
}

// fakeDNS records what the DNS-01 solver adds and removes.
type fakeDNS struct {
	added, deleted []libdns.Record
	zone           string
}

func (f *fakeDNS) GetRecords(context.Context, string) ([]libdns.Record, error) { return f.added, nil }
func (f *fakeDNS) AppendRecords(_ context.Context, zone string, recs []libdns.Record) ([]libdns.Record, error) {
	f.zone, f.added = zone, append(f.added, recs...)
	return recs, nil
}
func (f *fakeDNS) DeleteRecords(_ context.Context, _ string, recs []libdns.Record) ([]libdns.Record, error) {
	f.deleted = append(f.deleted, recs...)
	return recs, nil
}

func TestDNSChallengeAddsAndRemovesTXT(t *testing.T) {
	p := &fakeDNS{}
	d := &dnsChallenge{p: p, log: func(string, ...any) {}, findZone: func(string) (string, error) { return "example.com.", nil }}
	if err := d.Present("*.shop.example.com", "tok", "key-auth"); err != nil {
		t.Fatal(err)
	}
	rr := p.added[0].RR()
	if p.zone != "example.com." || rr.Type != "TXT" || rr.Name != "_acme-challenge.shop" || rr.Data == "" {
		t.Fatalf("added: %s %+v", p.zone, rr)
	}
	if err := d.CleanUp("*.shop.example.com", "tok", "key-auth"); err != nil || len(p.deleted) != 1 {
		t.Fatalf("cleanup: %v %v", err, p.deleted)
	}
	// Credentials are checked before any provider is built.
	if _, err := newDNSProvider(helperapi.DNSCredentials{Provider: "cloudflare", Fields: map[string]string{}}); code(err) != helperapi.CodeDNSCredentials {
		t.Fatalf("missing token: %v", err)
	}
	if _, err := newDNSProvider(helperapi.DNSCredentials{Provider: "rfc2136", Fields: map[string]string{"server": "127.0.0.1:53", "key_name": "k.", "key": "c2VjcmV0"}}); err != nil {
		t.Fatalf("rfc2136: %v", err)
	}
}

func TestCertIssueRequestValidation(t *testing.T) {
	good := helperapi.CertIssueRequest{ID: "c0123456789abcde", Domains: []string{"example.com", "www.example.com"},
		CA: helperapi.ACMECA{Name: "letsencrypt"}, Challenge: helperapi.ChallengeHTTP01, KeyType: "ec256"}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(r *helperapi.CertIssueRequest){
		"wildcard over http":  func(r *helperapi.CertIssueRequest) { r.Domains = []string{"*.example.com"} },
		"ip address":          func(r *helperapi.CertIssueRequest) { r.Domains = []string{"203.0.113.7"} },
		"zerossl without eab": func(r *helperapi.CertIssueRequest) { r.CA = helperapi.ACMECA{Name: "zerossl"} },
		"custom over http": func(r *helperapi.CertIssueRequest) {
			r.CA = helperapi.ACMECA{Name: "custom", Directory: "http://ca.example/dir"}
		},
		"unknown ca":            func(r *helperapi.CertIssueRequest) { r.CA = helperapi.ACMECA{Name: "nope"} },
		"dns without provider":  func(r *helperapi.CertIssueRequest) { r.Challenge = helperapi.ChallengeDNS01 },
		"bad key type":          func(r *helperapi.CertIssueRequest) { r.KeyType = "dsa" },
		"half an eab":           func(r *helperapi.CertIssueRequest) { r.CA.EABKid = "kid" },
		"bad id":                func(r *helperapi.CertIssueRequest) { r.ID = "../x" },
		"directory on known ca": func(r *helperapi.CertIssueRequest) { r.CA.Directory = "https://evil.example/dir" },
	} {
		r := good
		r.Domains = append([]string(nil), good.Domains...)
		mutate(&r)
		if err := r.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
