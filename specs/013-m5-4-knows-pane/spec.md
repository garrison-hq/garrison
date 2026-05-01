# Feature specification: M5.4 — "WHAT THE CEO KNOWS" knowledge-base pane

**Feature branch**: `013-m5-4-knows-pane`
**Created**: 2026-05-01
**Status**: Draft
**Input**: see [`specs/_context/m5-4-context.md`](../_context/m5-4-context.md) and [`docs/research/m5-4-spike-minio.md`](../../docs/research/m5-4-spike-minio.md). This spec is the thin layer above the context — structure for /speckit.clarify and /garrison-plan, not re-derivation of the binding decisions the context already made.

## Clarifications

### Session 2026-05-01

- Q: Editor library for the Company.md surface (CodeMirror v6 via wrapper, raw v6 packages, or react-simple-code-editor + prismjs)? → A: `@uiw/react-codemirror` + `@codemirror/lang-markdown` (React wrapper around CodeMirror v6; batteries-included; ~80 KB gzipped).
- Q: Auth-expiry mid-edit behaviour for Company.md `Save`? → A: Server returns typed `AuthExpired` error; the editor preserves the unsaved buffer and surfaces an inline "session expired; sign in again to save" notice with a sign-in link that returns to `/chat` post-auth. No auto-redirect (which would lose the buffer).
- Q: Refresh-button UX for palace-writes + KG-facts tabs during in-flight fetch? → A: Keep the prior list visible at 60% opacity, disable the `Refresh` button, show an inline spinner next to it. Matches M3 list-refresh precedent (`/agents`, `/tickets` lists).
- Q: Next.js mechanism for Company.md read + save (Server Actions, mixed RSC + Action, or HTTP API routes)? → A: Server Actions for both — `getCompanyMD()` + `saveCompanyMD(content, ifMatchEtag)` in `lib/actions/companyMD.ts`. Matches M3/M4/M5.2 dashboard-internal precedent.
- Q: Where does Company.md read/save physically happen — dashboard or supervisor-proxy? → A: Supervisor-side HTTP proxy (`GET /api/objstore/company-md`, `PUT /api/objstore/company-md`); the dashboard Server Actions are thin wrappers calling the proxy. Symmetric with the MemPalace proxy (FR-680..686) — only the supervisor process holds Infisical-scoped MinIO creds; no Node MinIO SDK dependency on the dashboard side.

## User scenarios & testing

### User story 1 — operator reads Company.md in the chat right pane (priority: P1)

The operator opens `/chat` (any session). The right pane (currently the `KnowsPanePlaceholder` from M5.2) renders a tabbed component. The default-active tab is `Company.md`. The pane fetches the current `company.md` object from MinIO and displays its Markdown content with syntax highlighting (no rendered-HTML preview — the raw Markdown is what's shown, color-coded). The display is read-only by default; an inline `Edit` affordance switches the surface into editable mode.

**Why this priority**: Company.md is the operator's strategic context. The chat container is summoned per-message and the operator may want to glance at the document while composing a thought without leaving `/chat`. Without P1 the operator either keeps Company.md somewhere outside the dashboard (defeating the milestone) or doesn't reference it at all. Closes the M5.2 placeholder gap on the operator's most-used surface.

**Independent test**: Boot the stack with a seeded `company.md` object in the MinIO bucket. Navigate to `/chat`. Right pane renders the tab strip with `Company.md` active, the rendered surface shows the Markdown contents with syntax highlighting on heading marks / code fences / links. No edit is possible without clicking the affordance.

**Acceptance scenarios**:

1. **Given** a seeded `company.md` object, **when** the operator opens `/chat`, **then** the right pane shows the `Company.md` tab active, the syntax-highlighted Markdown content, and a non-active `Edit` affordance.
2. **Given** an empty / missing `company.md` object, **when** the operator opens `/chat`, **then** the right pane shows an empty-state hint inviting the operator to create the document via the `Edit` affordance, with no error surfaced.
3. **Given** the MinIO container is unreachable, **when** the operator opens `/chat`, **then** the pane renders a typed error block (mirroring the M5.2 `ChatErrorBlock` shape) and the rest of the chat surface stays functional.

---

### User story 2 — operator edits Company.md and sees the change immediately (priority: P1)

The operator clicks `Edit` on the `Company.md` tab. The surface switches into editable mode (same component, `readOnly: false`). The operator types changes; the editor surfaces syntax highlighting in real time. The operator clicks `Save`. The dashboard PUTs the new content to MinIO with an `If-Match` header carrying the ETag the editor opened with. On 200 success the editor receives the new ETag + saved content, switches back to read-only mode, and shows the just-committed content. On 412 Precondition Failed the editor surfaces "this document was changed elsewhere; refresh to load the latest version" inline, the editor stays in editable mode, and the unsaved changes stay in the buffer.

**Why this priority**: Company.md being editable from the dashboard is what makes this milestone valuable beyond a read-only knowledge surface. Without it the operator edits Company.md outside the dashboard (no place to do that today — there's no host volume mount, the file doesn't exist anywhere yet). Same priority as P1 because the editable + readable flows are inseparable for the milestone to be useful.

**Independent test**: Boot the stack. Navigate to `/chat`, click `Edit` on the Company.md tab, type changes, click `Save`. The editor flips back to read-only mode showing the new content. Open a second browser window, edit + save first; in the original window, edit + save → expect a "stale" notice and unsaved changes preserved.

**Acceptance scenarios**:

1. **Given** the operator opened the Company.md tab, **when** they click `Edit`, **then** the surface flips to editable, the saved content is in the buffer, and a `Save` button + `Cancel` button are visible.
2. **Given** the operator typed a change and clicks `Save`, **when** MinIO accepts the PUT (matching `If-Match`), **then** the editor flips to read-only with the new content + new ETag and surfaces a brief "saved" toast.
3. **Given** another window saved a different change first, **when** the operator clicks `Save`, **then** MinIO returns 412 Precondition Failed, the editor surfaces an inline "stale" notice, the unsaved changes stay in the buffer, and the operator may explicitly `Refresh and discard my changes` or copy out the buffer before refreshing.
4. **Given** the operator's content contains a verbatim secret-shape value (`sk-…`, `xoxb-…`, AWS `AKIA…`, GitHub PAT, PEM header, bearer-shape, etc.), **when** they click `Save`, **then** the dashboard server action runs the M2.3/M5.3 `scanForSecrets` set against the proposed content, rejects the save with a typed `LeakScanFailed` error, and the editor surfaces the rejection inline. **No PUT lands at MinIO.**
5. **Given** the operator's content exceeds the 64 KB cap, **when** they click `Save`, **then** the dashboard server action rejects with a typed `TooLarge` error and the editor surfaces "Company.md is capped at 64 KB; current size is N KB."
6. **Given** the operator clicks `Cancel` while in editable mode, **when** they confirm the discard, **then** the editor flips back to read-only with the originally-loaded content and the unsaved buffer is dropped.

---

### User story 3 — operator browses recent palace writes (priority: P2)

The operator clicks the `Recent palace writes` tab. The pane fetches the most recent 30 palace drawer entries (across all wings/rooms) and renders them as a list with timestamp, source (drawer name / source agent), and a truncated prose preview (~200 chars). The list is recency-ordered, newest first. A `Refresh` affordance re-fetches on demand.

**Why this priority**: Operator visibility into the palace is observability, not a primary workflow. The operator doesn't need this to get value out of the chat; but the palace is opaque without it. P2 because the milestone is incomplete without it but the chat itself works without it.

**Independent test**: Boot the stack with seeded palace drawers. Click the tab. List renders with timestamp + source + preview, ordered newest first. Click `Refresh`; list re-fetches.

**Acceptance scenarios**:

1. **Given** ≥30 palace drawers exist, **when** the operator clicks the `Recent palace writes` tab, **then** the list renders the 30 most-recent entries with timestamp / source / preview, newest first.
2. **Given** <30 palace drawers exist, **when** the operator clicks the tab, **then** all entries render in recency order with no padding / no error.
3. **Given** zero palace drawers exist, **when** the operator clicks the tab, **then** an empty-state hint renders ("No palace writes yet — agents will record their work here.").
4. **Given** the MemPalace HTTP sidecar is unreachable, **when** the operator clicks the tab, **then** a typed error renders ("MemPalace is unavailable — the activity feed and chat continue to work; this pane will populate once MemPalace responds again."). No retry storm; user's `Refresh` click triggers the next attempt.
5. **Given** the operator clicks `Refresh`, **when** the supervisor proxy returns updated rows, **then** the rendered list reflects the new data without requiring a page reload.

---

### User story 4 — operator browses recent KG facts (priority: P2)

The operator clicks the `KG recent facts` tab. The pane fetches the most recent 30 KG triples and renders them as a list with timestamp, `subject — predicate — object` triple, and (where present) a deep-link to the source ticket / agent / wing. Recency-ordered, newest first. `Refresh` re-fetches.

**Why this priority**: Same shape as user story 3 — observability into the knowledge graph layer agents are populating. P2 alongside palace-writes for the same reason.

**Independent test**: Boot the stack with seeded KG triples. Click the tab. List renders triples with timestamp + S/P/O + optional deep-link, ordered newest first.

**Acceptance scenarios**:

1. **Given** ≥30 KG triples exist, **when** the operator clicks the `KG recent facts` tab, **then** the list renders the 30 most-recent triples in `subject — predicate — object` form with timestamps.
2. **Given** a triple's `source` metadata references a ticket, **when** the row renders, **then** the row includes a deep-link to `/tickets/<id>`; otherwise no deep-link is shown for that row.
3. **Given** zero KG triples exist, **when** the operator clicks the tab, **then** an empty-state hint renders.
4. **Given** the MemPalace KG endpoint returns an error, **when** the operator clicks the tab, **then** a typed error renders identical in shape to user story 3 acceptance #4.

## Functional requirements

The spec uses the same FR-NNN convention as M5.1–M5.3. M5.4 reserves FR-600 → FR-699.

### Pane wiring (FR-600 → FR-619)

- **FR-600**: The `KnowsPanePlaceholder` component (M5.2 ship surface) is replaced by a new `KnowsPane` component rendering a tab strip + per-tab content area.
- **FR-601**: The tab strip exposes three tabs in this order: `Company.md`, `Recent palace writes`, `KG recent facts`. Default-active tab on first render is `Company.md`.
- **FR-602**: Tab state is local React state, not URL-routed. Switching tabs does not change `/chat/<sessionId>` URL semantics from M5.2.
- **FR-603**: The pane lives inside the existing M5.2 three-pane layout's right column. At `<1024px` viewport width the right pane collapses to the M5.2 header strip; the `KnowsPane` is hidden in that mode.
- **FR-604**: The pane fetches its content on first render of each tab. Subsequent tab-switches re-use the in-React state without re-fetching unless the operator clicks `Refresh`.
- **FR-605**: The pane renders independently per tab — a fetch failure on one tab does not break the other two tabs.

### Company.md storage + read path (FR-620 → FR-639)

- **FR-620**: Company.md is stored as a single object in a MinIO bucket named `garrison-company`. The object key is `<companyId>/company.md`, where `<companyId>` is the UUID of the row in the existing `companies` table. Single-company today (one row); the key shape is forward-compatible with multi-company.
- **FR-621**: The `companies.company_md TEXT NOT NULL` column documented in ARCHITECTURE.md (line 151) is NOT added to the schema. M5.4 amends ARCHITECTURE.md to remove that column from the documented schema and reference the MinIO object instead.
- **FR-622**: The dashboard reads + saves Company.md via Next.js **Server Actions** in `lib/actions/companyMD.ts` — `getCompanyMD()` and `saveCompanyMD(content, ifMatchEtag)` (clarified Session 2026-05-01; matches M3/M4/M5.2 dashboard-internal precedent). The Server Actions are **thin wrappers around supervisor-side HTTP endpoints** (FR-688..691) — the dashboard process never imports a MinIO SDK and never holds MinIO credentials; only the supervisor talks to MinIO. `getCompanyMD()` returns `{ content: string, etag: string }` to the client component; `saveCompanyMD()` returns `{ content: string, etag: string }` on success or a typed error (`Stale | LeakScanFailed | TooLarge | AuthExpired | MinIOUnreachable`) on rejection.
- **FR-623**: The MinIO client uses **scoped service-account credentials** fetched from Infisical at startup, not the MinIO root credentials. See FR-660.
- **FR-624**: If the requested object does not exist, the Server Action returns `{ content: '', etag: null }` and the editor renders an empty-state inviting the operator to create the document.
- **FR-625**: If MinIO returns a non-404 error (network failure, auth failure, etc.), the Server Action returns `{ error: 'MinIOUnreachable' | 'MinIOAuthFailed' | 'MinIOUnknown' }`. The editor renders a typed error block; the rest of the dashboard remains functional (FR-605 carryover).

### Company.md edit path (FR-640 → FR-659)

- **FR-640**: The editor is a syntax-highlighted Markdown surface. Implementation uses **CodeMirror v6 via `@uiw/react-codemirror` + `@codemirror/lang-markdown`** (clarified Session 2026-05-01). Same component instance is used for both read and edit modes; mode switches via `readOnly: boolean`.
- **FR-641**: Clicking `Edit` sets the editor to `readOnly: false`, focuses the textarea, and exposes `Save` + `Cancel` buttons. The buffer is initialised from the most recently fetched content; the read-time ETag is captured for use in the `If-Match` header at save time.
- **FR-642**: Clicking `Save` invokes a `saveCompanyMD(content, ifMatchEtag)` Server Action that:
  - Validates content size ≤ 64 KB; rejects with typed `TooLarge` error if not.
  - Runs the M2.3/M5.3 `scanForSecrets` set (10 patterns: sk-prefix, xoxb, AWS `AKIA`, PEM header, GitHub PAT/App/User/Server/Refresh, bearer-shape) against the content; rejects with typed `LeakScanFailed` error if any pattern hits. The reject message names the matched pattern category but NOT the matched substring (Rule 1 carryover — never echo the secret).
  - PUTs the content to MinIO with `If-Match: <etag>`; on 412 returns typed `Stale` error.
  - On MinIO success returns `{ content: string, etag: string }` from the PUT response so the client renders the saved state without a second round-trip.
- **FR-643**: On `Stale` error the editor surfaces an inline notice ("This document was changed elsewhere; refresh to load the latest version") and an explicit `Refresh and discard my changes` button. The unsaved buffer is preserved until the operator confirms.
- **FR-644**: On `LeakScanFailed` error the editor surfaces "Save rejected: a value matching `<pattern category>` was detected in the content. Remove it before saving." The unsaved buffer is preserved.
- **FR-645**: On `TooLarge` error the editor surfaces "Company.md is capped at 64 KB; current size is N KB." The unsaved buffer is preserved.
- **FR-646**: Clicking `Cancel` flips the editor back to `readOnly: true` with the originally-loaded content; the unsaved buffer is dropped. If unsaved changes exist, a confirm dialog gates the discard.
- **FR-647**: If the operator's better-auth session has expired by the time `Save` is clicked, the Server Action rejects with a typed `AuthExpired` error (clarified Session 2026-05-01). The editor:
  - Stays in editable mode, the unsaved buffer is **preserved** (no redirect that would discard it).
  - Surfaces an inline notice ("Your session expired — sign in again to save your changes.") with a sign-in link that routes through `/login?next=/chat` so the operator returns to `/chat` post-auth and can re-click `Save`.
  - The supervisor / dashboard logs the failed save attempt at info level (event-shape only — no body bytes, no scan-result detail).

### MinIO container (FR-660 → FR-679)

- **FR-660**: The `docker-compose.yml` adds a 4th service `minio` joining the existing `garrison-net` Docker network alongside `supervisor`, `mempalace`, and `socket-proxy`. Image is digest-pinned to the spike-time digest `sha256:69b2ec208575b69597784255eec6fa6a2985ee9e1a47f4411a51f7f5fdd193a9` (`minio/minio` upstream). /garrison-plan refreshes the digest if a newer release is preferred at implement time.
- **FR-661**: MinIO data is stored in a Docker named volume `garrison-minio-data`, mounted into the container at `/data`. No host bind mounts.
- **FR-662**: The MinIO root credentials (`MINIO_ROOT_USER` / `MINIO_ROOT_PASSWORD`) are passed as environment variables to the MinIO container itself — they live in the operator's deploy environment / `.env` file and never leave the host. They are not stored in Infisical.
- **FR-663**: A scoped MinIO service account is created post-deploy via the operator running `mc admin user svcacct add` (ops checklist step). The access key + secret key from that service account are stored in Infisical under a new path (e.g. `/companies/<companyId>/minio` with keys `MINIO_ACCESS_KEY` + `MINIO_SECRET_KEY`).
- **FR-664**: The supervisor fetches the scoped access key + secret from Infisical at startup using the existing `internal/vault.Client` path. The fetch writes a `vault_access_log` row inside the startup tx (M2.3 Rule 4 carryover, fail-closed on INSERT failure). **The dashboard process never reads MinIO credentials** (clarified Session 2026-05-01) — Company.md flows through the supervisor's HTTP proxy (FR-668..671) instead, keeping the dashboard out of the MinIO credential surface entirely.
- **FR-665**: The supervisor runs a startup probe (under `internal/objstore/`) that:
  - Reads the scoped credentials from Infisical via `vault.Client`.
  - Constructs a MinIO client.
  - Calls `BucketExists(ctx, "garrison-company")`.
  - If false, calls `MakeBucket(ctx, "garrison-company", ...)`.
  - Logs the bootstrap outcome (created / already-exists) at info level.
  - Returns a non-nil error if MinIO is unreachable; the supervisor exits with `ExitFailure` per AGENTS.md fail-closed pattern.
- **FR-666**: The MinIO container does NOT have a Docker-level healthcheck baked into the compose service; the supervisor's startup probe (FR-665) is the actual readiness signal. /garrison-plan may add a Compose-level `healthcheck` if there's a downstream `depends_on: condition: service_healthy` need.
- **FR-667**: The MinIO API port (9000) is internal to `garrison-net` only; no host port forwarding in production. The development compose layer may forward ports `9000` + `9001` (admin console) for local debugging — that is a `dev` profile concern, not in the production shape.

#### Supervisor-side objstore HTTP proxy (FR-668 → FR-671)

Symmetric with the MemPalace proxy (FR-680..687). Keeps the dashboard process out of MinIO's credential surface — only the supervisor talks to MinIO.

- **FR-668**: The supervisor exposes two new HTTP endpoints for the dashboard:
  - `GET /api/objstore/company-md` — returns `{ content: string, etag: string }` for the current Company.md object. Returns `{ content: '', etag: null }` (HTTP 200) if the object does not yet exist (empty-state UX path).
  - `PUT /api/objstore/company-md` — accepts `Content-Type: text/markdown` body + `If-Match: <etag>` header. On success returns `{ content: string, etag: string }` (echoing the body + the new ETag from MinIO). On rejection returns a typed error response with `{ error: 'Stale' | 'LeakScanFailed' | 'TooLarge' | 'AuthExpired' | 'MinIOUnreachable', message?: string, pattern_category?: string }` and an appropriate HTTP status (412 / 422 / 413 / 401 / 503).
- **FR-669**: The supervisor proxy authenticates inbound requests via the existing dashboard ↔ supervisor auth path (dashboard role + better-auth session). Anonymous access is rejected with HTTP 401 (`AuthExpired`).
- **FR-670**: The PUT endpoint runs the leak-scan (FR-642 third bullet) + size-cap (FR-642 first bullet) **before** the MinIO PUT. Server-side enforcement is the source of truth; the dashboard Server Action calls into this endpoint and surfaces the typed errors back to the client without doing its own scan or size-check.
- **FR-671**: The objstore proxy logs each request shape (verb + outcome + ETag-before / ETag-after) at info level. **Body bytes are NEVER logged** (SEC-5 carryover) — the leak-scan reject log line names the matched pattern *category* but not the matched substring (Rule 1 carryover from FR-642).

### MemPalace read transport (FR-680 → FR-699)

- **FR-680**: The dashboard does NOT call MemPalace's HTTP sidecar directly. Reads go through a supervisor-side HTTP proxy.
- **FR-681**: The supervisor exposes two new HTTP endpoints for the dashboard:
  - `GET /api/mempalace/recent-writes?limit=N` — returns the most recent N drawer entries with `{ id, drawer_name, room_name, wing_name, source_agent_role_slug, written_at, body_preview }` shape. `body_preview` is truncated to ≤200 chars.
  - `GET /api/mempalace/recent-kg?limit=N` — returns the most recent N KG triples with `{ id, subject, predicate, object, source_ticket_id?, source_agent_role_slug?, written_at }` shape.
- **FR-682**: The supervisor proxy authenticates inbound requests via the existing dashboard ↔ supervisor auth path (dashboard role + better-auth session). Anonymous access is rejected with 401.
- **FR-683**: The supervisor proxy calls into the existing `internal/mempalace.Client` HTTP client to talk to the MemPalace sidecar. No new MemPalace MCP tools are introduced.
- **FR-684**: If the MemPalace sidecar returns an error or is unreachable, the proxy returns a typed error response `{ error: 'MempalaceUnreachable' | 'MempalaceUnknown' }` with a 503 status. The dashboard renders the typed error block (user story 3 / 4 acceptance #4).
- **FR-685**: Default `limit` is 30 if the dashboard does not supply one; the proxy clamps `limit` to ≤ 100 to bound the response size.
- **FR-686**: The proxy does NOT introduce any new MemPalace write paths. The dashboard cannot write palace drawers or KG triples through M5.4 surfaces; those remain agent-side.
- **FR-687**: The `Recent palace writes` and `KG recent facts` tabs each expose a `Refresh` button that re-fetches their respective endpoint. While a fetch is in flight (clarified Session 2026-05-01):
  - The previously rendered list stays visible at 60% opacity (`opacity: 0.6` or equivalent design-token).
  - The `Refresh` button is `disabled` for the duration of the request.
  - An inline spinner renders next to the button (M3 `/agents`, `/tickets` list-refresh precedent — same affordance shape).
  - On success, the list swaps to the new data and full opacity returns.
  - On error, the prior list stays visible at full opacity and the typed error block (FR-684 / acceptance scenarios #4) renders inline above the list.

## Non-functional requirements

- **NFR-1**: Company.md read latency on the dashboard is bounded by the supervisor's MinIO `GetObject` call. The spike (§F4) showed sub-100ms reads on a local single-node deployment. M5.4 has no formal latency SLO; if the operator surfaces lag during operator-week-of-use, post-M5 may layer in client-side caching.
- **NFR-2**: Concurrent edits across two browser windows are detected via ETag/If-Match (FR-642 + FR-643). Last-writer-wins is acceptable in the single-operator-per-Constitution-X case; the ETag check upgrades silent overwrite to surfaced staleness.
- **NFR-3**: The MinIO container restart-recovery preserves the bucket and the Company.md object as long as the named volume `garrison-minio-data` is intact (spike §F4). `docker compose down -v` (which removes named volumes) is a destructive op outside M5.4's scope; the ops checklist documents this.
- **NFR-4**: The supervisor's MinIO startup probe (FR-665) is the only fail-closed gate for MinIO reachability. If MinIO is unreachable at boot, the supervisor exits with `ExitFailure` and the operator must restart MinIO before the supervisor can serve. Mid-runtime MinIO failure surfaces as user-story-1 acceptance #3 (typed error block on the pane, rest of dashboard unaffected).
- **NFR-5**: The dashboard's MemPalace proxy endpoints (FR-681) do not poll. The dashboard fetches once per tab-mount + on operator `Refresh`. No polling, no SSE, no WebSocket. Fits the static-reads-with-refresh model recorded in the context.
- **NFR-6**: Concurrency rules from AGENTS.md §"Concurrency discipline" apply unchanged. The new `internal/objstore/` package threads `context.Context` through every MinIO call. The startup probe respects ctx cancellation and short-circuits if the supervisor is signalling shutdown mid-bootstrap.

## Security requirements

- **SEC-1**: Company.md content is operator-authored. M5.4 applies the M2.3/M5.3 leak-scan (FR-642 third bullet) to reject saves containing verbatim secret values. Reuses `internal/finalize.scanAndRedactPayload`'s 10-pattern set unchanged.
- **SEC-2**: MinIO root credentials never leave the operator's deploy environment. Scoped service-account credentials live in Infisical; their fetch follows M2.3 Rule 4 (audit row per fetch, fail-closed on INSERT). Vault remains opaque to the dashboard process — the dashboard reads scoped MinIO creds from Infisical via the same supervisor-mediated path used for other dashboard secrets.
- **SEC-3**: The MemPalace proxy does NOT expose admin endpoints, write endpoints, or bulk-export endpoints. Only the two read endpoints in FR-681. The proxy code in `internal/mempalace_proxy/` (or similar — /garrison-plan picks the package name) explicitly enumerates exposed paths; a default-deny shape rejects anything else with 404.
- **SEC-4**: The leak-scan (SEC-1) reject message names the matched pattern *category* ("a value matching `aws-access-key` was detected") but NEVER echoes the matched substring back to the editor. M2.3 Rule 1 carryover — the operator's UX for finding-the-violating-line is "scan your own buffer."
- **SEC-5**: Company.md content is NOT logged at any level by the supervisor or dashboard. The `internal/objstore/` client logs object key + ETag + outcome only; no body bytes. The `vaultlog` analyzer-allowed patterns from M2.3 do not apply to MinIO (no SecretValue type involvement); standard slog hygiene applies.

## Architecture amendment

- **ARCH-1**: ARCHITECTURE.md is amended in the same PR as M5.4 implementation:
  - Section "Schema" (line 151) — remove `company_md TEXT NOT NULL` from `companies` table; add a sentence "`companies.company_md` is no longer a Postgres column; Company.md content lives at `s3://garrison-company/<companyId>/company.md` in the MinIO sidecar (M5.4)."
  - Section "M5 — CEO chat (summoned)" (line 575) — extend the M5.4 sentence: "M5.4 ships the 'WHAT THE CEO KNOWS' knowledge-base pane: tabbed surface for Company.md (MinIO-backed, CEO-editable) + recent palace writes + recent KG facts (read-only via supervisor-side proxy to MemPalace)."
  - Section "Deployment topology" — add MinIO as a 4th container alongside supervisor + mempalace + socket-proxy. Note the named volume + scoped-service-account credential model.
- **ARCH-2**: `dashboard/tests/architecture-amendment.test.ts` extends the substring-match assertion set to pin:
  - The MinIO line in the deployment-topology amendment.
  - The `s3://garrison-company/<companyId>/company.md` reference in the schema section.
  - The new M5.4 sentence in the M5 build-plan section.
  Test failure blocks merge — same FR-501 carryover from M5.3.

## Out of scope

The context's §"Out of scope" + §"What this milestone is NOT" sections are binding here unchanged. This spec does not relitigate them. Summary, for completeness:

- LLM-mediated summarisation of palace / KG content
- SSE / live-tailing of palace writes
- Multi-document Company.md
- Per-thread context-token counter (deferred to M6)
- Always-in-context wiring of Company.md into chat prompts (Option B per context — defer to a future milestone with its own threat-model treatment)
- Pinning / curation UI for palace writes or KG facts
- Multi-operator visibility
- Workspace-sandboxing fix (tracked in `docs/issues/agent-workspace-sandboxing.md`)
- MinIO backup / replication design

## Locked-deps additions

Per AGENTS.md soft-rule on dependencies, M5.4 adds:

- **`github.com/minio/minio-go/v7`** (Go, supervisor side) — MinIO Go SDK. Justified per spike §F8: lighter than `aws-sdk-go-v2/s3`, upstream-maintained by the same team that ships the MinIO server, supports MinIO server semantics exactly. Alternatives considered: `aws-sdk-go-v2/s3` (rejected as heavier with extra surface), raw `net/http` + S3 sig-v4 (rejected as effectively reimplementing the SDK).
- **`@uiw/react-codemirror` + `@codemirror/lang-markdown`** (TypeScript, dashboard side) — syntax-highlighted Markdown editor surface. Justified: M5.4 needs a single-component editor that toggles between read-only and editable modes with Markdown syntax coloring. Alternatives considered: plain `<textarea>` (rejected — no syntax highlighting per operator preference); `react-simple-code-editor` + `prismjs` (rejected — older API, less React-native); Monaco Editor (rejected — heavier dependency footprint, more capability than M5.4 needs).

The retro flags both additions per AGENTS.md ("New dependencies must also be flagged in the milestone retro").

## What this spec does NOT pre-decide (handed to /garrison-plan)

- Package layout for `internal/objstore/` (the supervisor-side MinIO client wrapper) and `internal/mempalace_proxy/` (the supervisor-side proxy endpoints) — file names, function signatures, package boundaries.
- Dashboard component file layout for `KnowsPane` + sub-components.
- Database migration plan — M5.4 does NOT add new Postgres columns or tables (Company.md lives in MinIO, palace + KG reads go through the proxy without mirroring), so migration scope is limited to documenting the schema-amendment in `ARCHITECTURE.md`.
- Exact CodeMirror v6 import set (`@uiw/react-codemirror` is a wrapper around the underlying CodeMirror v6 packages; /garrison-plan may import the underlying packages directly if the wrapper adds no value).
- Operations-checklist text for the post-deploy `mc admin user svcacct add` step.
- Test fixture seeds for MinIO + MemPalace integration tests.

## What this spec hands to /speckit.clarify

After full reading of binding inputs, the only genuine ambiguity is:

- *(Resolved Session 2026-05-01 — see Clarifications)*

Items the context did NOT pre-decide that this spec resolved with stated rationale (do NOT re-clarify):

- Bucket name (`garrison-company`)
- Object key shape (`<companyId>/company.md`)
- Default `limit` for palace + KG reads (30; clamped to 100)
- Read-after-edit refresh mechanism (server returns saved content + new ETag in PUT response — single round-trip)
- MemPalace transport (supervisor-side proxy, not direct dashboard ↔ mempalace)
- Sidebar entry (none — pane only)
- Company.md size cap (64 KB)
- Edit-conflict semantics (ETag/If-Match)
- Always-in-context chat-prompt wiring (Option B — defer)
- Per-thread context-token counter (defer to M6)

## Spec-kit flow next

1. `/speckit.clarify` — one item flagged (C1 above) plus any genuine ambiguity discovered while drafting the plan.
2. `/garrison-plan m5.4`.
3. `/garrison-tasks m5.4`.
4. `/speckit.analyze`.
5. `/garrison-implement m5.4`.
6. M5.4 retro at `docs/retros/m5-4.md`, palace mirror, ARCHITECTURE.md amendment + test pin in the same PR. M5 closes; M6 starts from the resulting substrate.
