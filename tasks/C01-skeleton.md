# C01 — Spec, skeleton, domain types, interfaces

**Depends on:** nothing. **Blocks:** everything.

**Owns:** `go.mod`, `go.sum`, `Makefile`, `.gitignore`, `.golangci.yml`,
`cmd/plantation/main.go`, `internal/config/**`, `internal/domain/**`, and the interface
+ `Noop` declaration files in `internal/care`, `internal/weather`, `internal/climate`,
`internal/notify`.

## Goal

Establish the shared context every other card reads. Thirteen agents are about to write
against these types and signatures without talking to each other, so a wrong decision
here is expensive. Take the time.

## What to build

1. **Module.** `go mod init github.com/OWNER/plantation`, substituting a real owner with
   `go mod edit -module`. Go 1.23+. Dependencies: `github.com/jackc/pgx/v5`,
   `github.com/google/uuid`, `gopkg.in/yaml.v3`. Nothing else.

2. **Layout.** Create every package directory listed in [SPEC.md](../SPEC.md) §2, each
   with at least a doc comment so the tree is real rather than aspirational.

3. **`internal/domain`.** Every type in SPEC §3, verbatim field sets: `Location`,
   `TaskKind`, `SubstrateKind`, `Date` (with `TodayIn`, `AddDays`, `Sub`, `Before`,
   `String`), `Species`, `FixedTask`, `Plant`, `Overrides`, `CareEvent`. Plus
   `care.Effective(p domain.Plant, s domain.Species) Params` — the single place where
   `COALESCE(override, species)` is resolved. It cannot live in `domain` without an
   import cycle (`ScheduleWater` takes `domain.CareEvent`).

   `Date` is the one type worth obsessing over: it is a civil date with no location, and
   its `Sub` returns whole days. Test it across a DST boundary and across a year end.

4. **Interfaces and Noops.** `WeatherProvider`, `IndoorClimateProvider`, `Notifier`,
   `Clock` exactly as in SPEC §4, each with a `Noop` implementation in the same package
   and a `var _ Iface = (*Noop)(nil)` assertion. Also the value types they return:
   `weather.DailyWeather`, `climate.RoomClimate`, `climate.LinkStatus`,
   `notify.Message`. A `Noop` returns empty data and no error — never a "not
   implemented" error, because the whole point is that the app runs happily without
   these features.

   `care.Params`, `care.EnvSeries`, `care.DayEnv`, `care.IndoorDay`, `care.Due`,
   `care.Status` and `care.Explanation` are declared here too (C03 fills in the logic).

5. **`internal/config`.** Every variable in SPEC §12 parsed into a typed `Config`,
   validated at boot: an unparseable timezone, an out-of-range digest hour, or a missing
   required value is a fatal startup error with a message naming the variable. Resolve
   `*time.Location` once, here.

6. **`cmd/plantation/main.go`.** Subcommand dispatch (`serve` is the only one that works
   yet; `send-test-digest` and `backfill-weather` arrive with C08 and C05), config load,
   `slog` JSON handler at the configured level, graceful shutdown on SIGTERM.
   **`import _ "time/tzdata"`** — see SPEC §9 for why this is mandatory and not
   cosmetic.

7. **`Makefile`** with `build`, `test`, `lint`, `run`. **`.golangci.yml`** enabling at
   least `errcheck`, `govet`, `staticcheck`, `revive`, `ineffassign`.

## Definition of done

- `go build ./... && go vet ./... && golangci-lint run` clean.
- `go test ./... -race` passes: `Date` arithmetic across DST and year boundaries, config
  validation rejecting each bad input with a useful message.
- Every interface has a compile-time `Noop` assertion.
- `./plantation serve` starts with only `PLANTATION_DATABASE_URL`, `PLANTATION_TZ` and
  `PLANTATION_BASE_URL` set, logs a structured startup line, and exits cleanly on
  SIGTERM. It does not need the database to exist yet.
- `SPEC.md` is updated if you had to change any signature — and say so in your summary,
  loudly, because other cards are being written against it.

## Do not

Implement any scheduling logic, any SQL, any HTTP client, or any handler. Declaring the
shapes is the whole job.
