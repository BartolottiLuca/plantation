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
