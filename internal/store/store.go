// SPDX-License-Identifier: AGPL-3.0-only

// Package store opens the panel's SQLite database and applies schema
// migrations. Data access lives with the packages that own the tables.
package store

import (
	"context"
	"database/sql"
	"embed"
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

// Migrate applies every embedded migration that has not run yet. Migration
// files are named NNNN_description.sql and run in order, each in its own
// transaction.
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
	for _, f := range files {
		base := strings.TrimPrefix(f, "migrations/")
		num, _, ok := strings.Cut(base, "_")
		if !ok {
			return fmt.Errorf("store: bad migration name %s", base)
		}
		version, err := strconv.Atoi(num)
		if err != nil {
			return fmt.Errorf("store: bad migration version in %s", base)
		}
		if err := apply(ctx, db, version, f); err != nil {
			return fmt.Errorf("store: migration %s: %w", base, err)
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
