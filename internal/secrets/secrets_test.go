// SPDX-License-Identifier: AGPL-3.0-only

package secrets

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSealOpenBindsContext(t *testing.T) {
	b, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := b.Seal([]byte("JBSWY3DPEHPK3PXP"), "totp:user:1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "JBSWY3DPEHPK3PXP") || !strings.HasPrefix(sealed, versionPrefix) {
		t.Fatalf("unexpected sealed form %q", sealed)
	}
	pt, err := b.Open(sealed, "totp:user:1")
	if err != nil || string(pt) != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("open: %q %v", pt, err)
	}
	if _, err := b.Open(sealed, "totp:user:2"); !errors.Is(err, ErrOpen) {
		t.Fatal("value opened under another context")
	}
	tampered := sealed[:len(sealed)-2] + "AA"
	if _, err := b.Open(tampered, "totp:user:1"); !errors.Is(err, ErrOpen) {
		t.Fatal("tampered value opened")
	}
	// Same plaintext seals differently each time (random nonce).
	again, _ := b.Seal([]byte("JBSWY3DPEHPK3PXP"), "totp:user:1")
	if again == sealed {
		t.Fatal("nonce reuse")
	}
}

func TestKeyFileIsCreatedPrivateAndReused(t *testing.T) {
	dir := t.TempDir()
	b1, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(dir, keyFile))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode: %v %v", st.Mode(), err)
	}
	sealed, _ := b1.Seal([]byte("x"), "c")
	b2, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b2.Open(sealed, "c"); err != nil {
		t.Fatal("reloaded key differs")
	}
}

func TestCredentialsDirectoryWins(t *testing.T) {
	creds := t.TempDir()
	data := t.TempDir()
	key := strings.Repeat("A", 43) + "=" // 32 zero bytes, base64
	if err := os.WriteFile(filepath.Join(creds, keyFile), []byte(key), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREDENTIALS_DIRECTORY", creds)
	if _, err := LoadOrCreate(data); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(data, keyFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a data-dir key was created although a credential exists")
	}
}
