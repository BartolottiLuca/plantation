# C02 — Schema, migrations, store layer

**Depends on:** C01. **Blocks:** C04, C05, C06, C08, C09, C10.

**Owns:** `internal/store/**`, `compose.yaml`.

## Goal

Every table in [SPEC.md](../SPEC.md) §5, a migration runner that is safe to run on every
boot, and typed repositories the rest of the app uses instead of writing SQL.

## What to build

1. **Migrations.** Numbered SQL files under `internal/store/migrations/`
   (`0001_init.sql`, …), embedded with `go:embed`. The runner takes
   `pg_advisory_lock(<constant>)` for the duration, records applied versions in a
   `schema_migrations` table, and applies each pending file in one transaction.
   Forward-only; no down migrations.

2. **Pool.** `pgxpool` configured per SPEC §14: `HealthCheckPeriod`, `ConnectTimeout`,
   `MaxConnLifetime`. Boot must **tolerate an absent database** — retry connecting with
   capped exponential backoff and log each attempt, rather than exiting. ArgoCD brings
   the Deployment up independently of CNPG being ready, so a pod that dies because
   Postgres is thirty seconds behind is a pod in CrashLoopBackOff for no reason.

3. **Transient retry helper.** Retry SQLSTATE `08xxx`, `57P01` and `40001`. Every write
   in this app is an append-only insert or an idempotent upsert, so retries are already
   safe; the helper just makes that explicit and keeps the call sites clean.

4. **Repositories**, one file each, returning `internal/domain` types — never raw rows:
   - `SpeciesRepo` — `UpsertAll([]domain.Species)` (wholesale, from the catalog),
     `List`, `Get(slug)`. Never deletes; `retired` is a column.
   - `PlantRepo` — CRUD, soft delete (`active=false`), list with species joined.
   - `CareTaskRepo` — per-plant task rows, enable/disable, snooze.
   - `CareEventRepo` — `Add`, `Void(id)`, `LatestByKind(plantID)`,
     `SinceDate(plantID, kind, from)`. Append-only: **no `DELETE` statement may exist in
     this package.**
   - `WeatherRepo` — `UpsertDays`, `Range(locationKey, from, to)`, `LastFetchedAt`.
     `Range` prefers `observed` over `forecast` for the same date. `UpsertDays` must
     **never let a forecast row overwrite a stored observed row for a past date** — the
     upsert is keyed on `(location_key, date, kind)` and the read resolves the
     precedence, so state this explicitly in a test.
   - `ClimateRepo` — `AddSamples`, `DailyMeans(roomID, from, to)`, `LatestSample(roomID)`.
   - `TadoTokenRepo` — `Load`, and `WithLock(ctx, fn)` which opens a transaction,
     `SELECT … FOR UPDATE` on the single row, and hands the callback a writer. C06
     depends on this shape; do not give it a plain `Save`.
   - `NotificationRepo` — `Claim(dedupeKey, kind) (claimed bool, err error)`,
     `MarkSent`, `MarkFailed`, `RecordSkipped`, `StaleClaims(olderThan)`.

5. **`compose.yaml`** with a Postgres 17 service for local development, matching the
   credentials in the README.

## Definition of done

- Migrations applied twice against a fresh database leave identical state; applied
  against an already-migrated database are a no-op.
- `go test ./internal/store/... -race` passes against `PLANTATION_TEST_DSN` and skips
  cleanly when it is unset.
- Tests prove: a forecast row cannot overwrite an observed row for a past date;
  `Range` prefers observed; `Claim` returns false for a key that already exists;
  `WithLock` serialises two concurrent callers.
- Stopping and restarting Postgres mid-test does not kill the process — the retry helper
  recovers.
- `grep -ri "delete from care_events" internal/store` finds nothing.

## Do not

Write business logic. The repositories move rows; deciding what the rows mean belongs to
C03 and C09.
