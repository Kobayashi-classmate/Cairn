// SPDX-License-Identifier: AGPL-3.0-only

package modules_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// A restore takes a copy of the database first unless asked not to.
func TestDatabaseRestoreCopy(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	e.opsSucceed(helperapi.ActionDBUserSet, helperapi.ActionDBCreate, helperapi.ActionDBRestore)
	e.helper.respond(helperapi.ActionDBStatus, mysqlStatus(nil, nil))
	admin.elevate("")
	admin.runJob(admin.expect(admin.do("POST", "/api/v1/databases/mysql/dbs", map[string]any{"name": "shop"}), 202, ""))
	dir := filepath.Join(e.dataDir, "backups/db/mysql")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "shop_1.sql"), []byte("SELECT 1;"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, skip := range []bool{false, true} {
		admin.runJob(admin.expect(admin.do("POST", "/api/v1/databases/mysql/dbs/shop/restore", map[string]any{"file": "shop_1.sql", "skipCopy": skip}), 202, ""))
		if q := sentAs[helperapi.DBRestoreRequest](t, e, helperapi.ActionDBRestore); q.SkipCopy != skip || q.User != "shop" {
			t.Fatalf("skipCopy %v: helper got %+v", skip, q)
		}
	}
}
