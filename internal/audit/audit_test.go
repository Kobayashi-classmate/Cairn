// SPDX-License-Identifier: AGPL-3.0-only

package audit_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Kobayashi-classmate/Cairn/internal/audit"
	"github.com/Kobayashi-classmate/Cairn/internal/store"
)

// A caller that puts a secret into an entry anyway: the value of a
// parameter whose name says it is one is not stored. Flags, numbers and
// everything else are.
func TestRecordMasksSecrets(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "cairn.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	type spec struct {
		Name      string `json:"name"`
		SecretKey string `json:"secretKey"`
	}
	params := map[string]any{
		"password": "hunter2-password", "newPassword": "hunter2-new", "api_token": "hunter2-token", "client-secret": "hunter2-secret",
		"privateKey": "hunter2-key", "passphrase": "hunter2-phrase",
		"nested":  map[string]any{"mfaToken": "hunter2-mfa", "user": "deploy"},
		"list":    []any{map[string]any{"token": "hunter2-list"}},
		"spec":    spec{Name: "backup", SecretKey: "hunter2-struct"},
		"secrets": []string{"hunter2-a", "hunter2-b"},
		// Kept: a flag saying a password was set, a count, an empty value,
		// and names that only contain a secret word.
		"passwordSet": true, "password2": "kept-2", "secretChanged": true, "tokens": 3, "token_name": "kept-name", "key": "kept-key",
		"fingerprint": "SHA256:kept", "id": int64(9007199254740993),
	}
	if err := audit.Record(ctx, db, audit.Entry{Action: "test.secrets", Params: params, Result: "ok"}); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := db.QueryRow(`SELECT params FROM audit_log WHERE action = 'test.secrets'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "hunter2") {
		t.Fatalf("a secret was stored: %s", stored)
	}
	var got map[string]any
	dec := json.NewDecoder(strings.NewReader(stored))
	dec.UseNumber()
	if err := dec.Decode(&got); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]any{"password": "[redacted]", "passwordSet": true, "password2": "kept-2", "secretChanged": true,
		"token_name": "kept-name", "key": "kept-key", "fingerprint": "SHA256:kept", "secrets": "[redacted]"} {
		if got[k] != want {
			t.Errorf("%s = %v, want %v", k, got[k], want)
		}
	}
	if got["id"] != json.Number("9007199254740993") || got["tokens"] != json.Number("3") {
		t.Errorf("numbers changed: %v %v", got["id"], got["tokens"])
	}
	if n, _ := got["nested"].(map[string]any); n["user"] != "deploy" || n["mfaToken"] != "[redacted]" {
		t.Errorf("nested %v", got["nested"])
	}
	if s, _ := got["spec"].(map[string]any); s["name"] != "backup" || s["secretKey"] != "[redacted]" {
		t.Errorf("struct %v", got["spec"])
	}

	// Entries without secrets are stored as they were encoded.
	plain := map[string]any{"site": "shop", "domains": []string{"a.example", "b.example"}, "html": "<b>&</b>"}
	if err := audit.Record(ctx, db, audit.Entry{Action: "test.plain", Params: plain, Result: "ok"}); err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(plain)
	if err := db.QueryRow(`SELECT params FROM audit_log WHERE action = 'test.plain'`).Scan(&stored); err != nil || stored != string(want) {
		t.Fatalf("stored %s, want %s (%v)", stored, want, err)
	}
}
