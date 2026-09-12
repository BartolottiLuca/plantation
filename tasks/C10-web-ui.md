# C10 — Web UI

**Depends on:** C02, C03. **Blocks:** nothing.

**Owns:** `internal/web/**`, including `templates/` and `static/`.

## Goal

The interface Luca actually uses: see what needs doing, mark it done, add and remove
plants. Server-rendered, phone-friendly, no build step.

## What to build

1. **Routes** as listed in [SPEC.md](../SPEC.md) §11 — dashboard, plant list, new,
   detail, edit, soft delete, log care, void an event. `/healthz` and `/readyz` come
   from C12; do not duplicate them.

2. **Rendering.** `html/template` with `go:embed` for templates and static assets. HTMX
   for interaction, vendored as `static/htmx.min.js` — no CDN (there is no guarantee of
   outbound internet from the pod, and a plant tracker should not need one) and no other
   JavaScript. One stylesheet, hand-written, readable at 400 px wide.

3. **The dashboard** is the app's whole value: overdue first, then due today, then a
   short "coming up". Each row names the plant, the task, and the one-line
   `Explanation.Summary`, with a single-click "done" button that POSTs and swaps the row.

4. **The explanation panel** on the plant detail page renders the full
   `care.Explanation`: capacity, current deficit, threshold, mean ETc, effective rain,
   whether deferral applied and why, which clamp bit, and which data was stale. This is
   what makes a wrong answer diagnosable instead of mysterious — show the numbers, not a
   verdict.

5. **Forms.** Add and edit take species (a picker from the catalog), name, location,
   place, Tado room (a picker when C07 is present, a text field otherwise), pot
   diameter, `f_exposure` and `f_rain` as labelled choices rather than raw floats
   ("sheltered / full sun or windy", "indoors or under eaves / partly sheltered / fully
   open"), and the override fields behind a collapsed "advanced" section.

6. **Care logging.** `POST /plants/{id}/care/{kind}` appends a `care_event`. The
   `?action=water` query parameter — which is how the Discord digest deep-links —
   preselects the action on the detail page. Undo voids the event; nothing is ever
   deleted.

7. **Safety.** Every mutation is POST-only, so a crawler's GETs cannot change state.
   `X-Robots-Tag: noindex` on every response. When `PLANTATION_WRITE_TOKEN` is set, POST
   routes require it as a bearer token. A banner shows when the last digest failed.

8. **Empty and degraded states.** Zero plants, a plant with no environment data at all,
   a plant whose species was retired: each renders something sensible, never a blank page
   and never a 500.

## Definition of done

- `httptest` handler tests asserting status codes and key rendered strings for every
  route, including the three degraded states above.
- No JavaScript beyond the vendored `htmx.min.js`; no external asset references.
- Every mutation is POST and returns an HTMX fragment.
- Usable at 400 px: no horizontal scroll, tap targets that work with a thumb.
- A plant can be added, watered, edited and removed entirely from the UI with the
  database as the only backing service.
