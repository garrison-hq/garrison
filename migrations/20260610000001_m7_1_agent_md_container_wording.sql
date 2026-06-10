-- M7.1 data migration: agent_md container wording (FR-015; Q1; D23).
--
-- Container-executed agents carry no mempalace MCP server (the
-- per-spawn config is postgres + finalize + garrison-mutate only), so
-- the seeded agent_md must stop instructing agents to call mempalace
-- tools mid-turn. Wording shifts to "use the wake-up context
-- provided"; palace writes continue exclusively via the finalize
-- payload — that wording stays untouched.
--
-- Data-only: no DDL, so no sqlc schema entry and no drizzle pull.
-- Every UPDATE is replace(agent_md, <old>, <new>) with old-strings
-- lifted verbatim from 20260424000006_m2_2_2_compliance_calibration
-- .sql, so the statements no-op on operator-edited rows where the
-- seeded text is gone (idempotent, non-clobbering). Down restores the
-- seeded text via the inverse replaces and equally no-ops where the
-- up never matched.

-- +goose Up

-- (1) engineer: seeded "Mid-turn MemPalace usage (optional)" section
-- becomes the wake-up-context paragraph; `mempalace MCP (…)` drops
-- out of "Tools available".
-- +goose StatementBegin
UPDATE agents
SET agent_md = replace(replace(agent_md,
$m71_eng_mid_old$## Mid-turn MemPalace usage (optional)

Before starting work, decide whether palace context is needed:

- **Skip palace search if** the ticket is straightforward — small
  diary entries, hello-world-style tasks, one-line changes. Palace
  search isn't cost-effective here (<5% of budget).
- **Search palace if** the ticket mentions cross-cutting concerns,
  references prior tickets or ongoing threads, or the objective is
  ambiguous enough that prior context would help. Budget up to 3 tool
  calls: one `mempalace_search(wing='wing_frontend_engineer',
  query=<keywords>)` plus one optional `mempalace_list_drawers`
  narrowing plus one optional targeted read.
- **In doubt, skip.** Finalizing without palace context is recoverable
  (operator can enrich the diary after). Hitting the budget cap
  mid-exploration is not.

Mid-flight you MAY call `mempalace_add_drawer(wing=
'wing_frontend_engineer', room='hall_discoveries', content='...')` to
record a reusable pattern. Those writes land alongside (not instead
of) the supervisor's completion diary.$m71_eng_mid_old$,
$m71_eng_mid_new$## Mid-turn context

Use the wake-up context the supervisor injected at turn start — it is
your palace context for this turn. There are no mid-turn palace
tools; anything worth remembering goes into your `finalize_ticket`
payload (diary entry, discoveries, KG triples).$m71_eng_mid_new$),
$m71_eng_tools_old$postgres MCP (read-only SQL), mempalace MCP (search, read, list,
add-drawer, kg-add, etc.), finalize MCP (the completion tool),
workspace file tools (Read, Write, Edit).$m71_eng_tools_old$,
$m71_eng_tools_new$postgres MCP (read-only SQL), finalize MCP (the completion tool),
workspace file tools (Read, Write, Edit).$m71_eng_tools_new$)
WHERE role_slug = 'engineer';
-- +goose StatementEnd

-- (2) qa-engineer: same two rewrites against the QA seed text.
-- +goose StatementBegin
UPDATE agents
SET agent_md = replace(replace(agent_md,
$m71_qa_mid_old$## Mid-turn MemPalace usage (optional)

QA's role is verification of a handoff, not greenfield work:

- **Always read engineer's wing diary for this ticket** — the
  handoff depends on it, not optional. The drawer's body opens with
  the objective prose (supervisor-prepended), so semantic search
  lands reliably.
- **Skip searches outside `wing_frontend_engineer`** unless the
  engineer's diary explicitly references them. Don't wander the
  palace looking for tangentially-related context.
- **Budget up to 3 calls for the diary lookup**: one
  `mempalace_search(wing='wing_frontend_engineer', query=<ticket.objective>)`
  plus one optional `mempalace_list_drawers` narrowing plus one
  optional targeted read.

You MAY record QA findings mid-flight via
`mempalace_add_drawer(wing='wing_qa_engineer', room='hall_discoveries',
content='...')`. Those are separate from the supervisor's diary.$m71_qa_mid_old$,
$m71_qa_mid_new$## Mid-turn context

Use the wake-up context the supervisor injected at turn start — it is
your palace context for this turn, including the prior engineering
work behind the handoff. There are no mid-turn palace tools; record
QA findings in your `finalize_ticket` payload (diary entry,
discoveries, KG triples).$m71_qa_mid_new$),
$m71_qa_tools_old$postgres MCP (read-only SQL), mempalace MCP (especially
`mempalace_search` for the engineer's diary), finalize MCP (the
completion tool), workspace file tools for read-only inspection.$m71_qa_tools_old$,
$m71_qa_tools_new$postgres MCP (read-only SQL), finalize MCP (the completion tool),
workspace file tools for read-only inspection.$m71_qa_tools_new$)
WHERE role_slug = 'qa-engineer';
-- +goose StatementEnd

-- +goose Down

-- Inverse replaces restore the M2.2.2 seeded wording. Each no-ops on
-- rows the up never touched (operator-edited agent_md).
-- +goose StatementBegin
UPDATE agents
SET agent_md = replace(replace(agent_md,
$m71_eng_mid_new$## Mid-turn context

Use the wake-up context the supervisor injected at turn start — it is
your palace context for this turn. There are no mid-turn palace
tools; anything worth remembering goes into your `finalize_ticket`
payload (diary entry, discoveries, KG triples).$m71_eng_mid_new$,
$m71_eng_mid_old$## Mid-turn MemPalace usage (optional)

Before starting work, decide whether palace context is needed:

- **Skip palace search if** the ticket is straightforward — small
  diary entries, hello-world-style tasks, one-line changes. Palace
  search isn't cost-effective here (<5% of budget).
- **Search palace if** the ticket mentions cross-cutting concerns,
  references prior tickets or ongoing threads, or the objective is
  ambiguous enough that prior context would help. Budget up to 3 tool
  calls: one `mempalace_search(wing='wing_frontend_engineer',
  query=<keywords>)` plus one optional `mempalace_list_drawers`
  narrowing plus one optional targeted read.
- **In doubt, skip.** Finalizing without palace context is recoverable
  (operator can enrich the diary after). Hitting the budget cap
  mid-exploration is not.

Mid-flight you MAY call `mempalace_add_drawer(wing=
'wing_frontend_engineer', room='hall_discoveries', content='...')` to
record a reusable pattern. Those writes land alongside (not instead
of) the supervisor's completion diary.$m71_eng_mid_old$),
$m71_eng_tools_new$postgres MCP (read-only SQL), finalize MCP (the completion tool),
workspace file tools (Read, Write, Edit).$m71_eng_tools_new$,
$m71_eng_tools_old$postgres MCP (read-only SQL), mempalace MCP (search, read, list,
add-drawer, kg-add, etc.), finalize MCP (the completion tool),
workspace file tools (Read, Write, Edit).$m71_eng_tools_old$)
WHERE role_slug = 'engineer';
-- +goose StatementEnd

-- +goose StatementBegin
UPDATE agents
SET agent_md = replace(replace(agent_md,
$m71_qa_mid_new$## Mid-turn context

Use the wake-up context the supervisor injected at turn start — it is
your palace context for this turn, including the prior engineering
work behind the handoff. There are no mid-turn palace tools; record
QA findings in your `finalize_ticket` payload (diary entry,
discoveries, KG triples).$m71_qa_mid_new$,
$m71_qa_mid_old$## Mid-turn MemPalace usage (optional)

QA's role is verification of a handoff, not greenfield work:

- **Always read engineer's wing diary for this ticket** — the
  handoff depends on it, not optional. The drawer's body opens with
  the objective prose (supervisor-prepended), so semantic search
  lands reliably.
- **Skip searches outside `wing_frontend_engineer`** unless the
  engineer's diary explicitly references them. Don't wander the
  palace looking for tangentially-related context.
- **Budget up to 3 calls for the diary lookup**: one
  `mempalace_search(wing='wing_frontend_engineer', query=<ticket.objective>)`
  plus one optional `mempalace_list_drawers` narrowing plus one
  optional targeted read.

You MAY record QA findings mid-flight via
`mempalace_add_drawer(wing='wing_qa_engineer', room='hall_discoveries',
content='...')`. Those are separate from the supervisor's diary.$m71_qa_mid_old$),
$m71_qa_tools_new$postgres MCP (read-only SQL), finalize MCP (the completion tool),
workspace file tools for read-only inspection.$m71_qa_tools_new$,
$m71_qa_tools_old$postgres MCP (read-only SQL), mempalace MCP (especially
`mempalace_search` for the engineer's diary), finalize MCP (the
completion tool), workspace file tools for read-only inspection.$m71_qa_tools_old$)
WHERE role_slug = 'qa-engineer';
-- +goose StatementEnd
