# Garrison — Architecture v0.4

A zero-human company driver. Event-driven agent organization backed by Postgres + MemPalace, orchestrating Claude Code subprocesses as ephemeral workers. Replaces the earlier multi-agent setup I ran with something cheaper when idle, faster to configure, and with genuine institutional memory.

---

## Core principles

1. **Postgres is the source of truth for current state.** Tickets, workflows, agents, hiring, concurrency — everything durable lives here. pg_notify is the event bus.
2. **MemPalace is the source of truth for memory.** Decisions, diaries, knowledge graph. Survives across agent instances and across time.
3. **Agents are ephemeral processes.** Spawned on events, run to completion, die. No long-running agent daemons. Zero idle token cost.
4. **The Kanban board is the coordination primitive.** Column transitions fire events. Agents listen for transitions that concern them.
5. **The CEO is summoned per-message.** No long-running CEO process. The palace holds the CEO's memory between invocations.
6. **Skills come from skills.sh.** Not a bespoke library — the public agent skills marketplace, installed per-department.
7. **Soft gates, not hard gates.** Workflow transitions succeed even if memory writes are thin; a dashboard surfaces hygiene issues for weekly review.
8. **The web UI is the operator console.** You live here day-to-day. Chat, Kanban, hiring, skill browser, dashboards.

---

## System components

### Postgres
The durable state store and event bus. Every write that another part of the system needs to react to fires `pg_notify` on a channel matching the event shape. Single database, schemas per concern (`org`, `work`, `memory_hygiene`, `ops`).

Channel names are dot-delimited and qualified: `work.ticket.created.<dept_slug>.<column_slug>` (from M2.1 onwards).

### Supervisor
A single long-running Go binary. Its job:

- Listen on pg_notify channels via a dedicated `pgx` connection (not from the pool)
- Check concurrency caps before spawning
- Spawn Claude Code subprocesses with the right `agent.md`, `cwd`, skill set, and MCP tool allowlist
- Track live agent processes in Postgres (pid, ticket, started_at, department)
- Clean up on exit, handle crashes, enforce timeouts via `context.Context`

The supervisor does not reason. It is a process manager.

**Stack**: Go 1.23+, `jackc/pgx/v5` for Postgres and LISTEN/NOTIFY, `sqlc` for typed query generation, `log/slog` for structured logging, `golang.org/x/sync/errgroup` for concurrent subsystem management, stdlib `os/exec` with `CommandContext` for subprocess lifecycle. Deployed as a single static binary via Docker. The allowed-dependency list is locked — agents implementing the supervisor cannot add dependencies without explicit justification.

**Subprocess lifecycle**: Claude Code subprocesses are spawned under their own process group (`SysProcAttr.Setpgid = true`) and signaled by process group, not PID. This is required because Claude Code may spawn child processes (MCP servers, tool executions) that must be terminated alongside the parent. PID-level signals are insufficient. This rule applies to all subprocesses from M2.1 onwards. See AGENTS.md concurrency rule 7.

**Subprocess output handling**: stdout/stderr pipes are drained to completion before `cmd.Wait` is called. Violating this causes truncated reads on short streams. Implemented in `internal/spawn/pipeline.go` per AGENTS.md concurrency rule 8, discovered via M2.1 retro §1.

**MCP health detection**: the supervisor does not manage MCP servers directly. Each Claude subprocess spawns its own MCP servers per the `--mcp-config` file. To detect broken MCP servers, the supervisor parses Claude Code's `system`/`init` event (first NDJSON line of stdout in stream-json mode) and checks the `mcp_servers[]` array — any server whose `status` is not `"connected"` causes the supervisor to kill the Claude process group immediately and mark the agent_instance as failed. Claude Code's exit code is not a reliable MCP health signal. This contract is binding from M2.1 onwards and was characterized in `docs/research/m2-spike.md`.

**Missed-event handling**: LISTEN is the fast path, but notifications are lost during reconnects. The event tables include a `processed_at` column, and the supervisor runs a fallback poll (`SELECT ... WHERE processed_at IS NULL LIMIT N`) every N seconds as a safety net.

### Postgres MCP server (`internal/pgmcp`)
An in-tree Go MCP server that runs as `supervisor mcp postgres` subcommand. ~300 LOC plus tests. Stdio JSON-RPC. Exposes two tools: `query` and `explain`. Read-only enforced via two layers: a Postgres role (`garrison_agent_ro`) with SELECT-only grants on the M2.1 read surface **plus `agent_instances`** (added at M2.3 commit `59fc977` for the finalize-tool precheck — see `docs/forensics/pgmcp-three-bug-chain.md`), plus a protocol-layer SELECT filter for defense-in-depth.

First-party component added in M2.1. Agents reach Postgres through this server; the supervisor never exposes Postgres credentials directly to Claude subprocesses. **The role explicitly has NO grants on the vault tables** (`agent_role_secrets`, `vault_access_log`, `secret_metadata`) — vault is opaque to agents per the M2.3 threat model. Returns use the MCP-spec-compliant `CallToolResult` envelope shape (`{"content":[{"type":"text","text":<payload>}],"isError":false}`); UUID columns are encoded as 36-char hex strings (not `[16]byte` integer arrays — both fixes landed in `59fc977` after the post-M2.2.2 pgmcp investigation).

### Agent processes
Each agent is a Claude Code subprocess started by the supervisor. It receives:

- Its `agent.md` as the system prompt
- A scoped working directory (the department's workspace; **post-M2 work will replace this with a per-agent Docker container — see Target-state architecture below**)
- A set of installed skills (from that department's `.claude/skills/`)
- MCP tools: Postgres (always, from M2.1), MemPalace (always, from M2.2), `finalize_ticket` (always, from M2.2.1 — the only path that commits a transition), plus whatever the agent's config declares via the `agents.mcp_config` JSONB column. Any MCP server entry in `agents.mcp_config` that references the vault is rejected at spawn time by `mcpconfig.CheckExtraServers` (M2.3 Rule 3); vault is opaque to agents
- A wake-up context injection from `mempalace wake-up --wing <role_wing>` (~170 tokens, from M2.2)
- Vault-fetched secrets injected as environment variables (M2.3) — see Target-state architecture for the seven-step spawn ordering. Secrets never enter `agent.md` or any context window
- A specific event payload: the ticket it's acting on, the transition that triggered it

It runs until it has either committed a transition via `finalize_ticket` or hit a timeout / budget cap / non-finalize exit. Then the process exits.

**Real-world cost baseline**: a trivial agent invocation (hello-world file write, ~8 Claude turns) observed at ~$0.04 per run in M2.1 (vs. $0.003 in mockclaude fixtures — 13× delta). Cost captured on `agent_instances.total_cost_usd` from the terminal `result` event's `total_cost_usd` field. **Caveat**: clean-finalize runs currently record `$0.00` because the supervisor signal-kills before claude's `result` event lands — see `docs/issues/cost-telemetry-blind-spot.md`. Failure-mode exits (`finalize_never_called`, `budget_exceeded`, `claude_error`) record cost correctly.

### CEO
Not a daemon. When you send a message in the CEO chat:

1. Web UI writes the message to `ceo_conversations`, fires pg_notify
2. Supervisor spawns a Claude process with `ceo.md` + `company.md` + MemPalace MCP + Postgres MCP
3. CEO reads conversation history from Postgres (last N turns), queries palace/KG as needed, replies
4. Reply written back to `ceo_conversations`, process exits
5. Before exit, CEO extracts any new decisions made in this turn into KG triples

No long-running context. Every message is a fresh spawn. The palace is the only memory.

### Web UI
Next.js 16 + React 19. Reads from Postgres, writes to Postgres. WebSocket feed for real-time updates driven by pg_notify. Main surfaces (see dashboard section below). Shares Drizzle schema types with any TS tooling in the monorepo, but not with the supervisor — supervisor uses `sqlc`-generated Go types from the same migrations, so both sides derive from the SQL as source of truth.

### MemPalace
Runs as an MCP server. Shared across every agent. The memory of the entire organization. Bootstrapped at a dedicated path (`~/.garrison/palace/` or an operator-configured path) outside any git-tracked directory. Wing and room creation is on-write via MCP `add_drawer`; init-time scanning defines only defaults.

---

## Target-state architecture

The communication contract that emerges from M1 → M2.3 and the post-M2 Docker-per-agent work. This section is the single answer to "who is allowed to talk to whom, and what mediates each edge?"

### Communication contract

```
Supervisor ──► Vault (Infisical)            ✓  Universal Auth, secret fetch
Supervisor ──► Postgres                     ✓  full read+write (sqlc, migrations, triggers)
Supervisor ──► MemPalace                    ✓  out-of-band (hygiene checker via short-lived
                                                 docker-exec'd MCP client)
Supervisor ──► Agent container (spawn)      ✓  env-var injection only (vault secrets;
                                                 NEVER prompt content)

Agent      ──► Postgres (via pgmcp)         ✓  read-only, garrison_agent_ro role
Agent      ──► MemPalace (via MCP)          ✓  read + write, wing-scoped
Agent      ──► finalize_ticket MCP          ✓  single tool, transactional, only commit path
Agent      ──► Vault                        ✗  FORBIDDEN — vault is opaque to agents
                                                 (Rule 3: mcpconfig.CheckExtraServers blocks
                                                  any agent.mcp_config referencing the vault)
Agent      ──► Host filesystem outside      ✗  FORBIDDEN — enforced by per-agent Docker
              /workspace                         container (planned post-M3; tracked in
                                                 docs/issues/agent-workspace-sandboxing.md)
Agent      ──► Inter-agent direct           ✗  FORBIDDEN by construction — agents are
                                                 ephemeral and stateless; coordination
                                                 happens through Postgres + MemPalace
```

### Seven-step spawn ordering (M2.3, FR-416 / D4.5)

Every agent spawn executes in this order. Each step's failure mode has a distinct `exit_reason`:

```
1. ListGrantsForRole                        → grants_lookup_failed
2. mcpconfig.CheckExtraServers (Rule 3)     → vault_in_agent_mcp_config
3. vault.Fetch (Infisical Universal Auth)   → vault_unavailable | vault_auth_failed |
                                                vault_permission_denied | vault_rate_limited
4. RuleOneLeakScan (raw secret in agent.md) → vault_value_leaked_in_prompt
5. WriteAuditRow (in spawn tx, fail-closed) → vault_audit_write_failed
6. cmd.Start (env-injected) + defer Zero()  → spawn_failed
7. M2.2.x subprocess pipeline → finalize    → existing M2.2.x exit_reason vocabulary
```

Steps 1–6 happen synchronously before the subprocess is started. Step 7 is the existing M2.2 / M2.2.1 / M2.2.2 pipeline unchanged. The spawn transaction wraps steps 1–6 plus the final `agent_instances` row insert; rolling back the tx undoes the audit row write so partial spawns leave no false-audit history.

### Sandboxing — planned post-M3

The current spawn machinery does not `chdir` the claude subprocess into the workspace tempdir; agents inherit the operator's shell cwd. Haiku has demonstrated this leak by writing to `~/changes/` instead of the workspace `changes/` directory (see `experiment-results/post-uuid-fix-haiku-run.md`). Forward path: **per-agent Docker containers** with `/workspace` as the cwd, host filesystem hidden, network policy controlling which external APIs the agent can reach, and vault-injected secrets entering through container env (not host env). The two threat models (vault opacity + workspace sandbox) compose into "agent has credentials AND cannot exfiltrate them"; neither alone is sufficient. Tracking doc: `docs/issues/agent-workspace-sandboxing.md`.

### Decision provenance

The Supervisor↔Vault (not Agent↔Vault) commitment, the SkillHub-as-target-state-skill-registry decision (M7), and the MCPJungle-as-target-state-MCP-server-registry decision (M8) were made in the 2026-04-24 architecture session. Frozen snapshot: `docs/architecture-reconciliation-2026-04-24.md`.

---

## Data model (sketch)

```sql
-- Schema: org

CREATE TABLE companies (
  id UUID PRIMARY KEY,
  name TEXT NOT NULL,
  company_md TEXT NOT NULL,      -- CEO's always-in-context document
  mission TEXT, vision TEXT
);

CREATE TABLE departments (
  id UUID PRIMARY KEY,
  company_id UUID REFERENCES companies(id),
  slug TEXT NOT NULL,             -- 'engineering', 'marketing'
  name TEXT NOT NULL,
  manager_agent_id UUID,          -- the proxy-to-CEO role
  workspace_path TEXT NOT NULL,   -- filesystem path for cwd
  concurrency_cap INT DEFAULT 3,
  workflow JSONB NOT NULL         -- columns, transitions, gates (see below)
);

CREATE TABLE agents (
  id UUID PRIMARY KEY,
  department_id UUID REFERENCES departments(id),
  role_slug TEXT NOT NULL,        -- 'frontend-engineer', 'cto'
  agent_md TEXT NOT NULL,         -- system prompt
  model TEXT NOT NULL DEFAULT 'claude-opus-4-7',
  skills JSONB NOT NULL,          -- array of skills.sh repo refs installed for this agent
  mcp_tools JSONB NOT NULL,       -- extras beyond the baseline
  mcp_config JSONB,               -- per-agent extra MCP server specs (M2.3); validated by
                                  -- mcpconfig.CheckExtraServers — Rule 3 rejects any entry
                                  -- referencing the vault MCP at spawn time
  listens_for JSONB NOT NULL,     -- event patterns this agent wakes on
  palace_wing TEXT NOT NULL,      -- 'wing_frontend_engineer'
  status TEXT NOT NULL            -- 'active', 'archived'
);

CREATE TABLE agent_instances (
  id UUID PRIMARY KEY,
  agent_id UUID REFERENCES agents(id),
  ticket_id UUID,
  pid INT,
  started_at TIMESTAMPTZ,
  finished_at TIMESTAMPTZ,
  status TEXT,                    -- 'running', 'succeeded', 'failed', 'timeout'
  exit_reason TEXT,               -- canonical vocabulary in internal/spawn/exitreason.go;
                                  -- extended by M2.2 (budget_exceeded), M2.2.1 (finalize_*),
                                  -- M2.3 (vault_unavailable, vault_auth_failed,
                                  -- vault_permission_denied, vault_rate_limited,
                                  -- vault_value_leaked_in_prompt, vault_audit_write_failed,
                                  -- vault_in_agent_mcp_config, grants_lookup_failed)
  total_cost_usd NUMERIC(10,6),   -- from result event in stream-json (M2.1); reads $0.00
                                  -- on clean-finalize runs — see
                                  -- docs/issues/cost-telemetry-blind-spot.md
  wake_up_status TEXT,            -- mempalace wake-up outcome (M2.2): 'ok' | 'failed' | NULL
  role_slug TEXT                  -- denormalised from agents (M2.2) for dispatch routing
);

-- Schema: work

CREATE TABLE tickets (
  id UUID PRIMARY KEY,
  department_id UUID REFERENCES departments(id),
  origin TEXT NOT NULL,           -- 'ui', 'ceo_chat', 'agent_spawned'
  origin_agent_instance_id UUID,  -- if agent_spawned
  parent_ticket_id UUID,          -- if subtask
  objective TEXT NOT NULL,
  acceptance_criteria TEXT NOT NULL,
  deliverable_type TEXT,          -- 'code', 'spec_doc', 'copy', 'research_brief', ...
  column_slug TEXT NOT NULL,      -- current position in the dept's workflow
  priority INT DEFAULT 3,
  created_at TIMESTAMPTZ,
  updated_at TIMESTAMPTZ,
  metadata JSONB                  -- dept-specific fields
);

CREATE TABLE ticket_transitions (
  id UUID PRIMARY KEY,
  ticket_id UUID REFERENCES tickets(id),
  from_column TEXT,
  to_column TEXT NOT NULL,
  triggered_by_agent_instance_id UUID,
  triggered_by_user BOOLEAN DEFAULT FALSE,
  at TIMESTAMPTZ NOT NULL,
  hygiene_status TEXT             -- vocabulary, accumulated across milestones:
                                  --   M2.2:    'clean', 'pending', 'missing_diary',
                                  --            'missing_kg', 'thin'
                                  --   M2.2.1:  'finalize_failed', 'finalize_partial', 'stuck'
                                  --   M2.3:    'suspected_secret_emitted'
                                  -- No CHECK constraint — vocabularies coexist during the
                                  -- M2.2 → M2.2.1 transition window. Listener routes by
                                  -- agent_instances.exit_reason: finalize-shaped rows go
                                  -- through pure-Go EvaluateFinalizeOutcome; legacy M2.2
                                  -- rows go through palace-query Evaluate.
);

CREATE TABLE hiring_requests (
  id UUID PRIMARY KEY,
  department_id UUID REFERENCES departments(id),
  proposed_role_slug TEXT,
  proposed_agent_md TEXT,
  rationale TEXT,
  suggested_skills JSONB,
  status TEXT NOT NULL,           -- 'proposed', 'approved', 'rejected'
  created_at TIMESTAMPTZ,
  decided_at TIMESTAMPTZ,
  decided_by TEXT
);

CREATE TABLE ceo_conversations (
  id UUID PRIMARY KEY,
  turn_index INT,
  role TEXT NOT NULL,             -- 'user', 'ceo'
  content TEXT NOT NULL,
  tool_calls JSONB,
  created_at TIMESTAMPTZ
);

-- Schema: vault (M2.3 — Infisical integration)

CREATE TABLE agent_role_secrets (   -- links a role slug to an Infisical secret path +
  id UUID PRIMARY KEY,              -- env var name; the data source for the vault grant
  role_slug TEXT NOT NULL,          -- query at spawn time
  customer_id UUID,
  secret_path TEXT NOT NULL,
  env_var_name TEXT NOT NULL,
  created_at TIMESTAMPTZ
);

CREATE TABLE vault_access_log (     -- per-spawn audit record (M2.3, FR-415); never stores
  id UUID PRIMARY KEY,              -- the secret value (enforced at the schema layer)
  agent_instance_id UUID,
  role_slug TEXT NOT NULL,
  secret_path TEXT NOT NULL,
  outcome TEXT NOT NULL,            -- 'granted' | 'denied' | 'error'
  error_class TEXT,
  at TIMESTAMPTZ NOT NULL
);

CREATE TABLE secret_metadata (      -- per-path metadata; allowed_role_slugs is a denorm
  secret_path TEXT PRIMARY KEY,     -- column rebuilt by trigger on agent_role_secrets writes
  provenance TEXT,
  rotation_cadence INTERVAL,
  last_accessed_at TIMESTAMPTZ,     -- updated inside the spawn tx on OutcomeGranted
  allowed_role_slugs TEXT[]         -- denorm; rebuilt by rebuild_secret_metadata_role_slugs()
);
```

**Triggers** (production schema):

- `emit_ticket_created` (M2.1) — fires `work.ticket.created.<dept>.<column>` after INSERT on `tickets`.
- `emit_ticket_transitioned` (M2.2) — fires `work.ticket.transitioned.<dept>.<from>.<to>` after INSERT on `ticket_transitions`. Resolves `department_id` via subquery on `tickets`.
- `rebuild_secret_metadata_role_slugs` (M2.3) — Garrison's first PL/pgSQL trigger. AFTER INSERT/DELETE on `agent_role_secrets`, rebuilds `secret_metadata.allowed_role_slugs` for the affected path. Pattern recommended for denorm columns where the rebuild is cheap (array aggregation); avoid for cross-schema joins or expensive computation.

Every INSERT / UPDATE on `tickets`, `hiring_requests`, `ceo_conversations`, `ticket_transitions` fires `pg_notify` on a channel derived from the event, e.g. `work.ticket.transitioned.engineering.in_dev.qa_review`.

---

## Workflow as YAML

Each department's workflow is JSONB in the database, edited through the UI or imported from the bootstrap YAML. Shape:

```yaml
department: engineering
workspace_path: /workspaces/engineering
concurrency_cap: 3
columns:
  - slug: todo
    label: To do
    entry_from: [backlog, needs_review]
  - slug: in_dev
    label: In development
    entry_from: [todo]
    on_enter:
      - spawn_agent: frontend-engineer
        select_by: ticket.metadata.surface == 'frontend' || 'fullstack'
      - spawn_agent: backend-engineer
        select_by: ticket.metadata.surface == 'backend'
  - slug: qa_review
    label: QA
    entry_from: [in_dev]
    on_enter:
      - spawn_agent: qa-engineer
    gates:
      expected_writes:
        - palace.diary_entry in wing_frontend_engineer
        - kg.triple (agent_instance, completed, ticket)
  - slug: done
    label: Done
    entry_from: [qa_review]
```

`gates.expected_writes` is declarative. After the transition, a background job checks whether those writes exist. If they don't, the transition is marked `hygiene_status = 'missing_diary'` or similar. The UI dashboard surfaces these. Nothing is blocked — you review weekly.

---

## Event flow — canonical example

A user creates a ticket in the UI:

1. UI → `INSERT INTO tickets (..., column_slug='todo', origin='ui')`
2. Trigger fires `pg_notify('work.ticket.created.engineering.todo', ticket_id)`
3. Supervisor's listener wakes, loads the engineering department config
4. No spawn rule on `todo`, so nothing happens — ticket sits in the column waiting for manual move or for the CTO to triage
5. CTO agent is configured to `listens_for: ticket.created.*.todo`. Supervisor checks concurrency — CTO is 1-instance-cap, not currently running. Spawn.
6. CTO Claude Code process starts with `cto.md`, MemPalace MCP + Postgres MCP, `cwd = /workspaces/engineering`
7. CTO reads the ticket, queries palace for similar past tickets, decides which engineer role is needed, updates `ticket.metadata.surface = 'frontend'`, transitions to `in_dev`
8. Transition fires `pg_notify('work.ticket.transitioned.engineering.todo.in_dev', ticket_id)`
9. Supervisor checks spawn rules on `in_dev`. Spawn frontend-engineer with the ticket id as payload (concurrency cap allowing).
10. Engineer runs, commits code, writes diary entry to `wing_frontend_engineer`, writes KG triples, transitions to `qa_review`
11. Supervisor checks hygiene gates async, annotates `ticket_transitions.hygiene_status`
12. qa-engineer spawns on `qa_review` entry, does its thing, moves to `done`

At no point is anyone polling. At no point is an idle agent burning tokens.

---

## Agent.md template

```markdown
# Frontend Engineer

## Role
You implement frontend features for tickets assigned to the engineering department.
You work on whatever repo is set as your cwd.

## Context at wake
- `company.md` is available via `mempalace_search`
- Your wing is `wing_frontend_engineer`. Every past frontend ticket is there.
- Check `hall_discoveries` in your wing for patterns worth knowing before you start.

## Work loop
1. Read the ticket from Postgres: `SELECT * FROM tickets WHERE id = $TICKET_ID`
2. Check your wing for related prior work: `mempalace_search("<objective keywords>", wing="wing_frontend_engineer")`
3. Implement. Commit with a descriptive message that references the ticket id.
4. Write a diary entry. Follow the completion protocol below.
5. Transition the ticket.

## Completion protocol (MANDATORY)
Before transitioning this ticket to the next column, you MUST:

1. Write a diary entry to `wing_frontend_engineer` using `mempalace_add_drawer`:
   - Format: YAML frontmatter + body
   - Required fields:
     - ticket_id
     - outcome: one-line summary
     - artifacts: list of files changed/created
     - rationale: why you made the design decisions you made
     - blockers: anything that blocked you, even briefly
     - discoveries: patterns or gotchas worth remembering for future instances

2. Write KG triples via `mempalace_kg_add`:
   - `(you, completed, ticket_<id>, today)`
   - For every new component/module you created:
     `(<component_name>, created_in, ticket_<id>)`
   - For every significant decision:
     `(<thing_decided>, decided_because, <reason>)`

3. If you discovered something that future instances of frontend-engineer should always check before starting, write to `hall_discoveries` in your wing.

4. Only then: `UPDATE tickets SET column_slug = 'qa_review' WHERE id = $TICKET_ID`

## Tools available
- Claude Code built-in: file I/O, bash, git
- MCP: mempalace (read + write), postgres (read tickets; write ticket state)
- MCP: github (PR creation, issue linking)
- Skills: whatever is installed in /workspaces/engineering/.claude/skills/

## What you do not do
- You do not escalate to human unless blocked after 3 attempts at the same approach. The dashboard is where humans look; write your blocker to the ticket metadata and mark it `needs_review`.
- You do not create tickets for other departments. If you need design or copy input, write to ticket metadata that you need it, and mark `needs_review`. The CTO handles cross-department coordination.
```

Same shape for every role, specialized on role. The completion protocol is non-negotiable and identical across all workers. Managers (CTO, CMO, etc.) have a different protocol — they also write KG triples for assignment and delegation decisions.

---

## Hiring flow (UI-native, not git)

When the CEO (or you directly) decides a new agent is needed:

1. **Trigger**: CEO decides in chat ("we need an SEO specialist"), or you click "Propose hire" in the UI. If CEO-initiated, CEO generates a hiring request; if UI-initiated, CEO is summoned to fill out the proposal.
2. **Skill search**: CEO queries skills.sh via its API (or HTTP fetch of https://skills.sh) for skills matching the proposed role. Returns top N with descriptions and install counts. CEO picks a shortlist and writes rationale for each.
3. **Draft agent.md**: CEO drafts the full `agent.md` including the role-specific section and the mandatory completion protocol. Includes department assignment and palace wing name.
4. **Proposal written**: `INSERT INTO hiring_requests` with `status = 'proposed'`. pg_notify fires.
5. **Your review**: UI shows a side-by-side: proposed `agent.md`, proposed skills (with descriptions), CEO's rationale, which department, concurrency slot impact. You can edit anything inline, approve, or reject.
6. **Approval**: On approve, system runs `npx skills add <repo>` for each approved skill into `<workspace>/.claude/skills/`, inserts the new row into `agents`, updates the department config. New agent is live on next matching event.

This never touches git. Configs live in Postgres. If you want version control, export the full org state to YAML on demand (same schema the bootstrap YAML uses).

---

## MemPalace write contract

Because soft gates + explicit writes means the system's memory is only as good as what agents write, this is the highest-leverage area to get right.

**Wing structure:**
- `wing_<role_slug>` per agent role — shared across instances (e.g. `wing_frontend_engineer`)
- `wing_company` — CEO-level decisions, strategy, goals
- `wing_<department_slug>` — department-level decisions by the manager agent
- Halls within each wing: `hall_facts`, `hall_events`, `hall_discoveries`, `hall_preferences`, `hall_advice`

**Diary entry schema** (YAML frontmatter in the drawer body):
```yaml
ticket_id: <uuid>
outcome: <one line>
artifacts: [<file paths or urls>]
rationale: |
  <paragraph>
blockers: [<list of short strings>]
discoveries: [<list of short strings>]
completed_at: <iso timestamp>
```

**KG triple conventions:**
- `(agent_instance_<id>, completed, ticket_<id>, valid_from=now)` — always
- `(<artifact>, created_in, ticket_<id>)` — for every new artifact
- `(<decision>, decided_because, <reason>)` — for every non-trivial choice
- `(ticket_a, blocks, ticket_b)` — cross-department dependencies
- `(<role>, owns, <surface>)` — ownership facts, written by managers

**Hygiene check** (runs async after every `ticket_transitions` insert):
- Did `mempalace_add_drawer` get called with the ticket_id in the body during the window between the agent_instance start and the transition?
- Did at least one KG triple mentioning this ticket get written?
- Thin writes (e.g. diary body < 100 chars) flagged as `hygiene_status = 'thin'`
- Results stored on `ticket_transitions.hygiene_status`, surfaced in the dashboard

**Hygiene status vocabulary** (accumulated across milestones; full set documented on the schema column above):
- **M2.2 (palace-query path)**: `clean`, `pending`, `missing_diary`, `missing_kg`, `thin`
- **M2.2.1 (finalize-shaped path)**: `finalize_failed`, `finalize_partial`, `stuck` — evaluated by pure-Go `EvaluateFinalizeOutcome` against the finalize-tool outcome rather than by querying the palace
- **M2.3 (vault-path scanner)**: `suspected_secret_emitted` — set when `scanAndRedactPayload` matches any of 10 secret-shape patterns in the diary rationale or KG triples, before the MemPalace write. Non-blocking (the value is redacted in-place, not rejected)

The two M2.2 / M2.2.1 evaluation paths coexist on the column. Routing is by `agent_instances.exit_reason`: finalize-shaped exit reasons go through the M2.2.1 path; the rest go through the original M2.2 palace-query path. No CHECK constraint, no retroactive UPDATE during the transition window.

---

## The bootstrap YAML

```yaml
company:
  name: <company name>
  company_md: |
    <full text of company.md — goals, vision, products, GTM, etc>

departments:
  - slug: engineering
    name: Engineering
    workspace_path: /workspaces/engineering
    concurrency_cap: 3
    workflow: <inline workflow as shown above>

  - slug: product
    name: Product research
    workspace_path: /workspaces/product
    concurrency_cap: 1
    workflow: <workflow for research>

  - slug: marketing
    name: Marketing
    workspace_path: /workspaces/marketing
    concurrency_cap: 2
    workflow: <workflow for marketing>

agents:
  - role_slug: ceo
    department: null            # org-wide
    agent_md_path: ./agents/ceo.md
    model: claude-opus-4-7
    palace_wing: wing_company
    skills: []
    listens_for: []             # summoned only, not event-driven

  - role_slug: cto
    department: engineering
    agent_md_path: ./agents/cto.md
    palace_wing: wing_engineering
    skills:
      - anthropics/skills:template-skill
    listens_for:
      - work.ticket.created.engineering.todo

  - role_slug: frontend-engineer
    department: engineering
    agent_md_path: ./agents/frontend-engineer.md
    palace_wing: wing_frontend_engineer
    skills:
      - vercel-labs/agent-skills:vercel-react-best-practices
      - anthropics/skills:frontend-design
      - shadcn/ui:shadcn
    listens_for:
      - work.ticket.transitioned.engineering.*.in_dev  # with metadata filter
    concurrency_cap_override: 3

  - role_slug: backend-engineer
    department: engineering
    # ...
```

`garrison bootstrap org.yaml` wipes Postgres (prompt first), installs all skills listed, creates the workspaces on disk, writes agents into the DB, ready to go.

---

## Build plan — milestones

Specs first, code second. Each milestone is scoped to a single specify-cli spec that produces an implementable, end-to-end-functional chunk. No milestone ships half-built scaffolding; each one must be usable on a real workstream before moving to the next. The discipline: each milestone ends with a retro. **Policy from M3 onwards**: retros land as both `docs/retros/m{N}.md` markdown (canonical) AND a MemPalace `wing_company / hall_events` drawer mirror via the wired-in `mempalace_add_drawer` MCP tool. Markdown is the source of truth on disagreement; the drawer keeps retro content in the same memory layer agents read from for context. M1, M2.1, and the M2.x arc retros (M2.2, M2.2.1, M2.2.2, M2.3, plus the closing arc retro) are markdown-only by historical decision — see AGENTS.md §Retros for the rationale.

Some milestones carry genuine external unknowns — how a tool actually behaves in practice, rather than how the docs describe it. Those milestones begin with a research spike (see RATIONALE §13). The spike produces a `docs/research/m{N}-spike.md` that becomes binding input to the milestone's context file. Spikes are exploratory and time-boxed; they do not produce production code. M2's spike delivered a 5:1 prevention-to-discovery ratio in M2.1 — discipline validated.

**M1 — Event bus + supervisor core.** ✅ Shipped 2026-04-22. Retro: `docs/retros/m1.md`.

**M2 — First real agent loop.** ✅ Shipped 2026-04-22 → 2026-04-24 across five sub-milestones (M2.1, M2.2, M2.2.1, M2.2.2, M2.3). The arc retro at `docs/retros/m2-2-x-compliance-retro.md` synthesises the M2.2.x compliance work and is essential reading before any code in this area.

**M2.1 — Claude Code invocation.** ✅ Shipped 2026-04-22. Retro: `docs/retros/m2-1.md`. Real per-invocation cost observed: ~$0.04 for trivial runs.

**M2.2 — MemPalace MCP wiring.** ✅ Shipped 2026-04-23. Retro: `docs/retros/m2-2.md`. MemPalace wired into every spawn — MCP tool always available, wake-up context injection, the completion protocol writes from `agent.md` become real. Three-container deployment topology (supervisor + mempalace sidecar + socket-proxy). New `garrison_agent_mempalace` Postgres role. `emit_ticket_transitioned` trigger added. The hygiene dashboard concept appears first here as a read-only query; the full hygiene UI waits for M3.

  The memory thesis from RATIONALE §3 became testable here. M2.2's live run revealed the soft-gate failure mode RATIONALE §5 anticipated: real haiku skipped MANDATORY palace writes despite the prompt. That observation drove M2.2.1.

**M2.2.1 — Structured completion via `finalize_ticket` tool.** ✅ Shipped 2026-04-23. Retro: `docs/retros/m2-2-1.md`. Re-architected completion around a mechanism instead of a prompt. New in-tree MCP server (`internal/finalize`) exposing a single `finalize_ticket` tool; the agent cannot exit with a successful transition any other way. Hygiene-status vocabulary extended (`finalize_failed`, `finalize_partial`, `stuck`).

**M2.2.2 — Compliance calibration.** ✅ Shipped 2026-04-24. Retro: `docs/retros/m2-2-2.md`. Richer structured error responses (line/column/excerpt/constraint/expected/actual/hint), Adjudicate precedence fix (budget-cap events no longer masked as `claude_error`), seed agent.md rewritten with front-loaded goal + palace-search calibration + example payload + retry framing. The original retro read the calibration thesis as falsified on live models; the post-ship pgmcp investigation revised that read — see arc retro.

  **Post-M2.2.2 investigation** (closes the arc): a three-bug chain in `internal/pgmcp` (missing `agent_instances` SELECT grant, wrong `CallToolResult` envelope shape, UUID encoded as `[16]byte` integer array) had been silently contaminating compliance signal across M2.2 / M2.2.1 / M2.2.2 retros. Fixed in commit `59fc977` plus migration `20260424000007_agent_ro_agent_instances_grant.sql`. Forensic at `docs/forensics/pgmcp-three-bug-chain.md`. Post-fix matrix: 4 clean / 6 partial / 2 fail under strict scoring; 10/12 mechanism-compliant if the orthogonal workspace-sandbox-escape is separated out.

**M2.3 — Secret vault (Infisical).** ✅ Shipped 2026-04-24. Retro: `docs/retros/m2-3.md`. Self-hosted Infisical with Garrison-native UI (operator never sees Infisical's UI directly); secrets injected as environment variables at spawn time; never enter agent prompts or context windows. Four vault rules (no-leaked-values, zero-grants-zero-secrets, vault-opaque-to-agents, fail-closed-audit). Custom `tools/vaultlog` go vet analyzer rejects slog/fmt/log calls with `vault.SecretValue` arguments at build time. Garrison's first PL/pgSQL trigger (`rebuild_secret_metadata_role_slugs`) shipped here. Design input at `docs/security/vault-threat-model.md`. **Locked-deps streak (M1 → M2.2.2: zero new deps) intentionally broken with two principled additions**: `infisical/go-sdk v0.7.1` and `golang.org/x/tools v0.44.0`.

**M3 — Dashboard read-only. Active.** Next.js 16 + React 19 app reading from Postgres, showing department Kanban, ticket detail, agent activity feed. No mutations yet. Read-only-first forces you to actually watch the system behave for a few days before giving yourself (and agents) the ability to change state — which is when you catch the "oh, the event payload shape is wrong" class of bugs cheaply. **Notes carried forward from M2**: (a) real Claude Code emits 5-10 `assistant` events per run (vs. 2 in test fixtures) — the activity feed must render high-volume streams without visual bloat; (b) M3 must surface the cost-telemetry blind spot (`agent_instances.total_cost_usd` reads $0.00 on clean finalizes — see `docs/issues/cost-telemetry-blind-spot.md`); (c) M3 must surface the hygiene-status vocabulary including the M2.3-era `suspected_secret_emitted`; (d) workspace-sandbox-escape ("artifact claimed vs artifact on disk") is a real failure mode operator triage will see — tracked at `docs/issues/agent-workspace-sandboxing.md`, fix lands as Docker-per-agent post-M3.

**M4 — Dashboard mutations.** Create tickets in UI, drag between columns, edit agent configs. Everything the operator does daily.

**M5 — CEO chat (summoned, read-only).** Conversation panel, summon-per-message pattern, Q&A only. CEO can query state and the palace but cannot create tickets yet.

**M6 — CEO ticket decomposition + hygiene checks.** CEO writes tickets from conversation. Hygiene dashboard shows thin/missing writes. Rate-limit back-off and cost-based throttling land here (M2.1 observes cost and rate-limit events; M6 acts on them).

**M7 — Hiring flow.** skills.sh integration plus **SkillHub (iflytek)** as the target-state private-skills registry alongside the public skills.sh feed (decision committed 2026-04-24; see `docs/skill-registry-candidates.md`). Proposal UI, approval writes agents + installs skills.

**M8 — Agent-spawned tickets, cross-department dependencies, MCP-server registry.** The last piece of the zero-human loop. Includes runaway control (per-department weekly ticket-creation budget) and an MCP-server registry — leading candidate **MCPJungle** (self-hosted, Go-based, Postgres-backed, combines registry + runtime proxy; maturity re-check at M7 kickoff). See `docs/mcp-registry-candidates.md`.

> **Decision provenance for the target-state items above** (Supervisor↔Vault rather than Agent↔Vault, SkillHub at M7, MCPJungle at M8, workspace-sandbox tracked separately): `docs/architecture-reconciliation-2026-04-24.md`. That file is a frozen snapshot of the 2026-04-24 architecture session — kept as committed history, not edited. Any future architecture-reconciliation pass gets its own dated file (`docs/architecture-reconciliation-YYYY-MM-DD.md`) following the same shape.

---

## Spec-first workflow

Specs are produced with `specify-cli` (with Garrison-flavored `/garrison-*` slash commands layered on) and live in a dedicated `specs/` directory in the repo. Each milestone has one spec (sub-milestones get their own specs — `specs/003-m2-1-claude-invocation/`, `specs/004-m2-2-mempalace/`). Specs are scoped narrowly — the M1 spec covers the event bus and supervisor core only, not the full system. A 40-page monolithic spec is an antipattern; it produces paralysis rather than code.

**What gets specified formally:**
- Event bus contract (pg_notify channel names, payload shapes, who writes and who listens)
- Database schema with constraints and the processed_at fallback pattern
- Supervisor state machine and concurrency invariants
- Agent spawn contract (env vars, args, cwd, stdin/stdout, timeout behavior)
- Hygiene check protocol (expected writes per transition, verification rules)
- Write contract for MemPalace (diary schema, KG triple conventions)

**What doesn't get formally specified:**
- Dashboard UI screens (iterate too fast; use design prompts instead)
- Individual agent.md contents (prompts, not code — evolve empirically)
- CEO conversational behavior (emerges from prompt + tools, not spec-able)

**What gets spiked before specified:** any milestone whose spec depends on external tool behavior that isn't characterized yet. See RATIONALE §13.

---

## Open source

The project is at `github.com/garrison-hq/garrison`. The specs are the primary contribution — more valuable than the code itself, because the code can be regenerated from good specs in a weekend, but the specs encode the architectural thinking that's hard to reproduce.

**Repo layout:**
```
garrison/
├── specs/                                 # specify-cli output, one dir per milestone
│   ├── m1-event-bus/                      # M1 (un-numbered, predates 00N scheme)
│   ├── 003-m2-1-claude-invocation/
│   ├── 004-m2-2-mempalace/
│   ├── 005-m2-2-1-finalize-ticket/
│   ├── 006-m2-2-2-compliance-calibration/
│   ├── 007-m2-3-infisical-vault/
│   └── _context/                          # filenames mix dots/hyphens (historical)
│       ├── m1-context.md
│       ├── m2.1-context.md
│       ├── m2.2-context.md
│       ├── m2-2-1-context.md
│       ├── m2.2.2-context.md
│       └── m2.3-context.md
├── docs/
│   ├── architecture-reconciliation-2026-04-24.md   # frozen decision-provenance snapshot
│   ├── mcp-registry-candidates.md         # M8 input: MCPJungle commitment
│   ├── skill-registry-candidates.md       # M7 input: SkillHub commitment
│   ├── ops-checklist.md                   # post-migrate and post-deploy steps
│   ├── getting-started.md
│   ├── research/m2-spike.md               # spike outputs feeding later milestones
│   ├── security/vault-threat-model.md
│   ├── forensics/
│   │   └── pgmcp-three-bug-chain.md       # post-M2.2.2 root-cause investigation
│   ├── issues/
│   │   ├── agent-workspace-sandboxing.md  # Docker-per-agent fix planned post-M3
│   │   └── cost-telemetry-blind-spot.md   # supervisor signal-handling fix
│   └── retros/                            # m1, m1-retro-addendum, m2-1, m2-2,
│                                          # m2-2-1, m2-2-2, m2-2-x-compliance-retro, m2-3
├── supervisor/                            # Go binary
│   ├── internal/                          # 19 packages (claudeproto, mcpconfig, pgmcp,
│   │                                      # agents, spawn/, mempalace, hygiene, finalize,
│   │                                      # vault, config, store, events, pgdb, recovery,
│   │                                      # health, concurrency, testdb, ...)
│   └── tools/vaultlog/                    # M2.3 custom go vet analyzer
├── dashboard/                             # Next.js 16 app (M3 — active)
├── migrations/                            # SQL, consumed by both Go (sqlc) and TS (Drizzle)
│   └── seed/                              # engineer.md, qa-engineer.md (embedded via
│                                          # +embed-agent-md markers into M2.2 / M2.2.2 migrations)
├── experiment-results/                    # exploratory matrices, not production
├── examples/                              # toy company YAML, example agent.md files
├── ARCHITECTURE.md                        # this file
├── RATIONALE.md
├── AGENTS.md
├── CONTRIBUTING.md
└── README.md
```

Real operator configs (`company.md`, `agents/*`, workspaces) live outside the repo. Example configs in `examples/` use a fictitious company so the public repo is clonable and runnable without containing operator-specific data.

**Licensing:** specs under CC-BY-4.0 (propagate freely), code under AGPL-3.0-only (protects against cloud reselling). Specs and code have different licenses intentionally.

**Contribution stance:** explicit in CONTRIBUTING.md that response times are measured in weeks and the project is driven by a solo founder.

---

## Dashboard surfaces

The UI is the product. Views to build across M3 and M4:

1. **Org overview** — one row per department, shows open ticket count per column, active agent count vs cap, recent transitions, hygiene warnings
2. **Department Kanban** — columns with tickets, drag to move (writes transition), click a ticket to see full detail (history, agent instances that touched it, palace entries linked)
3. **Agent activity feed** — live stream of agent spawns/completions, which ticket, which role, duration, exit status. Handles 5-10 `assistant` events per run without visual bloat (M2.1 real-claude observation).
4. **CEO chat** — conversation panel, tool-call traces visible (see what the CEO queried), ability to start new threads
5. **Hiring queue** — proposed hires, side-by-side editor, approve/reject (M7)
6. **Skill library** — installed skills per department, browse skills.sh inline, install/uninstall (M7)
7. **Memory hygiene** — tickets with thin or missing writes, clickable to see the transition and jump to the palace wing to backfill (M2.2 produces the data; M3/M6 build the UI)
8. **Settings per agent** — edit agent.md, model, skills, concurrency override, listens_for patterns
9. **Vault (secrets)** — Garrison-native UI on top of Infisical backend, agent-role-to-secret mapping, audit log, rotation status (M2.3 + M4)

---

## Open questions for later

- **Runaway control**: agent-spawned tickets can fan out. A per-department weekly ticket-creation budget, visible in the dashboard, covered in M8.
- **Cost accounting**: per-ticket and per-agent-instance token burn captured in M2.1 (`agent_instances.total_cost_usd`). Real baseline observed at ~$0.04 per trivial run. The aggregated view across tickets and agents comes in M6 alongside hygiene.
- **Cost cross-check with Anthropic billing dashboard**: M2.1 retro flagged this as unverified. The cross-check is currently blocked by the **cost-telemetry blind spot** (`docs/issues/cost-telemetry-blind-spot.md`) — clean-finalize runs record `$0.00` because the supervisor signal-kills before claude's `result` event lands. Once that fix lands (a supervisor-side signal-handling change letting `result` arrive before kill), do 5–10 supervisor runs and compare aggregate `total_cost_usd` to the Anthropic dashboard. Any discrepancy beyond the fix is a separate cost-capture bug.
- **Cross-department notification**: when the CTO sets a ticket to `needs_review` because it needs design input, who knows? A simple "needs_review" view in org overview is enough — the CEO doesn't need pushed alerts, you check the dashboard.
- **Model override per spawn**: skip for early milestones. All agents use their configured model. Add per-spawn overrides only when a specific task demonstrates a need.
- **Multi-company**: the schema has `companies` as a top-level entity but the initial build is single-company. Don't build multi-tenant until you need it.
- **Session persistence**: currently `--no-session-persistence`. Revisit when M3+ ticket work spans multiple turns.
- **Rate-limit back-off**: M2.1 observes `rate_limit_event` in stream-json but does not act on it. M6 adds back-off to the spawn loop.

---

## What this is *not*

- It is not a general orchestrator. Agents run Claude Code; that's the only runtime we care about supporting.
- It is not a no-code tool. Agents are configured by markdown + YAML. Dashboards edit those, but the model is "config files as first-class data in a database," not "visual workflow builder."
- It is not trying to replicate every feature of the earlier multi-agent setup I ran. It's trying to replace its value prop — cross-team agent orchestration — with something event-driven, memory-backed, and cheaper.
