// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// After an upgrade the panel's MySQL snippet takes the name the server
// reads last; a file of the same name the panel did not write is left
// alone.
func TestMySQLConfigMigration(t *testing.T) {
	e := newDBEnv(t, map[string]string{"mariadb-server": mariadbInstalled})
	ours := "# " + dbManagedNote + "\n[mysqld]\nbind-address = 0.0.0.0\n"
	writeSystemFile(t, e.d.Root, "/etc/mysql/mysql.conf.d/99-cairn.cnf", ours)
	writeSystemFile(t, e.d.Root, "/etc/mysql/conf.d/99-cairn.cnf", "[mysqld]\nmax_connections = 500\n")
	// Both names, from a change interrupted halfway: the new one wins.
	writeSystemFile(t, e.d.Root, "/etc/mysql/mariadb.conf.d/99-cairn.cnf", ours)
	writeSystemFile(t, e.d.Root, "/etc/mysql/mariadb.conf.d/zz-cairn.cnf", "# "+dbManagedNote+"\n[mysqld]\nbind-address = 127.0.0.1\n")
	e.d.MigrateConfig(slog.New(slog.NewTextHandler(io.Discard, nil)))
	read := func(p string) string {
		data, err := os.ReadFile(filepath.Join(e.d.Root, p))
		if err != nil {
			return "<none>"
		}
		return string(data)
	}
	if read("/etc/mysql/mysql.conf.d/zz-cairn.cnf") != ours || read("/etc/mysql/mysql.conf.d/99-cairn.cnf") != "<none>" {
		t.Fatalf("mysql.conf.d: zz %q, 99 %q", read("/etc/mysql/mysql.conf.d/zz-cairn.cnf"), read("/etc/mysql/mysql.conf.d/99-cairn.cnf"))
	}
	if read("/etc/mysql/conf.d/99-cairn.cnf") != "[mysqld]\nmax_connections = 500\n" || read("/etc/mysql/conf.d/zz-cairn.cnf") != "<none>" {
		t.Fatal("a file the panel did not write was moved")
	}
	if read("/etc/mysql/mariadb.conf.d/99-cairn.cnf") != "<none>" || read("/etc/mysql/mariadb.conf.d/zz-cairn.cnf") == ours {
		t.Fatalf("mariadb.conf.d: zz %q, 99 %q", read("/etc/mysql/mariadb.conf.d/zz-cairn.cnf"), read("/etc/mysql/mariadb.conf.d/99-cairn.cnf"))
	}
	// Nothing left to do the second time.
	e.d.MigrateConfig(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if read("/etc/mysql/mysql.conf.d/zz-cairn.cnf") != ours {
		t.Fatal("second run changed the file")
	}
}
