// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"path/filepath"
	"testing"
)

func openMigrated(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "cairn.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return db
}

func countMigrations(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMigrateAppliesEveryMigrationOnce(t *testing.T) {
	db := openMigrated(t)
	files, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("embedded migrations: %v %v", files, err)
	}
	if n := countMigrations(t, db); n != len(files) {
		t.Fatalf("%d migrations recorded, %d embedded", n, len(files))
	}
	// Every start of the panel migrates: a database this version made is
	// accepted again, unchanged.
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("second run on an up-to-date database: %v", err)
	}
	if n := countMigrations(t, db); n != len(files) {
		t.Fatalf("second run recorded %d migrations, want %d", n, len(files))
	}
}

func TestMigrateRefusesNewerSchema(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	// A newer version of the panel applied a migration this build lacks.
	if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES(9999, 0)`); err != nil {
		t.Fatal(err)
	}
	before := countMigrations(t, db)
	err := Migrate(ctx, db)
	t.Logf("Migrate on a database with migration 9999 applied: %v", err)
	if !errors.Is(err, ErrNewerSchema) {
		t.Fatalf("an older program runs on a database a newer one migrated: %v", err)
	}
	if n := countMigrations(t, db); n != before {
		t.Fatalf("the refused database was changed: %d migrations recorded, was %d", n, before)
	}
}
