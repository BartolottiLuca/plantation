# Plantation

Household plant care tracker. Says what needs watering, pruning, fertilizing or repotting
today, and sends a morning digest to Discord — but only when there is actually something
to do.

Outdoor plants are scheduled from real weather: the app tracks a soil-moisture deficit
from evapotranspiration minus rainfall, and defers watering when enough rain is forecast
in the next two days. Indoor plants are scheduled from the actual temperature and
humidity of the room they sit in, read from a Tado X thermostat.

Single Go binary, Postgres, one pod. No accounts, no app, no cloud service.

## Status

Greenfield. See [PLAN.md](PLAN.md) for the build order and [SPEC.md](SPEC.md) for the
full behavioural contract.

## How it decides

A pot is a reservoir measured in millimetres, because evapotranspiration and rainfall are
both depths. Capacity comes from pot diameter and substrate; each day the plant loses
`Kc × ET0` and gains whatever rain actually reaches it; when the deficit passes the
species' allowed depletion, it is due. Indoors the same model runs on an ET0 derived from
vapour pressure deficit instead of the weather forecast.

Every due date is computed on read from the care-event log and the weather series —
nothing is cached, so the number on the dashboard and the number in the digest can never
disagree, and the UI can always explain itself: *"deficit 14.2 mm of 13.0 mm allowed —
due today; 8 mm rain forecast Thursday at 80 %, so deferred to Friday."*

Species data lives as YAML in [catalog/species/](catalog/species/), not in the database.
Adding a plant species is one file and a pull request — see [AGENTS.md](AGENTS.md).

## Running locally

```sh
docker compose up -d          # Postgres
export PLANTATION_DATABASE_URL=postgres://plantation:plantation@localhost:5432/plantation
export PLANTATION_TZ=Europe/London
export PLANTATION_BASE_URL=http://localhost:8080
go run ./cmd/plantation serve
```

Weather and Tado are off unless configured; with both disabled every plant schedules on
its species' base interval, which is a working app rather than a stub. Point
`PLANTATION_DISCORD_WEBHOOK_URL` at a throwaway channel and run
`go run ./cmd/plantation send-test-digest` to see a digest without waiting for morning.

Full config table: [SPEC.md](SPEC.md) §12.

## Deploying

GitHub Actions builds a multi-arch image and pushes it to Docker Hub. A versioned
release is Actions → Release → Run workflow (`patch` / `minor` / `major`; first
tag is `0.1.0`). That run publishes the semver, commits it into
`deploy/chart/values.yaml`, and pushes git tag `vX.Y.Z`. ArgoCD watches the chart
and syncs. A plain push to `main` only adds a `sha-<short>` tag and does not move
the cluster.
Postgres is a CloudNativePG `Cluster` in the same namespace.

Cluster prerequisites:

- The CloudNativePG operator.
- **Cloudflare Access in front of the tunnel.** The app has no authentication of its own
  — that is a deliberate choice for a single-user homelab service, and it only holds if
  something in front of it is doing the authenticating. Without Access, every mutation is
  one misconfiguration away from being world-writable.

Coordinates, hostname and the Discord webhook URL are supplied on the cluster and are
never committed.
