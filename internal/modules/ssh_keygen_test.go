// SPDX-License-Identifier: AGPL-3.0-only

package modules_test

import (
	"bytes"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

const generatePath = "/api/v1/ssh/keys/generate"

type generatedKey struct {
	Key        helperapi.SSHKey `json:"key"`
	PublicKey  string           `json:"publicKey"`
	PrivateKey string           `json:"privateKey"`
	FileName   string           `json:"fileName"`
}

// generate asks for a key pair and checks the answer: 201, not cached,
// and a public key that is the line the helper was asked to authorize.
func generate(t *testing.T, e *env, c *client, req map[string]any) (generatedKey, ssh.PublicKey, string) {
	t.Helper()
	body, _ := json.Marshal(req)
	res, data := c.raw("POST", generatePath, body)
	if res.StatusCode != 201 {
		t.Fatalf("generate %v: %d %s", req, res.StatusCode, data)
	}
	if cc := res.Header.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control %q", cc)
	}
	var out generatedKey
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	sent := sentAs[helperapi.SSHKeyAddRequest](t, e, helperapi.ActionSSHKeyAdd)
	if sent.User != req["user"] || sent.Key != out.PublicKey {
		t.Fatalf("sent %+v, answered %q", sent, out.PublicKey)
	}
	pub, comment, options, rest, err := ssh.ParseAuthorizedKey([]byte(sent.Key))
	if err != nil || len(options) > 0 || len(rest) > 0 {
		t.Fatalf("public line %q: %v %v %q", sent.Key, err, options, rest)
	}
	if !strings.HasPrefix(out.PrivateKey, "-----BEGIN OPENSSH PRIVATE KEY-----\n") {
		t.Fatalf("private key %.40q", out.PrivateKey)
	}
	return out, pub, comment
}

// samePublic fails unless signer is the private half of pub.
func samePublic(t *testing.T, signer ssh.Signer, pub ssh.PublicKey) {
	t.Helper()
	if !bytes.Equal(signer.PublicKey().Marshal(), pub.Marshal()) {
		t.Fatalf("private key %s does not match public key %s", ssh.FingerprintSHA256(signer.PublicKey()), ssh.FingerprintSHA256(pub))
	}
}

func TestSSHKeyGenerate(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	ed := map[string]any{"user": "deploy", "type": "ed25519"}
	admin.expect(admin.do("POST", generatePath, ed), 403, "auth.step_up_required")
	if e.helper.called(helperapi.ActionSSHKeyAdd) {
		t.Fatal("a key was added without step-up")
	}
	admin.elevate("")
	e.helper.respond(helperapi.ActionSSHKeyAdd, helperapi.SSHKey{Type: "ssh-ed25519", Fingerprint: "SHA256:" + strings.Repeat("A", 43)})

	// Ed25519 without a passphrase: named after the user and the host.
	out, pub, comment := generate(t, e, admin, ed)
	host, _ := os.Hostname()
	if pub.Type() != ssh.KeyAlgoED25519 || out.FileName != "id_ed25519" || comment != "deploy@"+host {
		t.Fatalf("ed25519: %s %q %q", pub.Type(), out.FileName, comment)
	}
	signer, err := ssh.ParsePrivateKey([]byte(out.PrivateKey))
	if err != nil {
		t.Fatalf("ed25519 private key: %v", err)
	}
	samePublic(t, signer, pub)
	if out.Key.Fingerprint != "SHA256:"+strings.Repeat("A", 43) {
		t.Fatalf("key %+v", out.Key)
	}
	secrets := []string{out.PrivateKey}

	// RSA 4096 with a passphrase and a comment (trimmed).
	const pass = "correct horse battery"
	out, pub, comment = generate(t, e, admin, map[string]any{"user": "deploy", "type": "rsa", "comment": "  ci deploy  ", "passphrase": pass})
	if pub.Type() != ssh.KeyAlgoRSA || out.FileName != "id_rsa" || comment != "ci deploy" {
		t.Fatalf("rsa: %s %q %q", pub.Type(), out.FileName, comment)
	}
	if k, ok := pub.(ssh.CryptoPublicKey).CryptoPublicKey().(*rsa.PublicKey); !ok || k.N.BitLen() != 4096 {
		t.Fatalf("rsa size: %T", pub)
	}
	var missing *ssh.PassphraseMissingError
	if _, err := ssh.ParsePrivateKey([]byte(out.PrivateKey)); !errors.As(err, &missing) {
		t.Fatalf("an encrypted key opens without its passphrase: %v", err)
	}
	if _, err := ssh.ParsePrivateKeyWithPassphrase([]byte(out.PrivateKey), []byte("wrong")); err == nil {
		t.Fatal("the key opens with a wrong passphrase")
	}
	signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(out.PrivateKey), []byte(pass))
	if err != nil {
		t.Fatalf("rsa private key: %v", err)
	}
	samePublic(t, signer, pub)
	secrets = append(secrets, out.PrivateKey, pass)

	// The longest comment passes.
	_, _, comment = generate(t, e, admin, map[string]any{"user": "deploy", "type": "ed25519", "comment": strings.Repeat("c", 255)})
	if len(comment) != 255 {
		t.Fatalf("comment of %d bytes", len(comment))
	}

	// Invalid requests never reach the helper; the errors name the field
	// and rule, never the value.
	calls := len(e.helper.callsOf())
	for _, c := range []struct {
		req         map[string]any
		field, rule string
	}{
		{map[string]any{"user": "../etc", "type": "ed25519"}, "user", "posix_name"},
		{map[string]any{"user": "deploy", "type": "dsa"}, "type", "oneof"},
		{map[string]any{"user": "deploy"}, "type", "oneof"},
		{map[string]any{"user": "deploy", "type": "ed25519", "comment": strings.Repeat("c", 256)}, "comment", "max_length"},
		{map[string]any{"user": "deploy", "type": "ed25519", "comment": "a\tb"}, "comment", "printable"},
		{map[string]any{"user": "deploy", "type": "ed25519", "comment": "two\nlines"}, "comment", "printable"},
		{map[string]any{"user": "deploy", "type": "ed25519", "comment": "evil\u202etxt.exe"}, "comment", "printable"},
		{map[string]any{"user": "deploy", "type": "ed25519", "passphrase": strings.Repeat("p", 1025)}, "passphrase", "max_length"},
		{map[string]any{"user": "deploy", "type": "ed25519", "passphrase": "secret\nline"}, "passphrase", "value"},
	} {
		r := admin.expect(admin.do("POST", generatePath, c.req), 400, "validation.invalid")
		if p := r.params(); p["field"] != c.field || p["rule"] != c.rule {
			t.Errorf("%v: params %v", c.req, p)
		}
		if strings.Contains(r.raw, "secret") || strings.Contains(r.raw, "ppppp") {
			t.Errorf("%v: the error repeats the passphrase: %s", c.req, r.raw)
		}
	}
	admin.expect(admin.do("POST", generatePath, map[string]any{"user": "deploy", "type": "ed25519", "bits": 1024}), 400, "request.invalid_json")
	if n := len(e.helper.callsOf()); n != calls {
		t.Fatalf("invalid requests reached the helper: %v", e.helper.callsOf()[calls:])
	}

	// The helper's refusals come back as they do for a pasted key, and
	// the private key is dropped.
	for _, c := range []struct {
		user   string
		err    *helperapi.Error
		status int
	}{
		{"root", &helperapi.Error{Code: helperapi.CodeSSHRootDisabled}, 409},
		{"ghost", &helperapi.Error{Code: helperapi.CodeSSHUserNotFound, Params: map[string]any{"user": "ghost"}}, 404},
		{"deploy", &helperapi.Error{Code: helperapi.CodeSSHKeyInvalid, Params: map[string]any{"reason": "too_many"}}, 400},
	} {
		e.helper.fail(helperapi.ActionSSHKeyAdd, c.err)
		r := admin.expect(admin.do("POST", generatePath, map[string]any{"user": c.user, "type": "ed25519"}), c.status, c.err.Code)
		if strings.Contains(r.raw, "PRIVATE KEY") || strings.Contains(r.raw, "ssh-ed25519") {
			t.Fatalf("%s: the refusal carries a key: %s", c.err.Code, r.raw)
		}
	}
	e.helper.fail(helperapi.ActionSSHKeyAdd, nil)

	// Audited with the user, the type and (on success) the fingerprint;
	// nothing secret.
	rows, err := e.db.Query(`SELECT params, result FROM audit_log WHERE action = 'ssh.key_generate' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var results []string
	for rows.Next() {
		var params, result string
		if err := rows.Scan(&params, &result); err != nil {
			t.Fatal(err)
		}
		results = append(results, result)
		var p map[string]any
		_ = json.Unmarshal([]byte(params), &p)
		keys := []string{"type", "user"}
		if result == "ok" {
			keys = []string{"fingerprint", "type", "user"}
		}
		got := make([]string, 0, len(p))
		for k := range p {
			got = append(got, k)
		}
		slices.Sort(got)
		if !slices.Equal(got, keys) {
			t.Errorf("audit params %s (%s)", params, result)
		}
		for _, s := range secrets {
			if strings.Contains(params, s) || strings.Contains(params, "PRIVATE") {
				t.Errorf("audit entry carries a secret: %s", params)
			}
		}
	}
	want := []string{"ok", "ok", "ok", helperapi.CodeSSHRootDisabled, helperapi.CodeSSHUserNotFound, helperapi.CodeSSHKeyInvalid}
	if !slices.Equal(results, want) {
		t.Fatalf("audit results %v", results)
	}
	var leaked int
	for _, s := range secrets {
		var n int
		_ = e.db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE params LIKE ?`, "%"+s+"%").Scan(&n)
		leaked += n
	}
	if leaked != 0 {
		t.Fatalf("%d audit entries carry a secret", leaked)
	}
}

// API tokens cannot step up, so they can neither add nor generate keys,
// whatever their scopes.
func TestSSHKeyGenerateRefusesTokens(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	admin.elevate("")
	created := admin.expect(admin.do("POST", "/api/v1/me/tokens", map[string]any{
		"name": "ssh", "permissions": []string{"ssh.read", "ssh.manage"}, "expiresInDays": 30,
	}), 201, "")
	token := created.body["token"].(string)
	for path, body := range map[string]any{
		"/api/v1/ssh/keys": map[string]any{"user": "deploy", "key": "ssh-ed25519 AAAA x"},
		generatePath:       map[string]any{"user": "deploy", "type": "ed25519"},
	} {
		if r := e.bearer(token, "POST", path, body); r.status != 403 || r.code() != "auth.step_up_required" {
			t.Errorf("token on %s: %d %s", path, r.status, r.code())
		}
	}
	if e.helper.called(helperapi.ActionSSHKeyAdd) {
		t.Fatal("a token reached the helper")
	}
}
