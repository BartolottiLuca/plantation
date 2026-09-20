package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/BartolottiLuca/plantation/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("PLANTATION_TEST_DSN"))
	if dsn == "" {
		t.Skip("PLANTATION_TEST_DSN unset")
	}
	return dsn
}

func testLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func openPool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	pool, err := Open(ctx, dsn, testLog())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func migratedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := openPool(t, testDSN(t))
	ctx := context.Background()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	resetData(t, pool)
	return pool
}

func resetData(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	_, err := pool.Exec(ctx, `
		TRUNCATE notifications, room_climate_samples, weather_daily,
			care_events, care_tasks, species_tasks, plants, species
		RESTART IDENTITY CASCADE;
		UPDATE tado_token SET
			access_token = NULL,
			access_expires_at = NULL,
			refresh_token = NULL,
			previous_refresh_token = NULL,
			refresh_obtained_at = NULL,
			home_id = NULL,
			state = 'unlinked',
			updated_at = now()
		WHERE id = 1`)
	if err != nil {
		t.Fatalf("resetData: %v", err)
	}
}

func openFreshDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := testDSN(t)
	ctx := context.Background()

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	adminCfg := cfg.Copy()
	adminCfg.ConnConfig.Database = "postgres"
	admin, err := pgxpool.NewWithConfig(ctx, adminCfg)
	if err != nil {
		t.Fatalf("admin pool: %v", err)
	}

	var raw [6]byte
	if _, err := rand.Read(raw[:]); err != nil {
		admin.Close()
		t.Fatalf("rand: %v", err)
	}
	name := "pl_c02_" + hex.EncodeToString(raw[:])
	ident := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+ident); err != nil {
		admin.Close()
		t.Fatalf("CREATE DATABASE: %v", err)
	}

	testCfg := cfg.Copy()
	testCfg.ConnConfig.Database = name
	testCfg.HealthCheckPeriod = poolHealthCheckPeriod
	testCfg.MaxConnLifetime = poolMaxConnLifetime
	testCfg.ConnConfig.ConnectTimeout = poolConnectTimeout
	pool, err := pgxpool.NewWithConfig(ctx, testCfg)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+ident)
		admin.Close()
		t.Fatalf("test pool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+ident)
		admin.Close()
		t.Fatalf("test ping: %v", err)
	}

	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(),
			`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`,
			name)
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+ident)
		admin.Close()
	})
	return pool
}

func mustSpecies(t *testing.T, repo *SpeciesRepo, slug string) {
	t.Helper()
	s := domain.Species{
		Slug:             slug,
		CommonName:       "Test " + slug,
		ScientificName:   slug,
		Placement:        domain.Indoor,
		Kc:               0.7,
		Substrate:        domain.Peat,
		MAD:              0.5,
		BaseIntervalDays: 7,
		MinIntervalDays:  3,
		MaxIntervalDays:  21,
		DormancyFactor:   1,
		MinTempC:         8,
	}
	if err := repo.UpsertAll(context.Background(), []domain.Species{s}); err != nil {
		t.Fatalf("UpsertAll: %v", err)
	}
}
