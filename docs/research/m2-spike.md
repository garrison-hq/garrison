# M2 research spike — Claude Code + MemPalace

**Status**: exploratory findings, not production code.
**Duration**: ~3 hours of active work on 2026-04-22.
**Scratch dir**: `~/scratch/m2-spike/` (throwaway, not tracked).
**Invocations**: 9 Claude Code runs (EXP1–EXP9), ~20 MemPalace operations (CLI init/mine/search/wake-up + MCP tools/list + 12 drawer writes).
**Spend**: ~$0.06 total on Claude API (haiku-4-5, `--max-budget-usd` caps, `--no-session-persistence`).
**Binding**: this document is input to `specs/002-m2-1-claude-code/` and `specs/003-m2-2-mempalace/`.

---

## 1. Environment

- Host: Fedora 43 (Linux 6.19.8), local dev box.
- `claude` binary: `~/.local/share/claude/versions/2.1.117` (ELF x86-64, ~250 MB). `~/.local/bin/claude` is a symlink to that path.
- Auth: OAuth via `~/.claude/` (`apiKeySource:"none"` seen in stream-json init).
- MemPalace: `pip install mempalace` → 3.3.2, installed in a venv at `~/scratch/m2-spike/.venv/`.
- Python: 3.14 (venv).
- MCP servers already registered via `claude mcp add`: `claude.ai Google Drive`, `claude.ai Google Calendar`, `claude.ai Gmail` (all `needs-auth`).

---

## 2. Part 1 — Claude Code subprocess characterization

### 2.1 Invocation and output

- Non-interactive mode is `claude -p "<prompt>"` (alias `--print`). Exits when the turn completes.
- `--output-format` supports `text` (default), `json`, `stream-json`. `stream-json` requires `--verbose`.
- `json`: single object on stdout with `result`, `total_cost_usd`, `usage` (input/output/cache tokens), `modelUsage` (per-model), `permission_denials`, `terminal_reason`, `duration_ms`, `duration_api_ms`, `session_id`, `uuid`.
- `stream-json`: NDJSON, one event per line. Types observed:
  - `system` / `subtype:"init"` — includes `tools`, `mcp_servers`, `model`, `permissionMode`, `slash_commands`, `skills`, `plugins`, `memory_paths`, `apiKeySource`, `claude_code_version`, `cwd`, `session_id`.
  - `assistant` — message with `content[]` items of type `thinking` (with `signature`), `text`, or `tool_use`.
  - `user` — `tool_result` items with `is_error` boolean and full result text.
  - `system` / `subtype:"task_started"` — background task notifications (internal to claude's own task system).
  - `rate_limit_event` — fires mid-stream. Fields: `status` ("allowed_warning"), `resetsAt` (unix), `rateLimitType` (e.g. "seven_day"), `utilization` (0–1), `isUsingOverage`, `surpassedThreshold`. Critical: **the supervisor can detect rate-limit pressure from stdout alone, no side channel needed.**
  - `result` — terminal event with `duration_ms`, `total_cost_usd`, `terminal_reason`, full result text, `permission_denials`.

### 2.2 System prompt and inputs

- `--system-prompt "<text>"` injected verbatim at turn start. EXP4 used `--system-prompt "$(cat agents/hello.md)"`; the agent obeyed the format ("COMPLETE: …", one line, no tools) cleanly. No system_prompt field appears in the stream-json init event, but behavior confirmed the prompt was applied.
- Stdin is not read in `-p` mode (prompt comes from argv); stdin open but claude did not consume it.

### 2.3 Model selection and budget

- `--model claude-haiku-4-5-20251001` switches successfully. Bad model names fail fast: EXP6 with `--model claude-fake-model-that-doesnt-exist` → **exit 1 in 2s** with stderr `"There's an issue with the selected model (...). It may not exist or you may not have access to it."` Clean failure mode — supervisor can detect unsupported models.
- `--max-budget-usd 0.02` tested; all experiments stayed well under ($0.009–$0.03 per invocation with cache hits).

### 2.4 Cost surfaces

- `total_cost_usd` appears **only in the final `result` event / JSON object**, not updated incrementally in stream-json. Per-turn billing inside `usage.iterations[]` but no live running total mid-stream.
- `modelUsage[<model>]` breaks cost down per model used. Useful for supervisor cost attribution when multi-model runs happen.
- Cache tokens are tracked separately: `cache_creation_input_tokens`, `cache_read_input_tokens`. In our experiments, **cache reads dominated** (23k–85k tokens read vs 8–26 fresh input). Cache hits are the cost kill switch — if Garrison re-invokes with similar system prompts, costs drop an order of magnitude.

### 2.5 MCP configuration

- `claude mcp add <name> -- <cmd> [args...]`: stdio transport (default).
- `claude mcp add <name> --transport http <url>` and `--transport sse <url>`: remote transports.
- `claude mcp add-json <name> '<json>'`: full spec at once.
- `--mcp-config <file>` + `--strict-mcp-config` supplies MCP config for a single invocation, overriding global config.
- **Critical gap**: EXP7 used `--strict-mcp-config --mcp-config bad-mcp.json` with a nonexistent command (`/bin/nonexistent-command`). Claude **exited 0 with `terminal_reason:"completed"`, `is_error:false`**. The MCP server's failure to start was **silently tolerated**. `--strict-mcp-config` per `--help` means "only the specified servers are loaded" — it does *not* guarantee those servers start. **Supervisor must independently health-check each MCP server; cannot rely on claude's exit code.**

### 2.6 `--bare` mode

EXP5 with `--bare -p "hi"`:

- stream-json init shows `tools: ["Bash","Edit","Read"]` (three tools only), `mcp_servers:[]`, `memory_paths:null`, `plugins:[]`, `skills:["update-config","debug","simplify","batch","fewer-permission-prompts"]`.
- **Result**: `"Not logged in · Please run /login"`, `exit 1`, `cost: 0`, `terminal_reason:"completed"`.
- Per `claude --help`: "--bare ... Anthropic auth is strictly ANTHROPIC_API_KEY or apiKeyHelper via --settings (OAuth and keychain are never read)."
- For Garrison: `--bare` is the right primitive for subprocess isolation (tiny tool surface, no MCP contamination, no memory contamination, no plugins) but **requires `ANTHROPIC_API_KEY` in the spawn env or an `apiKeyHelper` via `--settings <path>`**. OAuth keychain is ignored by design. Plan the secret-handling in M2.1.

### 2.7 Kill behavior

- **EXP8 (SIGTERM to PID, no `setsid`)**: claude spawned with default pgid (shared with shell). After `kill -TERM $pid`, `wait` returned exit 143 (128+15) within 1ms. BUT: stream-json events continued to be written to stdout **~2.5s after** the kill timestamp. The claude process emitted a `result` event with `duration_ms:7402` despite being killed at wall-clock t+4s. Interpretation: either child processes kept writing, or buffered output flushed after signal. Either way, **PID-level SIGTERM is not sufficient for clean shutdown.**
- **EXP9 (SIGTERM to pgid via `setsid`)**: claude spawned under `setsid` (own pgid); `kill -TERM -$pgid` terminated the whole group in 1ms. `ps -o pid,pgid` after kill: process group gone.
- **Supervisor implication**: spawn claude with `syscall.SysProcAttr{Setpgid: true}` (Go) or `setsid`, and signal with `syscall.Kill(-pgid, SIGTERM)`. Do not trust PID-level kill to terminate children.

### 2.8 `--no-session-persistence`

- Tested throughout EXP1–EXP9. session_id is still issued (for in-run correlation), but no `~/.claude/projects/<cwd>/sessions/` JSONL files are left behind. Confirmed by spot-check: no new session files after EXP runs.
- For Garrison: every supervisor-spawned claude should pass `--no-session-persistence`. Session state belongs in the supervisor's Postgres, not in claude's filesystem log.

---

## 3. Part 2 — MemPalace MCP characterization

### 3.1 Install and CLI surface

- `pip install mempalace` (v3.3.2). Pure Python, no native build.
- Binary: `.venv/bin/mempalace` (Python entry point).
- Subcommands: `init`, `mine`, `sweep`, `search`, `compress`, `wake-up`, `split`, `hook`, `instructions`, `repair`, `mcp`, `migrate`, `status`.
- Palace path: `--palace <dir>` flag, or `MEMPALACE_PALACE` env, or default `~/.mempalace/palace`.

### 3.2 Init is interactive by default — `--yes` required

`mempalace init <dir>` scans the directory, auto-detects entities (wings) and rooms from folder names, then **prompts** for confirmation at stdin. In a non-interactive subprocess it crashes:

```
EOFError: EOF when reading a line
  File "...entity_detector.py", line 489, in confirm_entities
    choice = input("  Your choice [enter/edit/add]: ").strip().lower()
```

`mempalace init --yes .` is the non-interactive path. **For Garrison: any supervisor-invoked init must pass `--yes`.** This is not documented; discovered by reading the traceback.

Side effects of `init`:
- Writes `mempalace.yaml` into the **project directory** (not the palace dir).
- Writes `entities.json` into the project directory.
- **Auto-modifies `.gitignore`** to add `mempalace.yaml` and `entities.json`. (Critical: if Garrison's `supervisor/` dir ever gets `mempalace init` run on it, `.gitignore` will be mutated without explicit consent.)

### 3.3 Room and wing assignment

- Rooms are **inferred from top-level folder names** at init time. Example scan produced rooms: `runs`, `logs`, `agents`, `mempalace`, `design` (from `assets/`), `documentation` (from `docs/`), `testing` (from `tests/`), plus a fallback `general`.
- Wings are auto-detected entities (projects/people). Default naming aggregates multiple matches (e.g. "Auto", "Hybrid", "Setup", "Entity").
- **But** via MCP `add_drawer`, new wings can be created on the fly (see §3.6). The init-time schema is the *default* entity set, not a hard constraint.

### 3.4 Mining performance

- `mempalace mine <dir>` iterates all files, chunks them, computes embeddings (local sentence-transformers), writes to ChromaDB + SQLite.
- **Throughput is roughly linear in file count.** A 2-file corpus: 2.0s. The full scratch dir (~1k+ files including the cloned mempalace repo): pegged 99% CPU for 18 minutes before I killed it — never completed.
- For Garrison: `mine` is unsuitable as a general "write this note" path. It is a bulk-ingest tool for existing repos. The agent write-path should be MCP `add_drawer`, not CLI `mine`.

### 3.5 Palace on-disk layout

After init+mine of a 2-file corpus:

```
palace/
  chroma.sqlite3                                   # Chroma metadata + KG
  <uuid-1>/{header,data_level0,length,link_lists}.bin   # HNSW index for collection 1
  <uuid-2>/{header,data_level0,length,link_lists}.bin   # HNSW index for collection 2
```

Total size for 2 drawers: 556 KB. No JSON, no text — everything is either the SQLite DB or Chroma's binary index format. Direct inspection requires opening `chroma.sqlite3` or using the Python API / MCP tools.

### 3.6 MCP server — tool inventory

`python -m mempalace.mcp_server --palace <path>` spawns a **per-invocation** stdio MCP server (not a daemon). Claude Code would spawn a fresh instance per invocation via `claude mcp add mempalace -- python -m mempalace.mcp_server --palace <path>`.

Probed via raw JSONRPC. Full tool list (29 tools):

| Tool | Required args | Purpose |
|---|---|---|
| `mempalace_status` | — | Palace overview (counts) |
| `mempalace_list_wings` | — | Wing list + drawer counts |
| `mempalace_list_rooms` | — | Rooms in a wing (or all) |
| `mempalace_get_taxonomy` | — | wing → room → drawer count tree |
| `mempalace_get_aaak_spec` | — | The AAAK compressed-memory format spec |
| `mempalace_search` | `query` | Semantic search |
| `mempalace_check_duplicate` | `content` | Dedup check before write |
| `mempalace_add_drawer` | `wing,room,content` | **Live write; creates wing/room if missing** |
| `mempalace_get_drawer` | `drawer_id` | Read one |
| `mempalace_list_drawers` | — | Enumerate |
| `mempalace_update_drawer` | `drawer_id` | Edit |
| `mempalace_delete_drawer` | `drawer_id` | Remove |
| `mempalace_kg_add` | `subject,predicate,object` (+ optional `valid_from`, `source_closet`) | Add KG triple |
| `mempalace_kg_query` | `entity` (+ optional `as_of`, `direction`) | Query KG with temporal filter |
| `mempalace_kg_invalidate` | `subject,predicate,object` | Mark triple no-longer-true |
| `mempalace_kg_timeline` | — | Chronological triples |
| `mempalace_kg_stats` | — | KG overview |
| `mempalace_traverse` | `start_room` | Walk palace graph |
| `mempalace_find_tunnels` | — | Discover cross-room connections |
| `mempalace_create_tunnel` | `source_wing,source_room,target_wing,target_room` | Explicit cross-link |
| `mempalace_list_tunnels` | — | — |
| `mempalace_delete_tunnel` | `tunnel_id` | — |
| `mempalace_follow_tunnels` | `wing,room` | Traverse from a room |
| `mempalace_graph_stats` | — | Graph overview |
| `mempalace_diary_write` | `agent_name,entry` | **Per-agent diary** |
| `mempalace_diary_read` | `agent_name` | — |
| `mempalace_hook_settings` | — | Claude Code hook config |
| `mempalace_memories_filed_away` | — | Progress marker |
| `mempalace_reconnect` | — | Reload palace |

### 3.7 Live write via MCP — `add_drawer`

JSONRPC call to `mempalace_add_drawer{wing:"spike", room:"general", content:"..."}` on a palace with no pre-existing `spike` wing: **success**. Returned `drawer_id: drawer_spike_general_<hex>`, `wing:"spike"`, `room:"general"`. Stderr: `Filed drawer: drawer_spike_general_<hex> → spike/general`.

**Wings and rooms can be created by write.** The init-time folder-scan defines *defaults*, not a fixed schema. This is the key architectural insight for Garrison: agents can write to arbitrary `(wing, room)` tuples without needing `init` to have pre-defined them.

### 3.8 Concurrent writes

Two Python MCP-client processes (A, B), each spawning its own `mempalace.mcp_server` subprocess against the **same palace**, each writing 5 drawers to `wing:"concurrency", room:"worker_A"` or `"worker_B"`:

- A: 5/5 success, 1.55s total, ~310ms/op
- B: 5/5 success, 1.47s total, ~293ms/op
- Final `list_rooms`: `{"worker_A": 5, "worker_B": 5}` — all 10 writes landed, no corruption, no errors.

SQLite + Chroma handles 2-writer concurrency under this load. Not a stress test — would need 10+ writers and sustained throughput to characterize lock contention. But the basic concurrent-write path is clean.

### 3.9 Server startup cost

Per-invocation process. Startup: stderr prints `MemPalace MCP Server starting...` immediately, first JSONRPC response within ~500ms. No visible daemon/warm-cache benefit — each claude subprocess pays full Python+Chroma+SentenceTransformers load time (~500ms–1s). **For Garrison, per-invocation is acceptable at M2 scale; if throughput becomes a bottleneck, consider a long-running MemPalace MCP daemon the supervisor manages.**

---

## 4. Top 3 most surprising findings

1. **`--strict-mcp-config` does not fail on MCP server startup errors.** A bogus command in the MCP config produces exit 0, `terminal_reason:"completed"`, `is_error:false`. Garrison's supervisor cannot rely on claude's exit code to detect broken MCP wiring; it must spawn + health-check the MCP servers itself before handing them to claude, or parse the `mcp_servers` status array in the stream-json init event.

2. **MemPalace `init` is interactive by default and mutates the project `.gitignore`.** `--yes` is the non-interactive flag, discoverable only from the Python traceback (not documented in `--help`). Init also writes `mempalace.yaml` + `entities.json` into the scanned directory and auto-adds them to `.gitignore`. Garrison must not run `mempalace init` against the supervisor repo; the palace should be bootstrapped in a dedicated directory (`~/.mempalace/palace` or similar) with a **synthetic** or **empty** init target.

3. **Claude's `stream-json` emits `rate_limit_event` mid-run.** This is the supervisor's early-warning signal for backoff without a side channel. The events include `utilization` (0–1), `resetsAt` (unix timestamp), `surpassedThreshold`. Garrison's event-bus design assumed rate-limit visibility would require API polling; in fact it's in the stdout stream for free.

**Honorable mention**: SIGTERM to claude's PID does not reliably terminate its children — supervisor must run claude under a separate pgid (`setsid` / `Setpgid:true`) and signal the group, or children can outlive the supposedly-killed process.

---

## 5. Open questions (not resolved by this spike)

- **Claude Code**:
  - What is `duration_ms` measured against? In EXP8 it reported 7402ms for a process killed at t+4s, suggesting it's API-time, not wall-clock. Confirmation needed before using it for supervisor timing decisions.
  - Does `--max-budget-usd` enforce per-invocation or per-session? Spike used small values that never triggered; need a test that intentionally overruns.
  - Behavior under API rate-limit exhaustion (not just warning): does claude retry, bail, or stream an error?
  - Does `--permission-mode bypassPermissions` skip all tool-use confirmation, or only some? Need a tool-use test with a "dangerous" tool to confirm.
  - Does SIGINT (Ctrl-C) vs SIGTERM differ? Spike only tested SIGTERM.

- **MemPalace**:
  - Embedding model: sentence-transformers variant? Install size suggested a model download — need to measure cold-start cost.
  - Does the MCP server tolerate a palace being deleted/moved mid-session (and how does `mempalace_reconnect` interact with that)?
  - Concurrent write stress: 10+ writers, 100+ ops/writer. Is there a point where SQLite's writer lock becomes a bottleneck or corruption occurs?
  - How does `mempalace_check_duplicate` decide duplication — exact match, semantic similarity, threshold?
  - The AAAK dialect: what writes use it, is it auto-applied by `compress`, or must the writer encode it themselves?
  - Does MemPalace expose an HTTP MCP transport, or is stdio the only option? (`claude mcp add --transport http` is available, but we didn't test MemPalace supporting it.)

---

## 6. Implications for M2.1 (Claude Code integration)

The M2.1 spec must specify:

1. **Spawn model**: `exec.Command("claude", "-p", prompt, "--output-format", "stream-json", "--verbose", "--no-session-persistence", "--model", <model>, "--max-budget-usd", <cap>, "--mcp-config", <per-agent-mcp.json>, "--strict-mcp-config", "--system-prompt", <agent.md>)`, with `SysProcAttr{Setpgid: true}`. Not `--bare` unless the supervisor is prepared to inject `ANTHROPIC_API_KEY` (which introduces a secret-management requirement it should not accept at M2.1 — defer to M6).

2. **Stream consumption**: parse stdout as NDJSON. Route events by `type`:
   - `system`/`init` → record session_id, tools, MCP server status (early health-check signal).
   - `rate_limit_event` → backoff signal to the supervisor's dispatch loop.
   - `assistant` → log content (thinking, text, tool_use).
   - `user` → tool_result (observe for errors).
   - `result` → terminal record: cost, duration, result text, permission_denials.

3. **Termination**: `syscall.Kill(-pgid, SIGTERM)` on timeout or cancellation. Grace window (2–3s), then SIGKILL to the group.

4. **MCP health**: the supervisor must spawn each configured MCP server separately (or inspect the `init` event's `mcp_servers[].status` field) to detect startup failures — `--strict-mcp-config` does NOT guarantee servers started.

5. **Cost accounting**: use the final `result.total_cost_usd` as authoritative; cross-check with `modelUsage[].costUSD` for per-model attribution. Do not estimate mid-stream.

6. **Session persistence**: always `--no-session-persistence`. Supervisor owns run state in Postgres; claude's own session log is dead weight and introduces a disk-side-effect the supervisor doesn't observe.

---

## 7. Implications for M2.2 (MemPalace integration)

The M2.2 spec must specify:

1. **Palace location**: single palace at a dedicated path (e.g. `~/.garrison/palace/` or a supervisor-config path). **Not** co-located with any repo that might be git-tracked, because `init` mutates `.gitignore` of the scanned directory.

2. **Bootstrap**: init is run once per fresh install with `--yes` against a controlled target (probably an empty dir or a minimal manifest directory). Wings/rooms beyond the auto-detected ones are created **lazily via MCP `add_drawer`** — no re-init needed to add a new wing.

3. **Write path (agents)**: MCP `mempalace_add_drawer(wing, room, content)`. Optional `mempalace_check_duplicate` before write to reduce palace pollution. **Not** the CLI `mine` verb — that's a bulk-ingest tool unsuitable for live writes.

4. **Write path (bulk import)**: if Garrison ever needs to ingest existing docs (e.g. ARCHITECTURE.md into the palace), use CLI `mempalace mine` — but aware that it is O(files) and can take 10+ minutes on non-trivial corpora. Plan this as a one-off bootstrap task, not a live path.

5. **Diary pattern**: `mempalace_diary_write(agent_name, entry)` appears to be the MemPalace-native way to record per-agent observations. Garrison's agent.md "completion protocol" should emit both a drawer (for cross-agent recall) and a diary entry (for per-agent continuity).

6. **KG pattern**: `kg_add` / `kg_query` with `valid_from` / `as_of` dates. This is the "why did we decide X?" retrieval path. Agents should emit KG triples for durable decisions, not just prose drawers.

7. **Concurrency**: 2 concurrent writers tested clean. Under Garrison's 1-agent-at-a-time-per-role pattern this is far below the tested load. Revisit if department concurrency caps go above ~5 simultaneous MCP writes to the same palace.

8. **MCP server lifecycle**: per-invocation stdio spawn is the default. Every claude subprocess gets a fresh MemPalace process. Overhead ~500ms. Acceptable for M2 scale; a long-running MemPalace daemon is a future optimization if throughput demands it.

9. **Schema governance**: wings and rooms are effectively open-set (created on write). Garrison needs a **soft convention** (documented in agent.md files) for wing/room naming, not enforced at the MemPalace layer. The operator's "weekly hygiene" review becomes the governance mechanism, consistent with RATIONALE.md §5.

---

## 8. Scratch-dir inventory (for audit)

Not copied back. Left in `~/scratch/m2-spike/` as evidence:

- `agents/hello.md` — minimal test system prompt
- `runs/exp{1..9}.{ndjson,json,stderr,out}` — raw Claude outputs
- `bad-mcp.json` — malformed MCP config used in EXP7
- `mini/palace/` — 12-drawer palace (2 mined + 1 manual + 10 concurrent-test)
- `mini/mempalace.yaml`, `mini/entities.json` — init artifacts
- `.venv/` — mempalace 3.3.2 install
- `mempalace/` — shallow clone of `github.com/MemPalace/mempalace` (read-only reference)

All throwaway. Delete with `rm -rf ~/scratch/m2-spike/` once M2.1 and M2.2 specs are written and this document is accepted.

---

## A — MCP health-check follow-up (2026-04-22)

Follow-up to §2.5: does the `system`/`init` event in stream-json mode honestly report a broken MCP server, or does it claim everything is fine?

**Command run** (EXP10, same `bad-mcp.json` as EXP7, pointing at `/bin/nonexistent-command`):

```
claude -p "say hi" \
  --output-format stream-json --verbose \
  --strict-mcp-config --mcp-config bad-mcp.json \
  --model claude-haiku-4-5-20251001 \
  --no-session-persistence --max-budget-usd 0.02 \
  > runs/exp10.ndjson 2> runs/exp10.stderr
```

Exit 0 (same silent tolerance observed in EXP7). Stderr: empty.

**Raw `mcp_servers` block from the init event**:

```json
[
  {
    "name": "badserver",
    "status": "failed"
  }
]
```

**Finding**: the init event honestly reports the broken server. The `mcp_servers` array contains one entry per configured server, each with a `name` and a `status` field. For the broken `badserver`, `status` is the literal string `"failed"`. There is no separate `error`, `reason`, or `message` field — just `name` and `status`.

**Healthy-case confirmation (EXP11)**: same invocation pattern, same flags, with `good-mcp.json` pointing at a working MemPalace stdio server (`python -m mempalace.mcp_server --palace ./mini/palace`). The init event reported:

```json
[
  {
    "name": "mempalace",
    "status": "connected"
  }
]
```

All 29 MemPalace tools were present in the init event's `tools[]` array, prefixed `mcp__mempalace__<tool>`. So the full observed enum for stdio MCP servers is at least `{connected, failed, needs-auth}` — `connected` is the healthy value to check against.

**Implication for M2.1**: init-parse is viable. The supervisor reads the `system`/`init` event, iterates `mcp_servers[]`, and rejects any entry whose `status != "connected"` before allowing the run to proceed. Pre-spawning MCP servers independently is not required. (HTTP and SSE transports may have their own status values — not covered by this spike; confirm when M2.1 introduces a remote MCP server.)
