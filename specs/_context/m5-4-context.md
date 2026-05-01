# M5.4 — "WHAT THE CEO KNOWS" knowledge-base pane (context)

**Status**: context for `/speckit.specify`. M5.3 has shipped (PR #12 merged 2026-05-01); main is at the post-M5.3 substrate.

**Prior milestone**: [`docs/retros/m5-3.md`](../../docs/retros/m5-3.md) — chat-driven mutations under autonomous-execution posture. M5.3 left the right-pane placeholder as-is and named M5.4 as the milestone that takes Company.md / Recent palace writes / KG recent facts.

**M5 decomposition**: M5.1 → M5.2 → M5.3 → **M5.4**. M5 closes after M5.4; M6 (CEO ticket decomposition + hygiene) is the next milestone.

**Binding inputs** (read before specifying):
- [`ARCHITECTURE.md`](../../ARCHITECTURE.md) — M5 section (line 575), companies table schema (line 151 documents a `company_md TEXT NOT NULL` column), three-container deployment topology, "always-in-context document" framing.
- [`docs/retros/m5-1.md`](../../docs/retros/m5-1.md), [`docs/retros/m5-2.md`](../../docs/retros/m5-2.md), [`docs/retros/m5-3.md`](../../docs/retros/m5-3.md) — chat-stack substrate and what each milestone deferred to M5.4.
- [`docs/research/m2-spike.md`](../../docs/research/m2-spike.md) Part 2 — MemPalace MCP server lifecycle and query semantics. Determines what tools the dashboard can call against the palace.
- [`docs/security/chat-threat-model.md`](../../docs/security/chat-threat-model.md) — M5.3 amended threat model. Rule 1 (no agent.md may contain a verbatim secret) is the precedent for whether Company.md edits need a leak-scan.
- [`AGENTS.md`](../../AGENTS.md) — locked-deps soft rule (M5.4 introduces MinIO, see §Scope deviation), Concurrency discipline, Spec-kit workflow.
- [`RATIONALE.md`](../../RATIONALE.md) §13 — research-spike rule. M5.4 ships with a pre-spec spike (`docs/research/m5-4-spike-minio.md`) per this rule.
- **Spike output** (to be produced before `/speckit.specify`): `docs/research/m5-4-spike-minio.md`. Covers MinIO container behaviour, credential model, restart-recovery, bucket lifecycle, and Go SDK choice. Binding input to the spec.

---

## Scope deviation from committed docs

Two deviations from the existing ARCHITECTURE.md state that the operator has chosen to make in M5.4:

### 1. Company.md storage: MinIO bucket, not Postgres TEXT column

ARCHITECTURE.md line 151 documents the schema:

```sql
CREATE TABLE companies (
  id UUID PRIMARY KEY,
  name TEXT NOT NULL,
  company_md TEXT NOT NULL,      -- CEO's always-in-context document
  ...
);
```

In code, the M2.1 migration creates `companies` with `id / name / created_at` only — the `company_md` column has never landed. The architecture-docs description was aspirational. M5.4 is the milestone that actually wires Company.md into the system.

**Operator's decision (recorded here)**: Company.md will live in a new MinIO bucket, not as a Postgres TEXT column. Rationale: the operator has chosen not to use host-volume mounts for the dashboard/supervisor containers (workspace-sandboxing concerns are tracked separately in `docs/issues/agent-workspace-sandboxing.md`), so the file-on-disk path that ARCHITECTURE.md hints at (M3 wireframe says "842 lines · edit") doesn't apply. MinIO gives object storage with the same operational shape as Postgres (containerised, networked, credentialed) without the volume-mount surface.

**Consequence**: ARCHITECTURE.md must be amended in M5.4 alongside the implementation. The amendment removes `company_md TEXT NOT NULL` from the documented schema and replaces it with a reference to the MinIO `company-md` object (key path TBD by spec — likely `companies/<companyId>/company.md` or similar). The migration deletes nothing (the column never existed in code), so this is a docs-vs-code reconciliation, not a destructive schema change.

### 2. New 4th container (MinIO) added to the deployment topology

ARCHITECTURE.md describes a three-container deployment: supervisor + `Dockerfile.mempalace` + `linuxserver/socket-proxy`. M5.4 adds a 4th container (MinIO) on the existing `garrison-net` Docker network. The amendment lands as part of M5.4 in the same commit pattern as M5.3's "Chat ──► garrison-mutate MCP" diagram amendment (FR-501 architecture-amendment-test pattern).

The `dashboard/tests/architecture-amendment.test.ts` substring-match assertions extend to pin the new MinIO line so future regressions can't silently re-shape the deployment.

### 3. New Go dependency (MinIO Go SDK)

Per AGENTS.md soft-rule on dependencies (the locked-deps streak: M2.3 was the last addition with `infisical-go-sdk` + `golang.org/x/tools`), M5.4 introduces a 4th principled addition:

- **`github.com/minio/minio-go/v7`** — MinIO Go client SDK. Justified: the supervisor + dashboard need to read/write the Company.md object and signed URLs; the AWS S3 SDK (`aws-sdk-go-v2/s3`) is heavier and pointed at MinIO via endpoint override, while `minio-go` is the upstream-maintained client matching the MinIO server we're operating. Alternative (raw `net/http` + S3 signature v4 implementation) was rejected: signature v4 is non-trivial and we'd be reimplementing the SDK without the `minio-go` ergonomics.

The spike confirms this dep choice; if it surfaces a reason to prefer aws-sdk-go-v2/s3 (e.g., better signed-URL helpers), the spec records the swap.

---

## Why this milestone now

M5.1 → M5.3 shipped the operator-facing chat surface end-to-end: read-only backend, dashboard surface, autonomous chat-driven mutations. The right pane has been a placeholder (`KnowsPanePlaceholder`) since M5.2 — operators see "Knowledge-base context lands in M5.4. For now this pane is reserved." every time they open `/chat`.

The pane closes three substantive gaps:

1. **Company.md becomes real.** ARCHITECTURE.md has described "the CEO's always-in-context document" since M1; nothing in code reads or writes it. M5.4 is where it gets a storage location, an edit affordance, and (per the spec's resolution of an open question) becomes part of the chat prompt context.

2. **Operator visibility into the palace.** M2.2 wired MemPalace MCP for agents but not for operators. The Recent palace writes + KG recent facts tabs give the operator a window into what the agent knowledge layer actually contains — without spawning a chat just to ask. This is observability infrastructure, not a feature.

3. **Closes the M5 milestone arc.** With M5.4 shipped, M5 is done: chat works end-to-end, mutations work, knowledge-base visibility works. M6 (CEO ticket decomposition) starts from a substrate where the operator can read the company's strategic context (Company.md), agent memory (palace writes), and knowledge graph (KG facts) before composing tickets.

---

## In scope

### A — Knowledge-base pane wiring

The right pane in `/chat` (currently `<KnowsPanePlaceholder/>`) becomes a tabbed component with three tabs. Replaces the placeholder; renders alongside the existing thread list.

The tab strip + active-tab routing is M5.4-shape. Tab state is local UI state, not URL-routed (consistent with how M5.2 handled side-pane interactions). All three tabs read on mount; the spec resolves what "refresh on demand" looks like (button vs tab-switch reload vs both).

### B — Company.md tab (editable)

- **Storage**: new MinIO bucket (name TBD by spec, likely `garrison-company` or `garrison-knowledge`). Single object per company; key shape TBD (`company.md` flat, or `<companyId>/company.md` for multi-company forward-compat).
- **Read path**: dashboard fetches the object on mount, renders as Markdown.
- **Edit path**: dashboard exposes an "edit" affordance on the Company.md tab. CEO clicks edit → textarea (or richer editor — spec picks) → save → write to MinIO → refresh the rendered view so the editor sees their just-committed content.
- **Refresh-after-edit semantics**: after a successful save, the rendered view re-fetches the object so stale-write conflicts (multiple browser windows) surface immediately. No SSE-style live tailing.
- **Validation**: on save, the dashboard checks the Markdown for size (max TBD; ARCHITECTURE.md hints "842 lines" as a wireframe sample, so an order-of-10kB limit feels right) and rejects empty saves.

### C — Recent palace writes tab (read-only)

- **Source**: MemPalace's existing diary/drawer/tunnel surface. Static query (no LLM summarization) returning the most recent N entries (N TBD by spec, likely 20–50).
- **Display**: list view with timestamp, drawer name, source agent (from the M2.2 metadata), short prose preview (truncated to ~200 chars).
- **Read path**: dashboard → MemPalace transport (open question §1).

### D — KG recent facts tab (read-only)

- **Source**: MemPalace's existing KG triple surface. Static query returning the most recent N triples (N TBD; aim for similar order of magnitude as palace writes tab).
- **Display**: triple list as `subject — predicate — object` with timestamp and (where present) source-ticket link.
- **Read path**: dashboard → MemPalace transport (open question §1, same as tab C).

### E — MinIO container + ops wiring

- **New service** in `docker-compose.yml`: `minio/minio:latest` (or a pinned tag — spike confirms choice) on `garrison-net`, exposing the standard MinIO API port (9000) internal-only. Console port (9001) optional, gated behind dev-only configuration.
- **Bucket bootstrap**: ops checklist gains a "create the company-md bucket on first deploy" step. Either via `mc` CLI invocation post-up, or via a Go startup probe in the supervisor that creates the bucket if missing (spike picks).
- **Credential storage**: TBD by spec (open question §3) — Infisical-fetched (matches M2.3 vault pattern) vs plain env vars in the compose file.
- **Backups**: out of scope for M5.4 (operator-side MinIO snapshot/replication is post-M5; M5.4 documents the bucket name + key shape so future backup work has a fixed target).

### F — Architecture amendment + test pin

- ARCHITECTURE.md amendment: deployment topology (3 → 4 containers), schema snapshot (`companies.company_md` removed, MinIO reference added), M5 build plan line updated to reflect M5.4 as shipped.
- `dashboard/tests/architecture-amendment.test.ts` extension: substring-match assertions pin the new MinIO line so a future doc-edit doesn't silently undo this milestone's commitment.
- M5.4 retro at `docs/retros/m5-4.md`, palace mirror per AGENTS.md §Retros.

### G — Sidebar / navigation

If a "Knowledge" entry needs to land in the dashboard sidebar (e.g., as a `/knowledge` standalone page outside the `/chat` pane), the spec resolves it. Default lean: M5.4 ships only the in-chat right-pane content; a standalone page is post-M5.

---

## Out of scope

### LLM-mediated summarisation of palace/KG content

Tabs C + D show raw query results. No LLM call summarises the recent writes or distills KG facts. Operator decision (recorded here): "do static queries to it if its possible." Future polish (M6+ or post-M5) may layer in an "ask palace" affordance, but M5.4 ships verbatim renders.

### SSE / live-tailing of palace writes

Recent palace writes tab is static-on-mount + manual refresh + refresh-after-edit (Company.md only). No SSE producer for `mempalace.write_committed` events. If the operator-week-of-use shows that staleness is a friction, post-M5 may layer in a `chat.knowledge.palace_write` channel and SSE consumer — same pattern as M5.2's chip indicator. Tracked-not-addressed.

### Multi-document Company.md / multi-doc knowledge base

M5.4 ships a single Company.md per company. No Documents directory, no multiple .md files, no folder hierarchy. If the operator's knowledge base outgrows a single document, post-M5 (likely M6 or M7) splits it.

### Per-thread context-token counter

M5.2 context (line 141) listed "the per-thread context-token counter" as M5.4 scope. Reconsidered here: the counter is a chat-runtime instrumentation concern (it depends on what the supervisor sends to claude per turn), not a knowledge-pane UI concern. Two options:
- (a) Defer to M6 alongside the CEO ticket-decomposition work, where context-window pressure is more visceral.
- (b) Ship as a separate sub-milestone if M5.4 implementation surfaces it as easily-bundled.

Default: (a) — out of scope for M5.4. Spec can revisit if the implementation makes (b) trivial.

### Always-in-context wiring of Company.md into chat prompts

ARCHITECTURE.md describes Company.md as "always in context." Today, the chat transcript assembler (`internal/chat/transcript.go`) does NOT prepend Company.md content; it builds a transcript from `chat_messages` only. M5.4 has a choice:

- **Option A**: M5.4 also wires Company.md into the chat prompt (read from MinIO at `SpawnTurn` time, prepend to the system prompt or first user turn).
- **Option B**: M5.4 ships only the operator-facing pane; the chat prompt continues to ignore Company.md. M6 (or a later milestone) wires the prompt-side later.

Default lean: **Option B**. Reason: Option A doubles the milestone scope (chat-spawn-path changes need their own spec discipline + threat-model update — Company.md content flowing into agent prompts is a non-trivial info-flow shift). Option B keeps M5.4 inside the operator-observability frame; the prompt-wiring lands in a future milestone with its own scope. Operator can override at /speckit.specify if Option A is preferred.

### Pinning / curation UI for palace writes or KG facts

Recent tabs are recency-ordered, not curated. No pin / star / hide affordances. Recency is the only signal.

### CEO chat fully reading from Company.md tab dynamically

The pane is operator-facing. Even if Option A above is taken, the chat doesn't query the pane — the supervisor reads MinIO directly at spawn time. The pane and the spawn-path read from the same object but via independent code paths.

### Multi-operator visibility

Single-operator per Constitution X. No "shared knowledge base across operators" concerns; no edit-conflict resolution beyond the refresh-after-edit pattern (last-write-wins is acceptable for single-operator).

### Workspace sandboxing fix

`docs/issues/agent-workspace-sandboxing.md` remains tracked-not-addressed. M5.4 chose MinIO partly because it sidesteps the host-volume question, but does not solve workspace sandboxing for agent containers.

---

## Binding inputs (read first)

| Document | Why it's binding |
|---|---|
| `ARCHITECTURE.md` (M5 section, schema, deployment topology) | Defines what's documented vs. what M5.4 amends |
| `docs/retros/m5-1.md` / `m5-2.md` / `m5-3.md` | Substrate the pane sits on; what each prior milestone deferred to M5.4 |
| `docs/research/m2-spike.md` Part 2 | MemPalace query API surface; what tabs C + D can actually call |
| `docs/security/chat-threat-model.md` | M2.3/M5.3 threat-model precedent — does Company.md edit need leak-scan analog? Spec resolves |
| `AGENTS.md` | Locked-deps soft rule (MinIO + minio-go are M5.4 additions); concurrency rules; spec-kit workflow |
| `RATIONALE.md` §13 | Spike rule — M5.4 spikes MinIO before specifying |
| `docs/research/m5-4-spike-minio.md` (to be produced) | Characterises MinIO behaviour; binding input to the spec |
| `migrations/20260422000003_m2_1_claude_invocation.sql` | Companies table as it actually is in code (id, name, created_at — no company_md) |

---

## Open questions the spec must resolve

1. **Dashboard ↔ MemPalace transport.** Three options:
   - (a) Dashboard process talks to the mempalace HTTP sidecar directly via a new dashboard role / network policy.
   - (b) Supervisor exposes a thin HTTP proxy (`/api/mempalace/recent-writes`, `/api/mempalace/recent-kg`) that the dashboard calls; supervisor proxies to mempalace and applies authz.
   - (c) Mirror the palace writes / KG into Postgres on every write (supervisor-side post-write hook); dashboard reads the mirror via `dashboard_app` role.
   
   Trade-offs: (a) is shortest path but creates a second consumer of the mempalace API surface; (b) keeps a single supervisor-side authz boundary at the cost of a proxy layer; (c) gives Postgres queryability but creates a sync-correctness surface. Default lean: (b). Spec confirms.

2. **Company.md size limit + edit conflict semantics.** What's the max-size cap on a single Company.md object? What happens if two browser windows attempt concurrent saves? Default: ~64 KB cap; last-write-wins (single-operator constraint). Spec confirms.

3. **MinIO credential storage.** Two options:
   - (a) Fetch from Infisical (matches M2.3 vault pattern; the `garrison_supervisor` machine identity gets a new grant).
   - (b) Plain env vars in the compose file (`MINIO_ROOT_USER`, `MINIO_ROOT_PASSWORD`); operator sets them at deploy time.
   
   Trade-offs: (a) is more uniform with M2.3 but adds a vault dependency to a service that doesn't process secrets; (b) is operationally simpler but creates a config-drift surface. Default lean: (a) — the vault is already the pattern. Spec confirms.

4. **Company.md leak-scan on save.** M2.3 Rule 1 says no agent.md may contain a verbatim secret value; the M5.3 chat-threat-model carries this forward to chat-driven `edit_agent_config`. Does Company.md inherit the same rule? The CEO might paste OAuth tokens, API keys, etc. into Company.md by accident. Default lean: yes — apply the same scanForSecrets pattern set on the dashboard server-action save path, reject saves that match with `ChatError(LeakScanFailed)`-shaped error to the editor. Spec confirms; if yes, M5.4 reuses the existing 10-pattern set from `internal/finalize.scanAndRedactPayload`.

5. **Bucket bootstrap mechanism.** Two options:
   - (a) Supervisor startup probe: on boot, check the bucket exists; create if missing. Idempotent. Adds a startup dependency on MinIO being reachable.
   - (b) Ops-checklist `mc mb` step: operator runs `mc mb minio/garrison-company` post-deploy. No code; ops doc.
   
   Default lean: (a) — startup probe matches the M2.2 mempalace-bootstrap pattern. Spec confirms.

6. **Sidebar entry.** Does M5.4 add a "Knowledge" entry to the dashboard sidebar that links somewhere outside `/chat`, or is the pane the only surface? Default: pane only; no sidebar entry. Spec confirms.

---

## Acceptance-criteria framing

Not enumerating ACs — that's the spec's job. The framing the spec works within:

- **Operator can read + edit Company.md.** Open `/chat`, click the Company.md tab, see the rendered Markdown; click edit, change content, save, see the updated content (refresh-after-edit).
- **Operator can see recent palace writes.** Open `/chat`, click the Recent palace writes tab, see the most recent N writes with source agent + prose preview, ordered by recency.
- **Operator can see recent KG facts.** Open `/chat`, click the KG recent facts tab, see the most recent N triples in `subject — predicate — object` form, ordered by recency.
- **MinIO container starts cleanly.** A fresh `docker compose up` produces a healthy MinIO + the bucket exists by the time the dashboard or supervisor needs it.
- **Edit-with-leak-attempt is rejected.** (Conditional on Open Q4 resolution): a save attempt with a verbatim sk-/xoxb-/AKIA-shape secret rejects with a typed error and does NOT write the object.
- **Architecture amendment lands + is pinned by test.** ARCHITECTURE.md reflects 4-container topology + MinIO Company.md storage; the substring assertion test passes.

---

## What this milestone is NOT

- **Not** an LLM-mediated knowledge-base. Tabs C + D show raw queries. No summarisation, no embedding-based retrieval, no chat-style "ask the palace" surface.
- **Not** SSE-live updating of the pane. Static reads + refresh-after-edit + manual refresh.
- **Not** a multi-document or hierarchical knowledge-base. Single Company.md.
- **Not** a chat-spawn-path change. (Default — Option B above.) The chat transcript assembler keeps its current shape; agent prompts continue without Company.md prepended. Future milestone.
- **Not** a workspace-sandboxing fix. Choosing MinIO sidesteps host-volume mounts but doesn't address the agent-workspace-escape concern in `docs/issues/agent-workspace-sandboxing.md`.
- **Not** a dashboard-side mutation surface for palace writes or KG triples. The pane is read-only for B and C; only Company.md is editable.
- **Not** the per-thread context-token counter. Deferred to M6 (or surfaced if M5.4 implementation makes it trivial).
- **Not** a multi-operator surface. Single operator per Constitution X.
- **Not** a backup / replication design for MinIO. Bucket exists; backup is post-M5.

---

## Spec-kit flow for M5.4

1. **First**: branch — `git checkout -b 013-m5-4-knows-pane main` (or whatever NNN-prefix the next number lands at).
2. **Spike**: operator runs the MinIO spike in a scratch directory (`~/scratch/m5-4-spike/`), writes findings to `docs/research/m5-4-spike-minio.md`. Spike characterises: container boot + healthcheck shape, root-creds-vs-Infisical credential pattern, bucket-create idempotency on supervisor startup, restart-recovery (does the bucket survive `docker compose down -v`?), MinIO Go SDK ergonomics for read/write/signed URL, network topology with the existing `garrison-net` + `socket-proxy`. Time-box: 2–4 hours.
3. `/speckit.constitution` — already populated; reuse.
4. `/speckit.specify` — drafts `specs/013-m5-4-knows-pane/spec.md` from this context + the spike findings.
5. `/speckit.clarify` — resolves the 6 open questions above plus anything the spec surfaces.
6. `/garrison-plan m5.4` — implementation plan.
7. `/garrison-tasks m5.4` — task list.
8. `/speckit.analyze` — cross-artifact consistency.
9. `/garrison-implement m5.4` — execute.
10. **Then**: M5.4 retro at `docs/retros/m5-4.md`, palace mirror, ARCHITECTURE.md amendment + test pin landed in the same PR. M5 closes; M6 (CEO ticket decomposition + hygiene) starts from this substrate.
