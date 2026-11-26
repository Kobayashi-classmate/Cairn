// SPDX-License-Identifier: AGPL-3.0-only

package tlsutil

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func readFile(t *testing.T, p string) []byte {
	t.Helper()
	data, err := os.ReadFile(p) //nolint:gosec // G304: inside the test's directory.
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// served is the leaf certificate h hands out.
func served(t *testing.T, h *Holder) *x509.Certificate {
	t.Helper()
	c, err := h.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil || c == nil {
		t.Fatalf("no certificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return leaf
}

func leafOf(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	c, err := tls.X509KeyPair(certPEM, certPEM)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return leaf
}

// otherPair is a key and certificate of another self-signed pair, as one
// PEM file (key first, as the helper deploys them).
func otherPair(t *testing.T) (pair, cert, key []byte) {
	t.Helper()
	dir := t.TempDir()
	c, k, err := EnsureSelfSigned(dir)
	if err != nil {
		t.Fatal(err)
	}
	cert, key = readFile(t, c), readFile(t, k)
	return append(append([]byte{}, key...), cert...), cert, key
}

// A deployed certificate is served, from the single file the helper
// writes, and from the two files of earlier versions.
func TestHolderServesDeployedCertificate(t *testing.T) {
	pair, cert, key := otherPair(t)
	want := leafOf(t, pair)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, DeployedFile), pair, 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := NewHolder(dir, quiet())
	if err != nil {
		t.Fatal(err)
	}
	if got := served(t, h); !got.Equal(want) {
		t.Fatal("the deployed certificate is not served")
	}

	legacy := t.TempDir()
	if err := os.WriteFile(filepath.Join(legacy, "panel.crt"), cert, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "panel.key"), key, 0o600); err != nil {
		t.Fatal(err)
	}
	h, err = NewHolder(legacy, quiet())
	if err != nil {
		t.Fatal(err)
	}
	if got := served(t, h); !got.Equal(want) {
		t.Fatal("the deployed certificate of an earlier version is not served")
	}
}

// A deployed pair that does not load (the key of another certificate, a
// file cut short) does not keep the panel from starting: it serves its
// self-signed certificate, says so in its log, and takes a certificate
// deployed later.
func TestHolderFallsBackToSelfSigned(t *testing.T) {
	pair, cert, _ := otherPair(t)
	_, _, strayKey := otherPair(t)
	for name, files := range map[string]map[string][]byte{
		// The key of the next certificate in place before the certificate,
		// the way earlier versions deployed them.
		"mismatched pair": {"panel.crt": cert, "panel.key": strayKey},
		"cut short":       {DeployedFile: pair[:len(pair)/2]},
		"empty":           {DeployedFile: nil},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			self := selfSigned(t, dir)
			for n, data := range files {
				if err := os.WriteFile(filepath.Join(dir, n), data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var log bytes.Buffer
			h, err := NewHolder(dir, slog.New(slog.NewTextHandler(&log, nil)))
			if err != nil {
				t.Fatalf("the panel does not start: %v", err)
			}
			if got := served(t, h); !got.Equal(self) {
				t.Fatal("not the self-signed certificate")
			}
			if !strings.Contains(log.String(), "self-signed") {
				t.Fatalf("log:\n%s", log.String())
			}
			// A certificate deployed afterwards is taken up.
			_ = os.Remove(filepath.Join(dir, "panel.crt"))
			_ = os.Remove(filepath.Join(dir, "panel.key"))
			if err := os.WriteFile(filepath.Join(dir, DeployedFile), pair, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := h.Reload(); err != nil {
				t.Fatal(err)
			}
			if got := served(t, h); !got.Equal(leafOf(t, pair)) {
				t.Fatal("the certificate deployed later is not served")
			}
		})
	}
}

// A self-signed pair that does not load is made anew.
func TestHolderRenewsBrokenSelfSigned(t *testing.T) {
	for name, damage := range map[string]func(cert, key string){
		"mismatched": func(_, key string) {
			_, _, k := otherPair(t)
			_ = os.WriteFile(key, k, 0o600)
		},
		"empty certificate": func(cert, _ string) { _ = os.WriteFile(cert, nil, 0o600) },
		"garbage key":       func(_, key string) { _ = os.WriteFile(key, []byte("not a key"), 0o600) },
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			old := selfSigned(t, dir)
			damage(filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem"))
			h, err := NewHolder(dir, quiet())
			if err != nil {
				t.Fatalf("the panel does not start: %v", err)
			}
			got := served(t, h)
			if got.Equal(old) {
				t.Fatal("the broken pair is still served")
			}
			if fresh := leafOf(t, append(readFile(t, filepath.Join(dir, "key.pem")), readFile(t, filepath.Join(dir, "cert.pem"))...)); !got.Equal(fresh) {
				t.Fatal("the new pair on disk is not the one served")
			}
		})
	}
}

// selfSigned makes dir's self-signed pair and returns its certificate.
func selfSigned(t *testing.T, dir string) *x509.Certificate {
	t.Helper()
	c, k, err := EnsureSelfSigned(dir)
	if err != nil {
		t.Fatal(err)
	}
	return leafOf(t, append(readFile(t, k), readFile(t, c)...))
}
