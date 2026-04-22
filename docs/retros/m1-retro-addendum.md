## Addendum — 2026-04-22 (post-M2 research spike)

A research spike for M2 characterized Claude Code's subprocess behavior and surfaced a seventh item that belongs in the "what the spec got wrong" category, discovered only because the spike probed subprocess termination directly:

7. **SIGTERM-to-PID is insufficient for Claude Code subprocesses.** M1's `internal/spawn` uses `exec.CommandContext`, which signals the subprocess's PID. This works for M1's fake agent (`sh -c 'echo hello; sleep 2'`) because `sh` and its short-lived children share a process group with the supervisor by inheritance. It will not work for real Claude Code in M2.1: the spike observed that `kill -TERM $pid` against a Claude process returned exit 143 immediately but allowed child processes to continue writing stdout for ~2.5 seconds after the kill. The correct pattern is to spawn Claude under its own process group (`syscall.SysProcAttr{Setpgid: true}`) and signal the group (`syscall.Kill(-pgid, SIGTERM)`). The fix is scoped to M2.1's changes to `internal/spawn`; the M1 code is not broken for M1's purposes but is wrong for what comes next. See `docs/research/m2-spike.md` §2.7.

Two findings from the spike that did NOT change M1 but affect M2.1 design:

- **Claude Code's `--strict-mcp-config` silently tolerates MCP server startup failures.** A broken MCP server does not cause a non-zero exit. The `system`/`init` event in stream-json mode, however, reports each MCP server's status honestly (`connected` / `failed` / `needs-auth`). M2.1's supervisor will parse the init event to detect broken servers rather than relying on exit codes.

- **MemPalace `init` is interactive by default and auto-mutates `.gitignore` in the scanned directory.** M2.2's bootstrap must run `mempalace init --yes` against a dedicated palace directory outside any git-tracked repo. This becomes a rule in AGENTS.md.

No code changes to M1 as a result of the spike. The seventh item is noted for forward work; the M1 binary remains as shipped.
