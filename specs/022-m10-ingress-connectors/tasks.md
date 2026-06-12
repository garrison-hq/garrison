# M10 tasks — Ingress connectors (the company becomes reactive to the outside world)

**Branch**: `022-m10-ingress-connectors` | **Plan**: [plan.md](./plan.md) | **Spec**: [spec.md](./spec.md) | **Context**: [m10-context.md](../_context/m10-context.md) | **M9 retro**: [m9.md](../../docs/retros/m9.md) | **M8 retro**: [m8.md](../../docs/retros/m8.md)

18 tasks total (T001–T018). Executed linearly by a solo operator. Each task is one Claude Code session in scope and produces a reviewable commit. The repo is in a working state after every task. No new Go or TypeScript dependencies (SC-009, SR4, spike F5).

Risk callouts carried forward from the M9 retro (woven into specific tasks below):

- **Threat-model precedes connector code (FR-800)**: T001 commits `docs/security/ingress-threat-model.md` and **nothing in `internal/ingress/` may exist before T001 is committed** — asserted by the acceptance script's git-log check (the M9 SC-007 pattern). T002 may land alongside T001 in the same commit or immediately after, since the migration is schema, not connector code.
- **Migration version checked at plan time** (M9 retro gotcha 5): `20260612000000_m10_ingress_connectors.sql` — no collision with `20260610000002_m9_scheduled_wakeups.sql`. Verified.
- **Rate cap before delivery-row insert** (plan resolution R1, FR-602): a 429'd delivery writes no `ingress_deliveries` row so a later legitimate redelivery dedups correctly. This is the spec-faithful ordering; do not reorder to match SR6's prose literally.
- **Provenance lands in `tickets.metadata`**, not a literal `args_jsonb` column (plan resolution R2): the `tickets` table's JSONB column is `metadata`; keys `ingress_connector`, `external_id`, `external_url`.
- **Bad-signature-rejection count is an in-process counter** (plan resolution R3, FR-301/FR-702): rejected deliveries write no attacker-inflatable row; the count is a process-local atomic exposed via a cookie-auth `GET /ingress/status` on the dashboard-api port (8081), never on the public webhook port (8082).
- **Drizzle-pull empty-default mangling** (M7/M8/M9 gotcha): T002 includes the explicit `bunx tsc --noEmit` step + inline TS fix if it fires.
- **sqlc `sqlc.arg(name)` exclusively** in `m10_ingress.sql` (M7 gotcha).
- **Coverage clearance is its own step** (M6 retro gotcha 7): T017 runs the new-code coverage probe and tops up Go-side tests to ≥82% before the PR push, plus Sonar new-issues pre-clearance (M8/M9 T020 pattern).
- **Lint locally before every push**: `gofmt -l .` + `go vet ./...` from `supervisor/` (standing memory `feedback_lint_before_push`).
- **Any future finalize sibling must extend `FinalizeDeps.ToolName`** (M9 retro post-ship finding 7): M10 adds no new finalize tool; no touch required. Note carried so the next milestone author sees the seam.

Zero new Go dependencies. Zero new TypeScript dependencies.

---

## Ordering principle

T001 commits the threat model — the binding prerequisite before any connector code lands in git history. T002 lands the migration + sqlc + drizzle pull; every later Go task references the new schema. T003 (config env vars) and T004 (connector interface + signature verification) are foundations the handler and GitHub connector build on. T005 (GitHub connector filter/map) and T006 (idempotency + render) assemble the handler's data path. T007 (rate cap + throttle evidence) adds the cap before the ticket insert (FR-602). T008 (ingress.Server lifecycle + main.go wiring) closes the supervisor integration and makes the end-to-end path runnable.

T009–T012 are the unit test suites covering signature, render, GitHub connector, handler (httptest), and rate cap; each is self-contained per source file. T013 is the integration suite (testcontainers-go) covering the golden path, serial redelivery, security cases, dept-budget accounting, vault fail-closed, and the migration roundtrip. T014 is the chaos suite (concurrent-redelivery race + burst-cap bounded-fan-out). T015 ships the dashboard surfaces (TicketCard chip, ticket detail external link, connector-status page). T016 amends ARCHITECTURE.md + ops-checklist. T017 runs the scripted acceptance walk (all SC-001..SC-009) plus coverage probe and Sonar pre-clearance. T018 writes the retro.

---

## Phase 0 — Prerequisites (before connector code)

- [x] **T001** Threat model — `docs/security/ingress-threat-model.md` (committed before any file in `internal/ingress/`)
  - **Depends on**: M9 shipped (branch `022-m10-ingress-connectors` off `main`).
  - **Files**: `docs/security/ingress-threat-model.md` (new).
  - **Completion condition**: `ingress-threat-model.md` is committed and covers the eight FR-800-required areas: (1) raw-body-before-parse + timing-safe-comparison + fail-closed on missing/bad signature (FR-300, SR1); (2) idempotency-key forgery and replay, including the M1 concurrent-delivery race and why a pre-check SELECT does not survive it (FR-201, SR2); (3) payload-size and rate-bomb DoS with the per-connector cap as the structural answer and the `maxBodyBytes = 26 MB` LimitReader guard (FR-600, FR-800); (4) injection of attacker-controlled text into ticket bodies an agent later reads, with the provenance tag (`ingress_connector` / `external_id` / `external_url` in `tickets.metadata`) as the security control and the origin chip as the operator-observable signal (FR-501, FR-502); (5) connector-credential handling in the vault — webhook secret at path `ingress/github/webhook_secret`, never in agent context, fail-closed if vault unavailable at boot (FR-302, M2.3 Rule 2); (6) the trust-boundary-inversion risk of mounting webhook routes on the cookie-auth dashboard mux, and why a separate port (8082) is the answer (FR-103, SR7); (7) bounded blast radius of a compromised connector — can create tickets, cannot spawn agents directly, cannot mutate config, cannot reach vault, cannot act externally (FR-303, FR-700); (8) IP allow-list via GitHub `GET /meta` as optional defense-in-depth, not a gating control (spike F8). No code in this commit.
  - **Out of scope for this task**: any file under `internal/ingress/` (T003+); migration (T002); ARCHITECTURE.md annotation (T016); retro (T018).

- [x] **T002** Goose migration `20260612000000_m10_ingress_connectors.sql` + sqlc query file + drizzle pull
  - **Depends on**: T001.
  - **Files**: `supervisor/cmd/supervisor/migrations/20260612000000_m10_ingress_connectors.sql` (new — canonical embed location; `make copy-migrations` mirrors to `migrations/`); `migrations/queries/m10_ingress.sql` (new); `supervisor/sqlc.yaml` (extend — new schema file + queries file); `supervisor/internal/store/m10_ingress.sql.go` + `models.go` (sqlc-generated); `dashboard/drizzle/schema.supervisor.ts` (regenerated via `bun run drizzle:pull`).
  - **Completion condition**: migration applies via `goose up` against a fresh testcontainer Postgres and reverses via `goose down`. Up section covers: `ingress_deliveries` table (`id UUID PRIMARY KEY DEFAULT gen_random_uuid()`, `connector_id TEXT NOT NULL`, `external_delivery_id TEXT NOT NULL`, `ticket_id UUID NULL REFERENCES tickets(id)`, `created_at TIMESTAMPTZ NOT NULL DEFAULT now()`, `UNIQUE (connector_id, external_delivery_id)`, `idx_ingress_deliveries_connector_created` index on `(connector_id, created_at DESC)` for status reads); `throttle_events_kind_check` DROP and re-add adding `'ingress_rate_cap_exceeded'` to the existing three-value set `{company_budget_exceeded, rate_limit_pause, dept_weekly_ticket_budget_exceeded}` (M8 pattern); `GRANT SELECT ON ingress_deliveries TO garrison_dashboard_app`. Down section removes the M10 `throttle_events` rows before restoring the CHECK, then drops `ingress_deliveries`. `migrations/queries/m10_ingress.sql` carries: `InsertIngressDelivery :one` (INSERT RETURNING id; `23505` is the dedup signal), `BackfillIngressDeliveryTicket :exec` (UPDATE SET ticket_id), `InsertIngressTicket :one` (INSERT with `origin='ingress'`, `column_slug='todo'`, `metadata` jsonb_build_object with `ingress_connector`/`external_id`/`external_url`; RETURNING id, created_at), department-slug → id resolution reuses the existing `SelectDepartmentIDBySlug` from `tickets.sql` (do NOT add a duplicate query to `m10_ingress.sql`), `GetConnectorStatus :one` (last `created_at` + accepted count from `ingress_deliveries`; rate-cap breach count from `throttle_events WHERE kind='ingress_rate_cap_exceeded'`). All queries use `sqlc.arg(name)` style. `make sqlc` + `go build ./...` succeed; `bunx tsc --noEmit` from `dashboard/` passes (inline-fix any empty-default mangling). `TestM10MigrationRoundtrip` in `supervisor/internal/store/migrate_integration_test.go` asserts apply → rollback → apply fingerprint stability and asserts pre-existing `throttle_events` rows with the M8 three-value set `{company_budget_exceeded, rate_limit_pause, dept_weekly_ticket_budget_exceeded}` satisfy the extended CHECK both before and after (M9 added no throttle kind).
  - **Out of scope for this task**: any caller code in `internal/ingress/` (T003+); dashboard components (T015); ARCHITECTURE.md annotation (T016).

---

## Phase 1 — Framework foundations

- [x] **T003** `internal/config/` — ingress env vars
  - **Depends on**: T002.
  - **Files**: `supervisor/internal/config/config.go` (extend — `IngressPort`, `IngressGitHubEnabled`, `IngressGitHubConnectorID`, `IngressGitHubDepartment`, `IngressGitHubRatePerMin`, `IngressGitHubBurst`); `supervisor/internal/config/config_test.go` (extend).
  - **Completion condition**: `GARRISON_INGRESS_PORT` (int, default `8082`, reject `< 1`), `GARRISON_INGRESS_GITHUB_ENABLED` (bool, default `false`), `GARRISON_INGRESS_GITHUB_CONNECTOR_ID` (string, default `"github-sortie"`), `GARRISON_INGRESS_GITHUB_DEPARTMENT` (string, required when enabled, reject empty), `GARRISON_INGRESS_GITHUB_RATE_PER_MIN` (int, default `60`, reject `< 1`), `GARRISON_INGRESS_GITHUB_BURST` (int, default `30`, reject `< 1`) parse per the existing env-var patterns. Webhook secret is NOT an env var (vault-fetched at boot, T008). Tests pass: `TestConfigIngressDefaults`, `TestConfigIngressOverrides`, `TestConfigIngressRejectsZeroPort`, `TestConfigIngressRejectsEmptyDepartmentWhenEnabled`. `gofmt -l` + `go vet` clean.
  - **Out of scope for this task**: main.go consumption (T008); vault fetch (T008).

- [x] **T004** `internal/ingress/connector.go` + `signature.go` — connector interface + HMAC verification
  - **Depends on**: T002.
  - **Files**: `supervisor/internal/ingress/connector.go` (new — `Connector` interface with `ID`, `EventType`, `Subscribed`, `DeliveryID`, `VerifySignature`, `Filter`, `Map`; `FilterDecision` enum `FilterAccept`/`FilterDiscard`; `TicketDraft` struct; sentinel errors `ErrBadSignature`, `ErrMalformedDelivery`, `ErrNoMapping`, `ErrRateCapExceeded`, `ErrDuplicateDelivery`); `supervisor/internal/ingress/signature.go` (new — `verifyGitHubSignature(rawBody []byte, header string, secret []byte) error` using `crypto/hmac`, `crypto/sha256`, `crypto/subtle.ConstantTimeCompare`, `encoding/hex`).
  - **Completion condition**: package compiles; `verifyGitHubSignature` is the only exported-or-package-visible path to HMAC verification in the package; sentinel errors are defined; the `Connector` interface signature matches the plan §"connector abstraction shape." `gofmt -l` + `go vet` clean. No tests in this task (unit tests land in T009/T010).
  - **Out of scope for this task**: GitHub connector implementation (T005); handler (T006); unit tests (T009).

- [x] **T005** `internal/ingress/github.go` — GitHub connector Filter + Map + config struct
  - **Depends on**: T003, T004.
  - **Files**: `supervisor/internal/ingress/github.go` (new — `GitHubConfig` struct with `ConnectorID`, `Secret []byte`, `Routes map[string]Route`, `RatePerMin`, `Burst`; `Route` struct; `GitHubConnector` implementing `Connector`; `Filter` enforcing bot-sender + action-subtype + ping rules in the SR6 order); `supervisor/internal/ingress/render.go` (new — `renderTemplate(tmpl string, vars map[string]string) string` using `strings.ReplaceAll`, null-safe fallback literal `"(no description provided)"`).
  - **Completion condition**: `GitHubConnector.Filter` applies the SR6 step-3–4 rules in order: ping → `FilterDiscard`; `sender.type == "Bot"` or `sender.login` ending `[bot]` → `FilterDiscard`; `issues` action not in `{"opened","reopened"}` → `FilterDiscard`; `pull_request` action not `"review_requested"` → `FilterDiscard`; all others → `FilterAccept`. `GitHubConnector.Map` renders `issues` (objective from `issue.title`/`issue.html_url`, acceptance from `issue.body` with null-safe `"(no description provided)"` fallback, `ExternalID` = string coercion of `issue.id` via `strconv.FormatInt` or `json.Number.String()` — `issue.id` is a JSON integer, do not assign an `int64` directly to `ExternalID string`, `ExternalURL = issue.html_url`) and `pull_request` (from `pull_request.title`, `pull_request.html_url`, `pull_request.body` null-safe, `ExternalID` = string coercion of `pull_request.id`, `ExternalURL = pull_request.html_url`); `ErrNoMapping` on unrecognized event type. `renderTemplate` substitutes `{{title}}`, `{{url}}`, `{{body}}`, `{{number}}`, `{{sender}}` only; absent keys substitute their fallback literal without error. Package compiles. `gofmt -l` + `go vet` clean. No tests in this task (unit tests land in T010).
  - **Out of scope for this task**: idempotency (T006); rate cap (T007); handler (T006); unit tests (T010).

---

## Phase 2 — Handler + integration

- [x] **T006** `internal/ingress/idempotency.go` + `handler.go` (success path, no rate cap yet)
  - **Depends on**: T004, T005.
  - **Files**: `supervisor/internal/ingress/idempotency.go` (new — `insertDelivery(ctx, q, connID, deliveryID) (pgtype.UUID, error)` detecting `23505` via `*pgconn.PgError` → `ErrDuplicateDelivery`); `supervisor/internal/ingress/handler.go` (new — the SR6 pipeline through step 9, minus the rate-cap step 7, using `io.LimitReader` with `maxBodyBytes = 26_214_400` for raw body capture, `sync/atomic` rejection counter, writing the tx: InsertIngressDelivery → `23505` → rollback 200; InsertIngressTicket → BackfillIngressDeliveryTicket → COMMIT → 202).
  - **Completion condition**: the handler pipeline runs steps 1–6 and 8–9 of the plan §handler-pipeline (rate cap at step 7 is a no-op pass-through until T007). On a valid `issues:opened` POST with a correctly signed body, one `tickets` row and one `ingress_deliveries` row with backfilled `ticket_id` land in the DB, the existing `emit_ticket_created` trigger fires, and the handler returns 202. A `23505` on the delivery insert aborts before any ticket insert (decision 4). If `InsertIngressTicket` or COMMIT fails for any reason other than `23505` (already handled), the tx is rolled back and the handler returns 500 with the error logged — no partial state is committed. Bad or missing signature → 401; rejection counter incremented. Absent `X-GitHub-Event` or `X-GitHub-Delivery` → 200 or 400 per the pipeline. Package compiles; the binary (`go build ./...`) succeeds. `gofmt -l` + `go vet` clean.
  - **Out of scope for this task**: rate cap wiring (T007); ingress.Server lifecycle (T008); unit tests (T011); integration tests (T013).

- [x] **T007** `internal/ingress/ratecap.go` + `internal/throttle/ingress.go` — per-connector token bucket + M6 evidence
  - **Depends on**: T002, T006.
  - **Files**: `supervisor/internal/ingress/ratecap.go` (new — `RateCap` struct: `map[string]*bucket` + mutex; `bucket` with `tokens`, `lastRefill`, `capacity`, `refillPerNano`; `Allow(connectorID string) bool` refills by elapsed wall-clock, consumes one token, returns true/false; no goroutine, no ticker; deterministic-clock seam for tests); `supervisor/internal/throttle/ingress.go` (new — `FireIngressRateCap(ctx, q, companyID, connectorID, ratePerMin, burst) error` inserting `throttle_events` with `kind='ingress_rate_cap_exceeded'` + JSON payload `{connector_id, rate_per_minute, burst}` + `work.throttle.event` notify, reusing the existing `insertEventAndNotify` function); `supervisor/internal/ingress/handler.go` (extend — wire rate-cap step 7: `if !rateCap.Allow(conn.ID()) { FireIngressRateCap(...); return 429 }`); `supervisor/internal/throttle/throttle.go` or constants file (extend — `KindIngressRateCapExceeded = "ingress_rate_cap_exceeded"`).
  - **Completion condition**: step 7 of the handler pipeline is active — over-cap delivery returns 429 with no ticket and no `ingress_deliveries` row (FR-602); the breach writes a `throttle_events` row + `work.throttle.event` notify (FR-601). `Allow` is per-connector (two connectors have independent buckets). `FireIngressRateCap` receives `companyID pgtype.UUID` — the ingress package resolves `companyID` once at boot via a single-company DB lookup (`SELECT id FROM companies LIMIT 1`, the same pattern M6 uses in its throttle boot path), storing it in the `Deps` struct so the handler can pass it without a per-request query. The binary compiles. `gofmt -l` + `go vet` clean.
  - **Out of scope for this task**: ingress.Server lifecycle (T008); unit tests (T012); chaos burst test (T014).

- [ ] **T008** `internal/ingress/server.go` + `internal/ingress/config.go` (vault fetch) + `cmd/supervisor/main.go` wiring
  - **Depends on**: T003, T006, T007.
  - **Files**: `supervisor/internal/ingress/server.go` (new — `ingress.Server` mirroring `dashboardapi.Server`: `NewServer(cfg, deps) (*Server, error)` builds the mux, registers `POST /webhook/github`, fetches the vault secret at construction (fail-closed per FR-302), returns the wired server; `Serve(ctx) error` using `ListenAndServe` + `Shutdown` with `context.WithoutCancel` + `ShutdownGrace`); `supervisor/internal/ingress/config.go` (new — `Deps` struct carrying `*store.Queries`, `*pgxpool.Pool`, vault client seam, `RateCap`, `*sync/atomic.Int64` rejection counter, and `CompanyID pgtype.UUID` (resolved once at boot via single-company DB lookup before `NewServer` is called, the M6 pattern — passed to `FireIngressRateCap` in the handler so no per-request company query is needed); builds `GitHubConnector` from `config.Config` + vault-fetched secret); `supervisor/cmd/supervisor/main.go` (extend — build `ingress.Server` when `cfg.IngressGitHubEnabled` is true (skip when false, mirroring the `buildDashboardAPIServer` pattern); `g.Go(func() error { return ingressServer.Serve(gctx) })` in the existing errgroup); `supervisor/Dockerfile` (extend — add `EXPOSE 8082`); `docker-compose.yml` (extend — publish port 8082 for the webhook surface).
  - **Completion condition**: supervisor boots cleanly with `GARRISON_INGRESS_GITHUB_ENABLED=false` (default — no ingress server built). With `GARRISON_INGRESS_GITHUB_ENABLED=true` + a reachable vault containing `ingress/github/webhook_secret`, the ingress server starts on port 8082 and a valid POST to `/webhook/github` creates a ticket end-to-end. With `GARRISON_INGRESS_GITHUB_ENABLED=true` + unreachable vault, `main.go` returns `ExitFailure` (FR-302). SIGTERM gracefully shuts down both the ingress server and the supervisor. The supervisor's existing startup/shutdown integration tests pass unchanged (M1 regression). `gofmt -l` + `go vet` clean.
  - **Out of scope for this task**: dashboard surfaces (T015); acceptance script (T017); `GET /ingress/status` endpoint (T015).

---

## Phase 3 — Unit test suites

- [ ] **T009** Unit tests — `internal/ingress/signature_test.go` + `internal/ingress/render_test.go`
  - **Depends on**: T004, T005.
  - **Files**: `supervisor/internal/ingress/signature_test.go` (new); `supervisor/internal/ingress/render_test.go` (new).
  - **Completion condition**: the following tests pass:
    - `TestVerifyGitHubSignature_Valid` — golden body + secret + precomputed `sha256=` hex digest → no error.
    - `TestVerifyGitHubSignature_Mismatch` — wrong digest → `ErrBadSignature`.
    - `TestVerifyGitHubSignature_MissingPrefix` — header without `sha256=` → `ErrBadSignature`.
    - `TestVerifyGitHubSignature_EmptyHeader` — missing header → `ErrBadSignature` (fail-closed, FR-300).
    - `TestVerifyGitHubSignature_RawBodyExact` — verifies over exact captured bytes; re-encoded body fails (spike F1 edge case).
    - `TestRender_AllVars` — every `{{var}}` substitutes with the supplied map value.
    - `TestRender_AbsentVarFallback` — a template referencing an absent key substitutes the fallback literal `"(no description provided)"`, no error path.
    - `TestRender_UnknownVarPassthrough` — a `{{unknown}}` token not in the bounded set stays verbatim (no partial substitution surprises).
    All pass. `gofmt -l` + `go vet` clean.
  - **Out of scope for this task**: GitHub connector tests (T010); handler tests (T011).

- [ ] **T010** Unit tests — `internal/ingress/github_test.go`
  - **Depends on**: T005.
  - **Files**: `supervisor/internal/ingress/github_test.go` (new).
  - **Completion condition**: the following tests pass:
    - `TestGitHubFilter_PingDiscarded` — `ping` → `FilterDiscard` (FR-403, SR9).
    - `TestGitHubFilter_BotSenderTypeDiscarded` — `sender.type=="Bot"` → `FilterDiscard` (FR-401, spike F7).
    - `TestGitHubFilter_BotSenderLoginSuffixDiscarded` — `sender.login` ending `[bot]` → `FilterDiscard`.
    - `TestGitHubFilter_IssuesActionGate` — `opened` and `reopened` → `FilterAccept`; `labeled`, `assigned`, `closed`, `edited` → `FilterDiscard` (spike F6.1).
    - `TestGitHubFilter_PullRequestActionGate` — `review_requested` → `FilterAccept`; `opened`, `closed`, `synchronize` → `FilterDiscard` (spike F6.2).
    - `TestGitHubMap_IssueOpened` — populated payload; `TicketDraft` fields match the rendered templates, `ExternalID` and `ExternalURL` set from `issue.id` and `issue.html_url`. Note: `issue.id` is a JSON integer — coerce to string via `strconv.FormatInt` or `json.Number.String()` before assigning to `ExternalID string`; assigning an `int64` directly is a compile error.
    - `TestGitHubMap_NullIssueBody` — null `issue.body` → acceptance field contains `"(no description provided)"`, no error (FR-102, spike QS4).
    - `TestGitHubMap_NoRoute` — event type with no configured route → `ErrNoMapping`.
    - `TestGitHubEventType_MissingHeader` — absent `X-GitHub-Event` → `ok=false`.
    - `TestGitHubDeliveryID_MissingHeader` — absent `X-GitHub-Delivery` → empty string.
    All pass. `gofmt -l` + `go vet` clean.
  - **Out of scope for this task**: handler tests (T011); integration tests (T013).

- [ ] **T011** Unit tests — `internal/ingress/handler_test.go` (httptest, mocked store seam)
  - **Depends on**: T006, T007.
  - **Files**: `supervisor/internal/ingress/handler_test.go` (new).
  - **Completion condition**: the following tests pass using `net/http/httptest` and a minimal stub/mock of the `*store.Queries` seam (or interface):
    - `TestHandler_UnsubscribedEventType_200` — `X-GitHub-Event: push` → 200, no insert call (SR6 step 1, FR-401).
    - `TestHandler_BadSignature_401` — invalid signature → 401, no insert, rejection counter incremented (FR-300, FR-301).
    - `TestHandler_MissingSignature_401` — absent `X-Hub-Signature-256` → 401 (fail-closed, FR-300).
    - `TestHandler_Ping_200_NoTicket` — `ping` → 200, no `InsertIngressDelivery` call, no ticket insert (FR-403, SR9).
    - `TestHandler_BotSender_200_NoTicket` — bot-sourced delivery → 200, no ticket (FR-401).
    - `TestHandler_NonActionableSubtype_200_NoTicket` — `issues:labeled` → 200, no ticket (FR-401).
    - `TestHandler_RateCapExceeded_429_NoRow` — bucket empty → 429, `InsertIngressDelivery` NOT called, `FireIngressRateCap` invoked (FR-600, FR-602).
    - `TestHandler_OversizedBody_Rejected` — body over `maxBodyBytes` truncated by `LimitReader`, signature fails, rejected with 401 (DoS guard, FR-800).
    - `TestHandler_MissingDeliveryID_400` — absent `X-GitHub-Delivery` → 400.
    - `TestHandler_SuccessPath_202` — valid `issues:opened` → 202; `InsertIngressDelivery` and `InsertIngressTicket` each called once in the correct order.
    - `TestHandler_DuplicateDelivery_200` — `InsertIngressDelivery` returns `ErrDuplicateDelivery` → 200, `InsertIngressTicket` NOT called (FR-202).
    All pass. `gofmt -l` + `go vet` clean.
  - **Out of scope for this task**: integration tests against a real Postgres (T013).

- [ ] **T012** Unit tests — `internal/ingress/ratecap_test.go` + `internal/throttle/ingress_test.go`
  - **Depends on**: T007.
  - **Files**: `supervisor/internal/ingress/ratecap_test.go` (new); `supervisor/internal/throttle/ingress_test.go` (new).
  - **Completion condition**: the following tests pass:
    - `TestRateCap_AllowsWithinBurst` — first `Burst` calls return `true`.
    - `TestRateCap_RejectsOverBurst` — the `Burst+1`-th call within the window returns `false`.
    - `TestRateCap_RefillsOverTime` — after injecting elapsed time ≥ one refill interval, tokens return (deterministic-clock seam injected at construction).
    - `TestRateCap_PerConnectorIsolation` — two connector IDs have independent buckets; exhausting one does not affect the other.
    - `TestFireIngressRateCap_WritesEvidence` (integration-tagged, testcontainers) — inserts a `throttle_events` row with `kind='ingress_rate_cap_exceeded'`, correct JSON payload (`connector_id`, `rate_per_minute`, `burst`), fires `work.throttle.event` notify (mirrors `dept_weekly_test.go`).
    All pass. M6 throttle tests pass unchanged (regression). `gofmt -l` + `go vet` clean.
  - **Out of scope for this task**: burst chaos test (T014).

---

## Phase 4 — Integration + chaos suites

- [ ] **T013** Integration test suite — `internal/ingress/integration_test.go` (the milestone smoke tests)
  - **Depends on**: T008.
  - **Files**: `supervisor/internal/ingress/integration_test.go` (new, `-tags=integration`, testcontainers-go Postgres).
  - **Completion condition**: the following tests pass:
    - `TestIngress_IssueOpened_CreatesOneTicket` — POST a signature-valid `issues:opened` to a live `ingress.Server`; assert exactly one `tickets` row with `origin='ingress'`, `column_slug='todo'`, `metadata` carrying `ingress_connector`/`external_id`/`external_url`; one `ingress_deliveries` row with `ticket_id` backfilled; the `work.ticket.created.<dept>.todo` notify observed on a LISTEN conn; handler returns 202 (SC-001, US1-AS1).
    - `TestIngress_PullRequestReviewRequested_CreatesOneTicket` — same for `pull_request:review_requested` (SC-001, US1-AS3).
    - `TestIngress_NullIssueBody_GracefulFallback` — `issue.body` null → ticket created with `"(no description provided)"` fallback; no error (US1-AS4, spike QS4).
    - `TestIngress_SerialRedelivery_NoSecondTicket` — same GUID posted twice serially; second delivery → 200, no second ticket, no second `ingress_deliveries` row, no second notify (FR-202, US2-AS1).
    - `TestIngress_BadSignature_NoTicket_401` — forged signature against the live stack → 401, zero `tickets`, zero `ingress_deliveries` (SC-003, US3-AS1).
    - `TestIngress_BotSender_NoTicket_200` — bot-sourced `issues:opened` → 200, zero tickets (SC-004, US3-AS4).
    - `TestIngress_IngressTicketCountsAgainstDeptBudget` — an ingress ticket created inside an M8 dept-weekly window counts against that budget (FR-603, SC-005-partial; reuses the M8 budget query).
    - `TestIngress_VaultUnavailable_FailsClosed` — construct `ingress.Server` with GitHub enabled + unreachable vault → construction error (FR-302, US3 edge).
    All pass. `gofmt -l` + `go vet` clean.
  - **Out of scope for this task**: concurrent-redelivery race (T014); burst cap test (T014); dashboard (T015).

- [ ] **T014** Chaos test suite — `internal/ingress/chaos_test.go` (concurrent-redelivery race + burst cap)
  - **Depends on**: T013.
  - **Files**: `supervisor/internal/ingress/chaos_test.go` (new, `-tags=chaos`, testcontainers-go Postgres).
  - **Completion condition**: the following tests pass:
    - `TestIngress_ConcurrentRedelivery_RaceYieldsOneTicket` — two goroutines POST the **same** delivery GUID concurrently (start barrier, same shape as M8/M9 chaos tests) while contending on the `UNIQUE (connector_id, external_delivery_id)` insert; assert exactly one `tickets` row and exactly one `ingress_deliveries` row — the `23505` unique-violation is the dedup signal, not a pre-check SELECT; both GUIDs return 2xx (SC-002, US2-AS2).
    - `TestIngress_BurstExceedsCap_BoundedTickets` — drive 2× `Burst` deliveries as fast as possible at a configured low-cap connector; assert: (a) ticket count ≤ `Burst` (fan-out bounded at cap, SC-005); (b) at least one `throttle_events` row with `kind='ingress_rate_cap_exceeded'` written (FR-601); (c) over-cap responses are 429; (d) a subsequent legitimate delivery of a previously-429'd GUID (new unique GUID not in `ingress_deliveries`) processes to one ticket when the bucket is no longer empty (FR-602; composes with idempotency — a GUID rejected with 429 creates no row, so its "redelivery" is fresh).
    All pass. Prior M8/M9 chaos suites pass unchanged (regression). `gofmt -l` + `go vet` clean.
  - **Out of scope for this task**: acceptance script (T017).

---

## Phase 5 — Dashboard surfaces

- [ ] **T015** Dashboard surfaces — ingress-origin chip, ticket detail external link, connector-status page, `GET /ingress/status` endpoint
  - **Depends on**: T008.
  - **Files**: `dashboard/components/features/kanban/TicketCard.tsx` (extend — `ingressOrigin` chip mirroring `scheduledOrigin` chip: when `ticket.ingressOrigin` is truthy, render a chip `gh: <connector>` linking to `ticket.ingressOrigin.externalUrl`, `data-testid="ticket-ingress-origin-chip"`); `dashboard/lib/queries/kanban.ts` (extend — select `origin` + `metadata->>'ingress_connector'` + `metadata->>'external_url'` for the `ingressOrigin` chip); `dashboard/lib/queries/ticketDetail.ts` (extend — expose `metadata.external_url` as the external source link, shown only when `origin = 'ingress'`); `dashboard/app/[locale]/(app)/admin/connectors/page.tsx` (new — force-dynamic, read-only connector-status surface: per connector, last delivery received from `ingress_deliveries`, accepted-count, bad-signature-rejection count from `GET /ingress/status`, current rate-cap state from `throttle_events WHERE kind='ingress_rate_cap_exceeded'`; no CRUD, FR-701); `supervisor/internal/dashboardapi/ingress_status_handler.go` (new — `GET /ingress/status` on the dashboard-api port 8081, cookie-auth identical to existing handlers, returns JSON with per-connector bad-signature rejection count from the in-process atomic counter; no webhook-port exposure, plan resolution R3); `supervisor/internal/dashboardapi/server.go` (extend — route registration for `GET /ingress/status`); `dashboard/components/layout/Sidebar.tsx` (extend — add link to `/admin/connectors` under the admin group, mirroring M9's `/admin/recurring-jobs` entry).
  - **Completion condition**: a kanban ticket with `origin='ingress'` shows the ingress-origin chip distinct from operator / agent / schedule chips; clicking the chip opens the external GitHub URL. Ticket detail shows the external source link when `origin='ingress'`. The connector-status page (`/admin/connectors`) renders per-connector: last delivery received (or "never"), accepted count, bad-signature rejection count (from `GET /ingress/status`), and rate-cap breach count; page is read-only. `GET /ingress/status` returns 401 on unauthenticated request (cookie-auth enforced). `bunx tsc --noEmit` from `dashboard/` passes. `bun run build` (if a build gate exists) passes. Page renders against a seeded dev stack. Per repo convention, no TS/vitest tests land; the Go integration suite pins the row shapes these surfaces read.
  - **Out of scope for this task**: Go-side anything not listed above; acceptance script (T017); retro (T018).

---

## Phase 6 — Docs, acceptance, retro

- [ ] **T016** `docs/ops-checklist.md` M10 section + ARCHITECTURE.md §M10 shipped annotation + pin test
  - **Depends on**: T013 (the behavior being documented is proven).
  - **Files**: `docs/ops-checklist.md` (extend — M10 section: register the GitHub repo/org webhook (URL → `:8082/webhook/github`, content-type `application/json`, secret = vault value at `ingress/github/webhook_secret`, events = `issues` + `pull_request`); set env vars (`GARRISON_INGRESS_GITHUB_ENABLED=true`, `GARRISON_INGRESS_GITHUB_DEPARTMENT`, optional `GARRISON_INGRESS_PORT`, rate-cap env vars); note that secret rotation = supervisor restart; Dockerfile `EXPOSE 8082` + compose publish-port instruction; verify first delivery via connector-status surface at `/admin/connectors`); `ARCHITECTURE.md` (extend — §M10 paragraph annotated "Shipped" with per-thread implementation pointers: ingress package, GitHub connector, idempotency table, rate cap, dashboard surfaces, migration version); `dashboard/tests/architecture-amendment.test.ts` (extend — substring pin for the M10 "Shipped" annotation, M9 T019 pattern).
  - **Completion condition**: amendment pin test passes; ops-checklist section names every new env var, the webhook registration URL, the vault secret path, and the `/admin/connectors` URL. No code changes.
  - **Out of scope for this task**: retro (T018).

- [ ] **T017** Scripted acceptance — `scripts/m10-acceptance.sh` walking SC-001..SC-009 + coverage + Sonar pre-clearance
  - **Depends on**: T001–T016 all complete.
  - **Files**: `scripts/m10-acceptance.sh` (new); patches against earlier tasks' files only if a step fails.
  - **Completion condition**: the script maps each spec success criterion to its verifying suite/step and exits 0 with all steps green:
    - SC-001 → `TestIngress_IssueOpened_CreatesOneTicket` + `TestIngress_PullRequestReviewRequested_CreatesOneTicket` (T013).
    - SC-002 → `TestIngress_ConcurrentRedelivery_RaceYieldsOneTicket` (T014).
    - SC-003 → `TestIngress_BadSignature_NoTicket_401` (T013) + `TestVerifyGitHubSignature_*` (T009).
    - SC-004 → `TestIngress_BotSender_NoTicket_200` (T013) + `TestHandler_Ping_200_NoTicket` + `TestHandler_NonActionableSubtype_200_NoTicket` (T011).
    - SC-005 → `TestIngress_BurstExceedsCap_BoundedTickets` (T014) + `TestIngress_IngressTicketCountsAgainstDeptBudget` (T013). Both tests carry `FR-603` in their descriptions; `TestIngress_IngressTicketCountsAgainstDeptBudget` is the primary FR-603 verifier (ingress tickets count against the dept-weekly budget).
    - SC-006 → operator-attended dashboard walk: navigate to `/admin/connectors`, verify connector-status surface shows last delivery + rejection count + rate-cap state; navigate to kanban, verify ingress-origin chip + external link on a seeded ticket; verify the four origins are distinguishable. Data-layer correctness (`tickets.origin='ingress'`, `metadata` provenance keys) is already asserted by `TestIngress_IssueOpened_CreatesOneTicket` (T013); this step verifies the dashboard surfaces render that data correctly.
    - SC-007 → inbound-only boundary assertion: `git grep -r "github.com/google/go-github\|PostComment\|SendMail\|external_action"` under `supervisor/internal/ingress/` returns empty; no agent MCP verb exposes ingress.
    - SC-008 → `git log --oneline -- docs/security/ingress-threat-model.md supervisor/internal/ingress/` asserts the threat-model commit appears before the first `internal/ingress/` commit (the M9 SC-007 pattern).
    - SC-009 → `go mod tidy` produces no change to `go.mod` / `go.sum`; `bunx tsc --noEmit` passes; all M1–M9 regression suites pass (`go test ./...` default + `-tags=integration` + `-tags=chaos`).
    New-code coverage probe reports ≥82% on Go-side new code (top up tests if short — no production-code changes); Sonar new-issues API reports zero unresolved new issues before the push. If any step fails: focused patch against the relevant earlier task's files, then re-run all acceptance steps from the top. **No new features in this task.** `gofmt -l .` + `go vet ./...` clean before the push.
  - **Out of scope for this task**: retro (T018); any scope addition.

- [ ] **T018** Retro — `docs/retros/m10.md` + MemPalace drawer mirror
  - **Depends on**: T017 green.
  - **Files**: `docs/retros/m10.md` (new); MemPalace `wing_company / hall_events` drawer via `mempalace_add_drawer` (+ `mempalace_kg_add` for cross-milestone facts), per the M3+ dual-deliverable policy.
  - **Completion condition**: retro follows the AGENTS.md structure — what shipped (per task), what the spec got wrong, dependencies added outside the locked list (expected: none) with justifications if any, open questions deferred to the next milestone. Must answer the context file's named retro questions: (a) did idempotency hold under real redelivery and under the M1 concurrent-delivery race? (b) did signature verification fail closed against a forged delivery? (c) did the rate cap actually bound a burst and did the breach write correct M6 evidence? (d) did the inbound-only boundary hold — did anything accidentally reach back out to GitHub or any external service? (e) did the threat model (`ingress-threat-model.md`) precede all connector code in git history? Also answers: did the M9 spike-payoff question apply here (yes — `docs/research/m10-spike.md` is the M10 spike; report whether its SR1–SR10 findings were accurate vs. what implementation discovered). Notes whether any task pushed lint-failing code. Palace drawer mirrors the markdown (markdown canonical). If post-ship adversarial review finds issues (the M9 pattern), patches land before this task is checked off.
  - **Out of scope for this task**: everything else — the retro adds no code.
