// SPDX-License-Identifier: AGPL-3.0-only

// Package secrets encrypts values stored at rest (TOTP seeds today, provider
// API keys later) with AES-256-GCM under a master key.
//
// The master key is taken from systemd's credential directory when the unit
// passes one (LoadCredential/LoadCredentialEncrypted, which can be bound to a
// TPM); otherwise it is a 0600 file in the panel's data directory, created on
// first use. Every sealed value is bound to a context string (for example
// "totp:user:42"), so a ciphertext copied to another row does not decrypt.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	keyLen        = 32
	keyFile       = "master.key"
	versionPrefix = "v1."
)

// ErrOpen is returned for values that fail authentication: wrong key, wrong
// context, or tampering.
var ErrOpen = errors.New("secrets: cannot decrypt value")

// Box seals and opens values.
type Box struct {
	aead cipher.AEAD
}

// New returns a Box for a 32-byte key.
func New(key []byte) (*Box, error) {
	if len(key) != keyLen {
		return nil, fmt.Errorf("secrets: key must be %d bytes", keyLen)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// LoadOrCreate returns the Box for the installation. It prefers the systemd
// credential "master.key" and otherwise uses <dataDir>/master.key, creating
// it with mode 0600 if it does not exist yet.
func LoadOrCreate(dataDir string) (*Box, error) {
	if dir := os.Getenv("CREDENTIALS_DIRECTORY"); dir != "" {
		if key, err := readKey(filepath.Join(dir, keyFile)); err == nil {
			return New(key)
		}
	}
	path := filepath.Join(dataDir, keyFile)
	key, err := readKey(path)
	if errors.Is(err, os.ErrNotExist) {
		key, err = createKey(path)
	}
	if err != nil {
		return nil, err
	}
	return New(key)
}

func readKey(path string) ([]byte, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: fixed file name inside the data or credentials directory.
	if err != nil {
		return nil, err
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(key) != keyLen {
		return nil, fmt.Errorf("secrets: %s does not contain a valid key", path)
	}
	return key, nil
}

// createKey writes a new random key. O_EXCL makes concurrent first starts
// safe: the loser of the race reads the winner's key.
func createKey(path string) ([]byte, error) {
	key := make([]byte, keyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: fixed file name inside the data directory.
	if errors.Is(err, os.ErrExist) {
		return readKey(path)
	}
	if err != nil {
		return nil, err
	}
	if _, err := f.WriteString(base64.StdEncoding.EncodeToString(key) + "\n"); err != nil {
		return nil, errors.Join(err, f.Close())
	}
	if err := f.Sync(); err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return key, f.Close()
}

// Seal encrypts plaintext bound to context and returns printable text.
func (b *Box) Seal(plaintext []byte, context string) (string, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := b.aead.Seal(nonce, nonce, plaintext, []byte(context))
	return versionPrefix + base64.RawURLEncoding.EncodeToString(out), nil
}

// Open reverses Seal; the context must match the one used for sealing.
func (b *Box) Open(sealed, context string) ([]byte, error) {
	raw, ok := strings.CutPrefix(sealed, versionPrefix)
	if !ok {
		return nil, ErrOpen
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(data) < b.aead.NonceSize() {
		return nil, ErrOpen
	}
	n := b.aead.NonceSize()
	pt, err := b.aead.Open(nil, data[:n], data[n:], []byte(context))
	if err != nil {
		return nil, ErrOpen
	}
	return pt, nil
}
