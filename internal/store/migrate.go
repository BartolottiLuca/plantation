package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"regexp"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Session-level lock held for the whole migrate pass so two pods cannot apply
// the same pending file. Released before the connection returns to the pool.
const migrationLockKey int64 = 0x504C4E54

var migrationName = regexp.MustCompile(`^[0-9]{4}_[a-z0-9_]+\.sql$`)

// Migrate applies pending embedded SQL files in filename order. Each file runs
// in its own transaction. Already-applied versions are a no-op. Forward-only.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquiring migration connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockKey); err != nil {
		return fmt.Errorf("acquiring migration lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, migrationLockKey)
	}()

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version text PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("creating schema_migrations: %w", err)
	}

	applied, err := appliedVersions(ctx, conn)
	if err != nil {
		return err
	}

	names, err := listMigrationFiles()
	if err != nil {
		return err
	}

	for _, name := range names {
		if applied[name] {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("reading %s: %w", name, err)
		}
		if err := applyMigration(ctx, conn, name, string(body)); err != nil {
			return err
		}
	}
	return nil
}

func listMigrationFiles() ([]string, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("listing migrations: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !migrationName.MatchString(name) {
			return nil, fmt.Errorf("invalid migration filename %q", name)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func appliedVersions(ctx context.Context, conn *pgxpool.Conn) (map[string]bool, error) {
	rows, err := conn.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("listing applied migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[string]bool)
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("scanning schema_migrations: %w", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing applied migrations: %w", err)
	}
	return applied, nil
}

func applyMigration(ctx context.Context, conn *pgxpool.Conn, name, body string) error {
	if !migrationName.MatchString(name) {
		return fmt.Errorf("invalid migration filename %q", name)
	}
	// Filename is [0-9a-z_].sql only; interpolated so the whole file stays one
	// simple-protocol transaction (extended Exec cannot run multi-statement SQL).
	sql := "BEGIN;\n" + body + "\nINSERT INTO schema_migrations (version) VALUES ('" + name + "');\nCOMMIT;\n"
	if err := execSimple(ctx, conn, sql); err != nil {
		return fmt.Errorf("applying %s: %w", name, err)
	}
	return nil
}

// Multi-statement files need the simple protocol; pgx's default extended
// protocol accepts only one statement per Exec.
func execSimple(ctx context.Context, conn *pgxpool.Conn, sql string) error {
	return conn.Conn().PgConn().Exec(ctx, sql).Close()
}
