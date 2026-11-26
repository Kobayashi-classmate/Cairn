// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	stdlog "log"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge/dns01"
	"github.com/go-acme/lego/v4/challenge/http01"
	"github.com/go-acme/lego/v4/lego"
	legolog "github.com/go-acme/lego/v4/log"
	"github.com/go-acme/lego/v4/providers/http/webroot"
	"github.com/go-acme/lego/v4/registration"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/netx"
	"github.com/Kobayashi-classmate/Cairn/internal/tlsutil"
)

// LockACME serializes ACME issuance (one order at a time; lego's logger is
// global).
const LockACME = "acme"

// Locations of certificates. Keys never leave the root helper's state,
// except the copies nginx and the panel read.
const (
	certStateDir   = "/var/lib/cairn-helper/certs"
	acmeStateDir   = "/var/lib/cairn-helper/acme"
	panelCertState = "/var/lib/cairn-helper/panel-cert"
	nginxCertDir   = "/etc/nginx/cairn/certs"
	acmeWebroot    = "/var/lib/cairn-acme"
	panelDataDir   = "/var/lib/cairn"
	panelTLSName   = "tls" // in the panel's data directory
	panelTLSDir    = panelDataDir + "/" + panelTLSName
)

// Certs issues, imports and deploys certificates: through ACME (lego, with
// HTTP-01 through nginx or a temporary listener, DNS-01 through libdns
// providers) or uploaded, for sites (nginx) and for the panel itself.
type Certs struct {
	Sites *Sites
	Root  string
	// PanelUser owns the panel's copy of its certificate (cairn).
	PanelUser string
	// DataDir is the panel's data directory, whose tls/ holds that copy
	// (empty: /var/lib/cairn).
	DataDir string
	// Resolvers are the DNS servers DNS-01 checks propagation with
	// ("host:port"; configuration, tests). Empty uses the system's.
	Resolvers []string
	// HTTPTransport is the base transport to the CA (tests).
	HTTPTransport *http.Transport
}

// RegisterCerts adds the certificate actions.
func RegisterCerts(r *Registry, o *Ops, c *Certs) {
	issueLocks := func(helperapi.CertIssueRequest) []string { return []string{LockACME, LockWeb} }
	RegisterOp(r, o, helperapi.ActionCertIssue, OpSpec[helperapi.CertIssueRequest]{Locks: issueLocks, Run: c.issue})
	RegisterOp(r, o, helperapi.ActionCertImport, OpSpec[helperapi.CertImportRequest]{
		Locks: func(helperapi.CertImportRequest) []string { return []string{LockWeb} }, Run: c.importCert})
	RegisterOp(r, o, helperapi.ActionCertRemove, OpSpec[helperapi.CertRef]{
		Locks: func(helperapi.CertRef) []string { return []string{LockWeb} }, Run: c.remove})
	RegisterOp(r, o, helperapi.ActionPanelCert, OpSpec[helperapi.PanelCertRequest]{
		Locks: func(helperapi.PanelCertRequest) []string { return []string{LockWeb} }, Run: c.setPanel})
	Register(r, helperapi.ActionCertStatus, func(_ context.Context, _ helperapi.Caller, _ helperapi.Empty) (helperapi.CertStatus, error) {
		return c.Status(), nil
	})
	Register(r, helperapi.ActionDNSTest, func(ctx context.Context, _ helperapi.Caller, req helperapi.DNSTestRequest) (helperapi.DNSTestResult, error) {
		return testDNS(ctx, req)
	})
}

func (c *Certs) path(p string) string { return filepath.Join(c.Root, p) }

func (c *Certs) dataDir() string {
	if c.DataDir != "" {
		return c.DataDir
	}
	return panelDataDir
}

// stored certificates ------------------------------------------------------

// pemOnly re-encodes the PEM blocks of data that keep accepts (at most max
// of them, 0 for all) and drops everything else: text before, between and
// after the blocks, and the blocks' headers. Only PEM is ever stored or
// deployed, whatever else an upload carried along (pem.Decode skips it).
func pemOnly(data []byte, keep func(typ string) bool, max int) []byte {
	var out []byte
	for n := 0; max == 0 || n < max; {
		b, rest := pem.Decode(data)
		if b == nil {
			break
		}
		data = rest
		if keep(b.Type) {
			out = append(out, pem.EncodeToMemory(&pem.Block{Type: b.Type, Bytes: b.Bytes})...)
			n++
		}
	}
	return out
}

// cleanCertPEM keeps the certificates of a chain and the first block of a
// key: what parseCert reads of them.
func cleanCertPEM(certPEM, keyPEM []byte) ([]byte, []byte) {
	return pemOnly(certPEM, func(t string) bool { return t == "CERTIFICATE" }, 0),
		pemOnly(keyPEM, func(string) bool { return true }, 1)
}

// parseCert checks a PEM chain (leaf first) and key: they parse, the key
// belongs to the leaf, and it returns what the panel shows about them.
func parseCert(certPEM, keyPEM []byte) (helperapi.CertInfo, error) {
	var info helperapi.CertInfo
	var chain []*x509.Certificate
	rest := certPEM
	for {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		if b.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			return info, pkgErr(helperapi.CodeCertInvalid, map[string]any{"reason": "certificate: " + err.Error()})
		}
		chain = append(chain, cert)
	}
	if len(chain) == 0 {
		return info, pkgErr(helperapi.CodeCertInvalid, map[string]any{"reason": "no certificate in the PEM data"})
	}
	key, err := parseKey(keyPEM)
	if err != nil {
		return info, pkgErr(helperapi.CodeCertInvalid, map[string]any{"reason": "private key: " + err.Error()})
	}
	leaf := chain[0]
	if !samePublicKey(leaf.PublicKey, key) {
		return info, pkgErr(helperapi.CodeCertInvalid, map[string]any{"reason": "the private key does not belong to the certificate"})
	}
	for i := 1; i < len(chain); i++ {
		if err := chain[i-1].CheckSignatureFrom(chain[i]); err != nil {
			return info, pkgErr(helperapi.CodeCertInvalid, map[string]any{"reason": fmt.Sprintf("chain: certificate %d is not signed by the next one", i)})
		}
	}
	sum := sha256.Sum256(leaf.Raw)
	info = helperapi.CertInfo{Domains: append([]string{}, leaf.DNSNames...), NotBefore: leaf.NotBefore.Unix(), NotAfter: leaf.NotAfter.Unix(),
		Issuer: leaf.Issuer.String(), Serial: leaf.SerialNumber.Text(16), Fingerprint: hex.EncodeToString(sum[:]), KeyType: keyTypeOf(leaf.PublicKey)}
	for _, ip := range leaf.IPAddresses {
		info.Domains = append(info.Domains, ip.String())
	}
	if len(info.Domains) == 0 && leaf.Subject.CommonName != "" {
		info.Domains = []string{leaf.Subject.CommonName}
	}
	for _, cert := range chain {
		info.Chain = append(info.Chain, helperapi.ChainCert{Subject: cert.Subject.String(), Issuer: cert.Issuer.String(), NotAfter: cert.NotAfter.Unix()})
	}
	return info, nil
}

func parseKey(data []byte) (crypto.PrivateKey, error) {
	b, _ := pem.Decode(data)
	if b == nil {
		return nil, errors.New("no PEM block")
	}
	if strings.Contains(b.Type, "ENCRYPTED") {
		return nil, errors.New("the key is encrypted; upload it without a passphrase")
	}
	if k, err := x509.ParsePKCS8PrivateKey(b.Bytes); err == nil {
		return k, nil
	}
	if k, err := x509.ParseECPrivateKey(b.Bytes); err == nil {
		return k, nil
	}
	if k, err := x509.ParsePKCS1PrivateKey(b.Bytes); err == nil {
		return k, nil
	}
	return nil, errors.New("unsupported key format")
}

func samePublicKey(pub any, key crypto.PrivateKey) bool {
	type publicKeyer interface{ Public() crypto.PublicKey }
	pk, ok := key.(publicKeyer)
	if !ok {
		return false
	}
	type equaler interface{ Equal(crypto.PublicKey) bool }
	e, ok := pk.Public().(equaler)
	return ok && e.Equal(pub)
}

func keyTypeOf(pub any) string {
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		return fmt.Sprintf("ec%d", k.Curve.Params().BitSize)
	case *rsa.PublicKey:
		return fmt.Sprintf("rsa%d", k.N.BitLen())
	case ed25519.PublicKey:
		return "ed25519"
	}
	return "unknown"
}

func certDir(id string) string { return path.Join(certStateDir, id) }

// readCertInfo returns a stored certificate's description.
func readCertInfo(root, id string) (helperapi.CertInfo, bool) {
	data, err := os.ReadFile(filepath.Join(root, certDir(id), "info.json")) //nolint:gosec // G304: validated certificate ID.
	var info helperapi.CertInfo
	if err != nil || json.Unmarshal(data, &info) != nil || info.ID != id {
		return info, false
	}
	return info, true
}

// readCertFiles returns a stored certificate's PEM chain and key, as PEM
// only (certificates stored before uploads were cleaned may carry text).
func readCertFiles(root, id string) ([]byte, []byte, error) {
	cert, err := os.ReadFile(filepath.Join(root, certDir(id), "cert.pem")) //nolint:gosec // G304: as above.
	if err != nil {
		return nil, nil, err
	}
	key, err := os.ReadFile(filepath.Join(root, certDir(id), "key.pem")) //nolint:gosec // G304: as above.
	if err != nil {
		return nil, nil, err
	}
	cert, key = cleanCertPEM(cert, key)
	return cert, key, nil
}

// covers reports the names of domains a certificate's names do not cover
// (a wildcard covers one label).
func uncovered(domains, names []string) []string {
	var out []string
	for _, d := range domains {
		ok := false
		for _, n := range names {
			if n == d {
				ok = true
			} else if rest, found := strings.CutPrefix(n, "*."); found {
				if _, after, cut := strings.Cut(d, "."); cut && after == rest && !strings.HasPrefix(d, "*.") {
					ok = true
				}
			}
		}
		if !ok {
			out = append(out, d)
		}
	}
	return out
}

// store keeps a certificate in the helper's state, as PEM only.
func (c *Certs) store(id, source string, certPEM, keyPEM []byte) (helperapi.CertInfo, error) {
	certPEM, keyPEM = cleanCertPEM(certPEM, keyPEM)
	info, err := parseCert(certPEM, keyPEM)
	if err != nil {
		return info, err
	}
	info.ID, info.Source, info.UpdatedAt = id, source, time.Now().Unix()
	dir := c.path(certDir(id))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return info, err
	}
	meta, _ := json.Marshal(info)
	for name, data := range map[string][]byte{"cert.pem": certPEM, "key.pem": keyPEM, "info.json": meta} {
		if err := writePrivate(filepath.Join(dir, name), data); err != nil {
			return info, err
		}
	}
	return info, nil
}

// users of a certificate: the sites applied with it, and the panel.
func (c *Certs) usedBy(id string) ([]string, bool) {
	var sites []string
	if c.Sites != nil {
		states, _ := c.Sites.states()
		for code, s := range states {
			if s.Spec.TLS != nil && s.Spec.TLS.Cert == id {
				sites = append(sites, code)
			}
		}
	}
	sort.Strings(sites)
	return sites, c.panelCert() == id
}

func (c *Certs) panelCert() string {
	data, err := os.ReadFile(c.path(panelCertState))
	if err != nil {
		return ""
	}
	id := strings.TrimSpace(string(data))
	if !helperapi.ValidCertID(id) {
		return ""
	}
	return id
}

// Status lists the stored certificates.
func (c *Certs) Status() helperapi.CertStatus {
	st := helperapi.CertStatus{Certs: map[string]helperapi.CertInfo{}, Panel: c.panelCert()}
	entries, _ := os.ReadDir(c.path(certStateDir))
	for _, e := range entries {
		if info, ok := readCertInfo(c.Root, e.Name()); ok {
			info.Sites, info.Panel = c.usedBy(info.ID)
			st.Certs[info.ID] = info
		}
	}
	return st
}

// deploy puts a stored certificate where its users read it: nginx (with a
// check and reload when sites use it) and the panel.
func (c *Certs) deploy(ctx context.Context, log io.Writer, id string) error {
	certPEM, keyPEM, err := readCertFiles(c.Root, id)
	if err != nil {
		return err
	}
	sites, panel := c.usedBy(id)
	if len(sites) > 0 && c.Sites != nil {
		if err := privateDir(c.path(nginxCertDir)); err != nil {
			return err
		}
		changes := []fileChange{
			{path: path.Join(nginxCertDir, id+".crt"), data: certPEM},
			{path: path.Join(nginxCertDir, id+".key"), data: keyPEM, mode: 0o600},
		}
		if _, err := c.Sites.transact(ctx, log, changes, []configCheck{c.Sites.nginxCheck()}, []reloader{c.Sites.nginxReload()}); err != nil {
			return err
		}
	}
	if panel {
		return c.deployPanel(log, certPEM, keyPEM)
	}
	return nil
}

// panelTLS opens the panel's TLS directory (tls/ in its data directory).
// The data directory and everything in it belong to the web process, so
// every path is resolved inside it: a link planted there (tls itself, or a
// file in it) cannot send the helper's writes anywhere else. With create,
// a missing tls/ is made for the panel's user.
func (c *Certs) panelTLS(create bool, uid, gid int) (*os.Root, error) {
	data, err := os.OpenRoot(c.path(c.dataDir()))
	if err != nil {
		return nil, err
	}
	defer func() { _ = data.Close() }()
	if create {
		if err := data.Mkdir(panelTLSName, 0o700); err == nil {
			if err := data.Lchown(panelTLSName, uid, gid); err != nil {
				return nil, err
			}
		} else if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
	}
	info, err := data.Lstat(panelTLSName)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", path.Join(c.dataDir(), panelTLSName))
	}
	return data.OpenRoot(panelTLSName)
}

// writeOwned replaces name inside root with data: a new file of a random
// name (O_EXCL: never an existing file or link), given its mode and owner
// through its descriptor before the content, then renamed over name. As
// for writeFileAs, the content is on the disk before the rename, and the
// rename before it returns.
func writeOwned(root *os.Root, name string, data []byte, mode os.FileMode, uid, gid int) error {
	tmp := path.Join(path.Dir(name), "."+path.Base(name)+"."+newHexID(""))
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	done := false
	defer func() {
		if !done {
			_ = f.Close()
			_ = root.Remove(tmp)
		}
	}()
	if err := f.Chmod(mode); err != nil { // the umask may have taken bits
		return err
	}
	if err := f.Chown(uid, gid); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := fsync(f); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	done = true
	if err := root.Rename(tmp, name); err != nil {
		_ = root.Remove(tmp)
		return err
	}
	syncRootDir(root, path.Dir(name))
	return nil
}

// deployPanel writes the panel's copy, readable by the web process only;
// the web process picks it up without a restart. The key and the chain go
// into one file, which a single rename puts in place: the panel never
// finds the key of one certificate with another, whatever cuts the
// deployment short. The two files of earlier versions go afterwards.
func (c *Certs) deployPanel(log io.Writer, certPEM, keyPEM []byte) error {
	uid, gid, err := lookupUser(c.PanelUser)
	if err != nil {
		return err
	}
	root, err := c.panelTLS(true, uid, gid)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	pair := append(append([]byte{}, keyPEM...), certPEM...)
	if err := writeOwned(root, tlsutil.DeployedFile, pair, 0o600, uid, gid); err != nil {
		return err
	}
	if err := removePanelFiles(root, tlsutil.LegacyCertFile, tlsutil.LegacyKeyFile); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(log, "The panel serves the certificate now.\n")
	return nil
}

// removePanelFiles removes files of the panel's TLS directory, inside it:
// no link there may point the removal elsewhere.
func removePanelFiles(root *os.Root, names ...string) error {
	for _, name := range names {
		if err := root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// servableKey reports whether browsers take a server certificate with a
// key of this type (keyTypeOf): RSA of 2048 bits or more, or ECDSA on
// P-256 or P-384. No browser takes Ed25519 from a server, and Chrome does
// not take P-521.
func servableKey(keyType string) bool {
	switch keyType {
	case "ec256", "ec384":
		return true
	}
	bits, ok := strings.CutPrefix(keyType, "rsa")
	if !ok {
		return false
	}
	n, err := strconv.Atoi(bits)
	return err == nil && n >= 2048
}

// checkServable refuses a certificate browsers would refuse from a server
// at now: outside its validity period, or with a key they do not take.
func checkServable(info helperapi.CertInfo, now time.Time) error {
	if time.Unix(info.NotAfter, 0).Before(now) {
		return pkgErr(helperapi.CodeCertExpired, map[string]any{"notAfter": info.NotAfter})
	}
	if now.Before(time.Unix(info.NotBefore, 0)) {
		return pkgErr(helperapi.CodeCertNotYetValid, map[string]any{"notBefore": info.NotBefore})
	}
	if !servableKey(info.KeyType) {
		return pkgErr(helperapi.CodeCertKeyUnsupported, map[string]any{"keyType": info.KeyType})
	}
	return nil
}

// ACME ---------------------------------------------------------------------

type acmeAccount struct {
	Email string                 `json:"email"`
	Key   string                 `json:"key"` // PEM
	Reg   *registration.Resource `json:"registration"`
	key   crypto.PrivateKey
}

func (a *acmeAccount) GetEmail() string                        { return a.Email }
func (a *acmeAccount) GetRegistration() *registration.Resource { return a.Reg }
func (a *acmeAccount) GetPrivateKey() crypto.PrivateKey        { return a.key }

func accountDir(ca helperapi.ACMECA) string {
	if ca.Name == "custom" {
		sum := sha256.Sum256([]byte(ca.Directory))
		return path.Join(acmeStateDir, "custom-"+hex.EncodeToString(sum[:6]))
	}
	return path.Join(acmeStateDir, ca.Name)
}

func (c *Certs) httpClient(ca helperapi.ACMECA) (*http.Client, error) {
	tr := &http.Transport{TLSHandshakeTimeout: 30 * time.Second, ResponseHeaderTimeout: 2 * time.Minute}
	if c.HTTPTransport != nil {
		tr = c.HTTPTransport.Clone()
	}
	// A custom directory is any URL the panel's user typed: it (and every
	// URL the CA's answers lead to) may reach this host and the LAN (a
	// private CA), never a cloud metadata service. Checked on each
	// connection, after name resolution, so no proxy in between.
	tr.Proxy = nil
	tr.DialContext = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second,
		Control: netx.DialControl(netx.IsMetadata)}).DialContext
	if ca.RootPEM != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM([]byte(ca.RootPEM)) {
			return nil, pkgErr(helperapi.CodeCertAccount, map[string]any{"reason": "the CA root certificate does not parse"})
		}
		cfg := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
		tr.TLSClientConfig = cfg
	}
	return &http.Client{Transport: tr, Timeout: 5 * time.Minute}, nil
}

// client returns a lego client with the CA's account, registering it first
// if needed (once per CA; the account key stays in the helper's state).
func (c *Certs) client(log io.Writer, ca helperapi.ACMECA, keyType string) (*lego.Client, error) {
	dir := c.path(accountDir(ca))
	acct := &acmeAccount{}
	if data, err := os.ReadFile(filepath.Join(dir, "account.json")); err == nil { //nolint:gosec // G304: fixed state path.
		if err := json.Unmarshal(data, acct); err != nil {
			return nil, err
		}
		k, err := parseKey([]byte(acct.Key))
		if err != nil {
			return nil, err
		}
		acct.key = k
	} else {
		k, err := certcrypto.GeneratePrivateKey(certcrypto.EC256)
		if err != nil {
			return nil, err
		}
		acct.key, acct.Key, acct.Email = k, string(certcrypto.PEMEncode(k)), ca.Email
	}
	hc, err := c.httpClient(ca)
	if err != nil {
		return nil, err
	}
	cfg := lego.NewConfig(acct)
	cfg.CADirURL, cfg.HTTPClient, cfg.UserAgent = ca.DirectoryURL(), hc, "Cairn"
	cfg.Certificate.KeyType = map[string]certcrypto.KeyType{"ec256": certcrypto.EC256, "ec384": certcrypto.EC384,
		"rsa2048": certcrypto.RSA2048, "rsa4096": certcrypto.RSA4096}[keyType]
	cfg.Certificate.Timeout = 2 * time.Minute
	cl, err := lego.NewClient(cfg)
	if err != nil {
		return nil, pkgErr(helperapi.CodeCertAccount, map[string]any{"reason": tailString(err.Error(), 300), "directory": ca.DirectoryURL()})
	}
	if acct.Reg == nil {
		_, _ = fmt.Fprintf(log, "Registering an account at %s\n", ca.DirectoryURL())
		var reg *registration.Resource
		if ca.EABKid != "" {
			reg, err = cl.Registration.RegisterWithExternalAccountBinding(registration.RegisterEABOptions{TermsOfServiceAgreed: true, Kid: ca.EABKid, HmacEncoded: ca.EABHMAC})
		} else {
			reg, err = cl.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
		}
		if err != nil {
			return nil, pkgErr(helperapi.CodeCertAccount, map[string]any{"reason": tailString(err.Error(), 300)})
		}
		acct.Reg = reg
		data, _ := json.Marshal(acct)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		if err := writePrivate(filepath.Join(dir, "account.json"), data); err != nil {
			return nil, err
		}
	}
	return cl, nil
}

// nginxServing reports whether nginx runs (sites answer HTTP-01 through it).
func (c *Certs) nginxServing(ctx context.Context) bool {
	if c.Sites == nil || c.Sites.Unsupported != "" {
		return false
	}
	if _, err := os.Stat(c.path(nginxBinary)); err != nil {
		return false
	}
	if systemdRunning() {
		return c.Sites.unitActive(ctx, "nginx")
	}
	_, ok := processAlive(c.path(nginxPidFile))
	return ok
}

// http01 prepares HTTP-01: through nginx (every requested domain answered
// from the shared webroot; domains no site serves get a temporary server
// block), or a temporary listener on port 80 when nginx does not run. The
// returned function undoes the preparation.
func (c *Certs) http01(ctx context.Context, log io.Writer, id string, domains []string) (any, func(), error) {
	logf := func(format string, a ...any) { _, _ = fmt.Fprintf(log, format, a...) }
	if !c.nginxServing(ctx) {
		ln, err := net.Listen("tcp", ":80") //nolint:gosec // G102: only checks that port 80 is free for the validation listener.
		if err != nil {
			return nil, nil, pkgErr(helperapi.CodeCertHTTPNoServer, map[string]any{"reason": err.Error()})
		}
		_ = ln.Close()
		logf("nginx is not running; answering HTTP-01 on port 80 directly.\n")
		return http01.NewProviderServer("", "80"), func() {}, nil
	}
	if err := os.MkdirAll(c.path(acmeWebroot), 0o755); err != nil { //nolint:gosec // G301: nginx serves the challenge files.
		return nil, nil, err
	}
	// Sites serving the domains: re-applied so that their server blocks
	// carry the challenge location (blocks from before it existed).
	states, _ := c.Sites.states()
	served := map[string]bool{}
	for _, s := range states {
		hit := false
		for _, d := range s.Spec.Domains {
			if contains(domains, d) {
				served[d], hit = true, true
			}
		}
		if hit {
			if _, err := c.Sites.apply(ctx, helperapi.Caller{}, s.Spec, log); err != nil {
				return nil, nil, err
			}
		}
	}
	var rest []string
	for _, d := range domains {
		if !served[d] {
			rest = append(rest, d)
		}
	}
	undo := func() {}
	if len(rest) > 0 {
		conf := fmt.Sprintf("# %s\n# Temporary: answers ACME HTTP-01 for certificate %s.\nserver {\n    listen 80;\n    listen [::]:80;\n    server_name %s;\n%s\n    location / {\n        return 404;\n    }\n}\n",
			siteManaged, id, strings.Join(rest, " "), indent(acmeLocation))
		p := path.Join(nginxCairnDir, "sites", "_acme-"+id+".conf")
		changes := []fileChange{{path: path.Join(nginxConfD, "cairn.conf"), data: []byte(globalNginx)}, {path: p, data: []byte(conf)}}
		if _, err := c.Sites.transact(ctx, log, changes, []configCheck{c.Sites.nginxCheck()}, []reloader{c.Sites.nginxReload()}); err != nil {
			return nil, nil, err
		}
		undo = func() {
			_, _ = c.Sites.transact(context.WithoutCancel(ctx), log, []fileChange{{path: p}}, []configCheck{c.Sites.nginxCheck()}, []reloader{c.Sites.nginxReload()})
		}
	}
	prov, err := webroot.NewHTTPProvider(c.path(acmeWebroot))
	if err != nil {
		undo()
		return nil, nil, err
	}
	return prov, undo, nil
}

// issue implements cert.issue: a new certificate for the domains, stored
// and deployed to its users (renewal is the same request again).
func (c *Certs) issue(ctx context.Context, _ helperapi.Caller, req helperapi.CertIssueRequest, log io.Writer) (any, error) {
	logf := func(format string, a ...any) { _, _ = fmt.Fprintf(log, format, a...) }
	// Checked before the CA is asked: a certificate it issues counts
	// against its rate limits even when it is not used.
	if err := c.coverage(req.ID, req.Domains); err != nil {
		return nil, err
	}
	logf("Requesting a certificate for %s from %s (%s).\n", strings.Join(req.Domains, " "), req.CA.DirectoryURL(), req.Challenge)
	// lego logs through a global logger; issuance holds the acme lock.
	legolog.Logger = stdlog.New(log, "", 0)
	defer func() { legolog.Logger = stdlog.New(io.Discard, "", 0) }()

	cl, err := c.client(log, req.CA, req.KeyType)
	if err != nil {
		return nil, err
	}
	switch req.Challenge {
	case helperapi.ChallengeHTTP01:
		prov, undo, err := c.http01(ctx, log, req.ID, req.Domains)
		if err != nil {
			return nil, err
		}
		defer undo()
		p, ok := prov.(interface {
			Present(domain, token, keyAuth string) error
			CleanUp(domain, token, keyAuth string) error
		})
		if !ok {
			return nil, errors.New("http-01 provider")
		}
		if err := cl.Challenge.SetHTTP01Provider(p); err != nil {
			return nil, err
		}
	case helperapi.ChallengeDNS01:
		p, err := newDNSProvider(*req.DNS)
		if err != nil {
			return nil, err
		}
		solver := &dnsChallenge{p: p, resolvers: c.Resolvers, log: logf}
		// With configured resolvers (internal or split-horizon DNS) they
		// alone decide whether the record is visible: the zone's
		// authoritative servers may have names only they resolve.
		configured := len(c.Resolvers) > 0
		if err := cl.Challenge.SetDNS01Provider(solver,
			dns01.CondOption(configured, dns01.AddRecursiveNameservers(c.Resolvers)),
			dns01.CondOption(configured, dns01.DisableAuthoritativeNssPropagationRequirement())); err != nil {
			return nil, err
		}
	}
	res, err := cl.Certificate.Obtain(certificate.ObtainRequest{Domains: req.Domains, Bundle: true})
	if err != nil {
		return nil, pkgErr(helperapi.CodeCertACME, map[string]any{"reason": tailString(err.Error(), 600)})
	}
	info, err := c.store(req.ID, "acme", res.Certificate, res.PrivateKey)
	if err != nil {
		return nil, err
	}
	logf("Issued by %s, valid until %s.\n", info.Issuer, time.Unix(info.NotAfter, 0).UTC().Format(time.DateOnly))
	if err := c.deploy(ctx, log, req.ID); err != nil {
		return nil, err
	}
	info.Sites, info.Panel = c.usedBy(req.ID)
	return info, nil
}

// coverage refuses names that would no longer cover the domains of the
// sites using certificate id: a replacement or a re-issue must still
// serve every one of them.
func (c *Certs) coverage(id string, names []string) error {
	if _, ok := readCertInfo(c.Root, id); !ok || c.Sites == nil {
		return nil
	}
	sites, _ := c.usedBy(id)
	states, _ := c.Sites.states()
	for _, code := range sites {
		if miss := uncovered(states[code].Spec.Domains, names); len(miss) > 0 {
			return pkgErr(helperapi.CodeCertCoverage, map[string]any{"site": code, "domains": strings.Join(miss, " ")})
		}
	}
	return nil
}

// importCert implements cert.import.
func (c *Certs) importCert(ctx context.Context, _ helperapi.Caller, req helperapi.CertImportRequest, log io.Writer) (any, error) {
	info, err := parseCert([]byte(req.CertPEM), []byte(req.KeyPEM))
	if err != nil {
		return nil, err
	}
	if err := checkServable(info, time.Now()); err != nil {
		return nil, err
	}
	if err := c.coverage(req.ID, info.Domains); err != nil {
		return nil, err
	}
	info, err = c.store(req.ID, "upload", []byte(req.CertPEM), []byte(req.KeyPEM))
	if err != nil {
		return nil, err
	}
	_, _ = fmt.Fprintf(log, "Stored the certificate for %s (issued by %s).\n", strings.Join(info.Domains, " "), info.Issuer)
	if err := c.deploy(ctx, log, req.ID); err != nil {
		return nil, err
	}
	info.Sites, info.Panel = c.usedBy(req.ID)
	return info, nil
}

// remove implements cert.remove: only a certificate nothing uses.
func (c *Certs) remove(_ context.Context, _ helperapi.Caller, req helperapi.CertRef, log io.Writer) (any, error) {
	if _, ok := readCertInfo(c.Root, req.ID); !ok {
		return nil, pkgErr(helperapi.CodeCertNotFound, map[string]any{"id": req.ID})
	}
	sites, panel := c.usedBy(req.ID)
	if len(sites) > 0 || panel {
		return nil, pkgErr(helperapi.CodeCertInUse, map[string]any{"sites": strings.Join(sites, " "), "panel": panel})
	}
	for _, p := range []string{path.Join(nginxCertDir, req.ID+".crt"), path.Join(nginxCertDir, req.ID+".key")} {
		if err := os.Remove(c.path(p)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	if err := os.RemoveAll(c.path(certDir(req.ID))); err != nil {
		return nil, err
	}
	_, _ = fmt.Fprintf(log, "Certificate %s deleted.\n", req.ID)
	return helperapi.CertRef{ID: req.ID}, nil
}

// setPanel implements panel.cert.
func (c *Certs) setPanel(_ context.Context, _ helperapi.Caller, req helperapi.PanelCertRequest, log io.Writer) (any, error) {
	if req.ID == "" {
		root, err := c.panelTLS(false, 0, 0)
		switch {
		case err == nil:
			err = removePanelFiles(root, tlsutil.DeployedFile, tlsutil.LegacyCertFile, tlsutil.LegacyKeyFile)
			_ = root.Close()
			if err != nil {
				return nil, err
			}
		case !errors.Is(err, fs.ErrNotExist):
			return nil, err
		}
		if err := os.Remove(c.path(panelCertState)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		_, _ = fmt.Fprintf(log, "The panel serves its self-signed certificate again.\n")
		return helperapi.PanelCertRequest{}, nil
	}
	certPEM, keyPEM, err := readCertFiles(c.Root, req.ID)
	if err != nil {
		return nil, pkgErr(helperapi.CodeCertNotFound, map[string]any{"id": req.ID})
	}
	// Checked again on what is stored, whatever the web process saw: a
	// certificate browsers refuse would lock everyone out of the panel.
	info, err := parseCert(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	if err := checkServable(info, time.Now()); err != nil {
		return nil, err
	}
	if err := c.deployPanel(log, certPEM, keyPEM); err != nil {
		return nil, err
	}
	if err := writePrivate(c.path(panelCertState), []byte(req.ID+"\n")); err != nil {
		return nil, err
	}
	return req, nil
}

// writeFileMode writes a file atomically with a mode.
func writeFileMode(p string, data []byte, mode os.FileMode) error {
	return writeFileAs(p, data, mode)
}
