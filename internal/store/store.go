// SPDX-License-Identifier: AGPL-3.0-only

// Package store opens the panel's SQLite database and applies schema
// migrations. Data access lives with the packages that own the tables.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, keeps the binary static
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Open opens (creating if needed) the database at path with WAL journaling,
// a busy timeout and foreign keys enabled.
func Open(path string) (*sql.DB, error) {
	q := url.Values{}
	for _, p := range []string{"busy_timeout(10000)", "journal_mode(WAL)", "foreign_keys(1)", "synchronous(NORMAL)"} {
		q.Add("_pragma", p)
	}
	q.Set("_txlock", "immediate") // take the write lock at BEGIN to avoid upgrade deadlocks
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetConnMaxIdleTime(5 * time.Minute)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	return db, nil
}

// ErrNewerSchema reports a database that a newer version of Cairn has
// migrated: this program does not know its tables and must not use them.
var ErrNewerSchema = errors.New("store: the database was migrated by a newer version of Cairn")

// Migrate applies every embedded migration that has not run yet. Migration
// files are named NNNN_description.sql and run in order, each in its own
// transaction. A database that a newer version migrated further than this
// one knows is refused (ErrNewerSchema) and left as it is: after a
// downgrade without the database of the time, an older program would
// otherwise read and write tables whose meaning has changed.
func Migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL)`); err != nil {
		return err
	}
	files, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(files)
	versions := make([]int, len(files))
	known := 0
	for i, f := range files {
		base := strings.TrimPrefix(f, "migrations/")
		num, _, ok := strings.Cut(base, "_")
		if !ok {
			return fmt.Errorf("store: bad migration name %s", base)
		}
		if versions[i], err = strconv.Atoi(num); err != nil {
			return fmt.Errorf("store: bad migration version in %s", base)
		}
		known = max(known, versions[i])
	}
	var applied int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&applied); err != nil {
		return err
	}
	if applied > known {
		return fmt.Errorf("%w (schema version %d, this version knows up to %d): install that version again, or put back the database from before it",
			ErrNewerSchema, applied, known)
	}
	for i, f := range files {
		if err := apply(ctx, db, versions[i], f); err != nil {
			return fmt.Errorf("store: migration %s: %w", strings.TrimPrefix(f, "migrations/"), err)
		}
	}
	return nil
}

func apply(ctx context.Context, db *sql.DB, version int, file string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	body, err := migrationFS.ReadFile(file)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, string(body)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)`, version, time.Now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}
