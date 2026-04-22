# Garrison — Architecture v0.2

A zero-human company driver. Event-driven agent organization backed by Postgres + MemPalace, orchestrating Claude Code subprocesses as ephemeral workers. A rebuild after pain points I hit running multiple agents efficiently in an earlier setup: cheap when idle, fast to configure, with genuine institutional memory across restarts.

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

### Supervisor
A single long-running Go binary. Its job:

- Listen on pg_notify channels via a dedicated `pgx` connection (not from the pool)
- Check concurrency caps before spawning
- Spawn Claude Code subprocesses with the right `agent.md`, `cwd`, skill set, and MCP tool allowlist
- Track live agent processes in Postgres (pid, ticket, started_at, department)
- Clean up on exit, handle crashes, enforce timeouts via `context.Context`

The supervisor does not reason. It is a process manager.

**Stack**: Go 1.23+, `jackc/pgx/v5` for Postgres and LISTEN/NOTIFY, `sqlc` for typed query generation, `log/slog` for structured logging, `golang.org/x/sync/errgroup` for concurrent subsystem management, stdlib `os/exec` with `CommandContext` for subprocess lifecycle. Deployed as a single static binary via Docker on Hetzner + Coolify. The allowed-dependency list is locked — agents implementing the supervisor cannot add dependencies without an explicit hiring-style proposal.

**Missed-event handling**: LISTEN is the fast path, but notifications are lost during reconnects. The event tables include a `processed_at` column, and the supervisor runs a fallback poll (`SELECT ... WHERE processed_at IS NULL LIMIT N`) every N seconds as a safety net.

### Agent processes
Each agent is a Claude Code subprocess started by the supervisor. It receives:

- Its `agent.md` as the system prompt
- A scoped working directory (the department's workspace)
- A set of installed skills (from that department's `.claude/skills/`)
- MCP tools: MemPalace (always), Postgres (always), plus whatever the agent's config declares
- A wake-up context injection from `mempalace wake-up --wing <role_wing>` (~170 tokens)
- A specific event payload: the ticket it's acting on, the transition that triggered it

It runs until it has either completed the work (moved the ticket and written to the palace) or hit a timeout. Then the process exits.

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
Runs as an MCP server. Shared across every agent. The memory of the entire organization.

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
  exit_reason TEXT
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
  hygiene_status TEXT             -- 'clean', 'missing_diary', 'missing_kg', 'thin'
);

CREATE TABLE hiring_requests (
  id UUID PRIMARY KEY,
  department_id UUID REFERENCES departments(id),
  proposed_role_slug TEXT,
  proposed_agent_md TEXT,
  rationale TEXT,                 -- why the CEO thinks this hire is needed
  suggested_skills JSONB,         -- skills.sh repos the CEO found
  status TEXT NOT NULL,           -- 'proposed', 'approved', 'rejected'
  created_at TIMESTAMPTZ,
  decided_at TIMESTAMPTZ,
  decided_by TEXT                 -- user id or 'auto'
);

CREATE TABLE ceo_conversations (
  id UUID PRIMARY KEY,
  turn_index INT,
  role TEXT NOT NULL,             -- 'user', 'ceo'
  content TEXT NOT NULL,
  tool_calls JSONB,
  created_at TIMESTAMPTZ
);
```

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

---

## The bootstrap YAML

```yaml
company:
  name: Hey Anton
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

Specs first, code second. Each milestone is scoped to a single specify-cli spec that produces an implementable, end-to-end-functional chunk. No milestone ships half-built scaffolding; each one must be usable on a real Hey Anton workstream before moving to the next. The discipline: each milestone ends with a retro written to `wing_company / hall_events` in MemPalace, so the system dogfoods its own memory contract from day one.

**M1 — Event bus + supervisor core.** Spec the pg_notify contract, the processed_at fallback, concurrency accounting, and the subprocess spawn contract. Deliverable: a Go binary that listens on a channel, spawns a fake agent (`echo "hello from ticket $TICKET_ID"`), respects concurrency caps, handles timeouts and cleanup. No Claude Code yet, no MemPalace. Proves the plumbing and establishes the Go idioms before real agent work lands.

**M2 — First real agent loop.** Swap the fake spawn for actual Claude Code invocation with a minimal `agent.md`, one department (engineering), one trivial workflow, MemPalace MCP wired in. Create a ticket manually via SQL, watch an agent pick it up, do trivial work (write a hello-world file), write to the palace, move the ticket. This is the moment of truth for the architecture — if this works, everything else is extension. If it doesn't, the architecture is wrong and better to find out here than in M7.

**M3 — Dashboard read-only.** Next.js 16 + React 19 app reading from Postgres, showing department Kanban, ticket detail, agent activity feed. No mutations yet. Read-only-first forces you to actually watch the system behave for a few days before giving yourself (and agents) the ability to change state — which is when you catch the "oh, the event payload shape is wrong" class of bugs cheaply.

**M4 — Dashboard mutations.** Create tickets in UI, drag between columns, edit agent configs. Everything the operator does daily.

**M5 — CEO chat (summoned, read-only).** Conversation panel, summon-per-message pattern, Q&A only. CEO can query state and the palace but cannot create tickets yet.

**M6 — CEO ticket decomposition + hygiene checks.** CEO writes tickets from conversation. Hygiene dashboard shows thin/missing writes.

**M7 — Hiring flow.** skills.sh integration, proposal UI, approval writes agents + installs skills.

**M8 — Agent-spawned tickets, cross-department dependencies.** The last piece of the zero-human loop. Includes runaway control (per-department weekly ticket-creation budget).

---

## Spec-first workflow

Specs are produced with `specify-cli` and live in a dedicated `specs/` directory in the repo. Each milestone has one spec. Specs are scoped narrowly — the M1 spec covers the event bus and supervisor core only, not the full system. A 40-page monolithic spec is an antipattern; it produces paralysis rather than code.

Each spec is accompanied by a short `RATIONALE.md` capturing the decisions and trade-offs — the "why" that pure specify-cli output omits. Example rationales to capture: why soft gates instead of hard gates, why summoned CEO instead of long-running, why skills.sh instead of a curated library, why Go instead of Python/TS for the supervisor.

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

---

## Open source

The project is intended for open source release. The specs are the primary contribution — more valuable than the code itself, because the code can be regenerated from good specs in a weekend, but the specs encode the architectural thinking that's hard to reproduce.

**Repo layout:**
```
garrison/
├── specs/              # specify-cli output, one dir per milestone
│   ├── m1-event-bus/
│   ├── m2-agent-loop/
│   └── ...
├── supervisor/         # Go binary
├── dashboard/          # Next.js 16 app
├── migrations/         # SQL, consumed by both Go (sqlc) and TS (Drizzle)
├── examples/           # toy company YAML, example agent.md files
├── RATIONALE.md        # top-level design rationale
├── CONTRIBUTING.md     # explicit about response times and scope
└── README.md
```

Real Hey Anton configs (`company.md`, `agents/*`, workspaces) live outside the repo. Example configs in `examples/` use a fictitious company so the public repo is clonable and runnable without containing operator-specific data.

**Licensing:** specs under CC-BY or MIT (propagate freely), code under a license TBD based on solo-founder protection needs (MIT if permissive, AGPL or BSL if protecting against cloud reselling). Specs and code can have different licenses — this is intentional.

**Contribution stance:** explicit in CONTRIBUTING.md that response times are measured in weeks and the project is driven by a solo founder. Open to source-available-not-collaborative if contribution overhead eats too much time; no shame in that stance.

---

## Dashboard surfaces

The UI is the product. Views to build across M3 and M4:

1. **Org overview** — one row per department, shows open ticket count per column, active agent count vs cap, recent transitions, hygiene warnings
2. **Department Kanban** — columns with tickets, drag to move (writes transition), click a ticket to see full detail (history, agent instances that touched it, palace entries linked)
3. **Agent activity feed** — live stream of agent spawns/completions, which ticket, which role, duration, exit status
4. **CEO chat** — conversation panel, tool-call traces visible (see what the CEO queried), ability to start new threads
5. **Hiring queue** — proposed hires, side-by-side editor, approve/reject (M7)
6. **Skill library** — installed skills per department, browse skills.sh inline, install/uninstall (M7)
7. **Memory hygiene** — tickets with thin or missing writes, clickable to see the transition and jump to the palace wing to backfill (M6)
8. **Settings per agent** — edit agent.md, model, skills, concurrency override, listens_for patterns

---

## Open questions for later

- **Runaway control**: agent-spawned tickets can fan out. A per-department weekly ticket-creation budget, visible in the dashboard, covered in M8.
- **Cost accounting**: per-ticket and per-agent-instance token burn. Log from the Claude Code SDK's response, roll up in the dashboard. Add in M6 alongside hygiene.
- **Cross-department notification**: when the CTO sets a ticket to `needs_review` because it needs design input, who knows? A simple "needs_review" view in org overview is enough — the CEO doesn't need pushed alerts, you check the dashboard.
- **Model override per spawn**: skip for early milestones. All agents use their configured model. Add per-spawn overrides only when a specific task demonstrates a need.
- **Multi-company**: the schema has `companies` as a top-level entity but the initial build is single-company. Don't build multi-tenant until you need it.
- **Naming**: "Garrison" is a working title. Commit to a name before the first public commit — public repos are much harder to rename than private ones.

---

## What this is *not*

- It is not a general orchestrator. Agents run Claude Code; that's the only runtime we care about supporting.
- It is not a no-code tool. Agents are configured by markdown + YAML. Dashboards edit those, but the model is "config files as first-class data in a database," not "visual workflow builder."
- It is not trying to replicate every feature of the earlier multi-agent setup I ran. It's trying to solve the core value — cross-team agent orchestration — with something event-driven, memory-backed, and cheaper.
