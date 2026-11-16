// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"testing"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

func TestDatabaseOfBackup(t *testing.T) {
	for name, want := range map[string]string{
		"db-mysql-my_shop_20261116-030000.sql.zst.age":  "mysql/my_shop",
		"db-postgresql-17-my_shop_20261116-030000.dump": "postgresql-17/my_shop",
		"db-mysql-my_shop":                  "mysql/my_shop",
		"site-shop_20261116-030000.tar.zst": "",
	} {
		engine, db, ok := helperapi.DatabaseOfBackup(name)
		if got := engine + "/" + db; ok && got != want || !ok && want != "" {
			t.Errorf("%s: %s %v", name, got, ok)
		}
	}
}
