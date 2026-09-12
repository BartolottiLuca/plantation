# C07 — Tado rooms and climate sampling

**Depends on:** C06. **Blocks:** C09.

**Owns:** `internal/climate/tado/rooms.go` and its tests.

## Goal

Turn a linked Tado X account into a stream of per-room temperature and humidity samples
the care engine can integrate.

## What to build

1. **Home id.** `GET https://my.tado.com/api/v2/me`, cached in `tado_token.home_id` —
   it does not change.

2. **Rooms.** `GET https://hops.tado.com/homes/{homeId}/rooms`. Tado X uses **rooms**,
   not the legacy v2 **zones**; that host is undocumented and unversioned, so treat its
   response shape as hostile. Map each room to `climate.RoomClimate{RoomID, Name,
   TempC, HumidityPct, ObservedAt}`.

3. **Sampling.** `Sample(ctx)` persists one row per room into `room_climate_samples`,
   idempotent per timestamp bucket so a double tick cannot double-insert. C09 calls it
   every 30 minutes.

4. **Degradation.** An unexpected JSON shape, a missing field, or a room with no sensor
   degrades to "no data for that room" and logs — it never propagates an error upward
   and never fails the tick. Rooms with no plant mapped to them are ignored.

5. **Raw-response debug log.** A capped, rotating debug table (or a size-bounded log at
   debug level) holding the last few raw bodies. When the unversioned API changes shape,
   this is the difference between seeing what changed and guessing.

6. **Room list for the UI.** `Rooms(ctx)` returning id and name, so C10's plant form can
   offer a picker instead of asking for an opaque id.

## Definition of done

- Fixture tests from a recorded `rooms` response in `testdata/`, plus fixtures for: a
  room missing humidity, an unexpected top-level shape, and an empty room list. None
  error upward; all log.
- Sampling twice within one bucket inserts one row.
- Readings outside the sensor-fault ranges in [SPEC.md](../SPEC.md) §7.3 (`T ∉ [0,45]`,
  `RH ∉ [10,95]`) are stored but flagged, or dropped — pick one, state it in the package
  comment, and make the engine's "missing" path handle it.
- `Status` still reports honestly when the account is `needs_reauth`: no data, no error,
  no panic.

## Do not

Re-implement auth. Take the token accessor from C06 and let it handle refresh.
