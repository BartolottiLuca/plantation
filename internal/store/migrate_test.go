package store

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMigrateTwiceOnFreshDBLeavesIdenticalState(t *testing.T) {
	pool := openFreshDB(t)
	ctx := context.Background()

	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	first := schemaDump(t, pool)

	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	second := schemaDump(t, pool)

	if first != second {
		t.Fatalf("schema changed on second migrate\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
	if !strings.Contains(first, "0001_init.sql") {
		t.Fatalf("schema_migrations missing 0001_init.sql:\n%s", first)
	}
	if !strings.Contains(first, "tado_token\tunlinked") {
		t.Fatalf("seeded tado_token row missing:\n%s", first)
	}
}

func TestMigrateAlreadyAppliedIsNoop(t *testing.T) {
	pool := migratedPool(t)
	ctx := context.Background()

	beforeCount, beforeApplied := migrationSnapshot(t, pool)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate on already-migrated db: %v", err)
	}
	afterCount, afterApplied := migrationSnapshot(t, pool)
	if afterCount != beforeCount {
		t.Fatalf("schema_migrations count %d -> %d", beforeCount, afterCount)
	}
	if beforeApplied != afterApplied {
		t.Fatalf("applied_at changed on no-op migrate: %s -> %s", beforeApplied, afterApplied)
	}
}

func schemaDump(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	ctx := context.Background()
	var b strings.Builder

	rows, err := pool.Query(ctx, `
		SELECT c.relname, a.attname,
			pg_catalog.format_type(a.atttypid, a.atttypmod), a.attnotnull
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_attribute a ON a.attrelid = c.oid
		WHERE n.nspname = 'public' AND a.attnum > 0 AND NOT a.attisdropped
			AND c.relkind IN ('r', 'S')
		ORDER BY 1, 2`)
	if err != nil {
		t.Fatalf("columns: %v", err)
	}
	for rows.Next() {
		var rel, att, typ string
		var notnull bool
		if err := rows.Scan(&rel, &att, &typ, &notnull); err != nil {
			rows.Close()
			t.Fatalf("scan columns: %v", err)
		}
		b.WriteString(rel + "\t" + att + "\t" + typ + "\t")
		if notnull {
			b.WriteString("notnull\n")
		} else {
			b.WriteString("null\n")
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("columns: %v", err)
	}

	idx, err := pool.Query(ctx, `
		SELECT indexname, indexdef FROM pg_indexes
		WHERE schemaname = 'public'
		ORDER BY 1, 2`)
	if err != nil {
		t.Fatalf("indexes: %v", err)
	}
	for idx.Next() {
		var name, def string
		if err := idx.Scan(&name, &def); err != nil {
			idx.Close()
			t.Fatalf("scan indexes: %v", err)
		}
		b.WriteString(name + "\t" + def + "\n")
	}
	idx.Close()

	cons, err := pool.Query(ctx, `
		SELECT conname, pg_get_constraintdef(oid)
		FROM pg_constraint
		WHERE connamespace = 'public'::regnamespace
		ORDER BY 1, 2`)
	if err != nil {
		t.Fatalf("constraints: %v", err)
	}
	for cons.Next() {
		var name, def string
		if err := cons.Scan(&name, &def); err != nil {
			cons.Close()
			t.Fatalf("scan constraints: %v", err)
		}
		b.WriteString(name + "\t" + def + "\n")
	}
	cons.Close()

	vers, err := pool.Query(ctx, `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatalf("versions: %v", err)
	}
	for vers.Next() {
		var v string
		if err := vers.Scan(&v); err != nil {
			vers.Close()
			t.Fatalf("scan versions: %v", err)
		}
		b.WriteString("migration\t" + v + "\n")
	}
	vers.Close()

	var state string
	if err := pool.QueryRow(ctx, `SELECT state FROM tado_token WHERE id = 1`).Scan(&state); err != nil {
		t.Fatalf("tado_token seed: %v", err)
	}
	b.WriteString("tado_token\t" + state + "\n")
	return b.String()
}

func migrationSnapshot(t *testing.T, pool *pgxpool.Pool) (int, string) {
	t.Helper()
	var count int
	var applied string
	err := pool.QueryRow(context.Background(), `
		SELECT count(*), coalesce(max(applied_at)::text, '')
		FROM schema_migrations`).Scan(&count, &applied)
	if err != nil {
		t.Fatalf("migrationSnapshot: %v", err)
	}
	return count, applied
}
