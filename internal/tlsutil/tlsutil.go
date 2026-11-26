// SPDX-License-Identifier: AGPL-3.0-only

// Package tlsutil provides the panel's first certificate: a self-signed
// ECDSA P-256 certificate covering the host name and local addresses. It is
// replaced once the administrator configures an ACME certificate.
package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The certificate the root helper deploys for the panel, in the TLS
// directory: the key and the chain in one file, so that both are replaced
// by a single rename and the panel never finds the key of one certificate
// with another. Earlier versions wrote two files, LegacyCertFile and
// LegacyKeyFile, which are still served until a certificate is deployed
// again.
const (
	DeployedFile   = "panel.pem"
	LegacyCertFile = "panel.crt"
	LegacyKeyFile  = "panel.key"
)

// EnsureSelfSigned returns cert and key paths inside dir, generating a new
// self-signed pair when either file is missing.
func EnsureSelfSigned(dir string) (certFile, keyFile string, err error) {
	certFile, keyFile = selfSignedFiles(dir)
	if exists(certFile) && exists(keyFile) {
		return certFile, keyFile, nil
	}
	return newSelfSigned(dir)
}

func selfSignedFiles(dir string) (certFile, keyFile string) {
	return filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
}

// newSelfSigned makes a new self-signed pair in dir, over the old one.
func newSelfSigned(dir string) (certFile, keyFile string, err error) {
	certFile, keyFile = selfSignedFiles(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return "", "", err
	}
	hostname, _ := os.Hostname()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: hostname, Organization: []string{"Cairn self-signed"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           LocalIPs(true),
	}
	if hostname != "" && hostname != "localhost" {
		tmpl.DNSNames = append(tmpl.DNSNames, hostname)
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return "", "", err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", "", err
	}
	if err := writePEM(keyFile, "PRIVATE KEY", keyDER, 0o600); err != nil {
		return "", "", err
	}
	if err := writePEM(certFile, "CERTIFICATE", der, 0o644); err != nil {
		return "", "", err
	}
	return certFile, keyFile, nil
}

// LocalIPs lists the addresses of up interfaces, loopback included if asked.
func LocalIPs(withLoopback bool) []net.IP {
	var ips []net.IP
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ips
	}
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.IsLinkLocalUnicast() || (!withLoopback && ipn.IP.IsLoopback()) {
			continue
		}
		ips = append(ips, ipn.IP)
	}
	return ips
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func writePEM(path, typ string, der []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode) //nolint:gosec // G304: path is built from the configured data directory.
	if err != nil {
		return err
	}
	if err := pem.Encode(f, &pem.Block{Type: typ, Bytes: der}); err != nil {
		return errors.Join(err, f.Close(), os.Remove(path))
	}
	return f.Close()
}

// Holder serves the panel's certificate: the one the root helper deployed
// for it (DeployedFile in the TLS directory, or the two files of earlier
// versions) when present, the self-signed one otherwise. It notices new
// files within a few seconds, so a new or renewed certificate needs no
// restart.
type Holder struct {
	dir               string
	selfCert, selfKey string
	mu                sync.Mutex
	cert              *tls.Certificate
	stamp             string
	checked           time.Time
	CheckEvery        time.Duration
}

// NewHolder prepares the self-signed certificate in dir and loads the
// current one. A deployed certificate that does not load (its key is
// another certificate's, a file was cut short or changed by hand) does not
// keep the panel from starting: the self-signed certificate is served
// instead, and the browser's warning leads to the panel, where another
// certificate can be set. A self-signed pair that does not load is made
// anew.
func NewHolder(dir string, log *slog.Logger) (*Holder, error) {
	certFile, keyFile, err := EnsureSelfSigned(dir)
	if err != nil {
		return nil, err
	}
	h := &Holder{dir: dir, selfCert: certFile, selfKey: keyFile, CheckEvery: 5 * time.Second}
	err = h.Reload()
	if err == nil {
		return h, nil
	}
	if c, _ := h.Files(); c != h.selfCert {
		log.Error("the panel's certificate does not load; the panel serves its self-signed certificate until another one is set",
			"file", c, "err", err)
		if err = h.loadSelfSigned(); err == nil {
			return h, nil
		}
	}
	log.Warn("the panel's self-signed certificate does not load; a new one is made", "dir", dir, "err", err)
	if _, _, err := newSelfSigned(dir); err != nil {
		return nil, err
	}
	if err := h.loadSelfSigned(); err != nil {
		return nil, err
	}
	return h, nil
}

// loadSelfSigned serves the self-signed pair. The files in use are taken
// as seen: Reload tries them again once they change.
func (h *Holder) loadSelfSigned() error {
	stamp := stampOf(h.Files())
	cert, err := tls.LoadX509KeyPair(h.selfCert, h.selfKey)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cert, h.stamp, h.checked = &cert, stamp, time.Now()
	return nil
}

// Files returns the certificate and key files in use.
func (h *Holder) Files() (string, string) {
	if p := filepath.Join(h.dir, DeployedFile); exists(p) {
		return p, p
	}
	c, k := filepath.Join(h.dir, LegacyCertFile), filepath.Join(h.dir, LegacyKeyFile)
	if exists(c) && exists(k) {
		return c, k
	}
	return h.selfCert, h.selfKey
}

func stampOf(files ...string) string {
	var b strings.Builder
	for _, f := range files {
		if st, err := os.Stat(f); err == nil {
			fmt.Fprintf(&b, "%s:%d:%d;", f, st.Size(), st.ModTime().UnixNano())
		}
	}
	return b.String()
}

// Reload loads the certificate in use now. A deployed certificate that
// does not load (a half-written file) keeps the previous one.
func (h *Holder) Reload() error {
	certFile, keyFile := h.Files()
	stamp := stampOf(certFile, keyFile)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.checked = time.Now()
	if stamp == h.stamp && h.cert != nil {
		return nil
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		if h.cert != nil {
			return nil
		}
		return err
	}
	h.cert, h.stamp = &cert, stamp
	return nil
}

// GetCertificate implements tls.Config.GetCertificate.
func (h *Holder) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	h.mu.Lock()
	due := time.Since(h.checked) > h.CheckEvery
	h.mu.Unlock()
	if due {
		_ = h.Reload()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cert, nil
}
