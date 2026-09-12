# C08 — Discord notifier and the outbox

**Depends on:** C01, C02. **Blocks:** C09, C11.

**Owns:** `internal/notify/**` (except the interface C01 declared), the
`send-test-digest` subcommand.

## Goal

Send Discord messages exactly once per event, survive restarts, and never let a
notification failure become a data-loss or a duplicate-spam problem.

## What to build

1. **`SendOnce(ctx, dedupeKey, kind string, msg Message) error`** implementing the
   claim-then-send protocol in [SPEC.md](../SPEC.md) §8:
   - empty content → `RecordSkipped` and stop (recording the skip is what keeps the
     model from being re-evaluated sixty times an hour, and leaves an audit trail
     proving "nothing was due" rather than "the digest broke");
   - otherwise `Claim` → if not claimed, stop; POST; `MarkSent`.

   At-most-once is the deliberate choice: a crash between claim and POST loses one
   digest, which beats double-notifying.

2. **Discord client.** Embed payload. `429` → honour `retry_after` (a float, in seconds,
   in the JSON body). `5xx` → retry. `404` → the webhook was deleted: permanent, stop,
   `MarkFailed`. At most 3 attempts, then record `last_error`.

3. **Sweep.** `SweepStale(ctx, olderThan)` retries `claimed` rows older than 10 minutes
   while `attempts < 3`. C09 calls it at startup and hourly.

4. **Message formatting**, one function per kind so C09 just supplies data:
   - digest — one embed, `overdue` then `due today`, one line per task as
     `<plant> — <task> — <explanation summary>`, each linking to
     `<base_url>/plants/<id>`. Plain webhooks cannot carry buttons, so the link is the
     only way to act on a reminder;
   - frost, heatwave, rain-skip (aggregated across plants, one message), and ops alerts.

5. **`send-test-digest` subcommand** that renders and sends against the current data
   with a `test:<timestamp>` dedupe key.

## Definition of done

- `httptest` tests for 200, 429-with-`retry_after`, 404 and 500, asserting the attempt
  counts and final statuses.
- **A crash test**: claim, then abandon without sending; prove the sweep recovers it and
  that the whole sequence produces at most one duplicate.
- An empty actionable set records `status='skipped'` and sends nothing.
- Calling `SendOnce` twice with the same key sends once.
- The webhook URL never appears in a log line or an error string — including the error
  returned on a failed POST, which is the easy one to get wrong.

## Do not

Decide *what* is actionable or *when* to send. C09 computes the set and owns the clock;
this card owns delivery and idempotency.
