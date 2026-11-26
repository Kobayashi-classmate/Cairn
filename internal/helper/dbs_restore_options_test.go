// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"strings"
	"testing"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// A restore without a copy (a database that cannot be dumped any more)
// takes none, and a failure says the restore is half done.
func TestRestoreWithoutCopy(t *testing.T) {
	e := newMySQLRestoreEnv(t)
	e.dumpErr = exitErr("mariadb-dump: Got error: 1932: Table 'shop.x' doesn't exist in engine")
	writeSystemFile(t, e.d.Root, mysqlDumps+"/shop_up.sql", "CREATE TABLE t (id int);\n")
	req := helperapi.DBRestoreRequest{Engine: "mysql", Name: "shop", File: "shop_up.sql", User: "shop", Password: "pass#word1", SkipCopy: true}
	if op, log := e.run(helperapi.ActionDBRestore, req); op.State != helperapi.OpSucceeded || len(e.copies()) != 0 {
		t.Fatalf("restore: %+v %v\n%s", op, e.copies(), log)
	}
	for _, c := range e.calls {
		if strings.HasSuffix(c.name, "mariadb-dump") {
			t.Fatal("a copy was taken")
		}
	}
	e.load = func(int) error { return exitErr("ERROR 1064 (42000) at line 1: You have an error in your SQL syntax") }
	e.reset()
	op, _ := e.run(helperapi.ActionDBRestore, req)
	if op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeDBRestorePartial || op.Error.Params["copy"] != nil || strings.Contains(e.script(), "DROP DATABASE") {
		t.Fatalf("failed restore without a copy: %+v", op)
	}
}
