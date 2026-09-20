# C19 — Web and digest, addressed by task

**Depends on:** C18. **Blocks:** —.

**Owns:** `internal/web/`, `internal/scheduler/digest.go`, `internal/notify/format.go`.

## Goal

Show what each plant is and what it needs by name, and let every task be logged,
snoozed and retuned individually.

## What to build

1. **Routes move from kind to slug**: `POST /plants/{id}/care/{slug}` and
   `POST /plants/{id}/tasks/{slug}`. `water` is the reserved slug (C15), so watering is
   addressed exactly like everything else and the `?action=water` deep link still works.
   Reject an unknown slug with 400, the way an unknown kind is rejected today.

2. **Task settings list every declared task**, not three fixed ones, each with its own
   enable, snooze and interval override. Two tasks of the same kind must be
   distinguishable in the form — label them, do not print the kind twice.

3. **The plant page shows the species description**, separate from `care_advice`. The
   description says what the plant is; `care_advice` stays the short practical note.

4. **The digest uses task labels**: `<plant> — <task label> — <explanation summary>`.
   "Pinch flower spikes" is a usable reminder; "Prune" is not, when what the plant needs
   is its flowers removed. Grouping stays `overdue` then `due today`.

5. **SPEC §11's route table** is amended to match, and §5's `care_tasks` note is updated
   to say the control key is the task slug.

## Definition of done

- The dashboard, the plant page and the digest all render a species with two tasks of
  the same kind without ambiguity.
- The dashboard and the digest still agree: both go through `care.ScheduleAll`, and the
  existing tests pinning that (`TestDashboardHonoursSnooze`,
  `TestDigestHonoursTaskControls`) are updated to slugs rather than deleted.
- The dashboard query count stays flat in plant count — `TestDashboardQueryCountIsFlatInPlantCount`
  must still pass; rendering a task list must not reintroduce a per-plant read.
- `go test ./... -race` passes, `golangci-lint` is quiet, and the UI still works at
  phone width.
