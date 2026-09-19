# C12 — Container image and health endpoints

**Depends on:** C01. **Blocks:** C13, C14.

**Owns:** `Dockerfile`, `.dockerignore`, `internal/web/health.go`.

## Goal

A small, non-root, multi-arch image whose probes tell Kubernetes the truth.

## What to build

1. **Dockerfile.** Multi-stage: a Go builder with module caching, then a `scratch`
   final stage. `CGO_ENABLED=0`, trimmed build flags, version and commit injected with
   `-ldflags -X`. Runs as a numeric non-root uid with a read-only root filesystem.
   Targets `linux/amd64` and `linux/arm64` — the home server may be either.
   **Copy `/etc/ssl/certs/ca-certificates.crt` from the builder into the final stage.**
   `scratch` ships no trust store, and its absence breaks every outbound HTTPS call
   without failing the build or the health checks — see SPEC §14.

2. **`import _ "time/tzdata"`** must be present in `main.go` and must stay present. A
   distroless or scratch image carries no zoneinfo database, so `time.LoadLocation`
   fails and the app silently falls back to UTC — a bug discovered a day late, in the
   wrong timezone. Add a test that loads the configured zone and fails loudly if it
   cannot, so the image cannot regress on this quietly.

3. **Probes.**
   - `/healthz` — liveness. Returns 200 whenever the process is alive. **It must not
     touch the database.** If liveness checks the DB, a CNPG failover restarts the app
     for no reason, and a longer outage turns into CrashLoopBackOff.
   - `/readyz` — readiness. Pings the database with a short timeout and returns 503 when
     it cannot.
   - `/version` — build info, useful for confirming what ArgoCD actually deployed.

4. **`.dockerignore`** excluding `.git`, `deploy/`, `tasks/`, test data and local
   artifacts.

## Definition of done

- `docker buildx build --platform linux/amd64,linux/arm64 .` succeeds.
- The image is under ~30 MB, runs as non-root, and starts with a read-only root
  filesystem.
- With no database reachable: `/healthz` returns 200 and `/readyz` returns 503 — verify
  this by running the container with a bogus `PLANTATION_DATABASE_URL`.
- The container logs a structured startup line including version and resolved timezone,
  and exits 0 on SIGTERM within the grace period.
- `TZ`-sensitive behaviour is correct in the built image, not just under `go run`.
