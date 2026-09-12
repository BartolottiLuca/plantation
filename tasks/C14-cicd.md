# C14 — GitHub Actions: CI, image push, tag write-back

**Depends on:** C12, C13. **Blocks:** nothing.

**Owns:** `.github/workflows/ci.yaml`, `.github/workflows/release.yaml`.

## Goal

A push to `main` builds a multi-arch image, pushes it to Docker Hub, and commits the new
tag into the chart so ArgoCD syncs it — without triggering itself forever.

## What to build

1. **`ci.yaml`** on pull requests and on `main`: Go setup with module caching,
   `go vet`, `golangci-lint run`, `go test ./... -race` (with a Postgres service
   container and `PLANTATION_TEST_DSN` set so the store and catalog tests actually run),
   `helm lint`, and a docker build **without** push.

2. **`release.yaml`** on push to `main`:
   - buildx for `linux/amd64,linux/arm64`, pushing
     `docker.io/<DOCKERHUB_USERNAME>/plantation:sha-<short>`;
   - **immutable tags only. Never `latest`** — ArgoCD cannot detect a change to a
     mutable tag, so a `latest` deployment silently never updates;
   - then `yq` the new tag into `deploy/chart/values.yaml` and commit it back.

   Secrets: `DOCKERHUB_USERNAME`, `DOCKERHUB_TOKEN`.

3. **Loop guards**, all three, because any one of them alone is fragile:
   - push with the default `GITHUB_TOKEN`, which deliberately does not trigger new
     workflow runs;
   - `paths-ignore: ['deploy/chart/values.yaml']` on the build workflow;
   - `[skip ci]` in the bot commit message.

   Plus a rebase-and-retry around the push, so a concurrent merge does not fail the run.

4. **Placeholders.** `<DOCKERHUB_USERNAME>` and the repo slug are filled in when the
   repo is created; leave them as obvious placeholders with a comment, not as guesses.

## Definition of done

- A push to `main` produces exactly one immutable image tag and exactly one write-back
  commit, and that commit does not retrigger the workflow. Verify by watching the
  Actions tab, not by reasoning about it.
- A commit touching only `deploy/chart/**` does not rebuild the image.
- The workflow is green twice in a row from a cold cache.
- Two merges landing close together do not leave `values.yaml` on a stale tag.
- No secret is echoed, and `DOCKERHUB_TOKEN` is not passed to any step that does not
  need it.

## Do not

Add ArgoCD Image Updater — the write-back is the chosen mechanism, and running both
means two things racing to set the same field.
