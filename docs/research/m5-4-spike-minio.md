# M5.4 spike — MinIO container behaviour

**Run date**: 2026-05-01
**Run location**: `~/scratch/m5-4-minio-spike/` (scratch dir outside the Garrison repo per RATIONALE §13)
**Run duration**: ~30 minutes
**Image used**: `minio/minio:latest` resolved to `sha256:69b2ec208575b69597784255eec6fa6a2985ee9e1a47f4411a51f7f5fdd193a9` at spike time
**Docker version**: 29.3.0

This spike characterises MinIO behaviour for the M5.4 knowledge-base pane. M5.4 stores Company.md in a MinIO bucket; this document is the binding input to `/speckit.specify` per the milestone context's spec-kit flow.

---

## Environment

- Single MinIO container, no clustering. Production runs `mode = standalone` (single-node, single-disk). MinIO docs explicitly support this mode for small-footprint deployments.
- Docker Compose alongside the existing 3 containers (supervisor + mempalace + socket-proxy) on `garrison-net`. Spike confirmed: MinIO joins the network the same way the other services do; nothing about the existing containers needs to change.
- Image size 175 MB. Comparable to `python:3.11-slim` mempalace sidecar (~150 MB). Doesn't change deployment footprint character.

---

## Findings

### F1. Image is published as `minio/minio:latest`; pin by digest in production

The current `:latest` tag resolves to `sha256:69b2ec...`. MinIO's release cadence is roughly monthly with security patches in between; pinning by digest (not tag) keeps deploys deterministic. The Garrison Dockerfile pattern (M2.1 pinned Claude Code by GPG fingerprint + SHA256) carries forward here as a digest-pin.

**Implication**: M5.4 pins to the spike-time digest (or the latest digest at implementation start). The compose service uses `image: minio/minio@sha256:<digest>`, not a tag. A monthly bump (post-M5) re-runs basic smoke against a new digest before pinning.

### F2. No baked-in healthcheck; `/minio/health/live` is the standard probe

`docker inspect`'s `Healthcheck` field is `null`. Compose must provide one. The live endpoint `GET /minio/health/live` returns HTTP 200 within ~3 seconds of container start with empty data, no auth required.

**Implication**: docker-compose service block defines:

```yaml
healthcheck:
  test: ["CMD", "mc", "ready", "local"]
  # OR — without an mc binary in the image, an arbitrary HTTP probe:
  # test: ["CMD-SHELL", "curl -f http://localhost:9000/minio/health/live || exit 1"]
  interval: 10s
  timeout: 5s
  retries: 3
  start_period: 10s
```

The MinIO image doesn't ship `curl` or `mc` by default. Operator picks: install one as a Dockerfile.minio thin wrapper, or use Compose's `start_period` to give MinIO time to boot and skip a Docker-level healthcheck. Default lean for M5.4: skip a Docker-level healthcheck; the supervisor's startup probe (F4) is the actual readiness signal that gates supervisor work.

### F3. Boot requirements

Boot needs only:
- `MINIO_ROOT_USER` env (≥3 chars)
- `MINIO_ROOT_PASSWORD` env (≥8 chars)
- A data directory passed as an argument to `server`
- One port (default 9000) for the S3 API

Optional:
- `--console-address ":9001"` exposes the admin UI on a separate port (dev-only; production should not expose this externally)

`docker run -d -e MINIO_ROOT_USER=... -e MINIO_ROOT_PASSWORD=... -p 19000:9000 minio/minio:latest server /data` is the minimum viable command.

### F4. Persistence model — Docker named volume, not host bind mount

**Confirmed empirically**: running without any volume → `docker rm -f` wipes all bucket + object data. Buckets do NOT survive container removal in this mode.

Running with a **Docker named volume** (e.g. `docker volume create m54-minio-data` + `-v m54-minio-data:/data`) → data survives `docker rm -f` + `docker run` cycles. Re-attaching the same named volume restores all buckets + objects.

**Operator constraint** (recorded in m5-4-context.md): no host-volume mounts. Docker named volumes satisfy this — they're managed by Docker, stored under `/var/lib/docker/volumes/`, and don't expose host filesystem paths to the container in a way that creates the M2.x agent-workspace-escape concerns.

**Implication**: M5.4's docker-compose adds a top-level `volumes:` key declaring `garrison-minio-data`, mounted into the MinIO service at `/data`. Operator backup/restore happens via `docker run --rm -v garrison-minio-data:/data:ro -v $(pwd):/backup busybox tar czf /backup/minio-$(date).tar.gz /data` (operator-side, post-M5).

### F5. Bucket bootstrap is NOT idempotent by default

Empirical:
- `mc mb minio/garrison-company` succeeds on first run.
- `mc mb minio/garrison-company` on second run errors: *"Your previous request to create the named bucket succeeded and you already own it."*
- `mc mb --ignore-existing minio/garrison-company` is idempotent.

For a Go-SDK supervisor startup probe (the recommended bootstrap path), the equivalent pattern is `BucketExists(ctx, bucket)` → if false, `MakeBucket(ctx, bucket, ...)`. This is the standard MinIO Go SDK pattern.

**Implication**: M5.4 ships a supervisor startup hook (under `internal/objstore/` or similar) that runs once at boot:

```go
exists, err := client.BucketExists(ctx, cfg.CompanyMDBucket)
if err != nil { return fmt.Errorf("objstore: bucket exists check: %w", err) }
if !exists {
    if err := client.MakeBucket(ctx, cfg.CompanyMDBucket, minio.MakeBucketOptions{}); err != nil {
        return fmt.Errorf("objstore: create bucket: %w", err)
    }
    logger.Info("objstore: created bucket", "name", cfg.CompanyMDBucket)
}
```

Mirrors the M2.2 mempalace bootstrap pattern: idempotent, fail-closed at startup if MinIO is unreachable, logs the create-vs-exists outcome.

### F6. Scoped service accounts work; root creds stay separate

Empirical: `mc admin user svcacct add minio <root-user> --access-key garrisonsvc --secret-key <secret>` creates a scoped service account that authenticates as a child of root. The scoped account can read + write the buckets root owns; it cannot create new admin users or change MinIO config.

**Implication for credential storage** (Open Question §3 in m5-4-context.md):

- **Root creds** (`MINIO_ROOT_USER` / `MINIO_ROOT_PASSWORD`) live in env vars on the MinIO container itself. They never leave the operator's deploy environment. Setting them via Infisical doesn't add safety — MinIO itself reads them at boot, so they have to be on the host before the container starts.

- **Scoped service-account creds** (`access-key` / `secret-key`) used by the supervisor + dashboard SHOULD live in Infisical. The supervisor's existing `garrison_supervisor` Infisical machine identity gets a new grant set: `MINIO_ACCESS_KEY` + `MINIO_SECRET_KEY` (or equivalent). The supervisor fetches at boot via the existing `internal/vault.Client` path and zeroes the bytes after constructing the MinIO client.

- The scoped account creation itself is an ops-checklist step run once per deploy (mirrors the M2.2 `garrison_agent_mempalace` Postgres role-creation pattern): post-`docker compose up`, the operator runs `mc admin user svcacct add` to mint the scoped account, then puts the access key + secret in Infisical.

**Default to spec**: Option (a) from m5-4-context.md Open Question §3 (Infisical for the scoped account, env vars for root) — empirically confirmed as the right shape.

### F7. Concurrent writes default to last-write-wins; ETag enables optimistic concurrency

Empirical: 10 concurrent `mc pipe` writes to the same key all return success; the final read shows the content of the last write to physically commit. No errors, no automatic conflict detection. MinIO does NOT enable bucket versioning by default.

ETag is present on every object (e.g. `efe8a259b97ddf78d4440b2ef1a7aeec-1`). The S3 If-Match header (and the Go SDK's `PutObjectOptions.IfMatch` / `IfNoneMatch` equivalents) supports optimistic concurrency: client reads object + ETag, edits, sends If-Match=<etag> on PUT; MinIO rejects with 412 Precondition Failed if the object has been overwritten since.

**Implication for Open Question §2** (Company.md edit conflict semantics): single-operator constraint (Constitution X) makes hard conflict detection optional. Default lean stays last-write-wins for M5.4; the dashboard's edit affordance fetches the current ETag at edit-modal-open time and includes If-Match on save. If the save fails with 412, the dashboard refreshes the editor with the current content + a "your edit was based on a stale version" message. Two-line implementation cost; surfaces concurrent-window edits cleanly without adding a real merge UI.

### F8. Go SDK — `minio-go/v7` is the right choice

`go list -m -versions github.com/minio/minio-go/v7` shows v7.1.0 as the latest stable. Module is publicly reachable; no Garrison-side network constraints get in the way.

The `aws-sdk-go-v2/s3` alternative was considered: it's heavier (more transitive deps), supports more S3-specific features the spike doesn't need (multipart upload progress callbacks, S3-specific server-side encryption shapes, IAM role assumption), and points at MinIO via endpoint override — which works but means extra config surface. `minio-go` is upstream-maintained by the same team that ships the server, follows MinIO server semantics exactly, and ships smaller.

**Confirmed dependency for M5.4**: `github.com/minio/minio-go/v7@v7.1.0` (operator pins exact version at /garrison-plan time).

---

## Operational shape — proposed compose service block

```yaml
services:
  minio:
    image: minio/minio@sha256:69b2ec208575b69597784255eec6fa6a2985ee9e1a47f4411a51f7f5fdd193a9
    container_name: garrison-minio
    networks:
      - garrison-net
    volumes:
      - garrison-minio-data:/data
    environment:
      - MINIO_ROOT_USER=${MINIO_ROOT_USER}
      - MINIO_ROOT_PASSWORD=${MINIO_ROOT_PASSWORD}
    command: server /data
    # No host port exposure in production — supervisor + dashboard
    # reach MinIO via the internal `garrison-minio:9000` service name.
    # Dev compose can add `ports: ["9000:9000", "9001:9001"]` and
    # `--console-address ":9001"` for the admin UI.
    restart: unless-stopped

volumes:
  garrison-minio-data:
```

The supervisor's startup hook (F5) creates the `garrison-company` bucket if missing. The dashboard (or supervisor proxy, per Open Q §1) reads/writes `company.md` against that bucket using the scoped service account's credentials fetched from Infisical (F6).

---

## Surprises

### F9 — `mc` requires the `mc admin user svcacct add` form for scoped creds; the older `mc admin policy attach` is deprecated

Initial attempts using `mc admin user add` + `mc admin policy attach` failed with deprecation warnings in MinIO's recent releases. The current shape is `mc admin user svcacct add` with explicit `--access-key` + `--secret-key` flags. This is documented in MinIO's changelog but not prominently in the bucket-getting-started flow. The ops checklist must use the svcacct form.

### F10 — `latest` tag bumped during the spike

When the spike re-pulled `minio/minio:latest` after a teardown, the digest may shift between checks. M5.4's compose pin uses the digest captured here; a monthly version-bump pass (post-M5) re-confirms. This is the Garrison pattern: track upstream actively, but never trust `:latest` to be reproducible.

---

## Open questions deferred to the spec

These are the items the spike did NOT resolve; `/speckit.specify` + `/speckit.clarify` resolve them:

1. **Bucket name** — `garrison-company` vs `garrison-knowledge` vs something else. The spike used `garrison-company` as a placeholder.
2. **Object key shape** — flat `company.md` vs `<companyId>/company.md` for forward-compat. Single-company today, but M7 hiring may surface multi-company patterns.
3. **Dashboard ↔ MinIO transport** — direct (dashboard process talks to MinIO with scoped creds) vs supervisor-proxy (supervisor fronts the read/write surface). Tied to Open Q §1 in m5-4-context.md.
4. **Leak-scan analog on Company.md saves** — does the operator-facing save path inherit M2.3 Rule 1 / M5.3 chat-mutation Rule 1 leak-scan? Spike doesn't decide; spec resolves with operator input.
5. **Read-after-edit refresh mechanism** — client-side re-fetch on save success, or server-side return-the-saved-content. Both work; spec picks the cleaner shape.
6. **Backup/restore stance** — explicit ops-doc step, automated supervisor snapshot job, or operator-side `docker run` backup pattern. Out of M5.4 scope; backlog for post-M5.

---

## Implications for the upcoming milestone(s)

### M5.4 — knowledge-base pane

Spike validates the chosen architecture:
- MinIO container is operationally simple: one image, one volume, two env vars, one port.
- Bucket bootstrap is a 5-line supervisor startup hook.
- Scoped service-account credentials integrate cleanly with the existing M2.3 vault path.
- ETag-based optimistic concurrency is a 2-line addition to the dashboard save action and prevents silent multi-window overwrites.
- Go SDK is mature and lighter than the AWS S3 SDK alternative.

No spike findings invalidate the M5.4 architecture as documented in `specs/_context/m5-4-context.md`. Proceed to `/speckit.specify`.

### M6 — CEO ticket decomposition

If M6 (or later) chooses to wire Company.md into the chat prompt context (Option A in m5-4-context.md "What this milestone is NOT"), the supervisor read-path is the same one M5.4 builds. No additional spike needed at M6 time.

### Post-M5 — multi-document knowledge base

If the operator's knowledge base outgrows a single Company.md, MinIO supports it natively (multiple objects in the same bucket, prefix-based listing). The bucket-name + key-shape decisions in the spec should be forward-compatible with this growth path; e.g. `<companyId>/company.md` lets a future `<companyId>/<docName>.md` slot in without a bucket migration.
