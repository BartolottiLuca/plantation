# C06 — Tado device-code OAuth and token store

**Depends on:** C01, C02. **Blocks:** C07, C11.

**Owns:** `internal/climate/tado/auth.go` and its tests.

## Goal

Link the app to a Tado account with the device-code flow and keep it linked
indefinitely, without ever losing the rotating refresh token.

This is the card that breaks in production rather than in CI. Rotating refresh tokens,
plus a 30-day inactivity expiry, plus one stored copy, means a single bad interleaving
costs a manual re-link at a browser. Read [SPEC.md](../SPEC.md) §10.2 twice.

## What to build

1. **Device flow.** `POST /oauth2/device_authorize` with the client id and
   `scope=offline_access`; return the user code and verification URL for C11 to display;
   poll `POST /oauth2/token` with
   `grant_type=urn:ietf:params:oauth:grant-type:device_code` every 5 s, honouring
   `slow_down` by increasing the interval, until success, `expired_token` (300 s), or
   the caller's context is cancelled.

2. **Token persistence and rotation.** All of it inside
   `TadoTokenRepo.WithLock` (C02) — a transaction holding `SELECT … FOR UPDATE` on the
   single row. A Go mutex is not sufficient: the scheduler and an HTTP handler are both
   live, and spending the same rotating refresh token twice breaks the chain
   permanently.

   Order of operations is the whole card: call the token endpoint, **commit the new
   refresh token, then use the access token.** Keep the old value in
   `previous_refresh_token` for exactly one generation of recovery. If the endpoint
   succeeds and the commit fails, log at error and raise `ops:tado_token_lost` — a human
   is now required, and saying so immediately is better than discovering it in a week.

3. **Refresh policy.**
   - proactively at 5 minutes remaining on the 10-minute access token;
   - **unconditionally at least weekly**, even when nobody asked for indoor data. The
     30-day inactivity window is what kills this integration over a holiday;
   - warn (`ops:tado_reauth_soon`) when `refresh_obtained_at` is older than 21 days.

4. **State machine.** `unlinked → linked → needs_reauth → linked`. On `invalid_grant`,
   set `needs_reauth`, raise an ops alert linking `/settings/tado`, and stop trying.
   Document the states in the package comment.

5. **`Status(ctx) LinkStatus`** for the diagnostics page: state, home id, access token
   expiry, age of the refresh token. Never returns an error and **never returns token
   material**.

## Definition of done

- An `httptest` fake token server covers: successful device flow; `authorization_pending`
  then success; `slow_down` widening the interval; `expired_token`; successful refresh
  with rotation; `invalid_grant` driving `needs_reauth`.
- **A concurrency test**: two goroutines refresh simultaneously and exactly one token
  spend reaches the server; both end up with the same valid access token.
- A test proves the new refresh token is committed before the access token is returned
  to the caller — simulate a post-commit failure and show the stored token is the new
  one.
- No token, code, or webhook value appears in any log line, at any level.
- `go test ./internal/climate/... -race` clean.

## Do not

Call `hops.tado.com` or model rooms — that is C07. Build the settings page — that is
C11. This card ends at "there is a valid access token, or a clear reason why not".
