// SPDX-License-Identifier: AGPL-3.0-only

// Package cloud reads data published by the Cairn project: release
// information and official messages. Everything is a static file signed with
// ed25519; the panel only downloads, verifies and filters it locally. It
// never uploads anything and never executes anything it receives.
//
// File layout at the base URL: NAME (the payload) and NAME.sig, a single
// line "<key-id> <base64 signature>". The key ID is the first 8 bytes of
// SHA-256 over the raw 32-byte public key, in hex.
package cloud

import (
	"crypto/ed25519"
	"crypto/sha256"
	_ "embed" // trusted_keys.txt
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

//go:embed trusted_keys.txt
var trustedKeysFile string

// BuiltinKeys are the release signing keys trusted by every build, as
// base64 raw public keys, read from trusted_keys.txt. The official key is
// added there once the project owner has generated it (make release-keygen);
// the private key never enters any repository. Additional keys can be
// trusted through the configuration (self-hosted mirrors, tests).
var BuiltinKeys = ParseKeyList(trustedKeysFile)

// ParseKeyList returns the keys of a key list file: the first field of every
// line that is neither blank nor a "#" comment.
func ParseKeyList(text string) []string {
	keys := []string{}
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || strings.HasPrefix(f[0], "#") {
			continue
		}
		keys = append(keys, f[0])
	}
	return keys
}

var (
	// ErrNoTrustedKey means no signing key is configured at all.
	ErrNoTrustedKey = errors.New("cloud: no trusted signing key configured")
	// ErrSignature means the signature is missing, malformed or not made by
	// a trusted key.
	ErrSignature = errors.New("cloud: signature verification failed")
)

// KeyID identifies a public key.
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:8])
}

// ParsePublicKey decodes a base64 raw ed25519 public key.
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("cloud: invalid public key %q", s)
	}
	return ed25519.PublicKey(raw), nil
}

// Keyring holds the trusted public keys by ID.
type Keyring map[string]ed25519.PublicKey

// NewKeyring parses the built-in keys plus extra ones.
func NewKeyring(extra []string) (Keyring, error) {
	return KeyringOf(append(append([]string{}, BuiltinKeys...), extra...))
}

// KeyringOf parses exactly the given keys.
func KeyringOf(keys []string) (Keyring, error) {
	kr := Keyring{}
	for _, s := range keys {
		pub, err := ParsePublicKey(s)
		if err != nil {
			return nil, err
		}
		kr[KeyID(pub)] = pub
	}
	return kr, nil
}

// Sign returns the .sig file content for data (used by tests and tooling).
func Sign(data []byte, priv ed25519.PrivateKey) []byte {
	pub := priv.Public().(ed25519.PublicKey)
	sig := ed25519.Sign(priv, data)
	return []byte(KeyID(pub) + " " + base64.StdEncoding.EncodeToString(sig) + "\n")
}

// Verify checks that sigFile is a valid signature over data by a key in kr.
func (kr Keyring) Verify(data, sigFile []byte) error {
	if len(kr) == 0 {
		return ErrNoTrustedKey
	}
	id, b64, ok := strings.Cut(strings.TrimSpace(string(sigFile)), " ")
	if !ok {
		return ErrSignature
	}
	pub, known := kr[id]
	if !known {
		return fmt.Errorf("%w: unknown key %s", ErrSignature, id)
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil || len(sig) != ed25519.SignatureSize || !ed25519.Verify(pub, data, sig) {
		return ErrSignature
	}
	return nil
}
