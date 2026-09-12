# C11 — Settings and diagnostics

**Depends on:** C05, C06, C08 (and C10's layout). **Blocks:** nothing.

**Owns:** `internal/web/settings*.go` and its templates.

## Goal

Link Tado from a browser, and answer "is this thing actually working?" without kubectl.

## What to build

1. **`/settings/tado`.** A GET shows the current link state. A POST starts the
   device-code flow and renders the user code prominently alongside a link to
   `https://login.tado.com/oauth2/device`, then polls server-side (HTMX polling a
   status fragment) until the flow completes, expires after 300 s, or fails. On success,
   show the home id and the discovered rooms. On `needs_reauth`, the page is the
   recovery path the ops alert links to, so it must work when the stored token is
   already dead.

   Never render token material — not the access token, not the refresh token, not in an
   HTML comment.

2. **`/settings/diagnostics`.** One page, no cleverness:
   - weather: last successful fetch, staleness, whether the series is currently
     `Estimated`, the coordinates' `location_key` (not the coordinates);
   - Tado: link state, access token expiry countdown, age of the refresh token, and the
     21-day re-auth warning when it applies;
   - notifications: last digest status and time, any `failed` rows with their
     `last_error`, count of `skipped` days this week;
   - catalog: species count and whether the boot upsert succeeded;
   - scheduler: whether this pod holds the advisory lock, and the last tick time.

3. **`/settings/test-notification`.** POSTs a test message through C08 with a
   `test:<timestamp>` key, and reports the outcome inline — including the failure, which
   is the case that matters.

## Definition of done

- A human completes the Tado device flow end to end in a browser and sees their rooms.
- Every degraded state renders a clear explanation rather than an error page: no token,
  `needs_reauth`, expired device code, stale weather, failed digest, no webhook
  configured, weather disabled.
- No token, webhook URL or coordinate pair appears in the rendered HTML or the page
  source.
- Handler tests cover the states above with fake providers.

## Do not

Add configuration editing. Config is environment variables set by Helm; a settings page
that writes config is a second source of truth.
