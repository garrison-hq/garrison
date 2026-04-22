# RATIONALE.md

This document explains *why* Garrison is designed the way it is. The architecture doc tells you what the system does; this tells you what it does *not* do, and what we considered and rejected.

Read this before proposing changes. Most "wouldn't it be better if..." questions are answered here.

---

## 1. Why event-driven instead of polling

**Decision**: Postgres + pg_notify as the event bus. Agents spawn on events, run to completion, die. No polling loops, no scheduled heartbeats.

**Alternatives considered**:
- Scheduled heartbeats per agent (what I had running before)
- Message queue (Redis Streams, RabbitMQ, NATS)
- Serverless triggers

**Why pg_notify won**:
- Zero idle cost. Agents consume no tokens when no events match them. This is the single biggest failure of the scheduled-heartbeat approach I was running before — most agents spent most of their wake cycles checking a queue, finding nothing, and going back to sleep, burning tokens to produce no output.
- Transactional. The event fires inside the same transaction that changed state, so there's no window where state is updated but the event wasn't delivered.
- No extra infrastructure. Postgres is already required for application state; reusing it as the bus means one fewer thing to run, monitor, back up, and learn.
- Well-understood failure modes. Postgres disconnects are survivable with reconnect + processed_at fallback; message broker failure modes are more varied and less familiar.

**Trade-off accepted**: pg_notify is not at-least-once by default. Notifications are lost during reconnects. We pay for this with a `processed_at` column and a fallback poll that catches missed events. This is a small amount of extra code for a meaningful reduction in infrastructure complexity.

---

## 2. Why ephemeral agents instead of long-running ones

**Decision**: Agents are subprocesses spawned on events and terminated after completion. No long-running agent daemons.

**Alternatives considered**:
- Long-running agent processes with an internal event loop
- Persistent conversation state held in memory per agent
- Agent pools that keep N workers warm

**Why ephemeral won**:
- Clean state every spawn. An agent that ran a minute ago has zero influence on an agent running now except through persistent storage. This makes debugging tractable — every failure is reproducible from the event payload and the palace state at the time.
- Zero cost when idle. A long-running agent holds context in memory that costs tokens on every resume. An ephemeral agent costs nothing until it's needed.
- Parallelism is free. Running three frontend engineers simultaneously means spawning three processes, not managing a pool.
- Crash recovery is automatic. A crashed agent leaves no state to reconcile; the supervisor spawns a fresh one when the event fires again.

**Trade-off accepted**: Every spawn pays a cold-start cost — loading skills, reading the agent.md, executing the wake-up palace query. This is probably ~5-15 seconds of overhead per spawn. We accept this because tickets take minutes to hours; a 10-second spawn tax is negligible. If that ever stops being true for a specific agent type, pooling can be added for that type only, not system-wide.

---

## 3. Why MemPalace as memory instead of agent context

**Decision**: MemPalace holds all cross-agent and cross-time memory. Agents do not rely on their own context window to remember anything past their current invocation.

**Alternatives considered**:
- Long context windows with conversation history
- Per-agent vector stores
- Rolling summaries regenerated on each spawn
- Shared context files in each department's workspace

**Why MemPalace won**:
- Structured navigation via wings + rooms + halls outperforms flat vector search on real-world recall. The 34% retrieval improvement from wing+room filtering is not cosmetic.
- Wings shared across instances of the same role means institutional expertise accumulates. Instance #47 of frontend-engineer benefits from what instance #12 learned six months ago. This is the killer feature that context-window-based memory models cannot replicate.
- The temporal knowledge graph handles "who decided what when" natively. This is exactly what a CEO needs to answer "why are we doing X?".
- Local, free, no API dependency. Aligns with the self-hosted ethos.
- MCP-native, so every agent gets it through the same baseline tool set.

**Trade-off accepted**: MemPalace's retrieval quality depends entirely on what gets written. Soft gates (see §5) mean write quality is not enforced. We mitigate this with the completion protocol in each agent.md and the memory hygiene dashboard — but ultimately if the operator doesn't maintain the palace, the CEO gets dumber over time. This is a known risk and a weekly-review discipline, not a solved problem.

---

## 4. Why Postgres as state store instead of a filesystem

**Decision**: Current state of tickets, agents, departments, hiring requests, concurrency, and conversations lives in Postgres rows. Not in markdown files checked into git.

**Alternatives considered**:
- File-based state (ticket per markdown file, agent per YAML, git as the store)
- A lighter DB (SQLite) with file-based supplements
- Event sourcing with Postgres as the log

**Why Postgres won**:
- Postgres + pg_notify is the event bus already (§1). Using it for state too is zero marginal cost.
- Concurrency accounting under load requires row-level locking. Filesystems don't do this well.
- Queryable from the web UI without parsing. Every dashboard view is a SELECT.
- Transactions. A ticket transition that writes `ticket_transitions`, updates `tickets.column_slug`, and fires an event must happen atomically; Postgres gives this for free.

**Trade-off accepted**: Git is not a first-class citizen for agent configs. The hiring flow is UI-driven, not git-driven (§8). Some users will expect "I can `git diff` to see what changed about my org" — they will be disappointed. We provide YAML export/import as an escape hatch for version control and backup, but the live system is Postgres.

---

## 5. Why soft gates instead of hard gates on memory writes

**Decision**: Workflow transitions succeed even if the expected MemPalace writes didn't happen. A dashboard surfaces hygiene issues for weekly review.

**Alternatives considered**:
- Hard gates: transitions fail, ticket routes to needs_review until writes are correct
- Mandatory writes enforced by the supervisor before allowing the transition
- Advisory only: log warnings and move on, no dashboard

**Why soft won**:
- Hard gates create cascading failures. An agent that finishes code correctly but writes a thin diary would get its ticket rejected, forcing human intervention on a working deliverable. This optimizes for the wrong thing.
- The cost of a missed diary entry is diffuse — the CEO is slightly less informed — not acute. Diffuse costs tolerate eventual consistency better than acute costs do.
- Weekly review is a reasonable cadence for a solo operator. A dashboard that surfaces issues and lets you backfill in bulk is more pragmatic than real-time enforcement.
- Advisory-only is too weak: issues accumulate invisibly until they matter, then it's too late.

**Trade-off accepted**: If the operator doesn't review the hygiene dashboard regularly, memory quality degrades silently. This is a discipline problem we're choosing not to solve with code. We mitigate with the completion protocol in each agent.md making writes explicit and prominent, and with dashboard surfaces that make hygiene issues visible at a glance.

---

## 6. Why the CEO is summoned instead of always-on

**Decision**: The CEO is not a long-running agent. Each user message spawns a fresh CEO process that queries state, replies, and exits.

**Alternatives considered**:
- Long-running CEO with a live context window
- Hybrid: summoned for chat, scheduled background job to "read briefings" and update palace
- Periodic briefing pushes from department managers into the CEO's context

**Why summoned won**:
- Consistent with §2 (ephemeral agents). Making the CEO a special case is an architectural smell.
- No stale context. A long-running CEO could drift from ground truth — summoning forces every conversation to start from current state.
- Zero idle cost. A CEO that runs once a week costs nothing the other six days.
- MemPalace is already the memory layer. A long-running CEO would duplicate what MemPalace provides, creating two sources of truth.

**Trade-off accepted**: Every CEO message pays the cold-start cost (§2). For a conversational interface where the human expects sub-second latency, this is noticeable. We accept it because CEO conversations are infrequent and thoughtful, not chat-app-style. If this becomes painful, the first mitigation is to keep the CEO process warm for N minutes after the last message, then kill it — not to run it permanently.

---

## 7. Why skills.sh instead of a curated skill library

**Decision**: Skills come from skills.sh, the public agent skills marketplace, installed per-department via `npx skills add`.

**Alternatives considered**:
- Curated internal skill library (hand-write skills and version them in the repo)
- Inline skills inside each agent.md
- No skills abstraction at all — everything is in agent.md

**Why skills.sh won**:
- The ecosystem effect. 91k+ skills indexed, 15+ agent runtimes supported. Building on this means benefiting from other people's skill authoring without maintenance.
- Separation of concerns. Agent.md is who-the-agent-is; skills are what-the-agent-knows. Mixing them bloats agent.md and prevents reuse.
- Claude Code native support. Skills.sh installs drop into `.claude/skills/` where Claude Code picks them up automatically.
- Existing skills like `frontend-design`, `vercel-react-best-practices`, `systematic-debugging` are high-quality and would take weeks to reproduce.

**Trade-off accepted**: We depend on an external registry and its availability. If skills.sh goes down or changes its contract, our hiring flow breaks. We mitigate by caching installed skills locally (they're just files) and by auditing new skills during the hiring approval step. We do not mitigate by running our own mirror — the overhead outweighs the risk at our scale.

---

## 8. Why UI-driven hiring instead of git-PR hiring

**Decision**: Hiring happens in the web UI. The CEO proposes, the operator approves in the dashboard, the system writes to Postgres and installs skills. No git PR.

**Alternatives considered**:
- CEO opens a PR in a central `org` repo with the new agent.md; human reviews and merges; system hot-reloads
- Agent configs in a git repo, synced to Postgres on push
- Hybrid: configs live in git, UI is a view over them

**Why UI-driven won**:
- Solo operator. The PR workflow is optimized for teams reviewing each other's work. For one person reviewing AI-generated proposals, the UI is faster and the side-by-side skill browser is more useful than a GitHub diff.
- The hiring flow has richer structure than text-in-a-file: skill descriptions, install counts, impact previews, approval notes back to the CEO. A UI renders this natively; a PR crams it into markdown.
- Consistent with §4 (Postgres as state store). Git PR hiring creates two sources of truth.

**Trade-off accepted**: No native version control of agent configs. We provide YAML export/import as the escape hatch (§4). If you want to version-control the org state, you run the export on a cron and commit the output. This is good enough for audit and rollback without inverting the core model.

---

## 9. Why Go for the supervisor

**Decision**: The supervisor is written in Go. Not Python, not TypeScript, not Java.

**Alternatives considered**:
- Python with asyncio (fast to write, operator already knows it)
- TypeScript/Node (shared language with the dashboard, shared types)
- Java/Kotlin (operator's historical background)

**Why Go won**:
- The workload is subprocess supervision and event dispatch — Go's goroutines, channels, and `context.Context` cancellation map directly onto this. This is what Go was designed for.
- Static typing catches event payload shape drift at compile time. pg_notify payloads are JSON; without types, an agent-written supervisor is a debugging swamp.
- Single static binary deployment. One `go build` produces a Linux binary; the Dockerfile is six lines. Aligns with the self-hosted Hetzner+Coolify ethos.
- Agent-generated Go is more reliable than agent-generated Python. The compiler catches hallucinated APIs and type errors before runtime, which matters when an agent writes the code.
- Low-dependency culture. Go projects tend to stay small; Python and Node projects accrete dependencies. A supervisor should stay small.

**Trade-off accepted**:
- The operator does not know Go fluently. Accepted because agents write the code and the operator reads and reviews it — Go's readability is strong enough that review is manageable.
- No shared types with the dashboard. Mitigated by using `sqlc` on the Go side and Drizzle on the TS side, both deriving from the same SQL migrations. The SQL is the shared contract, not the type definitions.
- The ecosystem for AI tooling is thinner than Python's. Accepted because the supervisor does not do AI work — it supervises processes. If AI work needs to happen inside the supervisor itself, it's done by spawning an agent, not by calling an LLM library directly.

---

## 10. Why specs-first instead of code-first

**Decision**: Each milestone begins with a specify-cli spec. Code is produced from the spec, not the reverse.

**Alternatives considered**:
- Code-first: write, iterate, document after
- Architecture-doc-only: no formal specs, just the architecture.md
- Monolithic spec: one big spec covering everything up front

**Why narrow specs won**:
- Specs are what agents will implement. Specs are a machine-targetable artifact; architecture prose is not.
- Narrow milestone-scoped specs produce implementable chunks. Monolithic specs produce analysis paralysis.
- Specs are the highest-leverage open-source contribution. Code is regeneratable from good specs; specs are not regeneratable from code.
- Writing the spec forces the design work to happen before implementation. Code-first tends to bake in decisions by accident.

**Trade-off accepted**: Spec-writing adds time at the start of each milestone. We accept this because the alternative is correcting misaligned implementations after the fact, which is more expensive. If a spec is producing too much paralysis, the remedy is to narrow the spec further, not to abandon spec-first.

---

## 11. Why per-department concurrency caps instead of other bounds

**Decision**: Parallel agent instances are bounded by a per-department cap (e.g. Engineering = 3). Not per-agent-type, not per-cost-budget, not unbounded.

**Alternatives considered**:
- Per-agent-type cap in agent.md (frontend-engineer = 3)
- Token-budget-based throttling
- Unbounded, trusting infrastructure to throttle naturally
- Fixed org-wide cap

**Why per-department won**:
- Matches the real-world mental model: a department has a certain capacity. "Engineering has 3 engineers right now" is how a human thinks about team load.
- Simpler UI. One knob per department, editable live in the dashboard.
- Composable. If a department wants per-agent-type caps later, they layer on top of the department cap without changing the core model.
- Predictable cost behavior. The maximum active agents is bounded, so the maximum in-flight token burn is bounded.

**Trade-off accepted**: A department running three frontend-engineers can't simultaneously run a backend-engineer even if the workload would allow it. We accept this because it's easy to bump the cap in the dashboard when needed, and because starving some roles to run others is rarely the desired behavior.

---

## 12. What this system is not

Collected from the decisions above. These are the things someone might reasonably expect that we explicitly don't do:

- **Not a general-purpose agent orchestrator.** Claude Code is the only runtime we support. If another runtime matters later, it's a fork, not a feature.
- **Not a no-code tool.** Agents are configured by markdown + YAML. The UI edits them, but the mental model is "config files as first-class data in a database."
- **Not a replacement for every feature of the earlier setup I ran.** I rebuilt the core value — cross-team agent orchestration — event-driven and memory-backed. Specific features that aren't here (e.g. the CEO-as-dispatcher heartbeat model) were rejected deliberately.
- **Not a team-collaboration tool.** Built for a solo operator. Multi-user access control, permissions, activity attribution across humans — none of these exist.
- **Not git-native.** Git is for code the agents write, not for system state or agent configs.
- **Not cloud-first.** Self-hosted on Hetzner is the primary deployment model. Cloud deployments may work but are not optimized for.
- **Not a framework.** It's a specific system for a specific job. The specs are reusable; the code is not particularly abstracted for reuse.

---

## Meta: when to update this document

Update RATIONALE.md when:

- A decision is reversed (delete the old rationale, write the new one, note the reversal)
- A trade-off you accepted becomes intolerable in practice (write why and what changed)
- A new decision is made that future you or future contributors will want to understand

Do not update this document when:

- Implementation details change but the decision stands
- The code evolves but the underlying reasoning is intact
- You're tempted to document something the architecture doc already covers
