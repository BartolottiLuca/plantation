package store

import (
	"context"
	"strings"
	"testing"
	"time"

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
	if !strings.Contains(first, "0002_in_ground.sql") {
		t.Fatalf("schema_migrations missing 0002_in_ground.sql:\n%s", first)
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

// TestSpeciesTasksMigrationPreservesPreMigrationData seeds a database as it
// existed just before 0004_species_tasks.sql — species with the three old
// fixed-task columns, a care_task row keyed by kind, and a care_event logged
// by kind — then runs the real Migrate and checks nothing was lost or
// guessed. care_events is append-only and, per SPEC §5, the only
// irreplaceable table; this is the test that stands between this migration
// and a plant's history disappearing.
func TestSpeciesTasksMigrationPreservesPreMigrationData(t *testing.T) {
	pool := openFreshDB(t)
	ctx := context.Background()

	applyMigrationsThrough(t, pool, "0003_pot_diameter_cm.sql")

	var basilID string
	err := pool.QueryRow(ctx, `
		INSERT INTO species (slug, common_name, placement, kc, substrate, mad,
			base_interval_days, min_interval_days, max_interval_days, min_temp_c,
			prune_interval_days, prune_months, fert_interval_days, fert_months, repot_interval_days)
		VALUES ('ocimum-basilicum', 'Basil', 'outdoor', 1.1, 'peat', 0.5, 4, 1, 10, 10,
			14, '{5,6,7,8,9}', 14, '{4,5,6,7,8,9}', NULL)
		RETURNING slug`).Scan(&basilID)
	if err != nil {
		t.Fatalf("seeding species: %v", err)
	}

	var plantID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO plants (name, species_slug, location, pot_diameter_cm)
		VALUES ('Windowsill Basil', 'ocimum-basilicum', 'outdoor', 20)
		RETURNING id`).Scan(&plantID); err != nil {
		t.Fatalf("seeding plant: %v", err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO care_tasks (plant_id, kind, enabled) VALUES ($1, 'prune', true)`,
		plantID); err != nil {
		t.Fatalf("seeding care_task: %v", err)
	}

	doneAt := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	var eventID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO care_events (plant_id, kind, done_at, source)
		VALUES ($1, 'prune', $2, 'web') RETURNING id`, plantID, doneAt).Scan(&eventID); err != nil {
		t.Fatalf("seeding care_event: %v", err)
	}

	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// The event: kind intact, task_slug NULL — not guessed, not dropped.
	var kind string
	var taskSlug *string
	if err := pool.QueryRow(ctx, `SELECT kind, task_slug FROM care_events WHERE id = $1`, eventID).
		Scan(&kind, &taskSlug); err != nil {
		t.Fatalf("reading migrated event: %v", err)
	}
	if kind != "prune" {
		t.Errorf("event kind = %q, want prune", kind)
	}
	if taskSlug != nil {
		t.Errorf("event task_slug = %v, want nil (legacy event)", taskSlug)
	}

	// The control row: re-keyed to the task slug the migration derived.
	var controlSlug string
	var enabled bool
	if err := pool.QueryRow(ctx, `
		SELECT task_slug, enabled FROM care_tasks WHERE plant_id = $1`, plantID).
		Scan(&controlSlug, &enabled); err != nil {
		t.Fatalf("reading migrated care_task: %v", err)
	}
	if controlSlug != "prune" || !enabled {
		t.Errorf("care_task = (%q, enabled=%v), want (prune, true)", controlSlug, enabled)
	}

	// species_tasks: both fixed tasks carried across, repot correctly absent.
	rows, err := pool.Query(ctx, `
		SELECT slug, kind, label, interval_days, active_months, sort_order
		FROM species_tasks WHERE species_slug = $1 ORDER BY sort_order`, basilID)
	if err != nil {
		t.Fatalf("reading species_tasks: %v", err)
	}
	defer rows.Close()
	type gotTask struct {
		slug, kind, label string
		days              int
		months            []int32
		sort              int
	}
	var got []gotTask
	for rows.Next() {
		var g gotTask
		if err := rows.Scan(&g.slug, &g.kind, &g.label, &g.days, &g.months, &g.sort); err != nil {
			t.Fatalf("scanning species_tasks: %v", err)
		}
		got = append(got, g)
	}
	if len(got) != 2 {
		t.Fatalf("species_tasks = %+v, want exactly prune and fertilize", got)
	}
	if got[0].slug != "prune" || got[0].kind != "prune" || got[0].days != 14 || got[0].sort != 0 {
		t.Errorf("prune task = %+v", got[0])
	}
	if got[1].slug != "fertilize" || got[1].kind != "fertilize" || got[1].days != 14 || got[1].sort != 1 {
		t.Errorf("fertilize task = %+v", got[1])
	}

	// species: old columns gone, description present.
	var description string
	if err := pool.QueryRow(ctx, `SELECT description FROM species WHERE slug = $1`, basilID).
		Scan(&description); err != nil {
		t.Fatalf("reading species.description: %v", err)
	}
	if description != "" {
		t.Errorf("description = %q, want empty default", description)
	}
	var oldColumnCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'species' AND column_name IN
			('prune_interval_days', 'prune_months', 'fert_interval_days', 'fert_months', 'repot_interval_days')`).
		Scan(&oldColumnCount); err != nil {
		t.Fatalf("checking dropped columns: %v", err)
	}
	if oldColumnCount != 0 {
		t.Errorf("%d old species task columns still present, want 0", oldColumnCount)
	}
}

// applyMigrationsThrough applies embedded migrations up to and including
// lastFile, marking each applied in schema_migrations exactly as Migrate
// would — so a later real call to Migrate sees the same "already applied"
// state a database mid-rollout would have, and only runs what comes after.
func applyMigrationsThrough(t *testing.T, pool *pgxpool.Pool, lastFile string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquiring conn: %v", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version text PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
		t.Fatalf("creating schema_migrations: %v", err)
	}

	names, err := listMigrationFiles()
	if err != nil {
		t.Fatalf("listing migrations: %v", err)
	}
	for _, name := range names {
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		if err := applyMigration(ctx, conn, name, string(body)); err != nil {
			t.Fatalf("applying %s: %v", name, err)
		}
		if name == lastFile {
			return
		}
	}
	t.Fatalf("migration file %q not found", lastFile)
}
