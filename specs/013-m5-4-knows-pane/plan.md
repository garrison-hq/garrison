# Implementation plan: M5.4 — "WHAT THE CEO KNOWS" knowledge-base pane

**Branch**: `013-m5-4-knows-pane` | **Date**: 2026-05-01 | **Spec**: [spec.md](./spec.md)
**Input**: [spec.md](./spec.md) (FR-600 … FR-699 + Clarifications session 2026-05-01), [m5-4-context.md](../_context/m5-4-context.md), [MinIO spike](../../docs/research/m5-4-spike-minio.md), M5.1 [retro](../../docs/retros/m5-1.md) + M5.2 [retro](../../docs/retros/m5-2.md) + M5.3 [retro](../../docs/retros/m5-3.md), [vault threat model](../../docs/security/vault-threat-model.md), [chat threat model](../../docs/security/chat-threat-model.md), [AGENTS.md](../../AGENTS.md), [RATIONALE.md](../../RATIONALE.md), [ARCHITECTURE.md](../../ARCHITECTURE.md), existing supervisor + dashboard codebases.

---

## Summary

M5.4 closes the M5 arc by replacing M5.2's `KnowsPanePlaceholder` with a tabbed `KnowsPane` rendering three operator-facing surfaces in the chat right pane: **Company.md** (CEO-editable, MinIO-backed, syntax-highlighted CodeMirror surface), **Recent palace writes** (read-only list of MemPalace drawer entries), **KG recent facts** (read-only list of MemPalace KG triples). Both observability tabs share a refresh-on-demand UX with greyed-prior-list-during-fetch (M3 list-refresh precedent).

A new MinIO container joins the existing `garrison-net` topology as the 4th service. Persistence uses a Docker named volume (`garrison-minio-data`) — no host bind mounts, satisfying the operator constraint that drove the storage-location deviation away from ARCHITECTURE.md's never-landed `companies.company_md TEXT NOT NULL` column.

The supervisor gains two new packages: `internal/objstore/` (MinIO client wrapping `minio-go/v7`, idempotent `Bootstrap` startup probe per spike §F5, leak-scan + size-cap on Put, ETag-aware Get/Put with `If-Match` semantics per spike §F7) and `internal/dashboardapi/` (a second HTTP server alongside `internal/health/` exposing `/api/objstore/company-md` GET/PUT and `/api/mempalace/recent-{writes,kg}` reads, behind cookie-based session validation against the existing better-auth `better_auth_session` table). The existing `internal/mempalace/` gains a `QueryClient` for the recent-drawers/recent-KG read paths.

Dashboard side, M5.4 ships a `KnowsPane` component plus three per-tab sub-components, a CodeMirror-backed `CompanyMDEditor`, and Server Actions in `lib/actions/companyMD.ts` that are thin wrappers over the supervisor proxy — the dashboard process never imports a MinIO SDK and never holds MinIO credentials. Server-side enforcement of leak-scan + size-cap is the source of truth (FR-670).

Auth between dashboard and supervisor uses cookie-forwarding + Postgres lookup (decision 9a in the structural-decision slate): the dashboard's server-side `fetch()` carries the `better-auth.session_token` cookie; the supervisor's `dashboardapi` middleware queries `better_auth_session` for validity. A new `SELECT` grant on the `better_auth_session` table to the supervisor's Postgres role is the one schema-related change in this milestone — no new tables, no new sqlc-generated types beyond a single new query.

`ARCHITECTURE.md` gains three substring-pinned amendments (deployment-topology MinIO line, schema-section MinIO reference, M5 build-plan M5.4 sentence). Two new dependencies cross the locked-deps soft-rule: `github.com/minio/minio-go/v7` (Go) and `@uiw/react-codemirror` + `@codemirror/lang-markdown` (TS) — both flagged for the M5.4 retro per AGENTS.md.

---

## Technical context

**Language/Version**: Go 1.23 (supervisor — two new packages + existing `internal/mempalace/` + `internal/config/` + `cmd/supervisor/main.go` extensions); TypeScript 5.6+ / Next.js 16 / React 19 (dashboard — new components + Server Actions + queries module); SQL (one new permissions grant migration).

**Primary Dependencies (supervisor)**: existing locked list **plus** `github.com/minio/minio-go/v7`. Justification per spike §F8: lighter than `aws-sdk-go-v2/s3`, upstream-maintained, exact-match for MinIO server semantics. Version pinned at /garrison-implement time to the latest stable v7 release. Reuses `pgx/v5` (auth-middleware Postgres lookup), `slog`, `errgroup` (lifecycle), `testify` + `testcontainers-go` (tests), `internal/vault.Client` (scoped-creds fetch from Infisical), `internal/store` (sqlc — one new query type), `internal/mempalace` (extended with `QueryClient`).

**Primary Dependencies (dashboard)**: existing locked list **plus** `@uiw/react-codemirror` + `@codemirror/lang-markdown`. Justification: M5.4 needs a syntax-highlighted Markdown editor; CodeMirror v6 via the wrapper is React-19-stamped + 80 KB gzipped + the M3-precedent design-token shape transfers cleanly. The dashboard does **not** add a Node MinIO SDK (decision 1e — the proxy keeps MinIO opacity).

**Storage**: shared Postgres for the auth-middleware session lookup only. **No new tables, no new columns, no schema changes.** One new `GRANT SELECT ON better_auth_session TO garrison_supervisor` lands as a new migration. Company.md content lives in MinIO at `s3://garrison-company/<companyId>/company.md`. Palace writes + KG facts live in MemPalace's existing chromadb (no mirroring into Postgres).

**Testing**:
- Go test for `internal/objstore/` (unit: leak-scan, size-cap, ETag flow; integration via testcontainers-go generic-container API booting `minio/minio@<digest>`).
- Go test for `internal/dashboardapi/` (unit: auth middleware against fake `SessionValidator`; handler tests against fake `objstore.Client` + fake `mempalace.QueryClient`).
- Go test for `internal/mempalace/QueryClient` (unit: response parsing via httptest fakes).
- Vitest for `dashboard/lib/actions/companyMD.ts`, `dashboard/lib/queries/knowsPane.ts`, and the new `components/features/ceo-chat/{KnowsPane,CompanyMDTab,CompanyMDEditor,RecentPalaceWritesTab,KGRecentFactsTab}.tsx` (jsdom + `@testing-library/react`, established in M5.3).
- Playwright for the M5.4 golden-path: open `/chat`, see Company.md, edit + save, verify content updates; leak-scan reject; refresh palace tab. Single new spec file `dashboard/tests/integration/m5-4-knows-pane.spec.ts`.
- Architecture amendment test extension (`dashboard/tests/architecture-amendment.test.ts` — three new substring assertions).

**Target Platform**: Linux container for supervisor + dashboard + MinIO. Browser baseline carries from M5.2/M5.3 (`>=Chrome 130, >=Firefox 130, >=Safari 17.5`). MinIO spike §F1 confirms image runs on standard Docker 24+; CI's `ubuntu-latest` runner version (Docker 29.x) is comfortably ahead.

**Project Type**: Web application — supervisor extension (two new packages + lifecycle wiring) + dashboard extension (new component tree + Server Actions + queries module) + one permissions-grant migration + one new container in the deployment topology.

**Performance Goals**:
- Company.md read latency end-to-end (browser → Server Action → supervisor proxy → MinIO `GetObject` → return) ≤ 500 ms p95 on a local single-node deployment. No formal SLO; sub-100ms reads observed in spike §F4 against the empty bucket.
- `/api/mempalace/recent-{writes,kg}` proxy round-trip ≤ 1 s p95 against a populated MemPalace sidecar (default `limit=30` per FR-685).
- Save round-trip (PUT through proxy → MinIO `PutObject` with `If-Match`) ≤ 1 s p95.
- Playwright suite total runtime stays within the existing M5.x 12-minute envelope (M5.4 adds ~3 scenarios).

**Constraints**:
- AGENTS.md concurrency rules 1, 2, 6 bind on the new `internal/objstore/` and `internal/dashboardapi/` packages: every goroutine threads `context.Context`, the new HTTP server respects SIGTERM via the existing errgroup pattern, terminal writes use `context.WithoutCancel(ctx)` + grace where they matter.
- Operator constraint (recorded in m5-4-context.md): **no host bind mounts**. MinIO data lives in a Docker named volume; no `-v /host/path:/data` form anywhere.
- M2.3 carryover: vault remains opaque to the dashboard. The dashboard reads MinIO scoped creds *indirectly* — the supervisor reads them from Infisical, the dashboard never sees them.
- Rule 1 carryover (M2.3 + M5.3): leak-scan reject messages name the matched **pattern category** but never echo the matched substring.
- ARCHITECTURE.md amendment lands in the same PR as implementation; substring-match test pins all three amendments.

---

## Constitution check

Reading `.specify/memory/constitution.md` (M5 substrate):

- **Principle II (no premature abstraction)**: M5.4 introduces two new supervisor packages (`internal/objstore/`, `internal/dashboardapi/`) — both load-bearing for the milestone, neither speculative. The `dashboardapi` package is reused-by-design (it's the supervisor's HTTP surface for any future dashboard ↔ supervisor RPC) but M5.4 ships it with exactly the routes M5.4 needs; it does not pre-design for hypothetical M6+ endpoints.
- **Principle VI (concurrency discipline)**: AGENTS.md rules 1–8 carry. Specifically rule 4 (every spawned subprocess uses `exec.CommandContext`) does not apply — M5.4 spawns no subprocesses. Rule 6 (terminal writes during shutdown use `context.WithoutCancel(ctx)`) does not apply — neither package writes terminal state. Rule 8 (subprocess pipe drain before Wait) does not apply.
- **Principle VIII (test-first)**: every public function in `internal/objstore/` and `internal/dashboardapi/` has a paired unit test before the function is wired in `cmd/supervisor/main.go` (mirrors M5.3's per-verb pattern).
- **Principle IX (narrow specs per milestone)**: M5.4 stays inside the FR-600 → FR-699 envelope. Out of scope per spec/context: chat-prompt wiring of Company.md (Option B — defer), per-thread context-token counter (defer to M6), LLM summarisation, multi-document Company.md, SSE-live tailing, multi-operator, MinIO backup/restore design.
- **Principle X (single-operator)**: Constitution X. ETag/If-Match optimistic concurrency upgrades silent multi-window overwrite to surfaced staleness (FR-643), but the underlying authority model stays single-operator. No multi-operator edit surface.

Constitution check PASSES.

---

## Project structure

### Documentation (this feature)

```text
specs/013-m5-4-knows-pane/
├── spec.md              ← /garrison-specify + /speckit.clarify output
├── plan.md              ← this file
├── tasks.md             ← /garrison-tasks output (NOT created here)
└── acceptance-evidence.md  ← /garrison-implement evidence-bundle (NOT created here)
```

### Source code

```text
supervisor/
├── cmd/supervisor/
│   ├── main.go                      ← extended: dashboardapi.Server wired into the existing errgroup
│   └── (no new subcommands)
├── internal/
│   ├── objstore/                    ← NEW package
│   │   ├── client.go                ← Client struct, BootstrapBucket, GetCompanyMD, PutCompanyMD
│   │   ├── client_test.go           ← unit tests vs fake minio-go endpoint
│   │   ├── leakscan.go              ← reuses internal/finalize.scanAndRedactPayload pattern set
│   │   ├── leakscan_test.go         ← table-driven over 10 patterns
│   │   ├── sizecap.go               ← MaxCompanyMDBytes constant + check helper
│   │   ├── sizecap_test.go
│   │   ├── errors.go                ← ErrMinIOUnreachable, ErrMinIOAuthFailed, ErrStale, ErrLeakScanFailed, ErrTooLarge
│   │   ├── errors_test.go           ← errors.Is wiring
│   │   └── integration_test.go      ← //go:build integration; testcontainers-go generic MinIO container
│   ├── dashboardapi/                ← NEW package
│   │   ├── server.go                ← Server struct, NewServer, Serve(ctx), Shutdown
│   │   ├── server_test.go           ← lifecycle tests
│   │   ├── auth.go                  ← SessionValidator interface + sqlSessionValidator impl + middleware
│   │   ├── auth_test.go             ← fake validator scenarios
│   │   ├── objstore_handler.go      ← GET / PUT /api/objstore/company-md
│   │   ├── objstore_handler_test.go ← fake objstore.Client scenarios
│   │   ├── mempalace_handler.go     ← GET /api/mempalace/recent-writes, /api/mempalace/recent-kg
│   │   ├── mempalace_handler_test.go ← fake mempalace.QueryClient scenarios
│   │   ├── errors.go                ← ErrAuthExpired + JSON error-response shape
│   │   └── errors_test.go
│   ├── mempalace/
│   │   ├── queryclient.go           ← NEW: RecentDrawers, RecentKGTriples
│   │   ├── queryclient_test.go      ← NEW: httptest-driven sidecar fakes
│   │   └── (existing files unchanged)
│   ├── config/
│   │   └── config.go                ← extended: 6 new env vars (see §5 of the structural slate)
│   ├── store/
│   │   └── better_auth_session.sql.go  ← NEW sqlc-generated query: GetSessionByID
│   └── (other internal packages unchanged)
├── Dockerfile                       ← unchanged (supervisor binary unchanged)
└── docker-compose.yml               ← extended: minio service + named volume + env wiring

migrations/
├── 20260501000000_m5_4_supervisor_session_select_grant.sql  ← NEW
├── 20260501000001_m5_4_remove_documented_company_md_column.sql  ← NEW (pure docs-vs-code reconciliation; no DDL)

migrations/queries/
├── better_auth_session.sql          ← NEW: GetSessionByID query for sqlc

dashboard/
├── components/features/ceo-chat/
│   ├── KnowsPane.tsx                ← NEW: replaces KnowsPanePlaceholder; tab strip + per-tab content router
│   ├── KnowsPane.test.tsx           ← NEW
│   ├── CompanyMDTab.tsx             ← NEW: read/edit toggle, Save/Cancel, error-state rendering
│   ├── CompanyMDTab.test.tsx        ← NEW
│   ├── CompanyMDEditor.tsx          ← NEW: CodeMirror v6 wrapper, readOnly toggle
│   ├── CompanyMDEditor.test.tsx     ← NEW
│   ├── RecentPalaceWritesTab.tsx    ← NEW: list view, Refresh button, greyed-prior-list-during-fetch
│   ├── RecentPalaceWritesTab.test.tsx ← NEW
│   ├── KGRecentFactsTab.tsx         ← NEW: triple list, Refresh button, same UX shape as palace tab
│   ├── KGRecentFactsTab.test.tsx    ← NEW
│   └── (M5.2 KnowsPanePlaceholder.tsx ← DELETED)
├── lib/actions/
│   ├── companyMD.ts                 ← NEW: getCompanyMD, saveCompanyMD Server Actions
│   └── companyMD.test.ts            ← NEW: fetch-mocked
├── lib/queries/
│   ├── knowsPane.ts                 ← NEW: getRecentPalaceWrites, getRecentKGFacts
│   └── knowsPane.test.ts            ← NEW: fetch-mocked
└── tests/
    ├── architecture-amendment.test.ts  ← extended with 3 new substring assertions
    └── integration/
        └── m5-4-knows-pane.spec.ts  ← NEW Playwright spec

docs/
├── ops-checklist.md                 ← extended: post-deploy `mc admin user svcacct add` step + Infisical paths
└── retros/m5-4.md                   ← /garrison-tasks creates the skeleton; /garrison-implement fills it

ARCHITECTURE.md                       ← amended: 3 substrings (see ARCH-1 in spec)
```

---

## Subsystem walkthroughs

### 1. `internal/objstore/`

The MinIO client + leak-scan + size-cap + bucket-bootstrap. **Single-instance per supervisor process**, constructed at startup with scoped credentials fetched from Infisical via the existing `internal/vault.Client`.

```go
// client.go
package objstore

type Client struct {
    minio     *minio.Client          // from minio-go/v7
    bucket    string                 // from cfg.MinIOBucket; default "garrison-company"
    logger    *slog.Logger
}

func New(ctx context.Context, cfg *config.Config, vaultClient *vault.Client, logger *slog.Logger) (*Client, error) {
    // Fetch scoped creds from Infisical (writes vault_access_log row, fail-closed).
    // Construct minio-go client via minio.New(endpoint, &minio.Options{Creds: ..., Secure: cfg.MinIOUseTLS}).
    // Returns Client wrapping minio + bucket name.
}

// BootstrapBucket runs at supervisor startup BEFORE the event-bus listener.
// Idempotent (BucketExists → MakeBucket if false). Logs "created" or "exists".
// Returns ErrMinIOUnreachable if MinIO can't be reached; supervisor exits ExitFailure.
func (c *Client) BootstrapBucket(ctx context.Context) error

// GetCompanyMD fetches the object at "<companyId>/company.md".
// Returns (content, etag, nil) on hit, (nil, "", nil) on 404 (FR-624 empty-state),
// (nil, "", ErrMinIOUnreachable | ErrMinIOAuthFailed) on errors.
func (c *Client) GetCompanyMD(ctx context.Context, companyID string) ([]byte, string, error)

// PutCompanyMD writes the object with If-Match: ifMatchEtag.
// Pre-checks: size ≤ MaxCompanyMDBytes (64*1024); leakscan.Scan(content) returns no hits.
// Returns (newEtag, nil) on success, ("", ErrLeakScanFailed | ErrTooLarge | ErrStale | ...) on rejection.
func (c *Client) PutCompanyMD(ctx context.Context, companyID string, content []byte, ifMatchEtag string) (string, error)
```

**Error types (errors.go)** are sentinel `errors.New(...)` values; handlers classify via `errors.Is`. No structured error envelope on the Go side — the HTTP layer in `internal/dashboardapi/` is where errors get JSON-shaped.

```go
var (
    ErrMinIOUnreachable = errors.New("objstore: minio unreachable")
    ErrMinIOAuthFailed  = errors.New("objstore: minio auth failed")
    ErrStale            = errors.New("objstore: if-match precondition failed")
    ErrLeakScanFailed   = errors.New("objstore: leak scan rejected content")
    ErrTooLarge         = errors.New("objstore: content exceeds size cap")
)
```

`ErrLeakScanFailed` carries a wrapped `LeakScanError` with `.Category` (e.g. `aws-access-key`, `sk-prefix`, `bearer`) — exposed to the HTTP layer to populate the `pattern_category` JSON field. **Never carries the matched substring** (Rule 1 carryover; SEC-4).

**Leak-scan reuse** (leakscan.go): imports `internal/finalize` to share `scanAndRedactPayload`'s 10-pattern set rather than duplicating regex literals. `objstore.Scan(content) ([]MatchCategory, error)` returns matched categories without leaking the substring. If `internal/finalize` doesn't expose this surface, M5.4 extracts the patterns into a new `internal/leakscan/` package and both `internal/finalize` and `internal/objstore/` import it. /garrison-tasks decides per the actual `internal/finalize` surface at implement time.

**Size-cap** (sizecap.go): `const MaxCompanyMDBytes = 64 * 1024`. Sized per spike notes; ARCHITECTURE.md "842 lines" wireframe sample fits comfortably.

### 2. `internal/dashboardapi/`

A second HTTP server alongside the existing `internal/health/`. Owns: routing, auth middleware, error JSON shape.

```go
// server.go
package dashboardapi

type Server struct {
    httpServer    *http.Server
    shutdownGrace time.Duration
}

func NewServer(cfg *config.Config, deps Deps) *Server {
    mux := http.NewServeMux()
    auth := newAuthMiddleware(deps.SessionValidator, deps.Logger)
    mux.Handle("/api/objstore/company-md", auth(newObjstoreHandler(deps.Objstore, deps.Logger)))
    mux.Handle("/api/mempalace/recent-writes", auth(newRecentWritesHandler(deps.Mempalace, deps.Logger)))
    mux.Handle("/api/mempalace/recent-kg", auth(newRecentKGHandler(deps.Mempalace, deps.Logger)))
    return &Server{
        httpServer: &http.Server{
            Addr:              fmt.Sprintf("0.0.0.0:%d", cfg.DashboardAPIPort),
            Handler:           mux,
            ReadHeaderTimeout: 5 * time.Second,
        },
        shutdownGrace: cfg.ShutdownGrace,
    }
}

func (s *Server) Serve(ctx context.Context) error  // mirrors health.Server.Serve shape
```

**`Deps` shape**:

```go
type Deps struct {
    Objstore         *objstore.Client
    Mempalace        *mempalace.QueryClient
    SessionValidator SessionValidator
    Logger           *slog.Logger
}
```

**Auth middleware** (auth.go):

```go
type SessionValidator interface {
    ValidateCookie(ctx context.Context, sessionToken string) (UserID, error)
}

type UserID = pgtype.UUID

// Production impl wraps store.Queries.GetBetterAuthSessionByID.
type sqlSessionValidator struct { q *store.Queries }

func (v *sqlSessionValidator) ValidateCookie(ctx, token) (UserID, error) {
    row, err := v.q.GetBetterAuthSessionByID(ctx, token)
    // - errors.Is(err, pgx.ErrNoRows) → ErrAuthExpired
    // - row.ExpiresAt < time.Now() → ErrAuthExpired
    // - other errors → wrapped non-typed
}

// Middleware extracts cookie via http.Request.Cookie("better-auth.session_token"),
// calls ValidateCookie, on success injects UserID into request context for handlers.
func newAuthMiddleware(v SessionValidator, l *slog.Logger) func(http.Handler) http.Handler
```

**Cookie name** is `better-auth.session_token` — pinned at /garrison-tasks time after a one-line check against the dashboard's better-auth config (likely already authoritative in `dashboard/lib/auth/`).

**objstore handler** (objstore_handler.go):

```go
// GET /api/objstore/company-md
//   - Reads UserID + companyId from context (single-company posture: companyId resolved at startup from companies table; cached in handler).
//   - Calls Objstore.GetCompanyMD(ctx, companyId)
//   - On hit: 200 with {"content": "...", "etag": "..."}
//   - On 404: 200 with {"content": "", "etag": null}    (FR-624)
//   - On error: 503 with {"error": "MinIOUnreachable", "message": "..."}
//
// PUT /api/objstore/company-md
//   - Reads body (text/markdown), Content-Length-checks against MaxCompanyMDBytes
//   - Reads If-Match header; rejects 400 if missing
//   - Calls Objstore.PutCompanyMD(ctx, companyId, content, ifMatch)
//   - On success: 200 with {"content": <echoed>, "etag": <new>}
//   - On ErrLeakScanFailed: 422 with {"error": "LeakScanFailed", "pattern_category": "..."}
//   - On ErrTooLarge: 413 with {"error": "TooLarge"}
//   - On ErrStale: 412 with {"error": "Stale"}
//   - On ErrMinIOUnreachable: 503 with {"error": "MinIOUnreachable"}
```

**mempalace handlers** (mempalace_handler.go):

```go
// GET /api/mempalace/recent-writes?limit=N
//   - limit defaults to 30, clamped ≤100 (FR-685)
//   - Calls Mempalace.RecentDrawers(ctx, limit)
//   - On success: 200 with {"writes": [{...}]}
//   - On ErrSidecarUnreachable: 503 with {"error": "MempalaceUnreachable"}
//
// GET /api/mempalace/recent-kg?limit=N — same shape, calls RecentKGTriples
```

**Error JSON shape** is uniform (errors.go):

```go
type errorResponse struct {
    Error           string `json:"error"`
    Message         string `json:"message,omitempty"`
    PatternCategory string `json:"pattern_category,omitempty"` // only on LeakScanFailed
}
```

### 3. `internal/mempalace/QueryClient`

Extension of the existing `internal/mempalace/Client`. Reuses the same HTTP transport configuration (`PalaceContainer`, `DockerHost`, `PalacePath`) the M2.2 client already owns; adds two read methods.

```go
// queryclient.go
package mempalace

type QueryClient struct {
    httpClient *http.Client
    baseURL    string  // existing mempalace sidecar URL
    logger     *slog.Logger
}

type DrawerEntry struct {
    ID                  string    `json:"id"`
    DrawerName          string    `json:"drawer_name"`
    RoomName            string    `json:"room_name"`
    WingName            string    `json:"wing_name"`
    SourceAgentRoleSlug string    `json:"source_agent_role_slug,omitempty"`
    WrittenAt           time.Time `json:"written_at"`
    BodyPreview         string    `json:"body_preview"`  // ≤200 chars (truncated server-side)
}

type KGTriple struct {
    ID                  string    `json:"id"`
    Subject             string    `json:"subject"`
    Predicate           string    `json:"predicate"`
    Object              string    `json:"object"`
    SourceTicketID      *string   `json:"source_ticket_id,omitempty"`
    SourceAgentRoleSlug *string   `json:"source_agent_role_slug,omitempty"`
    WrittenAt           time.Time `json:"written_at"`
}

func (c *QueryClient) RecentDrawers(ctx context.Context, limit int) ([]DrawerEntry, error)
func (c *QueryClient) RecentKGTriples(ctx context.Context, limit int) ([]KGTriple, error)

var ErrSidecarUnreachable = errors.New("mempalace: sidecar unreachable")
```

**Body-preview truncation** happens supervisor-side, not in the sidecar. The QueryClient reads the full drawer body, then truncates to the first 200 chars (UTF-8-safe — does not split a multi-byte rune) before returning. Rationale: the existing MemPalace sidecar's diary-read API returns full bodies; pre-truncation in the supervisor avoids requiring a sidecar API change and keeps the UI payload bounded.

**Sidecar API mapping** is finalised at /garrison-tasks time after re-confirming the existing mempalace sidecar's HTTP surface from `docs/research/m2-spike.md` Part 2. If the sidecar lacks a "recent across wings" endpoint, M5.4 layers the cross-wing aggregation in the QueryClient (one HTTP call per wing the supervisor knows about, then merge-sort by `WrittenAt DESC`, then truncate to `limit`). If the sidecar has it natively, use it directly. **/garrison-tasks T-numbered task confirms which path before any QueryClient code lands.**

### 4. `cmd/supervisor/main.go` lifecycle wiring

Three changes:

1. Construct `objstore.Client` after `vault.Client` is constructed but before the event-bus listener starts. Run `BootstrapBucket(ctx)`. Fail-closed (return error → supervisor exits `ExitFailure`).
2. Construct `mempalace.QueryClient` from the existing `mempalace.Client`'s configuration.
3. Construct `dashboardapi.Server` and add it to the existing errgroup alongside `health.Server`. Same shutdown-grace semantics; same SIGTERM handling.

**Order matters** — `vault.Client` must be ready before `objstore.New` calls it; `objstore.BootstrapBucket` must succeed before `dashboardapi.Server` accepts any requests (no readiness probe needed; the dashboardapi server simply doesn't start until bootstrap returns). Mirrors the M2.2 mempalace bootstrap precedent.

### 5. Configuration

`internal/config/config.go` adds these fields (env-var loading follows the existing parser pattern):

| Env var | Field | Default | Notes |
|---|---|---|---|
| `GARRISON_MINIO_ENDPOINT` | `MinIOEndpoint string` | (required) | e.g. `garrison-minio:9000` (in-network DNS) |
| `GARRISON_MINIO_BUCKET` | `MinIOBucket string` | `garrison-company` | per FR-620 |
| `GARRISON_MINIO_USE_TLS` | `MinIOUseTLS bool` | `false` | in-network plaintext is fine; TLS is operator opt-in |
| `GARRISON_MINIO_ACCESS_KEY_PATH` | `MinIOAccessKeyPath string` | (required) | Infisical secret path, e.g. `/operator/MINIO_ACCESS_KEY` |
| `GARRISON_MINIO_SECRET_KEY_PATH` | `MinIOSecretKeyPath string` | (required) | Infisical secret path, e.g. `/operator/MINIO_SECRET_KEY` |
| `GARRISON_DASHBOARD_API_PORT` | `DashboardAPIPort int` | `8081` | dedicated; separate from `GARRISON_HEALTH_PORT` |

Dashboard side adds one env var (`DASHBOARD_SUPERVISOR_API_URL`, e.g. `http://garrison-supervisor:8081`) read at Server-Action call time.

### 6. Migrations

**One migration**, plus a documentation-only twin:

```sql
-- 20260501000000_m5_4_supervisor_session_select_grant.sql

-- +goose Up
GRANT SELECT ON better_auth_session TO garrison_supervisor;

-- +goose Down
REVOKE SELECT ON better_auth_session FROM garrison_supervisor;
```

```sql
-- 20260501000001_m5_4_remove_documented_company_md_column.sql
-- Pure docs-vs-code reconciliation. The companies.company_md column was
-- documented in ARCHITECTURE.md (line 151) but never landed in any prior
-- migration. M5.4 stores Company.md in MinIO instead (specs/_context/m5-4-context.md
-- §"Scope deviation"). This migration intentionally has NO DDL — its
-- presence in goose_db_version provides a deploy marker; the actual
-- documentation amendment is in ARCHITECTURE.md (ARCH-1 in spec).

-- +goose Up
SELECT 'm5.4 docs-vs-code reconciliation: companies.company_md never landed; Company.md now lives in MinIO' AS note;

-- +goose Down
SELECT 'no DDL to revert' AS note;
```

The marker-only migration follows the M2.2.2 precedent of "no-op migrations exist as deploy waypoints when the schema doesn't change but a deploy semantic does."

### 7. New sqlc query

`migrations/queries/better_auth_session.sql`:

```sql
-- name: GetBetterAuthSessionByID :one
SELECT id, user_id, expires_at
FROM better_auth_session
WHERE id = $1;
```

Generates `store.GetBetterAuthSessionByID(ctx, id) (GetBetterAuthSessionByIDRow, error)` plus the row struct. Used only by `dashboardapi.sqlSessionValidator`.

### 8. Dashboard component tree

```text
KnowsPane (top-level, replaces KnowsPanePlaceholder)
├── tab strip (3 tabs; FR-601)
└── per-tab content area
    ├── CompanyMDTab
    │   ├── view mode: <CompanyMDEditor readOnly content={loaded.content} />
    │   └── edit mode (after Edit click):
    │       ├── <CompanyMDEditor readOnly={false} value={buffer} onChange={setBuffer} />
    │       ├── <Save> button (disabled while in-flight)
    │       ├── <Cancel> button (confirm dialog if buffer != loaded.content)
    │       └── error block (if any: Stale | LeakScanFailed | TooLarge | AuthExpired)
    ├── RecentPalaceWritesTab
    │   ├── refresh button (disabled while fetching)
    │   ├── list (greyed at 60% opacity while fetching)
    │   └── error block (MempalaceUnreachable)
    └── KGRecentFactsTab — same shape as RecentPalaceWritesTab
```

**State management**: each tab owns its own `useState` for `{loaded, buffer, isInFlight, lastError}`. No global store needed; Server Actions are awaited per click, results land in local state.

**`CompanyMDEditor`** wraps `@uiw/react-codemirror` with a fixed extension set: `markdown()` from `@codemirror/lang-markdown` plus a syntax-highlight theme that matches the M3 design tokens (light + dark). The wrapper enforces `readOnly` via the `editable` prop and exposes a single `onChange(value: string)` callback. No event-loop cleverness, no debounced auto-save — operator clicks Save explicitly per FR-642.

### 9. Dashboard ↔ supervisor auth flow

Cookie-forward + Postgres-lookup, decision 9a from the structural slate:

1. Browser → dashboard server action `saveCompanyMD(content, etag)`. The Server Action runs in Node with full HTTP-cookie access via `next/headers.cookies()`.
2. Server Action constructs a `fetch()` to `${DASHBOARD_SUPERVISOR_API_URL}/api/objstore/company-md` with method PUT, body = content, headers = `{ "If-Match": etag, "Content-Type": "text/markdown", "Cookie": "better-auth.session_token=<value>" }`.
3. Supervisor's `dashboardapi` middleware reads the cookie, calls `SessionValidator.ValidateCookie(ctx, token)`.
4. Production validator queries `better_auth_session` (via the new SELECT grant + the new sqlc query), checks `expires_at > NOW()`, returns the row's `user_id` on success.
5. Middleware injects `userID` into the request context; handler proceeds.
6. On `ErrAuthExpired`, middleware responds `401 {"error": "AuthExpired"}`. The Server Action surfaces this typed error to the editor (FR-647).

**Why cookie-forward over HMAC**: single source of truth for sessions (better-auth's table is already authoritative); no shared-secret rotation surface; the supervisor's read-only `SELECT` grant is bounded — it cannot mint sessions, only validate them.

### 10. Docker compose extension

```yaml
# docker-compose.yml additions

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
    restart: unless-stopped

  supervisor:
    # existing fields unchanged; add env:
    environment:
      # ... existing ...
      - GARRISON_MINIO_ENDPOINT=garrison-minio:9000
      - GARRISON_MINIO_BUCKET=garrison-company
      - GARRISON_MINIO_USE_TLS=false
      - GARRISON_MINIO_ACCESS_KEY_PATH=${GARRISON_MINIO_ACCESS_KEY_PATH}
      - GARRISON_MINIO_SECRET_KEY_PATH=${GARRISON_MINIO_SECRET_KEY_PATH}
      - GARRISON_DASHBOARD_API_PORT=8081
    depends_on:
      - postgres
      - mempalace
      - minio                         # NEW

  dashboard:
    environment:
      # ... existing ...
      - DASHBOARD_SUPERVISOR_API_URL=http://garrison-supervisor:8081

volumes:
  garrison-minio-data:                # NEW named volume (no host bind mount)
```

**No host port forwarding** for MinIO in the production compose. Operator-local dev compose may forward `9000` + `9001`; that's a `dev` profile concern out of M5.4's production ship.

### 11. CI

- Add `minio/minio@sha256:69b2ec...` to the `Pre-pull testcontainer images` step in `.github/workflows/ci.yml`. One-line addition; no new job.
- The integration job naturally picks up `internal/objstore/integration_test.go` because it sits under `supervisor/internal/...` and the test job runs `go test -tags=integration ./...`.

### 12. ARCHITECTURE.md amendment

Three substring-pinned amendments (per spec ARCH-1):

1. **Schema section** (line 151): replace the documented `companies.company_md TEXT NOT NULL` with a sentence pointing at MinIO.
2. **M5 build-plan section** (line 575): extend the M5.4 sentence with the operator-facing copy ("CEO-editable Company.md backed by MinIO; recent palace writes + recent KG facts read-only via supervisor-side proxy to MemPalace").
3. **Deployment-topology section**: add MinIO as the 4th container alongside supervisor + mempalace + socket-proxy; note the named-volume + scoped-service-account credential model.

`dashboard/tests/architecture-amendment.test.ts` extends with three new substring assertions — one per amendment. Test failure blocks merge (FR-501 carryover from M5.3).

---

## Lifecycle + state machines

### `objstore.Client`

No state machine; the client is stateless beyond the underlying `minio-go` HTTP connection pool. Lifecycle is bounded by the supervisor process (constructed at boot, GC'd at shutdown).

### `dashboardapi.Server`

Mirrors `health.Server` lifecycle exactly:

```text
boot → ListenAndServe (goroutine) → ctx cancel → http.Server.Shutdown(grace) → done
                                  ↘ ListenAndServe error → return wrapped error
```

No internal state machine; per-request handlers are pure (auth, dispatch, response).

### MinIO container

Per spike §F4, MinIO has its own restart-recovery built in. Lifecycle from the supervisor's POV:

```text
docker compose up
  ↓
MinIO container starts (no compose-level healthcheck per FR-666)
  ↓
supervisor starts → vault.Client → objstore.New (fetches scoped creds from Infisical)
  ↓
objstore.BootstrapBucket
  ├ MinIO unreachable → ErrMinIOUnreachable → supervisor exits ExitFailure
  ├ Bucket exists      → log "exists"        → continue
  └ Bucket missing     → MakeBucket          → log "created" → continue
  ↓
dashboardapi.Server.Serve (errgroup)
  ↓
… steady state …
  ↓
SIGTERM → ctx cancel → both servers Shutdown(grace)
```

Bucket bootstrap is fail-closed at boot, but mid-runtime MinIO failures surface only as per-request 503s (FR-625, FR-668's typed errors). Mid-runtime crash-recovery loops are NOT in scope — operator notices via the typed-error block + restarts MinIO. M5.4 retro tracks any patterns that emerge from operator-week-of-use.

---

## Test strategy

### Unit tests (Go, default-tag)

#### `internal/objstore/`

| Test | File | Verifies |
|---|---|---|
| `TestBootstrapBucket_Idempotent_AlreadyExists` | `client_test.go` | fake minio: `BucketExists`=true → `Bootstrap` returns nil without calling `MakeBucket` |
| `TestBootstrapBucket_CreatesWhenMissing` | `client_test.go` | fake minio: `BucketExists`=false → `Bootstrap` calls `MakeBucket` and returns nil |
| `TestBootstrapBucket_PropagatesUnreachable` | `client_test.go` | fake minio returns net error → `Bootstrap` returns `ErrMinIOUnreachable` |
| `TestGetCompanyMD_HappyPath` | `client_test.go` | fake minio returns 200 + body + ETag → returns content + etag + nil |
| `TestGetCompanyMD_404ReturnsEmpty` | `client_test.go` | fake minio returns 404 → returns nil content + "" etag + nil error |
| `TestGetCompanyMD_PropagatesUnauthorized` | `client_test.go` | fake minio returns 403 → returns `ErrMinIOAuthFailed` |
| `TestPutCompanyMD_HappyPath` | `client_test.go` | clean content + matching etag → returns new etag + nil |
| `TestPutCompanyMD_RejectsTooLarge` | `client_test.go` | 70 KB content → returns `ErrTooLarge` BEFORE any minio call |
| `TestPutCompanyMD_RejectsLeakScan` | `client_test.go` | content with `sk-...` → returns `ErrLeakScanFailed` with category=`sk-prefix`; no minio call |
| `TestPutCompanyMD_PropagatesStaleEtag` | `client_test.go` | fake minio returns 412 → returns `ErrStale` |
| `TestLeakScan_AllPatternsReject` | `leakscan_test.go` | table-driven: each of the 10 patterns returns a hit with the right category |
| `TestLeakScan_AcceptsCleanContent` | `leakscan_test.go` | content with no patterns → returns no hits |
| `TestLeakScan_NeverReturnsSubstring` | `leakscan_test.go` | match result struct does NOT carry the matched substring (Rule 1 carryover) |
| `TestSizeCap_AcceptsAtBoundary` | `sizecap_test.go` | content == 64 KB → accepted |
| `TestSizeCap_RejectsOverBoundary` | `sizecap_test.go` | content == 64 KB + 1 → rejected |
| `TestErrors_IsClassification` | `errors_test.go` | `errors.Is` correctly classifies wrapped error chains |

#### `internal/dashboardapi/`

| Test | File | Verifies |
|---|---|---|
| `TestServerLifecycle_ServeAndShutdown` | `server_test.go` | server starts, accepts a request, shuts down on ctx cancel |
| `TestAuthMiddleware_RejectsAnonymous` | `auth_test.go` | request without Cookie → 401 AuthExpired |
| `TestAuthMiddleware_RejectsInvalidCookie` | `auth_test.go` | fake validator returns ErrAuthExpired → 401 AuthExpired |
| `TestAuthMiddleware_RejectsExpiredSession` | `auth_test.go` | fake row with expires_at in the past → 401 AuthExpired |
| `TestAuthMiddleware_AcceptsValidCookie` | `auth_test.go` | fake validator returns valid UserID → request reaches the handler with userID in context |
| `TestObjstoreHandler_GetReturnsContent` | `objstore_handler_test.go` | fake objstore.Client.GetCompanyMD returns content+etag → 200 with shape |
| `TestObjstoreHandler_GetReturnsEmptyForMissing` | `objstore_handler_test.go` | fake returns nil content + "" etag → 200 with `{"content": "", "etag": null}` |
| `TestObjstoreHandler_GetSurfacesUnreachable` | `objstore_handler_test.go` | fake returns ErrMinIOUnreachable → 503 with `{"error": "MinIOUnreachable"}` |
| `TestObjstoreHandler_PutHappyPath` | `objstore_handler_test.go` | clean content + valid etag → 200 with new etag |
| `TestObjstoreHandler_PutRejectsLeakScan` | `objstore_handler_test.go` | content with sk- pattern → 422 with category=sk-prefix; substring NOT in response |
| `TestObjstoreHandler_PutRejectsTooLarge` | `objstore_handler_test.go` | 70 KB body → 413 |
| `TestObjstoreHandler_PutRejectsStale` | `objstore_handler_test.go` | fake returns ErrStale → 412 |
| `TestObjstoreHandler_PutRequiresIfMatch` | `objstore_handler_test.go` | request without If-Match header → 400 |
| `TestMempalaceHandler_RecentWritesProxies` | `mempalace_handler_test.go` | fake QueryClient returns rows → 200 with `{"writes": [...]}` |
| `TestMempalaceHandler_ClampsLimitTo100` | `mempalace_handler_test.go` | request with limit=500 → fake called with limit=100 |
| `TestMempalaceHandler_DefaultsLimitTo30` | `mempalace_handler_test.go` | request without limit → fake called with limit=30 |
| `TestMempalaceHandler_SurfacesSidecarUnreachable` | `mempalace_handler_test.go` | fake returns ErrSidecarUnreachable → 503 MempalaceUnreachable |
| `TestMempalaceHandler_RecentKGShape` | `mempalace_handler_test.go` | fake KG response → 200 with triple shape including optional source_ticket_id |
| `TestErrors_JSONShape` | `errors_test.go` | error response struct serialises to the exact spec shape (FR-668's typed errors) |

#### `internal/mempalace/QueryClient`

| Test | File | Verifies |
|---|---|---|
| `TestRecentDrawers_ParsesSidecarResponse` | `queryclient_test.go` | httptest sidecar returns canonical drawer JSON → returns []DrawerEntry with right fields |
| `TestRecentDrawers_TruncatesBodyTo200Chars` | `queryclient_test.go` | 500-char body → preview ≤ 200 chars, UTF-8-safe |
| `TestRecentDrawers_PropagatesSidecarError` | `queryclient_test.go` | httptest returns 500 → returns ErrSidecarUnreachable |
| `TestRecentDrawers_PropagatesNetworkError` | `queryclient_test.go` | unreachable endpoint → returns ErrSidecarUnreachable |
| `TestRecentKGTriples_ParsesSidecarResponse` | `queryclient_test.go` | similar shape coverage for triples |
| `TestRecentKGTriples_HandlesOptionalSourceFields` | `queryclient_test.go` | triple without source_ticket_id → SourceTicketID is nil pointer |

### Integration tests (Go, `//go:build integration`)

#### `internal/objstore/integration_test.go`

testcontainers-go has no built-in MinIO module; use the generic-container API to boot `minio/minio@<digest>` with `MINIO_ROOT_USER` + `MINIO_ROOT_PASSWORD` env vars and a wait-strategy on `/minio/health/live`. Spike §F2 confirms this returns 200 within ~3s.

| Test | Verifies |
|---|---|
| `TestIntegration_BootstrapAgainstRealMinIO` | real container: `BootstrapBucket` creates the bucket on first call, no-ops on second |
| `TestIntegration_GetPutRoundtripWithEtag` | put + get + verify etag matches; second put with stale etag returns ErrStale |
| `TestIntegration_GetMissingReturnsEmpty` | get against an empty bucket → returns `(nil, "", nil)` |
| `TestIntegration_PutRejectsLeakBeforeMinIOCall` | put with sk- pattern → ErrLeakScanFailed; verify no PUT lands at MinIO (object still missing) |

### Vitest tests (dashboard)

Default-environment node, jsdom for the React-rendering tests (M5.3 precedent already established this surface).

#### `dashboard/lib/actions/companyMD.test.ts`

| Test | Verifies |
|---|---|
| `TestGetCompanyMD_HappyPath` | mocked fetch returns 200 + body → returns `{content, etag, error: null}` |
| `TestGetCompanyMD_PropagatesAuthExpired` | mocked fetch returns 401 → returns typed `AuthExpired` error |
| `TestSaveCompanyMD_HappyPath` | mocked fetch returns 200 + new etag → returns `{content, etag, error: null}` |
| `TestSaveCompanyMD_PropagatesStale` | mocked fetch returns 412 + `Stale` body → returns typed `Stale` error |
| `TestSaveCompanyMD_PropagatesLeakScan` | mocked fetch returns 422 + `LeakScanFailed` + category → returns typed error with category |
| `TestSaveCompanyMD_PropagatesTooLarge` | mocked fetch returns 413 → returns typed `TooLarge` error |
| `TestSaveCompanyMD_PropagatesAuthExpired` | mocked fetch returns 401 → returns typed `AuthExpired` error |
| `TestSaveCompanyMD_PropagatesUnreachable` | mocked fetch returns 503 → returns typed `MinIOUnreachable` error |
| `TestSaveCompanyMD_ForwardsCookie` | spy on fetch — Cookie header is forwarded |

#### `dashboard/lib/queries/knowsPane.test.ts`

| Test | Verifies |
|---|---|
| `TestGetRecentPalaceWrites_HappyPath` | mocked fetch returns rows → returns array shape |
| `TestGetRecentPalaceWrites_DefaultLimit` | call without limit → fetch URL contains `limit=30` |
| `TestGetRecentPalaceWrites_PropagatesUnreachable` | mocked 503 → returns typed `MempalaceUnreachable` error |
| `TestGetRecentKGFacts_HappyPath` | mocked fetch returns triples → returns array shape |
| `TestGetRecentKGFacts_PropagatesUnreachable` | mocked 503 → returns typed error |

#### `dashboard/components/features/ceo-chat/`

`@vitest-environment jsdom` directive; `@testing-library/react` rendering.

| Test | File | Verifies |
|---|---|---|
| `TestKnowsPane_RendersThreeTabs` | `KnowsPane.test.tsx` | tab strip has Company.md / Recent palace writes / KG recent facts in order |
| `TestKnowsPane_DefaultsToCompanyMDActive` | `KnowsPane.test.tsx` | first render: Company.md tab pane visible |
| `TestKnowsPane_SwitchesTabsOnClick` | `KnowsPane.test.tsx` | click second tab → second pane renders, first hidden |
| `TestCompanyMDTab_RendersReadOnlyByDefault` | `CompanyMDTab.test.tsx` | initial render: editor is readOnly, Edit button visible |
| `TestCompanyMDTab_FlipsToEditableOnEditClick` | `CompanyMDTab.test.tsx` | click Edit → editor becomes editable, Save+Cancel visible |
| `TestCompanyMDTab_SaveSuccessFlipsBackToReadOnly` | `CompanyMDTab.test.tsx` | mock saveCompanyMD success → editor flips back to readOnly with new content |
| `TestCompanyMDTab_StaleErrorPreservesBuffer` | `CompanyMDTab.test.tsx` | mock saveCompanyMD returns Stale → buffer preserved, inline notice + Refresh-and-discard button visible |
| `TestCompanyMDTab_LeakScanErrorPreservesBuffer` | `CompanyMDTab.test.tsx` | mock saveCompanyMD returns LeakScanFailed with category → buffer preserved, inline notice naming category |
| `TestCompanyMDTab_TooLargeErrorPreservesBuffer` | `CompanyMDTab.test.tsx` | mock saveCompanyMD returns TooLarge → buffer preserved, size-named inline notice |
| `TestCompanyMDTab_AuthExpiredShowsSigninLink` | `CompanyMDTab.test.tsx` | mock saveCompanyMD returns AuthExpired → buffer preserved, inline notice with `/login?next=/chat` link |
| `TestCompanyMDTab_CancelDiscardsBufferAfterConfirm` | `CompanyMDTab.test.tsx` | dirty buffer + Cancel → confirm dialog → confirm → editor flips back to readOnly with original content |
| `TestCompanyMDTab_CancelNoOpsIfNotDirty` | `CompanyMDTab.test.tsx` | clean buffer + Cancel → no confirm dialog; flips back |
| `TestCompanyMDTab_EmptyStateForMissingObject` | `CompanyMDTab.test.tsx` | initial load returns `{content: '', etag: null}` → empty-state hint visible inviting create |
| `TestCompanyMDEditor_RendersReadOnly` | `CompanyMDEditor.test.tsx` | readOnly=true → CodeMirror not editable, content rendered |
| `TestCompanyMDEditor_RendersEditable` | `CompanyMDEditor.test.tsx` | readOnly=false → CodeMirror accepts input |
| `TestCompanyMDEditor_OnChangeFiresOnInput` | `CompanyMDEditor.test.tsx` | typing → onChange called with new value |
| `TestRecentPalaceWritesTab_RendersList` | `RecentPalaceWritesTab.test.tsx` | mock returns 30 entries → 30 rows render |
| `TestRecentPalaceWritesTab_RefreshButtonGreysList` | `RecentPalaceWritesTab.test.tsx` | click Refresh → list at 60% opacity, button disabled, spinner visible |
| `TestRecentPalaceWritesTab_RefreshSuccessRestoresOpacity` | `RecentPalaceWritesTab.test.tsx` | mock resolves → list at full opacity, new data shown |
| `TestRecentPalaceWritesTab_EmptyState` | `RecentPalaceWritesTab.test.tsx` | mock returns 0 entries → empty-state hint |
| `TestRecentPalaceWritesTab_UnreachableShowsTypedError` | `RecentPalaceWritesTab.test.tsx` | mock returns MempalaceUnreachable → typed error block above prior list (full opacity) |
| `TestKGRecentFactsTab_*` | `KGRecentFactsTab.test.tsx` | parallel set covering the same 5 scenarios |

### Playwright tests

Single new spec file `dashboard/tests/integration/m5-4-knows-pane.spec.ts` reusing the M5.x `_chat-harness.ts` fixture. Three sub-scenarios:

| Test | Verifies |
|---|---|
| `TestM5_4_GoldenPath_EditAndSave` | open `/chat`, Company.md tab visible with seeded content; click Edit; type; click Save; verify content updates without page reload |
| `TestM5_4_LeakScanRejectsSecret` | open Edit; paste `sk-1234567890abcdef1234567890`; click Save; verify inline error block names `sk-prefix` category; verify object NOT updated server-side |
| `TestM5_4_PalaceTabRefresh` | switch to Recent palace writes tab; verify list renders; click Refresh; verify list re-fetches |

The harness gains a `seedMinIOCompanyMD(content)` helper that pre-populates the bucket via the `mc` CLI before the test runs.

### Architecture amendment test

`dashboard/tests/architecture-amendment.test.ts` extends with three `expect(content).toContain(...)` assertions — one per amendment per ARCH-1.

### Regression check

- Existing `internal/health/Server` tests pass unchanged.
- Existing `internal/mempalace/Client` tests pass unchanged (the new QueryClient is additive).
- Existing M5.x chat tests pass unchanged (KnowsPanePlaceholder removal does not affect any other test target — verified by /garrison-tasks before deleting the file).
- All Playwright specs pre-M5.4 stay green (the new MinIO container in compose is additive; the supervisor's new dashboardapi server runs on a separate port and doesn't intercept anything pre-M5.4 cared about).

---

## Open questions remaining for /garrison-tasks

These are items the plan defers to /garrison-tasks because they're refinement-level, not structural:

- Exact `internal/finalize` surface for leak-scan reuse. If `scanAndRedactPayload` is exported with a usable signature, `internal/objstore/` imports it directly. If not, /garrison-tasks T-numbered task extracts the patterns into `internal/leakscan/` and updates both call sites. Either way, no new patterns are added — the 10-pattern set carries unchanged.
- Exact MemPalace sidecar API for "recent across wings" (native endpoint vs supervisor-side aggregation). /garrison-tasks confirms by re-reading `docs/research/m2-spike.md` Part 2 + the existing `internal/mempalace/Client` HTTP shape.
- Exact better-auth session cookie name. Verify against `dashboard/lib/auth/` config; fall through to documented default if unambiguous.
- CodeMirror dark-mode theme integration: M3 design tokens vs CodeMirror's `oneDark` preset. /garrison-tasks picks based on whether the M3 token set has a Markdown-friendly mapping; if not, `oneDark` is a defensible default.
- Whether the supervisor pre-resolves `companyId` at startup (single-row companies table read once) or per-request (extra Postgres query). Default lean: pre-resolve at startup, cache on `dashboardapi.Server`. /garrison-tasks confirms.
- Marker-only migration shape: keep as a literal SELECT-only migration, or use a Goose `+goose StatementBegin` block with a comment. Tasks-level cleanup; spec-level no-op either way.

---

## What this plan does not pre-decide

- Function-body content (per the plan-skill rule).
- Operator-checklist phrasing (lands as a /garrison-implement task, not a plan decision).
- M5.4 retro content (lands at end of /garrison-implement).
- M6 hooks beyond the one-line forward-compat note: M6's CEO ticket-decomposition work can read from the same Company.md MinIO object via the same `objstore.Client` that M5.4 introduces; no API change needed.

---

## Spec-kit flow next

`/garrison-tasks m5.4` — split this plan into T-numbered, agent-followable tasks. Then `/speckit.analyze`, then `/garrison-implement m5.4`, then the M5.4 retro that closes the M5 arc.
