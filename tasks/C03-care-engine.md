# C03 — The care engine

**Depends on:** C01. **Blocks:** C09, C10.

**Owns:** `internal/care/**` (except the interface declarations C01 created).

## Goal

Implement [SPEC.md](../SPEC.md) §7 exactly: the reservoir model, the VPD-derived indoor
ET0, dormancy, clamps, forward projection, the wait-for-rain rule, and the `Explanation`
that lets the UI and the digest say *why*.

This card carries all of the project's domain risk and almost none of its integration
risk. A sign error here produces plausible-looking wrong dates that survive review for
months. Every other card trusts this output.

## What to build

```go
func ScheduleWater(p Params, events []domain.CareEvent, env EnvSeries, today domain.Date) (Due, Explanation)
func ScheduleFixed(t domain.FixedTask, kind domain.TaskKind, events []domain.CareEvent, today domain.Date) (Due, Explanation)
```

Follow SPEC §7 for every formula and constant — capacity from pot diameter and
substrate, the daily depletion recurrence, `Kc`/`f_exposure`/`f_rain`/`f_dormancy`/`MAD`,
Tetens and the `^0.7` VPD response, the three-condition deferral rule with its 2-day cap,
the min/max interval clamps, and the `base_interval_days` fallback. Do not re-derive any
of it and do not improve on it here; if a constant looks wrong, say so and amend the spec.

Points that are easy to get subtly wrong:

- **Branch on `Location` once, at the top.** An indoor plant must never see outdoor ET0;
  an outdoor plant must never see `f_dry`.
- `D` starts at zero on the date of the most recent **non-voided** `water` event. With no
  such event, start from the plant's `acquired_at` date, or from `today` if that is also
  missing.
- Deferral is evaluated **only on the day a plant becomes due**, never re-evaluated into
  a chain. Track the accumulated deferral so it cannot exceed 2 days for one due event.
- Beyond the forecast horizon, extrapolate with the trailing 14-day mean **observed** ET0
  and zero rain. Cap at 60 days and set `ClampedBy = "projection_cap"` rather than
  inventing a date.
- `ScheduleFixed` moves a due date landing outside `ActiveMonths` to the first day of the
  next active month.
- `Explanation.Summary` is one line, safe to drop into a Discord embed, and should read
  like a person wrote it: `deficit 14.2 of 13.0 mm — due today`, or
  `8 mm rain forecast Thu at 80% — deferred to Fri`.

## Definition of done

- **The five calibration scenarios in SPEC §7.4 pass within ±10 %**, as a table-driven
  test with the scenario names from the spec. These exist to catch unit mix-ups and sign
  errors; if one fails, the model is wrong, not the test.
- A property test over randomised inputs — including NaN, infinities, negative ET0,
  1000 mm rainfall, zero-diameter pots and empty event lists — proves the returned
  interval always lands within `[MinIntervalDays, MaxIntervalDays]` and the function
  never panics.
- Deferral tests: fires when all three conditions hold; does not fire when only two do;
  never exceeds 2 days; never fires for `MAD ≤ 0.35` or `f_rain = 0`.
- Fallback tests: empty `EnvSeries` yields `Mode = "base_interval"`; stale indoor data
  yields `f_dry = 1.0` and `IndoorDataStale = true`; out-of-range sensor values are
  treated as missing, not clamped into the formula.
- `go test ./internal/care/... -race` clean, and a test asserting the package's import
  graph is the standard library plus `internal/domain` only (walk `go list -deps` or
  assert on the parsed imports).

## Do not

Touch the database, read a clock, make an HTTP call, or import anything outside the
standard library and `internal/domain`. Everything this package needs arrives as an
argument — that is what makes the whole scheduling story testable.
