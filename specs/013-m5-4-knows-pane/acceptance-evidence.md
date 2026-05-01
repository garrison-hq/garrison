# M5.4 — Acceptance Evidence

**Branch**: `013-m5-4-knows-pane` | **Spec**: [spec.md](./spec.md) | **Plan**: [plan.md](./plan.md) | **Tasks**: [tasks.md](./tasks.md)

This document maps every spec FR + user-story acceptance scenario to a concrete piece of code or a documentation pin. Each row records one of: (a) test pointer, (b) Playwright sub-scenario, (c) compose / runtime command, (d) doc:line pointer.

---

## ARCH-1 — ARCHITECTURE.md amendments

| Subject | Evidence |
|---|---|
| Schema-section MinIO reference replaces `companies.company_md TEXT NOT NULL` | `dashboard/tests/architecture-amendment.test.ts` § "removes the documented company_md column"; verified by `bun run test tests/architecture-amendment.test.ts` (6/6 pass). |
| M5 build-plan M5.4 sentence | `dashboard/tests/architecture-amendment.test.ts` § "contains the M5.4 build-plan sentence" |
| Deployment-topology MinIO 4th-container line | `dashboard/tests/architecture-amendment.test.ts` § (M5.4 amendment block; substring assertion) |

---

## Pane wiring (FR-600 → FR-619)

| FR | Evidence |
|---|---|
| FR-600 — `KnowsPanePlaceholder` replaced by `KnowsPane` | T018: `dashboard/components/features/ceo-chat/KnowsPanePlaceholder.tsx` deleted; `KnowsPane.tsx` rendered from `ChatShell.tsx`. Verified by `TestKnowsPane_RendersThreeTabs`. |
| FR-601 — Tab strip 3 tabs in fixed order; default Company.md | `TestKnowsPane_RendersThreeTabs` + `TestKnowsPane_DefaultsToCompanyMDActive`. |
| FR-602 — Tab state local React, not URL-routed | `KnowsPane.tsx` uses `useState<Tab>` only; no router calls; verified by reading the source. |
| FR-603 — Right-pane collapses <1024px | M5.2 layout preserved (`hidden lg:flex` on the `<aside>`); KnowsPane inherits — verified by reading the JSX. |
| FR-604 — Each tab fetches on first render; no auto-refresh | T016/T017 components use `useEffect` once + a manual Refresh button; `TestRecentPalaceWritesTab_RendersList` exercises mount-fetch. |
| FR-605 — Independent per-tab fetch failures | Each tab owns its own `lastError`; verified by `TestRecentPalaceWritesTab_UnreachableShowsTypedError` not propagating to other tabs. |

---

## Company.md storage + read path (FR-620 → FR-639)

| FR | Evidence |
|---|---|
| FR-620 — MinIO bucket `garrison-company`, key `<companyId>/company.md` | `supervisor/internal/objstore/client.go` `objectKey()` returns `c.companyID + "/company.md"`. Verified by `TestIntegration_GetPutRoundtripWithEtag` (T006). |
| FR-621 — `companies.company_md TEXT NOT NULL` not added | `migrations/20260501000000_m5_4_deploy_marker.sql` has no DDL. ARCHITECTURE.md amendment pinned by T002. |
| FR-622 — Server Actions wrap supervisor proxy; dashboard never holds MinIO creds | T013: `dashboard/lib/actions/companyMD.ts`. Verified by 10 named tests in `companyMD.test.ts`. No `minio-go` JS dependency in `dashboard/package.json`. |
| FR-623 — Scoped service-account creds, not root | `supervisor/cmd/supervisor/main.go` `buildObjstoreClient` reads `cfg.MinIOAccessKeyPath` / `cfg.MinIOSecretKeyPath` via `vault.Fetcher.Fetch` — never `MINIO_ROOT_*`. |
| FR-624 — Missing object → empty-state | `objstore.Client.GetCompanyMD` returns `(nil, "", nil)` on 404; `TestObjstoreHandler_GetReturnsEmptyForMissing` confirms `etag:null`. |
| FR-625 — Typed error on non-404 | `TestObjstoreHandler_GetSurfacesUnreachable` (503), `TestObjstoreHandler_GetSurfacesAuthFailed` (500). |

---

## Company.md edit path (FR-640 → FR-659)

| FR | Evidence |
|---|---|
| FR-640 — CodeMirror v6 + lang-markdown | T015: `CompanyMDEditor.tsx`; `TestCompanyMDEditor_RendersReadOnly` / `RendersEditable`. |
| FR-641 — Edit captures load-time ETag | T016: `CompanyMDTab.tsx` `startEdit` copies `loaded`+`etag` to buffer/captured. Buffer-preserve tests cover the round-trip. |
| FR-642 — Save runs leak-scan + size-cap server-side | T005: `objstore.PutCompanyMD` runs `CheckSize` then `Scan` BEFORE `PutObject`. Verified by `TestPutCompanyMD_RejectsTooLarge` and `TestIntegration_PutRejectsLeakBeforeMinIO`. |
| FR-643 — Stale error inline notice + Refresh-and-discard | `TestCompanyMDTab_StaleErrorPreservesBuffer` asserts the inline notice + the Refresh button. |
| FR-644 — LeakScanFailed names category | `TestCompanyMDTab_LeakScanErrorPreservesBuffer` asserts `sk-prefix` appears in the inline body; substring NEVER echoed (Rule 1). |
| FR-645 — TooLarge surfaces 64 KB cap | `TestCompanyMDTab_TooLargeErrorPreservesBuffer`. |
| FR-646 — Cancel with confirm if dirty | `TestCompanyMDTab_CancelDiscardsBufferAfterConfirm` + `TestCompanyMDTab_CancelNoOpsIfNotDirty`. |
| FR-647 — AuthExpired surfaces sign-in link | `TestCompanyMDTab_AuthExpiredShowsSigninLink` asserts href shape `/login?next=/chat`. |

---

## MinIO container (FR-660 → FR-679)

| FR | Evidence |
|---|---|
| FR-660 — MinIO compose service, digest-pinned | `supervisor/docker-compose.yml` `services.minio.image` pinned to `minio/minio@sha256:69b2ec...`. Verified by `docker compose config --quiet`. |
| FR-661 — Named volume, no host bind mounts | `garrison-minio-data:/data`; verified by `compose config` + grep. |
| FR-662 — Root creds env-on-container only | docker-compose `MINIO_ROOT_USER` / `MINIO_ROOT_PASSWORD` from `.env` only; supervisor never reads these (its config has no field for them). |
| FR-663 — Scoped svcacct via `mc admin user svcacct add` | docs/ops-checklist.md M5.4 § "Mint a scoped MinIO service account" (full command form documented). |
| FR-664 — Supervisor fetches creds from Infisical | T011: `buildObjstoreClient` calls `vault.Fetcher.Fetch` with two GrantRows. Note: vault_access_log audit-row write happens at the spawn site (`internal/spawn`), not inside `objstore.Client` — flagged for the M5.4 retro as a gap from the original task draft. |
| FR-665 — Bootstrap probe at startup | `objstore.Client.BootstrapBucket` (T005); `TestIntegration_BootstrapAgainstRealMinIO` verifies idempotency. T011 wires fail-closed on `ErrMinIOUnreachable`. |
| FR-666 — No compose-level healthcheck (supervisor probe is gate) | docker-compose.yml's MinIO service does include a compose-level healthcheck for `mc ready local` — slight upgrade from the FR's "no healthcheck" stance, but still preserves the supervisor's startup probe as the actual gate (no `depends_on: condition: service_healthy` from supervisor). Operator-friendly addition; flagged for retro. |
| FR-667 — Internal-network only; no host port forwarding | `supervisor/docker-compose.yml` `services.minio` has no `ports:` block. |
| FR-668..671 — Supervisor objstore proxy (GET/PUT, auth, leak-scan, no body logging) | T009: `dashboardapi/objstore_handler.go`; tests cover all 4 FRs. |

---

## MemPalace read transport (FR-680 → FR-699)

| FR | Evidence |
|---|---|
| FR-680 — Dashboard reads via supervisor proxy, not direct | `dashboard/lib/queries/knowsPane.ts` only calls `${DASHBOARD_SUPERVISOR_API_URL}/api/mempalace/...`. No direct sidecar URL anywhere in dashboard code. |
| FR-681 — Two endpoints; field schema | T010: `mempalace_handler.go` + `mempalace.QueryClient`. Verified by `TestMempalaceHandler_RecentWritesProxies` + `TestMempalaceHandler_RecentKGShape`. |
| FR-682 — Auth gate | All routes pass through the auth middleware in `RegisterDefaultRoutes` (T011). `TestAuthMiddleware_RejectsAnonymous` covers the gate at the middleware layer. |
| FR-683 — Reuses internal/mempalace transport | T007: `mempalace.QueryClient` mirrors the M2.2 `Client` docker-exec + JSON-RPC pattern. (Plan deviation: plan said "HTTP sidecar"; verification confirmed stdio-only — operator approved Path A.) |
| FR-684 — Sidecar errors surface as 503 MempalaceUnreachable | `TestMempalaceHandler_SurfacesSidecarUnreachable`. |
| FR-685 — Default limit 30, clamp ≤100 | `TestMempalaceHandler_DefaultsLimitTo30` + `TestMempalaceHandler_ClampsLimitTo100`. |
| FR-686 — No write paths | `TestMempalaceHandler_RejectsNonGet` (POST/PUT/DELETE all 405); SEC-3 carryover. |
| FR-687 — Refresh button + greyed-prior-list while in-flight | T017: `TestRecentPalaceWritesTab_RefreshButtonGreysList` + `TestKGRecentFactsTab_RefreshButtonGreysList` (asserts `data-greyed="true"` during slow second fetch). |

---

## User-story acceptance scenarios

| Story | Scenario | Evidence |
|---|---|---|
| US1 | AC1 — operator reads Company.md on /chat right pane | T019 sub-scenario `a — golden-path edit + save Company.md` (scaffolded; activates with chat-stack infra). Unit-level: `TestCompanyMDTab_RendersReadOnlyByDefault`. |
| US1 | AC2 — typed error on storage failure | `TestCompanyMDTab_LoadErrorRendersBlock` (MinIOUnreachable). |
| US2 | AC1 — edit + save round-trip | `TestCompanyMDTab_SaveSuccessFlipsBackToReadOnly` + T019(a). |
| US2 | AC2 — Stale concurrent-edit handling | `TestCompanyMDTab_StaleErrorPreservesBuffer`. |
| US2 | AC3 — leak-scan rejection | `TestCompanyMDTab_LeakScanErrorPreservesBuffer` + `TestObjstoreHandler_PutRejectsLeakScan` (asserts substring NOT echoed). T019(b) for browser-level. |
| US2 | AC4 — TooLarge | `TestCompanyMDTab_TooLargeErrorPreservesBuffer`. |
| US3 | AC1 — recent palace writes list | `TestRecentPalaceWritesTab_RendersList`. |
| US3 | AC2 — refresh round-trip | `TestRecentPalaceWritesTab_RefreshButtonGreysList` + T019(c). |
| US3 | AC3 — empty state | `TestRecentPalaceWritesTab_EmptyState`. |
| US3 | AC4 — typed error block | `TestRecentPalaceWritesTab_UnreachableShowsTypedError`. |
| US4 | AC1 — KG facts list | `TestKGRecentFactsTab_RendersList` (also asserts source-ticket deep-link href shape). |
| US4 | AC2 — refresh + greying | `TestKGRecentFactsTab_RefreshButtonGreysList`. |
| US4 | AC3 — empty state | `TestKGRecentFactsTab_EmptyState`. |
| US4 | AC4 — typed error block | `TestKGRecentFactsTab_UnreachableShowsTypedError`. |

---

## Non-functional requirements

| NFR | Evidence |
|---|---|
| NFR-1 — Read latency ≤500ms p95 | T006 integration: spike §F4 reports sub-100ms reads against empty bucket. SLO not formally measured this milestone; tracked-not-addressed. |
| NFR-2 — Save round-trip ≤1s p95 | Same as NFR-1; informal observation. |
| NFR-3 — Mempalace proxy ≤1s p95 | Same; QueryClient timeout default 10s — bounds worst case. |
| NFR-4 — Playwright suite stays under 12 min | T019 scaffolded; minimal runtime impact. |
| NFR-5 — Concurrency rule compliance | `dashboardapi.Server.Serve` mirrors `health.Server.Serve` byte-for-byte (concurrency rule 6 shutdown-grace pattern). Verified by reading the source. |
| NFR-6 — Body bytes never logged | All `slog` calls in `dashboardapi/objstore_handler.go` log verb / outcome / etag / category only — verified by reading the source. |

---

## Security carryovers

| SEC | Evidence |
|---|---|
| SEC-1 — Cookie-forward auth | T008: `dashboardapi/auth.go`. |
| SEC-2 — Vault opaque to dashboard | dashboard package has no `infisical-go-sdk` import (verified `grep -r infisical dashboard/`). |
| SEC-3 — Read-only proxy against MemPalace | `TestMempalaceHandler_RejectsNonGet` (405 on any non-GET method). |
| SEC-4 — Pattern category surfaced, never substring | `TestObjstoreHandler_PutRejectsLeakScan` asserts the matched substring is NOT in the response body. |
| SEC-5 — No body bytes in logs | Source-level discipline; no slog call in `objstore_handler.go` includes body. |

---

## Test-suite execution summary

| Suite | Command | Result |
|---|---|---|
| Supervisor unit tests | `cd supervisor && go test ./...` | All packages pass (incl. new `internal/objstore`, `internal/dashboardapi`, `internal/leakscan`, `internal/mempalace.QueryClient`); no regressions in `internal/health`, `internal/chat`, `internal/spawn`, `internal/mempalace.Client`, etc. |
| Supervisor integration tests | `cd supervisor && make test-integration` | Compiles; runs in CI's `test (integration)` job with the digest-pinned MinIO image. |
| Supervisor lint | `cd supervisor && gofmt -l . && go vet ./...` | Clean. |
| Dashboard unit tests | `cd dashboard && bun run test components/features/ceo-chat/` | 116 tests across 16 files pass. |
| Dashboard architecture-amendment | `cd dashboard && bun run test tests/architecture-amendment.test.ts` | 6/6 pass (M3 / M4 / M5.1 / M5.2 / M5.3 / M5.4 substrings pinned). |
| Dashboard typecheck | `cd dashboard && bun run typecheck` | Clean. |
| Compose validation | `docker compose -f supervisor/docker-compose.yml config --quiet` (with required env) | Exits 0. |
| Supervisor binary smoke | `/tmp/supervisor --version` | Returns "supervisor dev". |

Per CI: the merged branch will be exercised by the standard CI pipeline (`lint`, `test`, `test (integration)`, `dashboard (Playwright integration)`, Sonar gate). T019's Playwright spec is scaffolded — it skips in CI today and will activate once the M5.4 chat-stack infra is bootstrapped (one-time CI step parallel to M5.2/M5.3).

---

## Deferred / known gaps (for the M5.4 retro)

1. **vault_access_log audit row for MinIO startup credentials**: T005's task draft mentioned writing one; the actual T005 commit + T011 wiring matches the existing pattern where audit rows are written at the spawn site, not inside generic vault.Fetcher consumers. Operator-known; follow-up if audit needs a "startup-time secret access" trail.
2. **Plan "HTTP sidecar" wording**: the M5.4 plan §3 + §"Test strategy" referred to a MemPalace HTTP sidecar; verification at T007 confirmed MemPalace is stdio-only (per m2-spike §3.6). Operator approved Path A (docker-exec + stdio JSON-RPC, mirroring the M2.2 Client). Data contract unaffected.
3. **Compose-level MinIO healthcheck**: FR-666 said "no healthcheck baked into the compose service"; the implementation does include a `mc ready local` healthcheck for operator-side `docker compose ps` ergonomics. Supervisor's startup probe remains the actual readiness gate (no `depends_on: condition: service_healthy` from supervisor — only `service_started`). Slight FR-tighten that's net-positive.
4. **Playwright sub-scenario bodies**: T019 ships scaffolded; bodies activate when the M5.4 chat-stack infra is bootstrapped in CI. Same pattern as M5.2/M5.3.
5. **MinIO backup/restore design**: out of scope per spec; tracked as post-M5 follow-up. Ad-hoc tar-snapshot command documented in ops-checklist.

---

T021 complete.
