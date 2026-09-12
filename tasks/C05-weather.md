# C05 — Open-Meteo provider and weather cache

**Depends on:** C01, C02. **Blocks:** C09, C11.

**Owns:** `internal/weather/**` (except the interface C01 declared), the
`backfill-weather` subcommand.

## Goal

Fetch daily weather for one location, cache it, and hand the care engine an
`EnvSeries` that degrades gracefully instead of disappearing when the API is down.

## What to build

1. **Client.** `GET https://api.open-meteo.com/v1/forecast` with the exact parameter set
   in [SPEC.md](../SPEC.md) §10.1 — including **`past_days=7` on every call**. That is
   not an optimisation: it means a week-long outage self-heals with real observed values
   the moment service returns, and no separate backfill path is ever needed. No API key.
   10 s timeout, 3 retries with exponential backoff and jitter.

   `et0_fao_evapotranspiration` is grass-reference ET0, which is exactly the quantity
   `Kc` is defined against. Do not substitute a different evapotranspiration field.

2. **Storage.** Days strictly before today are stored `kind='observed'`, today and later
   `kind='forecast'`. Reads prefer observed. A fresh forecast must never overwrite a
   stored observed row for a past date — that corruption is silent and makes historical
   due dates impossible to reconstruct.

3. **`EnvSeries` assembly.** Build `care.DayEnv` slices from the cache, ascending, with
   the `Observed` and `Estimated` flags set.

4. **Degradation ladder**, never blocking a digest:
   - cache fresh → use it;
   - cache older than 48 h → fill missing days with the trailing 14-day mean observed
     ET0 and zero rain, flagged `Estimated`;
   - staleness is **typed data on the result**, not an error, so the caller can annotate
     the `Explanation` rather than fail.
   - The scheduler raises `ops:weather_stale` only after 24 h of staleness — this card
     just reports the staleness accurately.

5. **`backfill-weather` subcommand** for a one-off wider fetch after a long outage.

6. **`location_key`** is `"lat,lon"` rounded to 3 decimals, so a values.yaml nudge does
   not orphan the cache.

## Definition of done

- Fixture-driven tests from one recorded Open-Meteo response in `testdata/` (strip
  nothing but keep it small). Null or missing daily entries — which the API does return
  at the horizon edges — never panic and never become zeroes that the model would treat
  as real data.
- A test proves a fresh forecast cannot overwrite a stored observed row.
- A test proves the degradation ladder: empty cache, 12 h stale, 72 h stale.
- A live smoke test against the real endpoint behind a `//go:build smoke` tag.
- Coordinates never appear in a log line.

## Do not

Decide when to fetch — that is C09's tick. This card exposes `Refresh(ctx)` and
`Series(ctx, from, to)`; the scheduler calls them.
