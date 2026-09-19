# Investigation — open findings

A backlog, not a plan. Findings from a review pass on 2026-09-19, at `e70c6e4`
(v0.1.14). Each item states the evidence, so a future reader can re-check it
rather than trust it. Nothing here is committed work; the fixes made during that
pass (`fix: water`, `fix: improve care task`) are already on `main`.

Ordered by value, not by effort.

---

## 1. The dashboard is 2N+1 queries

`schedulePlant` runs one `Events.LatestByKind` **and** one `Tasks.List` per
plant; `scheduleRows` calls it once per plant. Neither `CareEventRepo` nor
`CareTaskRepo` has a batch method, so a dashboard with 20 plants costs ~40 round
trips.

The second query per plant was introduced by the `care_tasks` fix (`e70c6e4`) —
it is the price paid for the dashboard and the digest agreeing. It was N+1
before, and is 2N+1 now.

**Impact:** small. Single-user app, local Postgres, sub-millisecond queries — a
few milliseconds per render. This is a tidiness item, not an outage waiting to
happen.

**Fix:** `ListByPlants(ctx, []uuid.UUID)` on both repos, called once in
`scheduleRows` and indexed into per plant. `care.ScheduleAll` already takes
controls as a plain slice, so nothing in the engine changes.

Files: [internal/web/schedule.go](internal/web/schedule.go),
[internal/store/care_task.go](internal/store/care_task.go),
[internal/store/care_event.go](internal/store/care_event.go)

---

## 2. Backups are disabled by default and never leave the cluster

`backup.enabled: false` in [deploy/chart/values.yaml:81](deploy/chart/values.yaml#L81).
Unless the cluster overlay turns it on, there are no backups at all.

When enabled, the CronJob runs `pg_dump` against the CNPG `-rw` Service and
writes to a **PVC in the same cluster as the database**. Nothing under `deploy/`
references S3, an object store, `barmanObjectStore` or restic, so the backup
shares a failure domain with the thing it is backing up. It survives a bad
migration or a dropped table; it does not survive losing the cluster or the
storage backend.

**Fix, in order of value:**

1. Confirm whether the cluster overlay enables it. If not, there is currently no
   recovery path at all and everything else here is lower priority.
2. CloudNativePG already supports `barmanObjectStore` — continuous WAL archiving
   to an S3-compatible target with point-in-time recovery. The operator is
   already a prerequisite, so this is configuration rather than new machinery,
   and it replaces nightly `pg_dump` with something strictly better.
3. Do one documented restore, once. An untested backup is a hypothesis. Worth a
   short "restoring from backup" section in `deploy/README.md` written *while*
   doing it, not from memory.

---

## 3. CI and Release skip any commit that only touches `values.yaml`

Both workflows carry:

```yaml
paths-ignore:
  - "deploy/chart/values.yaml"
  - "deploy/chart/Chart.yaml"
```

GitHub skips a workflow when **every** changed path matches the ignore list. The
intent is to stop the release write-back retriggering CI, and for the bot's
commits it works. But it is not scoped to the bot: a *human* commit touching only
`values.yaml` is skipped too, so **`helm lint` never runs on it**.

That is the file where `replicaCount`, resource limits and probe settings live,
and SPEC §14 specifically leans on `values.yaml` carrying "replicas must stay 1".
It is the chart file least well served by being unguarded.

**Fix:** the loop is already stopped twice over — the default `GITHUB_TOKEN` does
not start new workflow runs, and the bot commit carries `[skip ci]`.
`paths-ignore` is the third belt and the only one with a side effect. Either drop
it, or keep the skip but scope it to the bot (e.g. condition the job on
`github.event.head_commit.author.name != 'github-actions[bot]'`) so human edits
to those files still get linted.

Files: [.github/workflows/ci.yaml](.github/workflows/ci.yaml),
[.github/workflows/release.yaml](.github/workflows/release.yaml)

---

## 4. The chart write-back is avoidable, if Argo stops tracking git

**The question:** does the release workflow need to commit the image version back
to the repo?

**With the current Argo config, yes.** [deploy/argocd/application.yaml](deploy/argocd/application.yaml)
sets `repoURL: <this repo>`, `targetRevision: main`, `path: deploy/chart`. Argo
renders the chart *from git*, so git is the only place it looks for a version.
Something has to change on `main`. The workflow is not doing anything
gratuitous, and its three loop guards are all genuinely load-bearing.

**The alternative is to change what Argo tracks.** Publish the chart as an OCI
artifact instead:

```sh
helm package deploy/chart --version X.Y.Z --app-version X.Y.Z
helm push plantation-X.Y.Z.tgz oci://registry-1.docker.io/<user>
```

and point the Application at `repoURL: oci://…`, `chart: plantation`,
`targetRevision: 0.1.*`. Argo polls the registry, sees a chart version matching
the range, and syncs. No commit, no bot, no `[skip ci]`, no loop guards, no
push-retry loop, no `fetch-depth: 0`.

**What the current design costs:**

- **35% of the history is bot noise** — 15 of 43 commits are
  `chore(chart): deploy … [skip ci]`.
- Three loop guards that must all keep working. The workflow's own comment says
  "any one alone is fragile — keep all three", and PLAN.md flags C14 as the card
  that "loops forever if the write-back guards are wrong". OCI deletes the
  failure mode instead of guarding it.
- The `paths-ignore` blind spot in §3 above, which exists *because* of the
  write-back.

**What it buys:** one repo, and a git history that records every deployment.
That is a real benefit for a single-operator homelab and the reason this is
ranked below the blind spot rather than above it.

**Recommendation:** fix §3 now — it is a correctness hole and costs a few lines.
Treat OCI as a separate, deliberate migration whose payoff is mostly tidiness.
Doing §3 does not block it, and the two are independent.

---

## 5. Documentation is stale in two places

- **README says the project is greenfield.** "Status: Greenfield. See PLAN.md for
  the build order" — all fourteen cards are shipped and v0.1.14 is deployed. It
  is the first thing a newcomer reads and the first thing that misleads them.
- **PLAN.md and SPEC.md contradict each other on photos.** PLAN's "After P4"
  proposes photos as a candidate; SPEC §1 lists "Plant photos" under Non-goals.
  AGENTS.md says the spec wins, so either the spec needs amending or the
  candidate comes off the plan. Worth settling before anyone picks it up.

---

## 6. `internal/scheduler` coverage is 54.7%

Lowest in the repo, and it is the path where an engine bug becomes a wrong
Discord message rather than a wrong number on a page.

Worth being precise about what this is *not*: the spec-mandated tests in SPEC §15
are all present and good, including 30 simulated days across the 2026-03-29 DST
transition asserting exactly one digest per actionable day. The gap is error
branches and degraded paths, not headline behaviour.

---

## 7. Smaller items

- **`inspect` is a `TaskKind` nothing schedules.** It can be logged via
  `/plants/{id}/care/inspect` and shows up in history and labels, but no species
  field drives it and `care.ScheduleAll` ignores it. That looks deliberate —
  SPEC §7.7 only covers prune, fertilize and repot — but the spec never says so.
  One line in §3 settles whether it is log-only by design.
- **`cap`, `min` and `max` shadow builtins** in
  [internal/care/water.go](internal/care/water.go) and
  [internal/care/math.go](internal/care/math.go). The `predeclared` linter flags
  them. In a file doing capacity arithmetic a local named `cap` is a re-reading
  hazard, but the rename churns the most safety-critical file in the project, so
  it belongs in its own commit rather than riding along with a logic change.
- **Supply chain.** GitHub Actions are pinned to tags (`@v7`) rather than commit
  SHAs, `provenance: false` disables SLSA attestations, and no image scanning
  runs. All defensible for a single-user service behind Cloudflare Access; listed
  so the choice is explicit rather than accidental.
- **The CI smoke build is single-arch**, so an arm64-only break surfaces at
  release rather than on the PR. Low risk — the Dockerfile cross-compiles rather
  than emulating — but it is the one difference between the two build paths.

---

## On features

PLAN.md's "After P4" list still reads well. **Seasonal review** ("these three
were consistently late all summer") is the most valuable idea on it.

It is worth deliberately *not* starting yet. Two fixes landed on 2026-09-19 that
changed what the stored history means: `Explanation.DeficitMM` now reports
today's reservoir rather than a saturated 60-day projection, and due-ness now
tracks the current above-threshold run rather than the first crossing ever, so
rain can end a due state. A review feature mines exactly that history. Letting a
season of corrected data accumulate first costs nothing and makes the first
version of the feature trustworthy.
