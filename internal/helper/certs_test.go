// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libdns/libdns"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/netx"
	"github.com/Kobayashi-classmate/Cairn/internal/tlsutil"
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
	// The panel's data directory exists from the package install.
	if err := os.MkdirAll(filepath.Join(c.Root, panelDataDir), 0o750); err != nil {
		t.Fatal(err)
	}
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
	if info, err := os.Stat(filepath.Join(c.Root, panelTLSDir, tlsutil.DeployedFile)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("panel key: %v %v", info, err)
	}
	// The key and the chain in one file, which the panel loads as it is.
	pair := e.read(panelTLSDir + "/" + tlsutil.DeployedFile)
	if _, err := tls.X509KeyPair([]byte(pair), []byte(pair)); err != nil || !strings.HasPrefix(pair, "-----BEGIN EC PRIVATE KEY") {
		t.Fatalf("panel copy: %v\n%s", err, pair)
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
	if e.exists(panelTLSDir + "/" + tlsutil.DeployedFile) {
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

func TestCertPanelCopyStaysInDataDir(t *testing.T) {
	e, c := newCertEnv(t)
	ca := newTestCA(t)
	const id = "c0123456789abcde"
	chain, key := ca.leaf(t, time.Now().Add(90*24*time.Hour), "panel.example.com")
	// Text around the PEM blocks never reaches a file.
	junk := "#!/bin/sh\necho pwned\n"
	op, log := e.run(helperapi.ActionCertImport, helperapi.CertImportRequest{ID: id, CertPEM: junk + chain + junk, KeyPEM: junk + key + junk})
	if op.State != helperapi.OpSucceeded {
		t.Fatalf("import: %+v\n%s", op.Error, log)
	}
	for _, name := range []string{"cert.pem", "key.pem"} {
		data := e.read(certStateDir + "/" + id + "/" + name)
		if strings.Contains(data, "pwned") || !strings.HasPrefix(data, "-----BEGIN ") || !strings.HasSuffix(data, "-----\n") {
			t.Fatalf("stored %s:\n%s", name, data)
		}
	}

	// The web process owns the data directory: a tls/ replaced by a link
	// is refused, nothing is written where it points.
	tls := filepath.Join(c.Root, panelTLSDir)
	elsewhere := filepath.Join(c.Root, "etc", "bash_completion.d")
	if err := os.MkdirAll(elsewhere, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, tls); err != nil {
		t.Fatal(err)
	}
	if op, _ := e.run(helperapi.ActionPanelCert, helperapi.PanelCertRequest{ID: id}); op.State != helperapi.OpFailed {
		t.Fatal("a linked tls directory was written to")
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) > 0 {
		t.Fatalf("written through the link: %v", entries)
	}
	// Going back to self-signed does not remove anything through it either.
	writeSystemFile(t, c.Root, "/etc/bash_completion.d/panel.crt", "keep")
	if op, _ := e.run(helperapi.ActionPanelCert, helperapi.PanelCertRequest{}); op.State != helperapi.OpFailed || !e.exists("/etc/bash_completion.d/panel.crt") {
		t.Fatalf("removed through the link: %+v", op.Error)
	}
	if err := os.Remove(tls); err != nil {
		t.Fatal(err)
	}

	// A missing tls/ is made for the panel; a planted link in it is
	// replaced, not followed, and links under the names of earlier
	// versions go without what they point to.
	if op, log := e.run(helperapi.ActionPanelCert, helperapi.PanelCertRequest{ID: id}); op.State != helperapi.OpSucceeded {
		t.Fatalf("panel: %+v\n%s", op.Error, log)
	}
	if err := os.Remove(filepath.Join(tls, tlsutil.DeployedFile)); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{tlsutil.DeployedFile, tlsutil.LegacyCertFile, tlsutil.LegacyKeyFile} {
		if err := os.Symlink(filepath.Join(elsewhere, "panel.crt"), filepath.Join(tls, name)); err != nil {
			t.Fatal(err)
		}
	}
	if op, log := e.run(helperapi.ActionPanelCert, helperapi.PanelCertRequest{ID: id}); op.State != helperapi.OpSucceeded {
		t.Fatalf("panel again: %+v\n%s", op.Error, log)
	}
	if got := e.read("/etc/bash_completion.d/panel.crt"); got != "keep" {
		t.Fatalf("written through a link in tls/: %q", got)
	}
	info, err := os.Lstat(filepath.Join(tls, tlsutil.DeployedFile))
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("%s: %v %v", tlsutil.DeployedFile, info, err)
	}
	if entries, _ := os.ReadDir(tls); len(entries) != 1 {
		t.Fatalf("temporary or earlier files left: %v", entries)
	}
}

// leafWith is leaf with a key of the caller's choice and a validity period.
func (ca *testCA) leafWith(t *testing.T, key crypto.Signer, notBefore, notAfter time.Time, domain string) (string, string) {
	t.Helper()
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: domain}, DNSNames: []string{domain},
		NotBefore: notBefore, NotAfter: notAfter, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, key.Public(), ca.key)
	if err != nil {
		t.Fatal(err)
	}
	kd, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	chain := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})) + string(ca.pem)
	return chain, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kd}))
}

// Certificates that browsers do not take from a server are refused on
// upload and for the panel: a key other than RSA of 2048 bits or more,
// P-256 or P-384, or a certificate not valid yet. Those they take are
// served as before.
func TestCertRefusesWhatBrowsersRefuse(t *testing.T) {
	e, c := newCertEnv(t)
	ca := newTestCA(t)
	now := time.Now()
	gen := func(f func() (crypto.Signer, error)) crypto.Signer {
		k, err := f()
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	ec := func(curve elliptic.Curve) func() (crypto.Signer, error) {
		return func() (crypto.Signer, error) { return ecdsa.GenerateKey(curve, rand.Reader) }
	}
	rsaKey := func(bits int) func() (crypto.Signer, error) {
		return func() (crypto.Signer, error) { return rsa.GenerateKey(rand.Reader, bits) }
	}
	_, edKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	valid := [2]time.Time{now.Add(-time.Hour), now.Add(30 * 24 * time.Hour)}
	for i, tc := range []struct {
		name   string
		key    crypto.Signer
		period [2]time.Time
		code   string
	}{
		{"ed25519", edKey, valid, helperapi.CodeCertKeyUnsupported},
		{"P-521", gen(ec(elliptic.P521())), valid, helperapi.CodeCertKeyUnsupported},
		{"P-224", gen(ec(elliptic.P224())), valid, helperapi.CodeCertKeyUnsupported},
		{"RSA 1024", gen(rsaKey(1024)), valid, helperapi.CodeCertKeyUnsupported},
		{"not valid yet", gen(ec(elliptic.P256())), [2]time.Time{now.Add(time.Hour), now.Add(48 * time.Hour)}, helperapi.CodeCertNotYetValid},
		{"expired", gen(ec(elliptic.P256())), [2]time.Time{now.Add(-48 * time.Hour), now.Add(-time.Hour)}, helperapi.CodeCertExpired},
		{"P-256", gen(ec(elliptic.P256())), valid, ""},
		{"P-384", gen(ec(elliptic.P384())), valid, ""},
		{"RSA 2048", gen(rsaKey(2048)), valid, ""},
	} {
		chain, key := ca.leafWith(t, tc.key, tc.period[0], tc.period[1], "panel.example.com")
		id := fmt.Sprintf("c%015x", i+1)
		op, log := e.run(helperapi.ActionCertImport, helperapi.CertImportRequest{ID: id, CertPEM: chain, KeyPEM: key})
		if tc.code != "" {
			if op.State != helperapi.OpFailed || op.Error.Code != tc.code {
				t.Errorf("%s: upload %+v %+v", tc.name, op.State, op.Error)
			}
			// One stored by an earlier version is not served on the panel
			// either, and the panel keeps what it served.
			if _, err := c.store(id, "upload", []byte(chain), []byte(key)); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if op, _ := e.run(helperapi.ActionPanelCert, helperapi.PanelCertRequest{ID: id}); op.State != helperapi.OpFailed || op.Error.Code != tc.code {
				t.Errorf("%s: panel %+v %+v", tc.name, op.State, op.Error)
			}
			if e.exists(panelTLSDir+"/"+tlsutil.DeployedFile) || c.panelCert() != "" {
				t.Errorf("%s: deployed for the panel", tc.name)
			}
			continue
		}
		if op.State != helperapi.OpSucceeded {
			t.Fatalf("%s: upload %+v\n%s", tc.name, op.Error, log)
		}
		if op, log := e.run(helperapi.ActionPanelCert, helperapi.PanelCertRequest{ID: id}); op.State != helperapi.OpSucceeded || c.panelCert() != id {
			t.Fatalf("%s: panel %+v\n%s", tc.name, op.Error, log)
		}
		pair := e.read(panelTLSDir + "/" + tlsutil.DeployedFile)
		if _, err := tls.X509KeyPair([]byte(pair), []byte(pair)); err != nil {
			t.Fatalf("%s: deployed pair: %v", tc.name, err)
		}
		if op, _ := e.run(helperapi.ActionPanelCert, helperapi.PanelCertRequest{}); op.State != helperapi.OpSucceeded {
			t.Fatalf("%s: back to self-signed: %+v", tc.name, op.Error)
		}
	}
}

func TestServableKey(t *testing.T) {
	for kt, want := range map[string]bool{"ec256": true, "ec384": true, "rsa2048": true, "rsa3072": true, "rsa4096": true,
		"ec521": false, "ec224": false, "rsa1024": false, "rsa2047": false, "ed25519": false, "unknown": false, "rsa": false, "": false} {
		if got := servableKey(kt); got != want {
			t.Errorf("servableKey(%q) = %v", kt, got)
		}
	}
}

func TestCertReissueKeepsCoverage(t *testing.T) {
	e, c := newCertEnv(t)
	ca := newTestCA(t)
	const id = "c0123456789abcde"
	chain, key := ca.leaf(t, time.Now().Add(90*24*time.Hour), "shop.example.com", "www.shop.example.com")
	if op, log := e.run(helperapi.ActionCertImport, helperapi.CertImportRequest{ID: id, CertPEM: chain, KeyPEM: key}); op.State != helperapi.OpSucceeded {
		t.Fatalf("import: %+v\n%s", op.Error, log)
	}
	spec := helperapi.SiteSpec{Code: "shop", Type: helperapi.SiteStatic, Domains: []string{"shop.example.com", "www.shop.example.com"},
		TLS: &helperapi.SiteTLS{Cert: id}, Enabled: true}
	if op, log := e.run(helperapi.ActionSiteApply, spec); op.State != helperapi.OpSucceeded {
		t.Fatalf("site: %+v\n%s", op.Error, log)
	}
	// The keys' directory is root's alone.
	if info, err := os.Stat(filepath.Join(c.Root, nginxCertDir)); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("certificate directory: %v %v", info, err)
	}
	// A re-issue for fewer names is refused before the CA is asked.
	req := helperapi.CertIssueRequest{ID: id, Domains: []string{"shop.example.com"}, CA: helperapi.ACMECA{Name: "letsencrypt"},
		Challenge: helperapi.ChallengeHTTP01, KeyType: "ec256"}
	if _, err := c.issue(t.Context(), helperapi.Caller{}, req, io.Discard); code(err) != helperapi.CodeCertCoverage {
		t.Fatalf("narrow re-issue: %v", err)
	}
	if e.exists(acmeStateDir) {
		t.Fatal("the CA was contacted")
	}
}

func TestACMEClientRefusesMetadata(t *testing.T) {
	c := &Certs{}
	hc, err := c.httpClient(helperapi.ACMECA{Name: "custom", Directory: "https://ca.example/dir"})
	if err != nil {
		t.Fatal(err)
	}
	// A private CA on this host or the LAN is fine.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	t.Cleanup(srv.Close)
	resp, err := hc.Get(srv.URL)
	if err != nil {
		t.Fatalf("loopback CA: %v", err)
	}
	_ = resp.Body.Close()
	// Metadata services are not, and no proxy stands in between.
	t.Setenv("HTTP_PROXY", srv.URL)
	for _, u := range []string{"http://169.254.169.254/latest/meta-data/", "http://100.100.100.200/", "http://[fe80::1]/"} {
		resp, err := hc.Get(u)
		if err == nil {
			_ = resp.Body.Close()
		}
		var denied *netx.DeniedError
		if !errors.As(err, &denied) {
			t.Errorf("%s: %v", u, err)
		}
	}
}
