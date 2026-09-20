# C15 — Care vocabulary and species tasks

**Depends on:** — (amends the existing domain). **Blocks:** C16, C17, C18, C19.

**Owns:** `internal/domain/domain.go`, `SPEC.md` §3.

## Goal

Replace three hardcoded care slots with a list of named tasks, so a species can
declare what it actually needs — basil's flower pinching, lavender's two different
prunings — instead of forcing every activity through `prune`.

## Why the current shape does not stretch

`Species` has exactly three optional `*FixedTask` fields, and `TaskKind` is a closed
enum of five. Both limits are visible in the committed catalog today: basil schedules
`prune: interval_days: 14` when what you do is pinch flower spikes, and lavender's
`prune: interval_days: 365, active_months: [8, 9]` is a compromise standing in for a
hard prune in spring *and* a light trim after flowering.

## What to build

1. **Extend `TaskKind`** to a curated vocabulary. It stays a closed enum — the digest
   groups by it and the care history is queried by it, so a typo must not become a new
   task type. Adding one later is a SPEC amendment, deliberately:

   `water · prune · pinch · deadhead · fertilize · top_dress · repot · divide ·
   harvest · mulch · stake · inspect`

2. **`SpeciesTask`**, the new unit of scheduled care:

   ```go
   type SpeciesTask struct {
       Slug         string        // identity within the species, permanent
       Kind         TaskKind
       Label        string        // what the UI and the digest call it
       IntervalDays int
       ActiveMonths []time.Month  // empty means all year
   }
   ```

   `Slug` is a permanent identity in the same sense as a species slug: renaming it
   orphans the care events that reference it. It is unique per species, not globally,
   so two species may both have `feed`.

3. **`Species` gains `Description string` and `Tasks []SpeciesTask`**, and loses
   `Prune`, `Fertilize` and `Repot`. `FixedTask` is deleted — `SpeciesTask` replaces it.

4. **`CareEvent` gains `TaskSlug *string`.** C18 needs it to tell which task an event
   satisfied, and `domain.go` is this card's file — leaving it to C17 would mean that
   card editing a file it does not own. Nil means "logged before tasks had identity";
   see C18 for the matching rule that makes nil work.

5. **`water` is a reserved slug.** Watering is scheduled from the reservoir model and is
   per-plant, not per-species, so it never appears in `Tasks`. Reserving the slug keeps
   the routes in C19 uniform: every task, including water, is addressed by slug.

## Definition of done

- `SPEC.md` §3 lists the vocabulary, `SpeciesTask`, `Species.Description`,
  `Species.Tasks` and `CareEvent.TaskSlug`, and records that `water` is reserved and
  that a task slug is a permanent identity.
- `internal/domain` still imports only the standard library and `uuid`.
- The package compiles alone. Nothing else in the tree is expected to, yet — C16, C17,
  C18 and C19 land the consumers, and this card is deliberately the one that breaks
  them all at once rather than leaving two models alive simultaneously.
- Report the fact that the tree does not build to the next card's owner.
