# Contract: subprocess output → slog

**Source requirements**: spec FR-015; plan §"Subprocess output (decision 7)".

## Shape

For each line the subprocess emits to stdout or stderr, the supervisor writes exactly one `slog` record. Records are `slog.LevelInfo` by default; the supervisor does not attempt to parse subprocess output to re-level.

## Required fields on every record

| Field | Value |
|-------|-------|
| `service` | `"supervisor"` |
| `version` | build-time version string |
| `pid` | supervisor pid |
| `event_id` | UUID of the triggering event |
| `channel` | `"work.ticket.created"` |
| `ticket_id` | UUID |
| `department_id` | UUID |
| `agent_instance_id` | UUID of the row created by `InsertRunningInstance` |
| `stream` | `"stdout"` or `"stderr"` |
| `subprocess_pid` | child process pid |

`msg` carries the full line contents, unescaped except for slog's default JSON string encoding.

## Buffer and truncation

- One goroutine per stream uses `bufio.Scanner` with `scanner.Buffer(make([]byte, 64*1024), 1024*1024)`.
- Lines up to 1 MiB are emitted in full.
- Lines exceeding 1 MiB are emitted as a single `slog.Warn` record with `msg="line exceeded 1MiB buffer"`, `truncated=true`, and the first 64 KiB in `preview`. The scanner is reset to continue reading the remainder, which is treated as a new line.

## Ordering

- Stdout and stderr records may interleave arbitrarily; no synchronization is attempted between the two streams.
- Within a single stream, records appear in the order they were emitted by the subprocess.

## Lifecycle

The two output goroutines are members of the per-spawn errgroup. They exit when the stream closes (normal subprocess exit) or when the root context is cancelled. The supervisor does not write the terminal `agent_instances` row until both output goroutines have exited.

## What the contract does NOT include

- No line-level parsing (no JSON-inside-output detection, no ANSI-color stripping).
- No per-subprocess log files.
- No rotation.
- No routing subprocess output to anywhere other than the supervisor's own slog handler (i.e. stdout, JSON-framed).
