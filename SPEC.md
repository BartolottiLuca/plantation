# Plantation — specification

The behavioural contract for the app. Every task card in [tasks/](tasks/) assumes this
document and nothing else. If something here is ambiguous, fix this file first, then the
code.

Companion documents: [PLAN.md](PLAN.md) (phases and card index),
[AGENTS.md](AGENTS.md) (working conventions), [README.md](README.md) (how to run it).

## 1. Purpose

A single-user app that tracks household plants, indoor and outdoor, and says what needs
watering, pruning, fertilizing or repotting today. Reminders go to Discord. Outdoor
watering is driven by real weather, so the app can say "wait, it rains Thursday". Indoor
watering is driven by room temperature and humidity from a Tado X thermostat.

### Non-goals

- Multiple users, accounts, or roles.
- More than one replica.
- Plant photos.
- Interactive Discord buttons (plain incoming webhooks cannot carry components; acting on
  a reminder means following a link back to the UI).
- A mobile app. The web UI must simply work at phone width.

## 2. Module, layout, package ownership

Module path: `github.com/OWNER/plantation`. `OWNER` is substituted once, in C01, with
`go mod edit -module`; every card after that uses whatever is in `go.mod`.

```
cmd/plantation/main.go        wiring, subcommands, graceful shutdown
internal/config               env → typed Config, validated at boot
internal/domain               Plant, Species, CareEvent, TaskKind, Location, Date
internal/care                 the scheduling engine — pure, stdlib only
internal/store                pgxpool, embedded migrations, repositories
internal/weather              WeatherProvider + openmeteo client + cache repo
internal/climate              IndoorClimateProvider + tado auth and rooms
internal/notify               Notifier + discord client + outbox
internal/web                  handlers, templates, embedded static assets
internal/scheduler            the single ticker loop
catalog/species/*.yaml        curated species data, embedded
deploy/chart                  Helm chart
deploy/argocd                 ArgoCD Application
```

Ownership rule: a type is declared in exactly one package and imported everywhere else.
`internal/domain` owns the shared nouns. `internal/care` owns everything about deciding
when a task is due.

### Import rules (enforced by review, and by a test in C03)

- **`internal/care` may import only the Go standard library and `internal/domain`.**
  No `store`, no `web`, no `pgx`, no HTTP, no `time.Now`, no globals. Everything it
  needs arrives as arguments. Domain is the exception because the shared nouns live
  there; importing it does not give the engine a clock or a database.
- `internal/domain` may import only the standard library and `github.com/google/uuid`.
- `internal/store` must not import `web`, `scheduler`, or any provider package.
- Providers (`weather`, `climate`, `notify`) must not import each other.
- Only `cmd/plantation` may construct concrete implementations. Everything else takes
  interfaces.

## 3. Domain types

Declared in `internal/domain`. Field sets are the contract; add fields only by amending
this section.

```go
type Location string        // "indoor" | "outdoor"
type TaskKind string        // "water" | "prune" | "fertilize" | "repot" | "inspect"
type SubstrateKind string   // "peat" | "cactus" | "coir"

// Date is a civil date with no location. All scheduling comparisons use it.
type Date struct {
    Year  int
    Month time.Month
    Day   int
}

func TodayIn(now time.Time, loc *time.Location) Date
func (d Date) AddDays(n int) Date
func (d Date) Sub(o Date) int      // whole days, d - o
func (d Date) Before(o Date) bool
func (d Date) String() string      // "2026-09-11"

type Species struct {
    Slug             string          // stable identity, e.g. "monstera-deliciosa"
    CommonName       string
    ScientificName   string
    Placement        Location        // the usual place; a plant may override
    Kc               float64         // crop coefficient
    Substrate        SubstrateKind   // determines ThetaAW
    MAD              float64         // management allowed depletion, 0..1
    BaseIntervalDays int             // fallback when there is no environment data
    MinIntervalDays  int
    MaxIntervalDays  int
    DormantMonths    []time.Month
    DormancyFactor   float64         // applied during DormantMonths, default 1.0
    MinTempC         float64         // below this it needs protection
    FrostTender      bool
    Prune            *FixedTask
    Fertilize        *FixedTask
    Repot            *FixedTask
    CareAdvice       string
    Retired          bool
}

type FixedTask struct {
    IntervalDays int
    ActiveMonths []time.Month        // empty means all year
}

type Plant struct {
    ID              uuid.UUID
    Name            string
    SpeciesSlug     string
    Location        Location
    Place           string           // "living room windowsill", "front bed"
    TadoRoomID      *string
    PotDiameterMM   int
    FExposure       float64          // 1.0 sheltered, 1.3 full sun / windy
    FRain           float64          // 0.0 indoor or under eaves, 0.6 partly, 0.9 open
    AcquiredAt      *time.Time
    Active          bool
    Notes           string
    Overrides       Overrides        // all nullable; COALESCE over the species value
}

type CareEvent struct {
    ID       int64
    PlantID  uuid.UUID
    Kind     TaskKind
    DoneAt   time.Time
    Note     string
    Source   string                  // "web" | "cli"
    VoidedAt *time.Time              // append-only log; undo voids, never deletes
}
```

`Overrides` holds `*float64` / `*int` mirrors of the species tunables (`Kc`, `MAD`,
`BaseIntervalDays`, `Min/MaxIntervalDays`, `Substrate`):

```go
type Overrides struct {
    Kc               *float64
    MAD              *float64
    Substrate        *SubstrateKind
    BaseIntervalDays *int
    MinIntervalDays  *int
    MaxIntervalDays  *int
}
```

The effective value is always `COALESCE(plant override, species value)` — resolved in
one helper, `care.Effective(p domain.Plant, s domain.Species) Params`, so a YAML edit
can never silently clobber hand-tuning. It lives in `care` (not `domain`) because it
returns `care.Params`; putting it in `domain` would cycle the two packages.

## 4. Interfaces

```go
// internal/weather
type WeatherProvider interface {
    // Daily returns ascending days covering past `pastDays` and the forecast horizon.
    Daily(ctx context.Context, lat, lon float64, pastDays int) ([]DailyWeather, error)
}

// internal/climate
type IndoorClimateProvider interface {
    // Rooms returns one current reading per room, or an empty slice when unavailable.
    Rooms(ctx context.Context) ([]RoomClimate, error)
    // Status reports link state for the diagnostics page; never returns an error.
    Status(ctx context.Context) LinkStatus
}

// internal/notify
type Notifier interface {
    // SendOnce is idempotent on dedupeKey: a key already claimed is a no-op.
    SendOnce(ctx context.Context, dedupeKey, kind string, msg Message) error
}

// internal/care (and used by scheduler)
type Clock interface {
    Now() time.Time
}
```

Value types returned by those interfaces:

```go
// internal/weather
type DailyWeather struct {
    Date       domain.Date
    Kind       string // "observed" | "forecast"
    ET0MM      *float64
    PrecipMM   *float64
    PrecipProb *float64
    TMinC      *float64
    TMaxC      *float64
    FetchedAt  time.Time
}

// internal/climate
type RoomClimate struct {
    RoomID      string
    Name        string
    TempC       float64
    HumidityPct float64
    ObservedAt  time.Time
}

type LinkStatus struct {
    State             string // "unlinked" | "linked" | "needs_reauth"
    HomeID            string
    AccessExpiresAt   time.Time
    RefreshObtainedAt time.Time
}

// internal/notify
type Message struct {
    Title       string
    Description string
    URL         string
}
```

Measurement fields on `DailyWeather` are pointers because Open-Meteo returns null at
the horizon edges; a missing value must never become a zero the model would treat as
real data.

Every interface ships a `Noop` implementation in the same package, used by tests and by
`cmd` when a feature is disabled by config. `var _ WeatherProvider = (*NoopWeather)(nil)`
assertions live next to each.

## 5. Database

PostgreSQL, provisioned by CloudNativePG. Migrations are embedded SQL under
`internal/store/migrations/`, applied at startup inside `pg_advisory_lock`. Boot tolerates
an absent database: retry connecting with backoff rather than exiting, because ArgoCD
brings the Deployment up independently of the CNPG cluster being ready.

```sql
CREATE TABLE species (
  slug                text PRIMARY KEY,
  common_name         text NOT NULL,
  scientific_name     text NOT NULL DEFAULT '',
  placement           text NOT NULL CHECK (placement IN ('indoor','outdoor')),
  kc                  double precision NOT NULL,
  substrate           text NOT NULL CHECK (substrate IN ('peat','cactus','coir')),
  mad                 double precision NOT NULL,
  base_interval_days  int NOT NULL,
  min_interval_days   int NOT NULL,
  max_interval_days   int NOT NULL,
  dormant_months      int[] NOT NULL DEFAULT '{}',
  dormancy_factor     double precision NOT NULL DEFAULT 1.0,
  min_temp_c          double precision NOT NULL,
  frost_tender        boolean NOT NULL DEFAULT false,
  prune_interval_days int,
  prune_months        int[] NOT NULL DEFAULT '{}',
  fert_interval_days  int,
  fert_months         int[] NOT NULL DEFAULT '{}',
  repot_interval_days int,
  care_advice         text NOT NULL DEFAULT '',
  retired             boolean NOT NULL DEFAULT false,
  updated_at          timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE plants (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name             text NOT NULL,
  species_slug     text NOT NULL REFERENCES species(slug),
  location         text NOT NULL CHECK (location IN ('indoor','outdoor')),
  place            text NOT NULL DEFAULT '',
  tado_room_id     text,
  pot_diameter_mm  int NOT NULL CHECK (pot_diameter_mm BETWEEN 40 AND 2000),
  f_exposure       double precision NOT NULL DEFAULT 1.0,
  f_rain           double precision NOT NULL DEFAULT 0.0,
  acquired_at      timestamptz,
  active           boolean NOT NULL DEFAULT true,
  notes            text NOT NULL DEFAULT '',
  kc_override      double precision,
  mad_override     double precision,
  substrate_override text,
  base_interval_days_override int,
  min_interval_days_override  int,
  max_interval_days_override  int,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE care_tasks (
  id                    bigserial PRIMARY KEY,
  plant_id              uuid NOT NULL REFERENCES plants(id) ON DELETE CASCADE,
  kind                  text NOT NULL,
  enabled               boolean NOT NULL DEFAULT true,
  interval_days_override int,
  snoozed_until         date,
  UNIQUE (plant_id, kind)
);

CREATE TABLE care_events (
  id        bigserial PRIMARY KEY,
  plant_id  uuid NOT NULL REFERENCES plants(id) ON DELETE CASCADE,
  kind      text NOT NULL,
  done_at   timestamptz NOT NULL,
  note      text NOT NULL DEFAULT '',
  source    text NOT NULL DEFAULT 'web',
  voided_at timestamptz
);
CREATE INDEX ON care_events (plant_id, kind, done_at DESC);

CREATE TABLE weather_daily (
  location_key text NOT NULL,          -- "lat,lon" rounded to 3 decimals
  date         date NOT NULL,
  kind         text NOT NULL CHECK (kind IN ('observed','forecast')),
  et0_mm       double precision,
  precip_mm    double precision,
  precip_prob  double precision,
  tmin_c       double precision,
  tmax_c       double precision,
  fetched_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (location_key, date, kind)
);

CREATE TABLE room_climate_samples (
  tado_room_id text NOT NULL,
  observed_at  timestamptz NOT NULL,
  temp_c       double precision NOT NULL,
  humidity_pct double precision NOT NULL,
  PRIMARY KEY (tado_room_id, observed_at)
);

CREATE TABLE tado_token (
  id                     int PRIMARY KEY DEFAULT 1 CHECK (id = 1),
  access_token           text,
  access_expires_at      timestamptz,
  refresh_token          text,
  previous_refresh_token text,
  refresh_obtained_at    timestamptz,
  home_id                text,
  state                  text NOT NULL DEFAULT 'unlinked'
                          CHECK (state IN ('unlinked','linked','needs_reauth')),
  updated_at             timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE notifications (
  id         bigserial PRIMARY KEY,
  dedupe_key text NOT NULL UNIQUE,
  kind       text NOT NULL,
  status     text NOT NULL CHECK (status IN ('claimed','sent','skipped','failed')),
  claimed_at timestamptz NOT NULL DEFAULT now(),
  sent_at    timestamptz,
  attempts   int NOT NULL DEFAULT 0,
  body       text NOT NULL DEFAULT '',
  last_error text NOT NULL DEFAULT ''
);
```

Rules that the schema alone does not express:

- `species` is a **read-through projection of the YAML catalog**. It is overwritten
  wholesale at every boot and never written at runtime. A species with plants referencing
  it is never deleted — set `retired = true`.
- `care_events` is append-only. Undo sets `voided_at`; nothing ever issues `DELETE`.
- In `weather_daily`, a fresh forecast must never overwrite a stored `observed` row for a
  past date. Reads prefer `observed` when both exist.

## 6. Derived state is computed, never stored

There is no `next_due` column and there must never be one. Due dates are produced on
every read by a pure function:

```go
// internal/care
func Effective(p domain.Plant, s domain.Species) Params
func ScheduleWater(p Params, events []domain.CareEvent, env EnvSeries, today domain.Date) (Due, Explanation)
func ScheduleFixed(t domain.FixedTask, kind domain.TaskKind, events []domain.CareEvent, today domain.Date) (Due, Explanation)

type Params struct {
    Location         domain.Location
    Kc               float64
    Substrate        domain.SubstrateKind
    MAD              float64
    BaseIntervalDays int
    MinIntervalDays  int
    MaxIntervalDays  int
    DormantMonths    []time.Month
    DormancyFactor   float64
    FExposure        float64
    FRain            float64
    PotDiameterMM    int
    AcquiredAt       *time.Time
}

type DayEnv struct {
    Date       domain.Date
    ET0MM      float64
    PrecipMM   float64
    PrecipProb float64
    TMinC      float64
    TMaxC      float64
    Observed   bool
    Estimated  bool
}

type IndoorDay struct {
    Date        domain.Date
    TempC       float64
    HumidityPct float64
}

type EnvSeries struct {
    Days            []DayEnv
    Indoor          []IndoorDay
    OutdoorStale    bool
    IndoorDataStale bool
}

type Status string // "overdue" | "due_today" | "upcoming"

type Due struct {
    Kind   domain.TaskKind
    On     domain.Date
    Status Status
}
```

The watering model is path-dependent — it integrates ET0 minus rain since the last
watering — so a stored due date is a cache over an input series that keeps changing:
yesterday's forecast becomes today's observed, Open-Meteo backfills three days, the user
retroactively logs a watering. A stale cached date is indistinguishable from a correct
one. Recomputing every plant costs well under a millisecond.

`Explanation` is part of the return type, not a log line. The UI renders it as "why is
this due" and the digest quotes its one-line form:

```go
type Explanation struct {
    Mode            string   // "water_balance" | "base_interval" | "fixed"
    CapacityMM      float64
    DeficitMM       float64
    ThresholdMM     float64
    DepletionPct    float64
    MeanETcMMPerDay float64
    EffectiveRainMM float64
    Deferred        bool
    DeferredReason  string
    ClampedBy       string   // "" | "min_interval" | "max_interval" | "projection_cap"
    OutdoorStale    bool
    IndoorDataStale bool
    Summary         string   // one line, safe to put in a Discord embed
}
```

Time discipline: every timestamp column is `timestamptz`; every scheduling comparison
happens on `domain.Date` in the household timezone, obtained through the single converter
`domain.TodayIn(clock.Now(), cfg.Location)`.

## 7. The care algorithm

One reservoir model serves indoor and outdoor. ET0 and rainfall are both depths in
millimetres, so the pot's capacity is expressed as a depth too and the whole model is one
subtraction per day. Indoor and outdoor differ only in which ET0 feeds the recurrence and
in `FRain`. `Location` is an enum and the engine branches on it exactly once, at the top:
an indoor plant must never see outdoor ET0, an outdoor plant must never see `f_dry`.

### 7.1 Capacity

```
C     = θ_aw × D_pot × f_root            [mm]
D_pot = 0.8 × pot_diameter_mm            (pot depth; diameter is what people know)
f_root = 0.6                             (effective root zone; water perches below it)
```

| `θ_aw` by substrate | value |
|---|---|
| `peat` (standard potting mix) | 0.30 |
| `cactus` (gritty/succulent mix) | 0.15 |
| `coir` (moisture-retentive) | 0.35 |

### 7.2 Daily depletion

```
ETc_d = Kc × f_exposure × f_dormancy × ET0_d
D_d   = clamp( D_{d-1} + ETc_d − f_rain × P_d , 0 , C )
```

`D` starts at 0 on the day of the last non-voided `water` event, and the plant is **due**
when `D_d ≥ MAD × C`.

| Constant | Scope | Values |
|---|---|---|
| `Kc` | species | succulent/cactus 0.25 · typical foliage houseplant 0.7 · thirsty (basil, tomato, hydrangea, calathea) 1.1 |
| `f_exposure` | plant | 1.0 sheltered · 1.3 full sun or windy balcony |
| `f_rain` | plant | 0.0 indoor or under eaves · 0.6 partly sheltered · 0.9 fully open |
| `f_dormancy` | species, only in `dormant_months` | 1.0 default · ~0.5 houseplants |
| `MAD` | species | 0.5 default · 0.8 succulents · 0.3 ferns and moisture-lovers |

`f_exposure` exists because an isolated pot transpires more per unit area than the field
crop that ET0 is defined against. `f_dormancy` exists because outdoor dormancy arrives
free through ET0 but indoor dormancy does not — the flat is 21 °C in January, and
dormant-season overwatering is the main way houseplants die.

Beyond the forecast horizon, extrapolate with the trailing 14-day mean observed ET0 and
zero rain. Cap projection at 60 days and report "due in more than 60 days" rather than
inventing a date for a dormant cactus; set `ClampedBy = "projection_cap"`.

### 7.3 Indoor ET0 from Tado

```
es(T) = 0.6108 × exp( 17.27·T / (T + 237.3) )      [kPa]   (Tetens)
VPD   = es(T) × (1 − RH/100)                        [kPa]
f_dry = clamp( (VPD / 1.20)^0.7 , 0.5 , 2.0 )       VPD_ref = 1.20 kPa ≈ 21 °C / 50 % RH
ET0_indoor = 2.0 mm/day × f_dry
```

The `^0.7` exponent is load-bearing: transpiration rises with VPD, but stomata close as
VPD climbs, so the response is sub-linear. A linear model produces nonsense next to a
radiator in winter.

- Reject `T ∉ [0, 45] °C` or `RH ∉ [10, 95] %` as sensor faults and treat the sample as
  missing.
- Average samples per day and integrate day by day. Never schedule off a single spot
  reading taken while a window was open.
- If the newest sample for the plant's room is older than 3 hours, or the plant has no
  `tado_room_id`, use `f_dry = 1.0` and set `Explanation.IndoorDataStale`.

### 7.4 Calibration scenarios — required tests

These land on horticultural common sense, which is this model's strongest defence. C03
encodes all five as table-driven tests asserting the interval falls within ±10 % of the
stated value. They catch every future sign error and unit mix-up.

| Scenario | Inputs | C (mm) | ETc (mm/d) | Interval |
|---|---|---|---|---|
| Monstera, 18 cm, indoor summer | θ .30, Kc .7, ET0_in 2.0, MAD .5 | 25.9 | 1.40 | **9.3 d** |
| Outdoor shrub, 20 cm balcony | θ .30, Kc .8, exp 1.3, ET0 4.0, MAD .5 | 28.8 | 4.16 | **3.5 d** |
| Tomato, 25 cm full sun, heat | θ .30, Kc 1.15, exp 1.3, ET0 5.0, MAD .5 | 36.0 | 7.48 | **2.4 d** |
| Echeveria, 12 cm indoor | θ .15, Kc .25, ET0_in 2.0, MAD .8 | 8.6 | 0.50 | **13.8 d** |
| Fern, 16 cm indoor | θ .35, Kc 1.0, ET0_in 2.0, MAD .3 | 26.9 | 2.00 | **4.0 d** |

`f_root = 0.6` is what makes these land; without it every interval roughly doubles.

### 7.5 Wait-for-rain

Evaluated only on the day a plant becomes due. With a two-day lookahead:

```
R_eff = Σ_{i=1..2}  f_rain × P_i × (p_i / 100)     [mm]
```

Defer only when **all three** hold:

1. `R_eff ≥ 0.7 × D_now` — the rain covers most of the deficit;
2. `max(p_1, p_2) ≥ 60 %` — a real bet, not a 15 % drizzle smeared across two days;
3. `D_now + Σ ETc_i < 0.9 × C` — waiting will not reach the stress zone.

Hard limits: never defer more than **2 days total** for one due event (so repeated
deferral cannot chain into a week of drought), never for species with `MAD ≤ 0.35`, and
never when `f_rain = 0` (automatic, since `R_eff` is then zero). Deferral raises one
aggregated `rain_skip:<date>` alert covering every affected plant, not one per plant.

### 7.6 Guardrails and fallback

The final interval is always clamped to the effective `min_interval_days` /
`max_interval_days`, and `ClampedBy` records which bound bit. With no usable environment
data, fall back to `base_interval_days`, clamped identically, with
`Mode = "base_interval"` — the app degrades to a dumb timer, never to silence.

`base_interval_days` duplicates what the physics computes. The catalog validator (C04)
turns that redundancy into a consistency check: it fails when a declared base interval is
more than 40 % away from the value the model produces at ET0 = 3 mm/day in an 18 cm pot.

### 7.7 Non-watering tasks stay dumb

Prune, fertilize and repot are **not** environment-adjusted: a fixed `interval_days` plus
an optional `active_months` list (fertilize March–October, say). When the computed due
date falls outside `active_months`, move it to the first day of the next active month.
There is no defensible physics here and adding some would double the scheduler's surface.

## 8. Notifications

One Discord incoming webhook. Every send goes through the outbox:

1. Compute the actionable set for today's civil date.
2. Empty → `INSERT (status='skipped') ON CONFLICT DO NOTHING`, and stop. Recording the
   skip keeps the model from being re-evaluated sixty times an hour and leaves an audit
   trail proving "nothing was due" rather than "the digest broke".
3. Non-empty → `INSERT (status='claimed') ON CONFLICT DO NOTHING RETURNING id`. No row
   returned means today is already handled; stop.
4. POST to the webhook, then `UPDATE … SET status='sent', sent_at=now()`.

This is at-most-once. A crash between claim and POST loses one digest, which is strictly
better than double-notifying. Recover cheaply: at startup and hourly, sweep `claimed` rows
older than 10 minutes and retry them while `attempts < 3`.

Dedupe keys encode the **event**, not the send, so re-polling the forecast twelve times
cannot re-alert:

| Kind | Key |
|---|---|
| daily digest | `digest:2026-09-11` |
| frost | `frost:<plant-id>:2026-09-13` |
| heatwave | `heatwave:2026-07-02` |
| rain deferral | `rain_skip:2026-09-12` |
| ops (Tado re-auth, stale weather, send failure) | `ops:<reason>:2026-09-11` |

Material worsening gets a coarse severity suffix (`frost:…:sev2`). Keep the buckets
coarse or it becomes spam.

Discord failure handling: `429` honour `retry_after` (float seconds, in the JSON body);
`5xx` retry; `404` is permanent — stop and mark `failed`. At most 3 attempts, then record
`last_error` and surface a banner in the web UI. The UI is the source of truth; Discord is
a convenience channel. A failed digest must never block tomorrow's, which the per-day key
already guarantees.

Digest format: one embed, grouped by `overdue` then `due today`, one line per task as
`<plant name> — <task> — <explanation summary>`, each linking to
`<base_url>/plants/<id>`. Actionable lines carry a deep link because plain webhooks cannot
carry buttons.

## 9. Scheduler

One goroutine, one 60-second ticker, guarded by `pg_try_advisory_lock`. The lock is
defensive: it means a future `replicas: 2` — a Helm typo, someone's HPA — degrades to "one
scheduler, two web servers" instead of double notifications and racing token refreshes.

Each tick evaluates independent "is it time, and is it not already done" conditions, all
of them re-derivable from the database, so a pod restart loses nothing:

| Activity | Condition |
|---|---|
| refresh weather | last successful fetch older than 1 h |
| sample Tado rooms | last sample older than 30 min |
| refresh Tado token | access token within 5 min of expiry, **or** `refresh_obtained_at` older than 7 days |
| digest + alerts | local wall clock at or past `DIGEST_HOUR` today **and** no `digest:<today>` row |
| outbox sweep | hourly |

Expressing the digest that way — rather than a timer aimed at 09:00 — is DST-proof (no
offset arithmetic), restart-proof (a pod booting at 09:07 sends immediately), and has no
missed-wakeup semantics.

Urgent rules: frost when a forecast `tmin ≤ species.min_temp_c` for an outdoor
frost-tender plant; heatwave at `tmax ≥ 32 °C`; plus the aggregated rain-skip and the ops
alerts.

**`main.go` must `import _ "time/tzdata"`.** A distroless or scratch image carries no
zoneinfo database, so `time.LoadLocation` fails and the app silently falls back to UTC —
discovered a day late, in the wrong timezone.

## 10. Integrations

### 10.1 Open-Meteo

`GET https://api.open-meteo.com/v1/forecast` — no API key for non-commercial use, up to 16
forecast days.

```
latitude, longitude, timezone=<IANA>, past_days=7, forecast_days=16,
daily=et0_fao_evapotranspiration,precipitation_sum,precipitation_probability_max,
      temperature_2m_min,temperature_2m_max
```

`et0_fao_evapotranspiration` is grass-reference ET0 — exactly the quantity `Kc` is defined
against. Do not substitute anything else.

`past_days=7` on every call is deliberate: a week-long outage self-heals with real
observed values the moment service returns, and no separate backfill path is ever needed.
Days strictly before today are stored as `kind='observed'`, today and later as
`kind='forecast'`.

Degradation ladder, never blocking the digest: last-known forecast → if older than 48 h,
trailing 14-day mean ET0 with zero rain, annotated `Estimated` → alert
`ops:weather_stale` only after 24 h of staleness. Staleness is typed data on the result,
not an error. 3 retries, exponential backoff with jitter, 10 s timeout.

### 10.2 Tado X

The single biggest operational risk in the project. Rotating refresh tokens, plus a
30-day inactivity expiry, plus one stored copy, means a bad interleaving permanently loses
auth and needs a human at a browser.

```
POST https://login.tado.com/oauth2/device_authorize   client_id, scope=offline_access
POST https://login.tado.com/oauth2/token              grant_type=urn:ietf:params:oauth:grant-type:device_code
POST https://login.tado.com/oauth2/token              grant_type=refresh_token
client_id = 1bb50063-6b0c-4d11-bd99-387f4a91cc46
user visits https://login.tado.com/oauth2/device
GET  https://my.tado.com/api/v2/me                    → home id
GET  https://hops.tado.com/homes/{homeId}/rooms       → per-room temperature + humidity
```

Device code lives 300 s; poll every 5 s and honour `slow_down`. Access tokens last 10
minutes. Tado X uses **rooms**, not the legacy v2 **zones**; `hops.tado.com` is
undocumented and unversioned, so budget for it breaking.

Rules:

- Tokens live in Postgres (`tado_token`), never a file or a Secret.
- Serialize every refresh with `SELECT … FOR UPDATE` inside a transaction. A Go mutex is
  not enough: the scheduler and an HTTP handler are both live, and spending the same
  rotating refresh token twice breaks the chain.
- Call the token endpoint, **commit the new refresh token, then use the access token**.
  Keep `previous_refresh_token` for one generation of recovery. If the call succeeds and
  the commit fails, log at error and raise an ops alert — a human is now required.
- Refresh at 5 minutes remaining **and unconditionally at least weekly**, even when
  nobody asked for indoor data. The 30-day inactivity window is what kills this
  integration over a holiday. Warn at 21 days since `refresh_obtained_at`.
- On `invalid_grant`: set `state='needs_reauth'`, alert Discord with a link to
  `/settings/tado`, and degrade indoor plants to `f_dry = 1.0`.

Tado is an input to a heuristic, never a dependency. The app must not fail to start,
fail to render, or fail to send a digest because Tado is unhappy.

### 10.3 Discord

`POST <webhook url>` with an embed payload. The URL is a secret: it arrives by env var
from a Kubernetes Secret and is never logged, never rendered, and never committed.

## 11. Web UI

Server-rendered `html/template`, HTMX for interaction, everything embedded with
`go:embed`. No build step, no npm, no JavaScript beyond a vendored `htmx.min.js`. It must
be usable at phone width.

| Route | Method | Purpose |
|---|---|---|
| `/` | GET | dashboard: overdue, due today, upcoming |
| `/plants` | GET | all plants with status |
| `/plants/new` | GET, POST | add a plant (species picker, pot size, exposure, room) |
| `/plants/{id}` | GET | detail, explanation panel, care history |
| `/plants/{id}/edit` | GET, POST | edit, including overrides |
| `/plants/{id}/delete` | POST | soft delete (`active=false`) |
| `/plants/{id}/care/{kind}` | POST | log a care event; `?action=water` deep link preselects |
| `/events/{id}/void` | POST | undo a logged event |
| `/settings/tado` | GET, POST | start and complete the device flow |
| `/settings/diagnostics` | GET | weather staleness, last digest, token countdown, catalog version |
| `/settings/test-notification` | POST | send a test Discord message |
| `/healthz` | GET | liveness — process only, **must not touch the database** |
| `/readyz` | GET | readiness — DB ping |

Every mutation is POST-only and returns an HTMX fragment. Responses carry
`X-Robots-Tag: noindex`. If `PLANTATION_WRITE_TOKEN` is set, POST routes require it as a
bearer token.

## 12. Configuration

All config is environment variables, parsed into a typed struct and validated at boot;
an invalid value is a fatal startup error, never a silent default.

| Variable | Required | Default | Notes |
|---|---|---|---|
| `PLANTATION_DATABASE_URL` | yes | — | from the CNPG `*-app` Secret key `uri` |
| `PLANTATION_HTTP_ADDR` | no | `:8080` | |
| `PLANTATION_TZ` | yes | — | IANA name, e.g. `Europe/London` |
| `PLANTATION_DIGEST_HOUR` | no | `9` | 0–23, local |
| `PLANTATION_BASE_URL` | yes | — | for deep links in Discord |
| `PLANTATION_LATITUDE` | if weather explicitly enabled | — | never logged |
| `PLANTATION_LONGITUDE` | if weather explicitly enabled | — | never logged |
| `PLANTATION_WEATHER_ENABLED` | no | `true` | false wires `NoopWeather`. Unset + missing coordinates disables weather (a three-variable local boot must work). An explicit `true` without both coordinates is fatal. |
| `PLANTATION_TADO_ENABLED` | no | `true` | false wires `NoopClimate` |
| `PLANTATION_DISCORD_WEBHOOK_URL` | no | — | absent wires `NoopNotifier` |
| `PLANTATION_WRITE_TOKEN` | no | — | optional bearer on POST routes |
| `PLANTATION_LOG_LEVEL` | no | `info` | `debug\|info\|warn\|error` |

Coordinates, hostname and the webhook URL are supplied on the cluster and must never be
committed to git.

## 13. Conventions

- Structured logging with `log/slog`, JSON handler, level from config. Log keys are
  `snake_case`. **Never log** the Discord webhook URL, Tado tokens, or coordinates.
- Errors wrap with `fmt.Errorf("doing x: %w", err)`. Sentinel errors are exported only
  where callers branch on them. Providers return typed staleness/status data rather than
  errors for expected degraded states.
- `context.Context` is the first parameter of anything doing I/O; every outbound HTTP call
  has an explicit timeout.
- Comments explain why, not what. If deleting a comment loses no information that the code
  does not already carry, delete it.
- Tests are table-driven. Anything touching a clock takes a `Clock`. Anything touching
  HTTP is tested against `httptest` with recorded fixtures under `testdata/`.

## 14. Deployment contract

- Single replica, `strategy: Recreate`. With RollingUpdate two pods briefly overlap, and
  two schedulers is exactly what the advisory lock is there to survive but not what
  anybody wants. `values.yaml` carries a comment saying replicas must stay 1.
- Image: multi-stage, `CGO_ENABLED=0`, distroless, non-root, read-only root filesystem,
  built for `linux/amd64` and `linux/arm64`.
- `/healthz` must not touch the database. If liveness checks the DB, a CNPG failover
  restarts the app for no reason and a longer outage means CrashLoopBackOff. `/readyz`
  does the DB ping.
- `pgxpool` sets `HealthCheckPeriod`, `ConnectTimeout` and `MaxConnLifetime` so
  connections invalidated by a failover get cycled. Retry transient SQLSTATEs `08xxx`,
  `57P01`, `40001`. Every write is either an append-only event or an idempotent upsert, so
  retries are already safe.
- Images are tagged with an immutable semver (`X.Y.Z`) for what Argo deploys, plus
  `sha-<short>` for the exact commit. **Never deploy `latest`** — ArgoCD cannot detect
  a change to a mutable tag. Every push to `main` (except the chart write-back) is a
  release: the workflow reads the latest `vX.Y.Z` tag (first release is `0.1.0`) and
  bumps **major** on `type!:` / `BREAKING CHANGE:`, **minor** on `feat:` / `feat(scope):`,
  **patch** otherwise. Renovate squash-merges as `fix(deps): …` (never `feat:`), so a
  dependency bump — even a minor or major of the library — is always a plantation patch.
  It publishes the image, writes `image.tag` on `main` (what the Argo Application syncs),
  and pushes git tag `vX.Y.Z`. Do not retag an existing `X.Y.Z`. `workflow_dispatch` is
  only an override.
- **Security prerequisite:** the app has no authentication. Cloudflare Access in front of
  the tunnel is required, not optional — without it every mutation is one misconfiguration
  away from being world-writable, with no audit trail.

## 15. Testing requirements

| Area | What must be proven |
|---|---|
| care engine | the five calibration scenarios within ±10 %; a property test showing the interval always lands within `[min,max]_interval_days` for randomised inputs including NaN, negative and absurd weather; `internal/care` imports nothing outside stdlib |
| store | migrations run twice against a fresh database with identical end state |
| weather | a fresh forecast cannot overwrite a stored observed row; missing or null fields never panic |
| tado auth | successful rotation, `invalid_grant`, `slow_down`, and two concurrent goroutines refreshing where exactly one token spend occurs |
| notifier | a simulated crash between claim and send yields at most one duplicate; empty days record `skipped` |
| scheduler | a fake `Clock` over 30 days including a DST transition yields exactly one digest per actionable day, zero on empty days, and one alert per forecast frost event regardless of poll count |
| image | `/healthz` returns 200 with no database present while `/readyz` returns 503 |
| CI | a push to `main` publishes the next `X.Y.Z` (from the tagging strategy) and `sha-<short>`, writes `image.tag` once, pushes git tag `vX.Y.Z`, and that write-back does not retrigger the workflow |

`go test ./... -race` is the gate. Integration tests that need Postgres read
`PLANTATION_TEST_DSN` and skip when it is unset.
