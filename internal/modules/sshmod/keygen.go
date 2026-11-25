// SPDX-License-Identifier: AGPL-3.0-only

package sshmod

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"math/big"
	"net/http"
	"os"
	"strings"
	"unicode"

	"golang.org/x/crypto/ssh"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/modutil"
)

// Key pairs made on the server. The public half is authorized through the
// helper exactly like a pasted key, so the helper's checks (who may get
// keys, root only while root access is on, allowed types) still decide.
// The private half goes back to the browser once and is never stored,
// logged or audited; neither is the passphrase.

// Key types the panel generates.
const (
	keyTypeEd25519 = "ed25519"
	keyTypeRSA     = "rsa"
)

// Limits of a generate request.
const (
	rsaBits          = 4096
	keyCommentMax    = 255
	keyPassphraseMax = 1024
)

// keyFileNames are the names ssh-keygen gives the private key of each type,
// so a downloaded key lands where ssh looks by default.
var keyFileNames = map[string]string{keyTypeEd25519: "id_ed25519", keyTypeRSA: "id_rsa"}

// keyGenerateRequest asks for a new key pair for User.
type keyGenerateRequest struct {
	User string `json:"user"`
	// Type is keyTypeEd25519 or keyTypeRSA (4096 bits).
	Type string `json:"type"`
	// Comment ends the public key line; "" means "<user>@<host name>".
	Comment string `json:"comment,omitempty"`
	// Passphrase encrypts the private key; "" leaves it unencrypted.
	Passphrase string `json:"passphrase,omitempty"`
}

// keyGenerateResponse carries the only copy of the private key.
type keyGenerateResponse struct {
	Key helperapi.SSHKey `json:"key"`
	// PublicKey is the line added to authorized_keys.
	PublicKey string `json:"publicKey"`
	// PrivateKey is the OpenSSH private key (PEM), encrypted when a
	// passphrase was given.
	PrivateKey string `json:"privateKey"`
	// FileName is the usual name of the private key file.
	FileName string `json:"fileName"`
}

func invalidField(field, rule string, params map[string]any) error {
	p := map[string]any{"field": field, "rule": rule}
	for k, v := range params {
		p[k] = v
	}
	return api.NewError(http.StatusBadRequest, "validation.invalid", p)
}

// validate checks the request and trims the comment. Errors name the
// field and the rule only, never a value.
func (q *keyGenerateRequest) validate() error {
	if err := (helperapi.SSHKeysRequest{User: q.User}).Validate(); err != nil {
		return validation(err)
	}
	if _, ok := keyFileNames[q.Type]; !ok {
		return invalidField("type", "oneof", nil)
	}
	q.Comment = strings.TrimSpace(q.Comment)
	if len(q.Comment) > keyCommentMax {
		return invalidField("comment", "max_length", map[string]any{"max": keyCommentMax})
	}
	if !printable(q.Comment) {
		return invalidField("comment", "printable", nil)
	}
	if len(q.Passphrase) > keyPassphraseMax {
		return invalidField("passphrase", "max_length", map[string]any{"max": keyPassphraseMax})
	}
	// ssh asks for a passphrase on one line: a line break or another
	// control character would make the key impossible to unlock.
	if strings.ContainsFunc(q.Passphrase, unicode.IsControl) {
		return invalidField("passphrase", "value", nil)
	}
	return nil
}

// printable reports whether s has only printable characters: no control
// or format characters (bidirectional overrides among them), no line breaks
// or tabs, so the comment stays one readable line in authorized_keys.
func printable(s string) bool {
	return !strings.ContainsFunc(s, func(r rune) bool { return !unicode.IsPrint(r) })
}

// defaultComment names the key after the user and this host, as ssh-keygen
// does ("deploy@web1"), so it can be told apart in authorized_keys files.
func defaultComment(user string) string {
	host, err := os.Hostname()
	if err != nil || host == "" || !printable(host) || len(user)+1+len(host) > keyCommentMax {
		return user
	}
	return user + "@" + host
}

// keyPair is a new key pair: the authorized_keys line and the private key
// in OpenSSH format.
type keyPair struct {
	public  string
	private []byte
}

// generateKeyPair makes a key pair of kind. The private key is encrypted
// with passphrase unless it is empty. The key material is overwritten
// before returning; the caller overwrites private once it is sent.
func generateKeyPair(kind, comment string, passphrase []byte) (keyPair, error) {
	var signer crypto.Signer
	switch kind {
	case keyTypeRSA:
		k, err := rsa.GenerateKey(rand.Reader, rsaBits)
		if err != nil {
			return keyPair{}, err
		}
		defer wipeRSA(k)
		signer = k
	default:
		_, k, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return keyPair{}, err
		}
		defer clear(k)
		signer = k
	}
	pub, err := ssh.NewPublicKey(signer.Public())
	if err != nil {
		return keyPair{}, err
	}
	var block *pem.Block
	if len(passphrase) > 0 {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(signer, comment, passphrase)
	} else {
		block, err = ssh.MarshalPrivateKey(signer, comment)
	}
	if err != nil {
		return keyPair{}, err
	}
	defer clear(block.Bytes)
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	if comment != "" {
		line += " " + comment
	}
	return keyPair{public: line, private: pem.EncodeToMemory(block)}, nil
}

// wipeRSA overwrites the secret numbers of an RSA key. The standard
// library keeps further copies it does not expose, so this only shortens
// how long the obvious ones stay in memory.
func wipeRSA(k *rsa.PrivateKey) {
	secret := append([]*big.Int{k.D, k.Precomputed.Dp, k.Precomputed.Dq, k.Precomputed.Qinv}, k.Primes...)
	for _, n := range secret {
		if n != nil {
			clear(n.Bits())
		}
	}
}

// generateKey makes a key pair, authorizes its public key for the user
// and answers with both halves. Nothing of the private key or the
// passphrase is kept: the audit entry names the user, the type and the
// fingerprint only.
func (m *Module) generateKey(w http.ResponseWriter, r *http.Request) error {
	var q keyGenerateRequest
	if err := api.DecodeJSON(r, &q); err != nil {
		return err
	}
	if err := q.validate(); err != nil {
		return err
	}
	// The string cannot be overwritten; the copy handed to the encryption
	// can, and the request stops pointing at the original.
	passphrase := []byte(q.Passphrase)
	q.Passphrase = ""
	defer clear(passphrase)
	comment := q.Comment
	if comment == "" {
		comment = defaultComment(q.User)
	}
	who := modutil.ActorOf(r)
	params := map[string]any{"user": q.User, "type": q.Type}
	pair, err := generateKeyPair(q.Type, comment, passphrase)
	if err != nil {
		modutil.Audit(r, m.DB, m.Log, who, "ssh.key_generate", modutil.ErrorCode(err), params)
		return err
	}
	defer clear(pair.private)
	var key helperapi.SSHKey
	if err := m.call(r, helperapi.ActionSSHKeyAdd, helperapi.SSHKeyAddRequest{User: q.User, Key: pair.public}, &key); err != nil {
		modutil.Audit(r, m.DB, m.Log, who, "ssh.key_generate", modutil.ErrorCode(err), params)
		return err
	}
	params["fingerprint"] = key.Fingerprint
	modutil.Audit(r, m.DB, m.Log, who, "ssh.key_generate", "ok", params)
	w.Header().Set("Cache-Control", "no-store")
	return api.WriteJSON(w, http.StatusCreated, keyGenerateResponse{
		Key: key, PublicKey: pair.public, PrivateKey: string(pair.private), FileName: keyFileNames[q.Type],
	})
}
