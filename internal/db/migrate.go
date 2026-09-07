package db

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// migrationLockKey is an arbitrary constant used as a Postgres advisory lock key so
// concurrent RunMigrations calls (e.g. multiple app instances starting at once during
// a rolling deploy) serialize instead of racing on schema_migrations/CREATE TABLE.
const migrationLockKey = 727384910

// RunMigrations creates the schema_migrations tracking table and applies every
// not-yet-applied .sql file under migrations/, in sorted order. Safe to call
// concurrently from multiple instances — serializes via a Postgres advisory lock.
func RunMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire lock connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockKey); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, migrationLockKey)

	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			filename   TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	dir := "migrations"
	if d := os.Getenv("MIGRATIONS_DIR"); d != "" {
		dir = d
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read migrations dir %q: %w", dir, err)
	}

	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	for _, f := range files {
		var count int
		if err := pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM schema_migrations WHERE filename = $1`, f,
		).Scan(&count); err != nil {
			return fmt.Errorf("check migration %s: %w", f, err)
		}
		if count > 0 {
			continue
		}

		sql, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			return fmt.Errorf("read %s: %w", f, err)
		}

		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("apply %s: %w", f, err)
		}

		if _, err := pool.Exec(ctx,
			`INSERT INTO schema_migrations (filename) VALUES ($1)`, f,
		); err != nil {
			return fmt.Errorf("record %s: %w", f, err)
		}

		slog.Info("migration applied", "file", f)
	}

	return nil
}
