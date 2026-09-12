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
  location_key text NOT NULL,
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

INSERT INTO tado_token (id, state) VALUES (1, 'unlinked');
