# Plantation — build plan

Phases, task cards, and the order to run them in. The contract every card works against
is [SPEC.md](SPEC.md); the working conventions are [AGENTS.md](AGENTS.md).

## How to use this

Each card in [tasks/](tasks/) is sized for one agent holding only `SPEC.md`, `AGENTS.md`
and that card. Cards state their dependencies; do not start one whose dependencies are
unmerged. When a card's definition of done conflicts with `SPEC.md`, `SPEC.md` wins —
amend the spec first if the card is right.

## Phases

| Phase | Goal | Cards |
|---|---|---|
| **P0** Skeleton | it builds, it containers, it deploys | C01, C12, C13, C14 |
| **P1** Core | plants exist, due dates work, you can use it | C02, C03, C04, C10 |
| **P2** Reminders | Discord digest lands every morning it should | C08, C09 |
| **P3** Weather | outdoor plants get real ET0 and wait-for-rain | C05 |
| **P4** Tado | indoor plants get real VPD | C06, C07, C11 |

P3 and P4 slot into the engine through interfaces that exist from C01, so nothing before
them has to change when they arrive. Until C05 and C06 land, `NoopWeather` and
`NoopClimate` are wired and every plant schedules on `base_interval_days` — which is a
usable app, not a stub.

## Cards

| Card | Scope | Deps |
|---|---|---|
| [C01](tasks/C01-skeleton.md) | module, packages, domain types, interfaces + Noops, config | — |
| [C02](tasks/C02-store.md) | migrations, advisory-lock runner, pgxpool, repositories | C01 |
| [C03](tasks/C03-care-engine.md) | the reservoir model, VPD, clamps, deferral, `Explanation` | C01 |
| [C04](tasks/C04-species-catalog.md) | YAML schema, embed, validation, boot upsert | C01, C02 |
| [C05](tasks/C05-weather.md) | Open-Meteo client, forecast/observed cache, staleness ladder | C01, C02 |
| [C06](tasks/C06-tado-auth.md) | device flow, rotating-token store, weekly keepalive | C01, C02 |
| [C07](tasks/C07-tado-rooms.md) | home/room discovery, 30-min sampling | C06 |
| [C08](tasks/C08-notifier.md) | Discord client, outbox, dedupe, sweep | C01, C02 |
| [C09](tasks/C09-scheduler.md) | the 60 s tick, digest assembly, urgent alerts | C02, C03, C05, C07, C08 |
| [C10](tasks/C10-web-ui.md) | dashboard, plant CRUD, care logging, explanation panel | C02, C03 |
| [C11](tasks/C11-settings-ui.md) | Tado connect page, diagnostics, test notification | C05, C06, C08 |
| [C12](tasks/C12-image.md) | Dockerfile, health endpoints, tzdata | C01 |
| [C13](tasks/C13-helm-chart.md) | chart, CNPG Cluster, ArgoCD Application, backup CronJob | C12 |
| [C14](tasks/C14-cicd.md) | CI, multi-arch push, values.yaml write-back | C12, C13 |

## Dependency graph

```
C01 ──┬── C02 ──┬── C04
      │         ├── C05 ──┐
      │         ├── C06 ── C07 ──┤
      │         ├── C08 ──────────┼── C09
      │         └── C10 ──────────┤
      ├── C03 ───────────────────┘
      └── C12 ── C13 ── C14

C11 ← C05, C06, C08
```

## Waves

Run these in parallel within a wave; wait for the wave to merge before the next.

1. **C01 alone.** It is the shared context every other card reads. Do it carefully and
   review it properly — a wrong type here costs thirteen rewrites.
2. **C02, C03, C12.** No overlap in files.
3. **C04, C05, C06, C08, C13.**
4. **C07, C10, C14.**
5. **C09**, then **C11**.

## Where the risk actually is

- **C03** carries all the domain risk and almost no integration risk. Every other card
  trusts its output, and a sign error there produces plausible-looking wrong dates for
  months. Give it the strongest agent and read its tests personally.
- **C06** is the one that breaks in production rather than in CI. Rotating refresh tokens
  plus a 30-day inactivity window means one bad interleaving costs a manual re-link. The
  concurrency test is not optional.
- **C14** is the one that loops forever if the write-back guards are wrong. Verify it
  twice from a cold cache.

## Before the first deploy

Values that are supplied on the cluster, never committed: Docker Hub username, GitHub
repo slug, ingress hostname, IANA timezone, digest hour, latitude/longitude, Discord
webhook URL.

Cluster prerequisites: the CloudNativePG operator installed, and **Cloudflare Access in
front of the tunnel** — the app has no authentication of its own.

## After P4

Candidates, in rough order of value: a care-history chart per plant; seasonal review
("these three were consistently late all summer"); propagation and repotting notes;
per-room grouping in the digest; photos.
