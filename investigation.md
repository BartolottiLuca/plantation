# Investigation — findings

Findings from a review pass on 2026-09-19, and what came of them. Everything
under **Addressed** is done and verified; everything under **Open** is not.
Each item states its evidence so a future reader can re-check rather than trust.

---

# Open

## The chart write-back, and whether Argo should track git

**The question:** does the release workflow need to commit the image version
back to the repo?

**With the current Argo config, yes.** [deploy/argocd/application.yaml](deploy/argocd/application.yaml)
sets `repoURL: <this repo>`, `targetRevision: main`, `path: deploy/chart`. Argo
renders the chart *from git*, so git is the only place it looks for a version.
Something has to change on `main`. The workflow is not doing anything
gratuitous, and its loop guards are genuinely load-bearing.

**The alternative is to change what Argo tracks.** Publish the chart as an OCI
artifact instead:

```sh
helm package deploy/chart --version X.Y.Z --app-version X.Y.Z
helm push plantation-X.Y.Z.tgz oci://registry-1.docker.io/<user>
```

and point the Application at `repoURL: oci://…`, `chart: plantation`,
`targetRevision: 0.1.*`. Argo polls the registry, sees a chart version matching
the range, and syncs. No commit, no bot, no `[skip ci]`, no push-retry loop, no
`fetch-depth: 0`.

**What the current design costs:** roughly a third of the history is
`chore(chart): deploy … [skip ci]`, and PLAN.md flags C14 as the card that
"loops forever if the write-back guards are wrong" — OCI deletes that failure
mode rather than guarding it.

**What it buys:** one repo, and a git history that records every deployment.
That is a real benefit for a single-operator homelab, which is why this was
deferred rather than done. The CI blind spot that the write-back *caused* has
been fixed independently (see Addressed), so nothing forces this decision now.

## Backups do not leave the cluster

`backup.enabled` defaults to **false** in [deploy/chart/values.yaml](deploy/chart/values.yaml);
confirm whether the cluster overlay turns it on, because nothing else here
matters if it does not. When enabled, `pg_dump` writes to a PVC in the same
cluster as the database, so the backup shares a failure domain with what it is
backing up. It survives a bad migration; it does not survive losing the cluster.

The upgrade is CloudNativePG's `barmanObjectStore` — continuous WAL archiving to
an S3-compatible bucket with point-in-time recovery. The operator is already a
prerequisite, so it is configuration on the `Cluster` resource rather than new
machinery. This chart does not template it.

Deferred deliberately: it needs a bucket, credentials and a decision that are
not visible from the repo. The failure modes, the version constraint and a
restore procedure are now written down in [deploy/README.md](deploy/README.md),
which was the part that could be done from here.

**The restore procedure has never been exercised.** The first run is the test.

## Supply chain, accepted as-is

GitHub Actions are pinned to tags (`@v7`) rather than commit SHAs, so a
compromised action could change under a mutable tag. `provenance: false`
disables SLSA attestations, and no image scanning runs. All three were
considered and accepted for a single-user service behind Cloudflare Access.
Listed so the choice stays explicit rather than becoming an accident. Renovate
understands SHA-pinned actions if that changes.

---

# Addressed

## Dashboard and digest were 2N+1 queries

`schedulePlant` ran one `Events.LatestByKind` and one `Tasks.List` per plant, and
both the dashboard and the digest looped over every plant.

Both now read the whole set once, via `LatestByKindForPlants` and
`ListForPlants` on the two repos. The scheduler's `EventSource` and `TaskLister`
interfaces narrowed to just the batch form, since nothing in that package needs
the per-plant read any more; the web `Server` keeps both, because the plant
detail page legitimately schedules one plant.

Pinned by `TestDashboardQueryCountIsFlatInPlantCount`, which counts round trips:
**12 plants now cost 2 queries, not 25**. `TestBatchReadsMatchPerPlantReads`
checks the new SQL against real Postgres — that batch and per-plant agree, that
`DISTINCT ON (plant_id, kind)` collapses duplicate waterings to the newest, and
that voided events stay excluded.

## CI skipped any commit that only touched `values.yaml`

Both workflows carried `paths-ignore` on `values.yaml` and `Chart.yaml` to stop
the release write-back retriggering CI. GitHub skips a workflow when *every*
changed path matches, and `paths-ignore` cannot tell the bot from a human — so a
human commit touching only `values.yaml` skipped `helm lint` too. That is the
file holding `replicaCount`, resource limits and probes, and SPEC §14 leans on it
carrying "replicas must stay 1".

CI now keys the skip on the commit author instead:
`if: github.event.head_commit.author.name != 'github-actions[bot]'`. The bot is
still ignored; humans never are. `head_commit` is absent on `pull_request`
events, so PRs always run.

`release.yaml` keeps its `paths-ignore` deliberately. There it is loop guard #2
of three, and it has no correctness downside: a values-only edit should not cut
a new image, and Argo picks the change up from `main` regardless.

## The backup CronJob could not dump the database

`pg_dump` refuses to dump a server newer than itself. The chart hardcoded
`postgres:17` while `compose.yaml` and CI both run 18, and the CNPG `Cluster`
does not pin `imageName` — so the major version is the operator's default and
moves with operator upgrades. Confirmed against a real server rather than
inferred:

```
pg_dump: error: aborting because of server version mismatch
pg_dump: detail: server version: 18.6; pg_dump version: 17.11
EXIT=1
```

The reverse direction was checked too: `pg_dump` 18 against a server 17 exits 0.
So the backup image is now `backup.image`, defaulting to `postgres:18`, which is
correct whether the cluster runs 17 or 18. `deploy/README.md` records the rule —
raise `backup.image` **before** the database, never after — and why the failure
is quiet: a CronJob that exits non-zero at 03:00 and a directory that stops
growing.

## `internal/scheduler` coverage: 53.4% → 61.5%

`TestDigestHonoursTaskControls` is the digest half of the README's promise that
the dashboard and the digest can never disagree; the web half was already
pinned. It covers no control rows, enabled, disabled, snoozed into the future,
and a snooze already elapsed — asserting in each case that the day is still
*recorded*, so the model is not re-evaluated every tick (SPEC §8). Verified to
fail if the snooze comparison is re-inverted.

`TestHeatwaveAlertOncePerForecastDay` pins SPEC §9: one alert for the 34 °C day
and none for the 24 °C day, across five polls of the same forecast.
`TestDigestSurvivesATaskListerFailure` covers the degraded path.

## Documentation

README said the project was greenfield while all fourteen cards were shipped and
v0.1.14 deployed; it now describes the project as in service and points here for
open findings.

PLAN.md proposed photos while SPEC §1 listed them under Non-goals. AGENTS.md says
the spec wins, so photos came off PLAN's candidate list, with a line recording
that wanting them means amending §1 first rather than adding a card.

SPEC §3 now says `inspect` is log-only: it can be recorded and shows in the care
history, but no species field drives it and nothing schedules it. That was
always true and never written down.

## Shadowed builtins

`cap`, `min`, `max` and `real` were shadowed across `care`, `config`, `web` and a
`weather` test. Renamed, and `predeclared` added to `.golangci.yml` so it cannot
come back. The `care` rename was the one worth hesitating over — churn in the
most safety-critical file in the project — so it was done on its own, with the
five calibration scenarios and the property tests as the check.

## CI built one architecture

The smoke build was single-arch, so an arm64-only break surfaced at release
rather than on the PR. It now builds `linux/amd64,linux/arm64`, matching the
release. Cheap, because the Dockerfile cross-compiles from `BUILDPLATFORM` and
neither arch needs qemu for the Go step.

---

## On features

PLAN.md's "After P4" list still reads well, and **seasonal review** ("these three
were consistently late all summer") is the most valuable idea on it.

It is worth deliberately *not* starting yet. Two fixes on 2026-09-19 changed what
the stored history means: `Explanation.DeficitMM` now reports today's reservoir
rather than a saturated 60-day projection, and due-ness now tracks the current
above-threshold run rather than the first crossing ever, so rain can end a due
state. A review feature mines exactly that history. Letting a season of corrected
data accumulate first costs nothing and makes the first version trustworthy.
