# C09 — The scheduler loop

**Depends on:** C02, C03, C05, C07, C08. **Blocks:** nothing.

**Owns:** `internal/scheduler/**`.

## Goal

One goroutine that keeps data fresh and sends the right notifications on the right days,
and that loses nothing when the pod restarts.

## What to build

1. **The loop.** A 60-second ticker, guarded by `pg_try_advisory_lock`. The lock is
   defensive: it means a future `replicas: 2` — a Helm typo, someone's HPA — degrades to
   "one scheduler, two web servers" rather than double notifications and racing token
   refreshes. If the lock is not acquired, serve HTTP and skip scheduling, logging once.

2. **Tick conditions**, exactly the table in [SPEC.md](../SPEC.md) §9. Each is an
   independent "is it time, and is it not already done" question answered from the
   database, so no state lives in the goroutine:
   - weather refresh — last fetch older than 1 h;
   - Tado sampling — last sample older than 30 min;
   - Tado token — within 5 min of expiry, or `refresh_obtained_at` older than 7 days;
   - digest and alerts — **local wall clock at or past `DIGEST_HOUR` today, and no
     `digest:<today>` row**;
   - outbox sweep — hourly, plus once at startup.

   That formulation of the digest condition, rather than a timer aimed at 09:00, is
   DST-proof (no offset arithmetic anywhere), restart-proof (a pod booting at 09:07
   sends immediately), and has no missed-wakeup semantics.

3. **Digest assembly.** For every active plant and enabled task, call the C03 engine,
   partition into overdue / due today / upcoming, and hand the first two to C08's
   formatter. **An empty set sends nothing** — it records `skipped`.

4. **Urgent alerts**, each with the dedupe key from SPEC §8:
   - frost: a forecast `tmin ≤ species.min_temp_c` for an outdoor frost-tender plant,
     keyed per plant and per forecast date;
   - heatwave: forecast `tmax ≥ 32 °C`;
   - rain-skip: one aggregated message naming every plant deferred today;
   - ops: weather stale over 24 h, Tado `needs_reauth`, Tado re-auth due within 9 days,
     last digest failed.

   Keys encode the **event**, not the send, so re-polling the forecast twelve times
   cannot re-alert. Severity suffixes stay coarse.

5. **Resilience.** A failing provider logs and the tick continues. The loop must never
   exit on a provider error, and must never let one slow HTTP call block the next tick —
   give each activity its own timeout.

## Definition of done

- **A fake `Clock` simulating 30 days, including a DST transition**, asserts: exactly one
  digest per day that has actionable tasks, zero on days that do not, and exactly one
  frost alert per forecast frost event regardless of how many ticks observe it.
- A test where the process "restarts" at 09:07 sends that day's digest immediately, and
  one where it restarts at 09:20 after already sending does not resend.
- Injected failures from weather, climate, notifier and the database each leave the loop
  running and the other activities unaffected.
- `go test ./internal/scheduler/... -race` clean.
- Wired into `cmd/plantation serve` with graceful shutdown: SIGTERM cancels the context,
  the in-flight tick finishes or times out, the advisory lock is released.

## Do not

Reimplement due-date logic (C03), message formatting (C08), or fetching (C05, C07). This
card is orchestration: when, not how.
