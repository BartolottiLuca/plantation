# C13 — Helm chart, CNPG, ArgoCD

**Depends on:** C12. **Blocks:** C14.

**Owns:** `deploy/chart/**`, `deploy/argocd/application.yaml`.

## Goal

One chart that deploys the app and its database, and an ArgoCD Application that keeps
them synced.

## What to build

1. **Chart.** `deploy/chart` with `Chart.yaml`, `values.yaml`, and templates:
   - **Deployment** — `replicas: 1`, **`strategy: Recreate`**. With RollingUpdate two
     pods briefly overlap; the advisory lock survives that, but nobody wants two
     schedulers. `values.yaml` carries a comment saying replicas must stay 1 and why.
     Non-root, read-only root filesystem, dropped capabilities, resource requests and
     limits, liveness on `/healthz` and readiness on `/readyz` — wired to C12's split
     semantics, never both on the DB-touching one.
   - **Service**, and an **Ingress** behind `ingress.enabled` (Cloudflare Tunnel may
     target the Service directly, so it must be optional).
   - **ConfigMap** for non-secret config; `existingSecret` reference for the Discord
     webhook. No secret values in `values.yaml`, ever.
   - **CNPG `Cluster`** behind `postgresql.enabled`: 1 instance, storage size,
     `bootstrap.initdb` creating database `plantation` with owner `plantation`. The app
     takes `PLANTATION_DATABASE_URL` from the generated `<cluster>-app` Secret key
     `uri`, and connects through the `<cluster>-rw` Service.
   - **Backup CronJob** behind `backup.enabled`: `pg_dump` to a PVC, retention in days.
   - `image.tag` as a plain, greppable value — C14's write-back edits it with `yq`, so
     keep it a simple scalar, not a computed template expression.

2. **ArgoCD Application.** `deploy/argocd/application.yaml`: automated sync with `prune`
   and `selfHeal`, `CreateNamespace=true`, namespace `plantation`, source path
   `deploy/chart`, target revision `main`.

3. **Docs.** A short `deploy/README.md` listing the values that must be supplied on the
   cluster — Docker Hub repo, hostname, IANA timezone, digest hour, coordinates, the
   webhook Secret name — and the two prerequisites: the **CloudNativePG operator**, and
   **Cloudflare Access in front of the tunnel**, since the app has no authentication of
   its own.

## Definition of done

- `helm lint` and `helm template` clean, including with only the required values set.
- `helm template | kubectl apply --dry-run=server -f -` passes against a cluster that
  has the CNPG CRDs.
- Rendering with `postgresql.enabled=false` produces a working app that points at an
  external DSN.
- No placeholder in the chart resolves to a real hostname, coordinate or secret.
- The chart deploys cleanly into a fresh namespace and the app reaches `Ready` once CNPG
  does — including the case where the app pod starts first, which it will.

## Do not

Add an HPA, a PodDisruptionBudget for a single replica, or a service mesh sidecar. This
is a one-pod homelab app.
