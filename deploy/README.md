# Deploying Plantation

This directory holds the Helm chart (`chart/`) and the ArgoCD `Application`
(`argocd/application.yaml`) that keeps it synced. See `SPEC.md` §14/§15 for the
deployment contract this chart implements, and `PLAN.md` ("Before the first
deploy") for where this list comes from.

## Values that must be supplied on the cluster

None of these are committed to git — real values live only in a cluster-specific
`values` override (`helm install -f cluster-values.yaml ...` or `--set`) or in
Secrets created out-of-band. `deploy/chart/values.yaml` only ever holds obvious
placeholders.

| Value | `values.yaml` key | Notes |
|---|---|---|
| Docker Hub username/repo | `image.repository` | e.g. `janedoe/plantation` |
| Image tag | `image.tag` | Written by Release on each push to `main` (`0.1.0`, …); never `latest`. |
| Ingress hostname | `ingress.host` (and `config.baseURL`) | Only needed if `ingress.enabled: true`; a Cloudflare Tunnel may target the Service directly instead |
| IANA timezone | `config.tz` | e.g. `Europe/London` |
| Digest hour | `config.digestHour` | 0-23, local; defaults to `9` |
| Latitude / longitude | `config.latitude` / `config.longitude` | Required only if `config.weatherEnabled: true` (the default); the chart refuses to render otherwise |
| Discord webhook URL | `discord.existingSecretName` / `discord.existingSecretKey` | Name/key of a Secret you create yourself, e.g. `kubectl create secret generic plantation-discord --from-literal=webhookUrl=...`; empty name disables Discord |
| Write token (optional) | `writeToken.existingSecretName` / `writeToken.existingSecretKey` | Same pattern; optional bearer token required on POST routes |
| ArgoCD git repo URL | `argocd/application.yaml` → `spec.source.repoURL` | Never committed; fill in on the cluster's copy of this manifest |
| External database DSN (only if `postgresql.enabled: false`) | `externalDatabase.existingSecretName` / `externalDatabase.existingSecretKey` | Secret must contain a full `PLANTATION_DATABASE_URL`-compatible DSN |

## Cluster prerequisites

1. **The CloudNativePG operator installed.** The chart's `postgresql.Cluster`
   resource (guarded by `postgresql.enabled: true`, the default) is a CNPG
   `Cluster` custom resource and does nothing without the operator's CRDs and
   controller present.
2. **Cloudflare Access in front of the tunnel.** The app has no authentication of
   its own — every mutation is POST-only and would otherwise be a misconfiguration
   away from being world-writable, with no audit trail (SPEC.md §14).

## Install

```sh
helm install plantation deploy/chart -n plantation --create-namespace \
  -f cluster-values.yaml   # your own file, never committed
```

Or let the ArgoCD `Application` in `argocd/application.yaml` do it, once its
`spec.source.repoURL` placeholder is filled in on the cluster side and any
cluster-specific value overrides are wired via `spec.source.helm.valueFiles`.

## Backups

`backup.enabled` defaults to **false**. Nothing is backed up until the cluster overlay
turns it on — check that first, because the rest of this section is moot without it.

When enabled, the CronJob runs `pg_dump` against the CNPG `<release>-rw` Service on
`backup.schedule` (03:00 daily by default) and keeps `backup.retentionDays` of dumps on a
PVC as `plantation-<timestamp>.dump`, in `pg_dump` custom format.

### Keep `backup.image` at or ahead of the server

`pg_dump` refuses to dump a server newer than itself:

```
pg_dump: error: aborting because of server version mismatch
pg_dump: detail: server version: 18.6; pg_dump version: 17.11
```

A newer `pg_dump` reads an older server without complaint, so `backup.image` must be
**greater than or equal to** the cluster's PostgreSQL major version. It defaults to
`postgres:18`, matching `compose.yaml` and CI.

This matters because the CNPG `Cluster` in this chart does not pin `imageName` — the
major version is whatever the operator defaults to, and that default moves with operator
upgrades. When you raise the database major version, raise `backup.image` **first**. The
failure is quiet: a CronJob that exits non-zero at 03:00 and a backup directory that
simply stops growing.

### What this protects against, and what it does not

A nightly dump on a PVC recovers a dropped table, a bad migration, or a plant deleted by
mistake — the failure modes that actually happen. It does **not** survive losing the
cluster or the storage backend, because the dump lives in the same failure domain as the
database it came from. For a single-user homelab that may be an acceptable trade; it
should be a decision rather than a surprise.

The upgrade, when the trade stops being acceptable, is CloudNativePG's own
`barmanObjectStore`: continuous WAL archiving to an S3-compatible bucket, with
point-in-time recovery rather than nightly granularity. The operator is already a
prerequisite, so it is configuration on the `Cluster` resource rather than new
machinery. This chart does not template it today.

### Restoring

The dumps are `pg_dump` custom-format files on the backup PVC, and nothing mounts that
PVC except the CronJob — so restoring means starting a throwaway pod that mounts it and
carries the client tools.

```sh
# 1. Stop the app so nothing writes mid-restore. The scheduler's advisory lock is
#    not a table lock and will not block the restore — but a digest sent from
#    half-restored data is worse than a late one.
kubectl -n plantation scale deploy/plantation --replicas=0

# 2. Start a shell that mounts the backup PVC. Same image as the CronJob, so its
#    pg_restore is new enough for the server.
kubectl -n plantation run backup-shell --rm -it --restart=Never --image=postgres:18 \
  --overrides='{"spec":{"containers":[{"name":"backup-shell","image":"postgres:18","command":["bash"],"stdin":true,"tty":true,"volumeMounts":[{"name":"b","mountPath":"/backup"}]}],"volumes":[{"name":"b","persistentVolumeClaim":{"claimName":"plantation-backup"}}]}}'

# 3. Inside that shell: pick a dump and restore it over the network to the primary.
#    --clean --if-exists makes it idempotent, so a half-finished attempt can be rerun.
ls -la /backup
export PGPASSWORD=$(kubectl -n plantation get secret plantation-app -o jsonpath='{.data.password}' | base64 -d)   # from outside
pg_restore --clean --if-exists -h plantation-rw -U plantation -d plantation \
  /backup/plantation-<timestamp>.dump

# 4. Bring the app back. Migrations are re-checked at boot and are no-ops when the
#    restored schema is already current.
kubectl -n plantation scale deploy/plantation --replicas=1
```

Two things worth knowing before you need them. Care events are the only irreplaceable
data — `weather_daily`, `room_climate_samples` and `notifications` are caches the app
refills on its own, and `species` is rebuilt from the YAML catalog at every boot, so a
partial restore that loses those is survivable. And this procedure has not been exercised
on this cluster; the first run is the test, so do it deliberately rather than during an
incident.

## Notes

- `image.tag` is a plain scalar so Release can rewrite it with `yq` without a
  templating round-trip. A push to `main` bumps semver from commits since the last
  `vX.Y.Z` tag, writes `image.tag`, and pushes the new git tag. Argo syncs `main`;
  there is no per-bump edit of the Application manifest.
- Rendering with `postgresql.enabled: false` drops the CNPG `Cluster` entirely and
  points the Deployment at `externalDatabase.existingSecretName` instead.
- The app pod may reach `Ready` before CNPG does; `/readyz` (DB ping) fails until
  the database answers, `/healthz` never touches it, so Kubernetes does not
  restart the pod while it waits (SPEC.md §14).
