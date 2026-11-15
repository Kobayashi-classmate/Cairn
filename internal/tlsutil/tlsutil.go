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
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// EnsureSelfSigned returns cert and key paths inside dir, generating a new
// self-signed pair when either file is missing.
func EnsureSelfSigned(dir string) (certFile, keyFile string, err error) {
	certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if exists(certFile) && exists(keyFile) {
		return certFile, keyFile, nil
	}
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
// for it (panel.crt and panel.key in the TLS directory) when present, the
// self-signed one otherwise. It notices new files within a few seconds,
// so a new or renewed certificate needs no restart.
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
// current one.
func NewHolder(dir string) (*Holder, error) {
	certFile, keyFile, err := EnsureSelfSigned(dir)
	if err != nil {
		return nil, err
	}
	h := &Holder{dir: dir, selfCert: certFile, selfKey: keyFile, CheckEvery: 5 * time.Second}
	if err := h.Reload(); err != nil {
		return nil, err
	}
	return h, nil
}

// Files returns the certificate and key files in use.
func (h *Holder) Files() (string, string) {
	c, k := filepath.Join(h.dir, "panel.crt"), filepath.Join(h.dir, "panel.key")
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
