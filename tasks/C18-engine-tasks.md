# C18 — Care engine over a task list

**Depends on:** C15, C17. **Blocks:** C19.

**Owns:** `internal/care/tasks.go`, `internal/care/fixed.go`, `internal/care/types.go`.

## Goal

Schedule whatever tasks a species declares, keyed by slug, without the engine knowing
the names of any of them.

## What to build

1. **`ScheduleAll` iterates `s.Tasks`** instead of branching on `Prune`, `Fertilize`
   and `Repot`. Water keeps its own branch: it is the reservoir model, not a fixed
   interval. The two existing rules survive the rewrite:
   - an in-ground plant is never scheduled for `repot` (SPEC §7.1);
   - a task with no control row behaves exactly like an enabled one.

2. **`ScheduleFixed` takes a `domain.SpeciesTask`.** §7.7 is unchanged: a fixed interval
   plus optional active months, with a due date outside those months moved to the first
   day of the next active month. There is still no physics here and none should appear.

3. **`TaskControl` keys on `TaskSlug`, not `Kind`.** `ScheduledTask` gains `Slug` and
   `Label` so C19 can render and address tasks without re-deriving them. Snooze
   semantics are unchanged and stay covered: `snoozed_until` is the day the task comes
   back, not the day it goes quiet.

4. **Legacy events match by kind.** `lastEvent` must treat an event as satisfying a task
   when `event.TaskSlug == task.Slug`, **or** when the event has no slug and
   `event.Kind == task.Kind`. Without this, every plant looks overdue for everything the
   morning after the migration, because no historical event carries a slug.

   This has one transitional consequence worth stating rather than discovering: a single
   legacy `prune` event satisfies *both* of lavender's new prune tasks until each has
   been logged once. It decays on its own as real events accumulate, and the alternative
   — guessing which prune it was — is worse.

5. **Amend SPEC §7.7** with the matching rule. It is the kind of thing that looks like
   an implementation detail until someone "simplifies" it away.

## Definition of done

- `internal/care` still imports only the standard library and `internal/domain`; the
  import test in this package still passes.
- The five calibration scenarios are untouched and still land within ±10 %. This card
  must not move a single watering date.
- Table-driven tests for: two tasks of the same kind scheduling independently; a
  slug-less legacy event satisfying a task of its kind; a slugged event satisfying only
  its own task; the in-ground repot exclusion; and active-month bumping with several
  tasks in one species.
- `go test ./... -race` passes.
