# Implementation plan: M2.3 — Infisical secret vault

**Branch**: `007-m2-3-infisical-vault` | **Date**: 2026-04-24 | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification from `/specs/007-m2-3-infisical-vault/spec.md`
**Binding context**: [`specs/_context/m2.3-context.md`](../_context/m2.3-context.md), [`docs/security/vault-threat-model.md`](../../docs/security/vault-threat-model.md) Rules 1-7, [`AGENTS.md`](../../AGENTS.md) §§"Activate before writing code" + "Concurrency discipline" + "Stack and dependency rules", [`RATIONALE.md`](../../RATIONALE.md) §§3, 4, 9, [`docs/retros/m2-2-x-compliance-retro.md`](../../docs/retros/m2-2-x-compliance-retro.md) §13 readiness, [`.specify/memory/constitution.md`](../../.specify/memory/constitution.md). The shipped M1 + M2.1 + M2.2 + M2.2.x supervisor (`supervisor/`) is the foundation this plan extends; M2.2.x retro + post-ship pgmcp fix are prerequisite reading.

## Summary

M2.3 wires Infisical into the agent-spawn path so agents can receive credentials as environment variables without any secret value entering the LLM context window. The milestone adds one new internal package (`internal/vault`), three new Postgres tables (`agent_role_secrets`, `vault_access_log`, `secret_metadata`) with Garrison's first Postgres trigger for denormalization sync, three spawn-time enforcement checks (Rule 1 leak scan, Rule 2 grant query, Rule 3 no-vault-MCP), eight new `exit_reason` values, one new `hygiene_status` value (`suspected_secret_emitted`), and a best-effort secret-pattern scanner that redacts on the MemPalace-write boundary. Infisical runs as three new services on the existing Coolify compose network (Infisical + its own Postgres + Redis); the supervisor authenticates as a Machine Identity with auto-re-auth-once-per-spawn on HTTP 401. A custom `go vet` analyzer enforces the "no secret values in `slog`" property at compile time. The Infisical Go SDK is the sole permitted Go dependency addition (sixth consecutive milestone holding the locked-deps line otherwise).

The M2.3 milestone does not ship any operator-facing UI — browsing, CRUD, rotation initiation, grant editing, audit-log viewer are all deferred to M3 (read) and M4 (write) per the spec's scope deviation from the threat model. What changes when M2.3 ships: agents assigned grants receive secrets at spawn time; the data is ready for M3's read surfaces to consume. Workspace sandboxing remains orthogonal (post-M2.3 Docker-per-agent work per `docs/issues/agent-workspace-sandboxing.md`).

## Technical context

**Language/version**: Go 1.25 (inherited).
**Primary dependencies**: inherited unchanged — `github.com/jackc/pgx/v5`, `github.com/jackc/pgx/v5/pgxpool`, `golang.org/x/sync/errgroup`, `log/slog`, `github.com/pressly/goose/v3`, `github.com/stretchr/testify`, `github.com/testcontainers/testcontainers-go`, `github.com/google/shlex`. **Two new Go dependencies** (per FR-425 and operator decision during `/garrison-tasks` phase): (1) `github.com/infisical/go-sdk` — official Infisical Go SDK for vault client; (2) `golang.org/x/tools/go/analysis` — stdlib-adjacent analysis framework for the custom `vaultlog` vet analyzer that enforces SC-410 at build time. Both justified in their respective commit messages per AGENTS.md stack soft-rule and flagged in the M2.3 retro.
**Storage**: PostgreSQL 17+ (unchanged engine); three new tables + one trigger + one trigger function. MemPalace SQLite + ChromaDB (unchanged). Infisical gets its own Postgres 15 + Redis 7 sidecars per FR-421 — not sharing Garrison's Postgres.
**Testing**: stdlib `testing` + `testify` + `testcontainers-go`; build tags `integration`, `chaos`, `live_acceptance` reused from M2.2.x. One new testcontainer target (Infisical). One new top-level integration file `supervisor/integration_m2_3_vault_test.go`. New unit-test files under `internal/vault/`. Existing tests unchanged except additive exit_reason assertions.
**Target platform**: Linux server (Hetzner + Coolify); single static Go binary for the supervisor (unchanged). Three new containers in the compose topology.
**External binaries**: `claude` (2.1.117, unchanged); MemPalace sidecar, docker-proxy sidecar — unchanged from M2.2. New: `infisical/infisical:<pinned-digest>`, `postgres:15-alpine`, `redis:7-alpine` for the Infisical stack.
**Project type**: CLI/daemon. No new supervisor subcommand; vault is in-process.
**Performance goals**: Rule 1 leak scan is per-spawn substring-search — trivial at M2.3 scale (KB-sized agent.md × single-digit secret count). Auto-re-auth on 401 adds ≤1 Infisical API roundtrip per spawn in the rare-but-natural token-rotation case. No new NFR gates.
**Constraints**: locked dependency list preserved with the Infisical-SDK exception (FR-425); concurrency rules 1-8 from AGENTS.md inherited unchanged; the M2.2 socket-proxy topology is unchanged (FR-428); the threat model's Rules 1-7 are binding throughout; operator-facing UI remains out of scope (FR-426).
**Scale/scope**: single operator; single customer_id at M2.3 ship; zero grant rows seeded (FR-414); two roles (engineer + qa-engineer) that require no secrets today; future grants land via one migration per grant set (Q12).

## Constitution check

*Gate: must pass before tasks. Re-checked before `/garrison-implement`.*

| Principle | Compliance |
|---|---|
| I. Postgres is sole source of truth; pg_notify is the bus | Pass — three new tables live in Postgres; no new event bus surface. |
| II. MemPalace is sole memory store | Pass — vault is NOT a memory system. Secret values never enter MemPalace (the pattern scanner exists specifically to enforce this at the MemPalace-write boundary). |
| III. Agents are ephemeral | Pass — vault fetch happens per spawn; no per-agent daemon, no agent pool; rotation of a secret held by a running agent_instance is explicitly no-op (FR-429). |
| IV. Soft gates on memory hygiene | Pass — pattern-scanner match produces `hygiene_status='suspected_secret_emitted'` and redacts, but does not block the MemPalace write (FR-419). |
| V. Skills from skills.sh | N/A — M7. No skill changes. |
| VI. Hiring is UI-driven | N/A — M7. Grants land via migrations at M2.3 (Q12). |
| VII. Go supervisor with locked deps | Pass with declared exception — Infisical Go SDK is the sole addition per FR-425, commit-message justified and retro-flagged. |
| VIII. Every goroutine accepts context | Pass — `vault.Client.Fetch(ctx, ...)` threads context; no new bare goroutines; auto-re-auth retry runs on the caller's goroutine. |
| IX. Narrow specs per milestone | Pass — scope tightly bounded to plumbing; UI explicitly deferred to M3/M4 (FR-426); sandboxing explicitly orthogonal (FR-427). |
| X. Per-department concurrency caps | Pass — no concurrency change; vault client is goroutine-safe. |
| XI. Self-hosted on Hetzner | Pass — Infisical is self-hosted in the same Coolify project; no cloud service additions. |

No violations → Complexity tracking intentionally empty.

## Project structure

### Documentation (this feature)

```text
specs/007-m2-3-infisical-vault/
├── spec.md                      # Phase 0 (/garrison-specify + /speckit.clarify outputs)
├── plan.md                      # This file
├── tasks.md                     # Phase 2 output (/garrison-tasks)
└── acceptance-evidence.md       # Phase 4 output (/garrison-implement final acceptance task)
```

### Source code (changes only)

```text
supervisor/
├── cmd/supervisor/
│   └── main.go                  # MODIFY: construct vault.Client at startup, fail-fast on missing INFISICAL_* env vars (D4.1)
├── internal/
│   ├── vault/                   # NEW PACKAGE
│   │   ├── client.go            # NEW: vault.Client, Fetch, reauthenticate, auto-re-auth-once (D4.2)
│   │   ├── client_test.go       # NEW: unit tests per D10.1
│   │   ├── secretvalue.go       # NEW: SecretValue type with LogValue + Zero + UnsafeBytes (D2.1)
│   │   ├── secretvalue_test.go  # NEW: formatter tests; no-raw-leak via String/MarshalJSON
│   │   ├── errors.go            # NEW: sentinel errors + ClassifyExitReason (D2.2)
│   │   ├── errors_test.go       # NEW: classifier unit tests
│   │   ├── scanner.go           # NEW: pattern scanner ScanAndRedact (D1.2, D7.4)
│   │   ├── scanner_test.go      # NEW: per-pattern positive/negative per D10.2
│   │   ├── leakscan.go          # NEW: Rule 1 leak scanner (D1.3)
│   │   ├── leakscan_test.go     # NEW: unit tests per D10.3
│   │   ├── audit.go             # NEW: vault_access_log writer (same tx semantics per D3.3)
│   │   ├── audit_test.go        # NEW: audit writer unit tests (table-driven outcomes)
│   │   ├── testutil.go          # NEW (build tag integration): testcontainer Infisical helper; image version pin
│   │   └── vault_integration_test.go  # NEW: //go:build integration; D10.4
│   ├── spawn/
│   │   ├── exitreason.go        # MODIFY: add 8 new constants per D7.1
│   │   ├── exitreason_test.go   # MODIFY: extend assertions over the constant set
│   │   ├── spawn.go             # MODIFY: thread vault.Client into the spawn function; add call sites per D4.5
│   │   └── spawn_test.go        # MODIFY: additive exit_reason assertions; no existing-test rewrites
│   ├── mcpconfig/
│   │   ├── mcpconfig.go         # MODIFY: add RejectVaultServers; call from composition path (D1.4, D2.4)
│   │   └── mcpconfig_test.go    # MODIFY: add TestRejectVaultServers* (D10.5)
│   ├── finalize/
│   │   └── handler.go           # MODIFY: call vault.ScanAndRedact() on string-typed payload fields just before MemPalace write (FR-418)
│   ├── config/
│   │   ├── config.go            # MODIFY: add InfisicalAddr, InfisicalClientID, InfisicalClientSecret, CustomerID (D6.3)
│   │   └── config_test.go       # MODIFY: additive coverage of the four new env-reader paths
│   └── store/
│       └── (sqlc-generated)     # REGENERATE: queries from new SQL files in migrations/queries/*
├── tools/vaultlog/              # NEW: custom go vet analyzer per D10.8 + SC-410
│   ├── analyzer.go              # NEW: rejects slog.* calls with vault.SecretValue argument
│   ├── analyzer_test.go         # NEW: unit tests (positive + negative cases)
│   └── cmd/vaultlog/main.go     # NEW: standalone binary wiring analyzer for `go vet ./...`
├── integration_m2_3_vault_test.go  # NEW: //go:build integration — D10.6 (the 9 user stories)
├── docker-compose.yml           # MODIFY: add infisical + infisical-postgres + infisical-redis services (D5.2)
└── Makefile | justfile          # MODIFY (if present): add `vet-vaultlog` target wrapping the custom analyzer

migrations/
├── 20260425000008_m2_3_vault.sql  # NEW: three tables + trigger + function (D3.1, D3.5)
└── queries/
    ├── agent_role_secrets.sql     # NEW: sqlc queries — per-role grant lookup + CRUD for migrations
    ├── vault_access_log.sql       # NEW: sqlc queries — append-only insert + read helpers for M3
    └── secret_metadata.sql        # NEW: sqlc queries — upsert, last_accessed_at update, M3 read queries

docs/
├── ops-checklist.md             # MODIFY: add "M2.3 — Infisical deployment" section (D5.1)
└── retros/
    └── m2-3.md                  # NEW: post-acceptance retro (markdown pointer + palace drawer per AGENTS.md)
```

**Structure decision**: extend the M2.2.x layout in place. The only net-new package is `internal/vault`, which is self-contained: owns the Infisical SDK wrapper, the SecretValue type, the scanners, and the audit writer. Spawn path integration is a small set of new call sites in `internal/spawn/spawn.go`. MCP-config rejection lives where MCP config is composed (`internal/mcpconfig/`). The pattern scanner attaches at the finalize handler layer per Q2 clarification. The custom go vet analyzer lives under `supervisor/tools/vaultlog/` so it is separate from the production binary — built only when `go vet ./...` runs.

---

## Phase 0 — research

No new research artifacts. M2.3 does not carry a research spike:

- **Infisical SDK behavior** is documented on `infisical.com/docs` and the SDK's own README; Universal Auth, Machine Identity, path-based `GetSecretByPath`, audit-log API all have stable contracts. Any SDK deviation discovered during implementation becomes a clarify item, not a spike.
- **Pattern regexes** (Q7 list) are well-characterized public strings; no empirical work needed.
- **Postgres triggers** are stdlib Postgres 17 behavior; existing M2.2 trigger (`emit_ticket_transitioned`) is the implementation template.
- **Testcontainers-go Infisical** is exercised via the generic container module; pin the image version in a constant and move on.

The one exploratory unknown that would have merited a spike is SDK retry behavior on 401, but FR-405 + auto-re-auth semantics (Session 2026-04-24 clarification) are tight enough that implementation-time verification is cheap: write the test, observe behavior, adjust once.

Per RATIONALE §13 ("spike vs. spec-directly"): M2.3 falls on the spec-directly side — all external surfaces are well-characterized by their own docs; Garrison's specific decisions (spawn-path integration, error classification, audit semantics) are ours to make. No `docs/research/m2-3-spike.md` is produced.

---

## Phase 1 — design

### Code surface changes by file

#### `internal/vault/secretvalue.go` (new)

The type that represents a fetched secret value. Raw bytes only expose via a single named accessor; no formatter method returns anything but `[REDACTED]`.

```go
package vault

import "log/slog"

// SecretValue holds a secret fetched from Infisical. The zero value is
// valid and represents an absent secret. Never construct one with a raw
// string literal — use New or the vault.Client.Fetch return value.
//
// SecretValue deliberately implements slog.LogValuer and returns a
// redacted placeholder. It does NOT implement Stringer, MarshalText,
// MarshalJSON, MarshalBinary, or any other formatter that could print
// the raw value through a generic code path. The raw bytes are only
// reachable via UnsafeBytes, whose use is grep-auditable.
type SecretValue struct {
    b []byte
}

// New wraps raw bytes. The caller MUST NOT log or marshal src through
// any other path before passing it here.
func New(src []byte) SecretValue { return SecretValue{b: append([]byte(nil), src...)} }

// LogValue satisfies slog.LogValuer so slog calls produce "[REDACTED]".
func (v SecretValue) LogValue() slog.Value { return slog.StringValue("[REDACTED]") }

// UnsafeBytes returns the backing slice. This is the only way to reach
// the raw value. Every call site is reviewable; the vaultlog analyzer
// (see supervisor/tools/vaultlog) rejects any slog.* call that takes
// UnsafeBytes() as an argument.
func (v SecretValue) UnsafeBytes() []byte { return v.b }

// Zero wipes the backing slice. Call from every defer on every path
// that holds a SecretValue longer than the current function. Idempotent.
func (v *SecretValue) Zero() {
    for i := range v.b { v.b[i] = 0 }
    v.b = nil
}

// Empty reports whether the value is the zero value.
func (v SecretValue) Empty() bool { return len(v.b) == 0 }
```

**No `String()`** — `fmt.Sprintf("%s", secretValue)` compiles but invokes `%!s(vault.SecretValue={...})` Go-reflection fallback, which does not print the raw bytes. Combined with the vet analyzer, this makes accidental value leakage a compile-time or vet-time error.

#### `internal/vault/errors.go` (new)

```go
package vault

import (
    "errors"
    "github.com/garrison-hq/garrison/supervisor/internal/spawn"
)

var (
    ErrVaultUnavailable      = errors.New("vault: unavailable")
    ErrVaultAuthExpired      = errors.New("vault: auth expired (after auto-reauth retry)")
    ErrVaultPermissionDenied = errors.New("vault: permission denied by Infisical")
    ErrVaultRateLimited      = errors.New("vault: rate limited")
    ErrVaultSecretNotFound   = errors.New("vault: secret path not found")
    ErrVaultAuditFailed      = errors.New("vault: audit log write failed (fail-closed)")
)

// ClassifyExitReason maps a vault error to the canonical exit_reason
// string value. Non-vault errors return ExitClaudeError as a
// defensive default — callers should detect non-vault errors earlier
// in the chain and avoid this path. Nil returns "" (no-op).
func ClassifyExitReason(err error) string {
    switch {
    case err == nil:
        return ""
    case errors.Is(err, ErrVaultUnavailable):
        return spawn.ExitVaultUnavailable
    case errors.Is(err, ErrVaultAuthExpired):
        return spawn.ExitVaultAuthExpired
    case errors.Is(err, ErrVaultPermissionDenied):
        return spawn.ExitVaultPermissionDenied
    case errors.Is(err, ErrVaultRateLimited):
        return spawn.ExitVaultRateLimited
    case errors.Is(err, ErrVaultSecretNotFound):
        return spawn.ExitVaultSecretNotFound
    case errors.Is(err, ErrVaultAuditFailed):
        return spawn.ExitVaultAuditFailed
    default:
        return spawn.ExitClaudeError
    }
}
```

#### `internal/vault/client.go` (new)

```go
package vault

import (
    "context"
    "log/slog"
    "sync"
    "time"

    infisical "github.com/infisical/go-sdk"
)

// Client wraps the Infisical Go SDK plus Garrison-side audit + retry
// discipline. Safe for concurrent use by multiple spawns.
type Client struct {
    sdk        *infisical.InfisicalClient
    siteURL    string
    clientID   string
    clientSec  string
    customerID string
    logger     *slog.Logger

    mu        sync.Mutex
    authToken string
    authAt    time.Time
}

// NewClient authenticates immediately and returns a ready-to-use
// Client. Missing required inputs return an error; callers should
// surface the error from supervisor startup (fail-fast per D4.1).
func NewClient(ctx context.Context, cfg ClientConfig) (*Client, error) { /* ... */ }

// Fetch returns secret values for the requested set of (env_var_name,
// secret_path) pairs. Ordering matches input. On HTTP 401 the client
// calls reauthenticate() once, retries the fetch once, and surfaces
// ErrVaultAuthExpired only if the second attempt also returns 401.
// Running agents are NEVER touched by Fetch — it is a pre-spawn call
// only, invoked from internal/spawn with the spawn's context.
func (c *Client) Fetch(ctx context.Context, req []GrantRow) (map[string]SecretValue, error) {
    /* 1. ensure authToken fresh (under mu) */
    /* 2. call sdk.GetSecretByKey for each path */
    /* 3. classify errors per D2.2; on 401, reauth + one retry */
    /* 4. return map or classified error */
}

func (c *Client) reauthenticate(ctx context.Context) error { /* UniversalAuth login */ }
```

`ClientConfig` fields: `SiteURL`, `ClientID`, `ClientSecret`, `CustomerID`, `Logger`. All supplied from `config.Config` at supervisor startup.

#### `internal/vault/scanner.go` (new)

```go
package vault

import "regexp"

// Label is the pattern name that appears in the [REDACTED:<label>] replacement.
type Label string

const (
    LabelSKPrefix      Label = "sk_prefix"
    LabelXOXBPrefix    Label = "xoxb_prefix"
    LabelAWSAKIA       Label = "aws_akia"
    LabelPEMHeader     Label = "pem_header"
    LabelGitHubPAT     Label = "github_pat"     // ghp_
    LabelGitHubApp     Label = "github_app"     // gho_
    LabelGitHubUser    Label = "github_user"    // ghu_
    LabelGitHubServer  Label = "github_server"  // ghs_
    LabelGitHubRefresh Label = "github_refresh" // ghr_
    LabelBearerShape   Label = "bearer_shape"
)

// pattern binds a Label to its compiled regex.
var patterns = []struct{ label Label; re *regexp.Regexp }{
    {LabelSKPrefix,      regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`)},
    {LabelXOXBPrefix,    regexp.MustCompile(`xoxb-[A-Za-z0-9\-]{20,}`)},
    {LabelAWSAKIA,       regexp.MustCompile(`AKIA[0-9A-Z]{16}`)},
    {LabelPEMHeader,     regexp.MustCompile(`-----BEGIN [A-Z ]+-----`)},
    {LabelGitHubPAT,     regexp.MustCompile(`ghp_[A-Za-z0-9]{30,}`)},
    {LabelGitHubApp,     regexp.MustCompile(`gho_[A-Za-z0-9]{30,}`)},
    {LabelGitHubUser,    regexp.MustCompile(`ghu_[A-Za-z0-9]{30,}`)},
    {LabelGitHubServer,  regexp.MustCompile(`ghs_[A-Za-z0-9]{30,}`)},
    {LabelGitHubRefresh, regexp.MustCompile(`ghr_[A-Za-z0-9]{30,}`)},
    {LabelBearerShape,   regexp.MustCompile(`(?i)authorization:\s*bearer\s+[A-Za-z0-9\-\._~\+\/]+=*`)},
}

// ScanAndRedact replaces every pattern-match in s with [REDACTED:<label>]
// and returns the labels observed (nil if none matched). It is safe to
// call on arbitrary agent-produced strings. Best-effort per Q7 — a
// secret that does not match any known pattern slips through.
func ScanAndRedact(s string) (redacted string, matched []Label) {
    redacted = s
    for _, p := range patterns {
        if p.re.MatchString(redacted) {
            matched = append(matched, p.label)
            redacted = p.re.ReplaceAllString(redacted, "[REDACTED:"+string(p.label)+"]")
        }
    }
    return
}
```

#### `internal/vault/leakscan.go` (new)

```go
package vault

// RuleOneLeakScan substring-searches agentMD for every value in
// grantSet. Returns a non-empty list of env_var_names whose value was
// found as a literal substring; empty slice means no leak. Per FR-407
// + Session 2026-04-24 clarification: scan is per-spawn, not cached.
//
// SecretValue's UnsafeBytes is called here because the substring search
// needs the raw bytes. This is one of the two call sites that reach
// raw values (the other is spawn-path env-var injection); the vaultlog
// analyzer whitelists this call.
func RuleOneLeakScan(agentMD string, grantSet map[string]SecretValue) (leaked []string) {
    for envVar, val := range grantSet {
        if val.Empty() { continue }
        if strings.Contains(agentMD, string(val.UnsafeBytes())) {
            leaked = append(leaked, envVar)
        }
    }
    return
}
```

#### `internal/vault/audit.go` (new)

```go
package vault

// Outcome enumerates vault_access_log.outcome values per D7.2.
type Outcome string

const (
    OutcomeGranted         Outcome = "granted"
    OutcomeDeniedNoGrant   Outcome = "denied_no_grant"
    OutcomeDeniedInfisical Outcome = "denied_infisical"
    OutcomeErrorFetching   Outcome = "error_fetching"
    OutcomeErrorInjecting  Outcome = "error_injecting"
    OutcomeErrorAuditing   Outcome = "error_auditing"
)

// WriteAuditRow inserts one vault_access_log row and updates
// secret_metadata.last_accessed_at when outcome == OutcomeGranted, all
// inside the provided transaction. Callers on the granted path pass
// the same tx that enclosed the agent_instances INSERT (D3.3, D3.4).
// On auditing failure, returns ErrVaultAuditFailed for fail-closed
// handling at the spawn boundary.
func WriteAuditRow(ctx context.Context, tx pgx.Tx, row AuditRow) error { /* ... */ }

type AuditRow struct {
    AgentInstanceID uuid.UUID
    TicketID        *uuid.UUID // nil for non-ticket-bound operations
    SecretPath      string
    CustomerID      uuid.UUID
    Outcome         Outcome
    Timestamp       time.Time
}
```

#### `internal/spawn/exitreason.go` (modify)

Append to the constants block, matching the existing naming style:

```go
    // M2.3 additions (FR-405, FR-407, FR-410, Q9). Vault-related
    // terminal dispositions set by the spawn path's vault integration.
    // Comments cite the spec clause that mandates each value.
    ExitSecretLeakedInAgentMd = "secret_leaked_in_agent_md" // FR-407 Rule 1 scan matched.
    ExitVaultMCPInConfig      = "vault_mcp_in_config"        // FR-410 Rule 3 banned MCP server.
    ExitVaultUnavailable      = "vault_unavailable"          // FR-405 Infisical unreachable / TLS error.
    ExitVaultAuthExpired      = "vault_auth_expired"         // FR-405 HTTP 401 after one auto-reauth.
    ExitVaultPermissionDenied = "vault_permission_denied"    // FR-405 HTTP 403 at Infisical layer.
    ExitVaultRateLimited      = "vault_rate_limited"         // FR-405 HTTP 429.
    ExitVaultSecretNotFound   = "vault_secret_not_found"     // FR-405 HTTP 404 on the path.
    ExitVaultAuditFailed      = "vault_audit_failed"         // FR-404 / Q9 fail-closed.
```

#### `internal/mcpconfig/mcpconfig.go` (modify)

Add after the `osOps`-style helpers:

```go
// bannedMCPNamePatterns enforces Rule 3 (threat model §4): the vault
// is opaque to agents — no vault MCP tool may ever appear in a spawn's
// MCP config. Case-insensitive substring match against both the map
// key (server name) and the name-like fields of each server spec.
var bannedMCPNamePatterns = []string{"vault", "secret", "infisical"}

// RejectVaultServers returns an error if cfg contains any entry whose
// name matches a banned pattern. Called from Write before the config
// is serialized to disk (spawn's step 2 in D4.5 ordering).
func RejectVaultServers(cfg mcpConfig) error {
    for name := range cfg.MCPServers {
        for _, p := range bannedMCPNamePatterns {
            if strings.Contains(strings.ToLower(name), p) {
                return fmt.Errorf("mcpconfig: banned vault-pattern server %q in MCP config", name)
            }
        }
    }
    return nil
}
```

The error is caught in `internal/spawn/spawn.go`, routed to `ExitVaultMCPInConfig`. No existing baseline server (`postgres`, `mempalace`, `finalize`) matches any banned pattern.

#### `internal/spawn/spawn.go` (modify)

Insert vault integration into the existing spawn function's ordering per D4.5:

```
// existing M2.2.x spawn path
1. Resolve agent row + agent.md content (agents.Cache lookup)
2. ── NEW: Rule 2 grant query via store.ListGrantsForRole(role_slug, customer_id)
3. ── NEW: Rule 3 check via mcpconfig.RejectVaultServers(composedCfg)
            → on error: exit_reason = ExitVaultMCPInConfig
4. ── NEW: vault.Client.Fetch(grants) → map[env_var_name]SecretValue
            → on error: classify via vault.ClassifyExitReason; audit the failure outcome; abort
5. ── NEW: leaked := vault.RuleOneLeakScan(agentMD, fetchedSecrets)
            → if len(leaked) > 0: exit_reason = ExitSecretLeakedInAgentMd; Zero() all; abort
6. ── NEW: vault.WriteAuditRow(tx, {outcome: OutcomeGranted, ...}) inside same tx as agent_instances INSERT
            → on error: Zero() all; exit_reason = ExitVaultAuditFailed; abort
7. Compose mcp-config, write to disk (existing M2.1/M2.2 path)
8. exec.CommandContext with env-var injection (Env = base ++ vaultInjected)
9. After exec.Start returns: Zero() all SecretValue entries
10. After subprocess exit: supervisor finalize path + hygiene check
                           + vault.ScanAndRedact on finalize payload strings (FR-418)
                           + update ticket_transitions.hygiene_status
```

Call sites added; existing function body preserved otherwise. The `vault.Client` is plumbed via the existing `Deps` struct — one new field `Vault *vault.Client`.

#### `internal/finalize/handler.go` (modify)

Insert the scanner just before the MemPalace write (which lives in the atomic-write path):

```go
// Around line ~120 (before the AddDrawer call), iterate string-typed
// fields and redact. Matched labels are OR'd into a new local
// `matched []string`; if non-empty, set ticket_transitions.hygiene_status
// = "suspected_secret_emitted" in the same transaction as the transition
// INSERT. Per FR-418 + FR-419 + FR-420.
var matched []vault.Label
payload.DiaryEntry.Rationale, m := vault.ScanAndRedact(payload.DiaryEntry.Rationale); matched = append(matched, m...)
for i := range payload.KGTriples {
    payload.KGTriples[i].Subject,   m = vault.ScanAndRedact(payload.KGTriples[i].Subject);   matched = append(matched, m...)
    payload.KGTriples[i].Predicate, m = vault.ScanAndRedact(payload.KGTriples[i].Predicate); matched = append(matched, m...)
    payload.KGTriples[i].Object,    m = vault.ScanAndRedact(payload.KGTriples[i].Object);    matched = append(matched, m...)
}
// ... proceed with existing atomic write; pass matched into the
// transition writer so hygiene_status carries suspected_secret_emitted
// when len(matched) > 0.
```

The M2.2.1 atomic-write transaction semantics are preserved exactly (Decision 2 from that context, binding here) — the scanner is a pre-write string transformation, not a change to the transaction itself.

#### `internal/config/config.go` (modify)

Add four new accessor methods mirroring the M2.1/M2.2 pattern:

```go
// InfisicalAddr returns the hostname+port for the supervisor's Infisical
// client. Read at startup from GARRISON_INFISICAL_ADDR (e.g.
// "http://garrison-infisical:8080" on the Coolify network).
func (c *Config) InfisicalAddr() string

// InfisicalClientID returns the Machine Identity client_id. Startup
// error if unset.
func (c *Config) InfisicalClientID() string

// InfisicalClientSecret returns the Machine Identity client_secret.
// Startup error if unset.
func (c *Config) InfisicalClientSecret() string

// CustomerID returns the UUID of the operating entity's row in
// companies. Resolved once at startup by SELECT id FROM companies
// LIMIT 1; multi-company makes this stale (acceptable at M2.3 per
// OQ-2 resolution).
func (c *Config) CustomerID() uuid.UUID
```

New required env vars added to the startup-validation block: `GARRISON_INFISICAL_ADDR`, `GARRISON_INFISICAL_CLIENT_ID`, `GARRISON_INFISICAL_CLIENT_SECRET`. Parallel to `GARRISON_AGENT_RO_PASSWORD` / `GARRISON_AGENT_MEMPALACE_PASSWORD` existing validation.

### Migration shape

#### `migrations/20260425000008_m2_3_vault.sql` (new)

```sql
-- M2.3 migration: Infisical secret vault — tables + trigger + denorm
-- sync. Three new tables:
--   - agent_role_secrets (FR-411): per-role grants; authoritative Rule 2 policy
--   - vault_access_log   (FR-412): audit record, no secret values
--   - secret_metadata    (FR-413): denormalized metadata for M3 reads
--
-- Introduces Garrison's first Postgres trigger function for denorm
-- sync of secret_metadata.allowed_role_slugs (FR-413a, Session
-- 2026-04-24 clarification). Follows M2.2's emit_ticket_transitioned
-- trigger style (goose StatementBegin/End delimiters).
--
-- NO seed rows inserted; zero grants at M2.3 ship (FR-414).
-- Operator runs follow-up INSERTs from the ops checklist as secrets
-- are seeded into Infisical.

-- +goose Up

-- Section 1 — agent_role_secrets (FR-411).
CREATE TABLE agent_role_secrets (
    role_slug        TEXT        NOT NULL,
    secret_path      TEXT        NOT NULL,
    env_var_name     TEXT        NOT NULL,
    customer_id      UUID        NOT NULL,
    granted_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    granted_by       TEXT        NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (role_slug, env_var_name, customer_id),
    FOREIGN KEY (role_slug) REFERENCES agents(role_slug) ON DELETE RESTRICT
);
CREATE INDEX idx_agent_role_secrets_secret_path
    ON agent_role_secrets (secret_path, customer_id);

-- Section 2 — vault_access_log (FR-412). No secret-value column.
CREATE TABLE vault_access_log (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_instance_id   UUID        NOT NULL REFERENCES agent_instances(id),
    ticket_id           UUID        NULL     REFERENCES tickets(id),
    secret_path         TEXT        NOT NULL,
    customer_id         UUID        NOT NULL,
    outcome             TEXT        NOT NULL,
    timestamp           TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_vault_access_log_agent_instance ON vault_access_log (agent_instance_id);
CREATE INDEX idx_vault_access_log_ticket         ON vault_access_log (ticket_id) WHERE ticket_id IS NOT NULL;

-- Section 3 — secret_metadata (FR-413).
CREATE TABLE secret_metadata (
    secret_path        TEXT        NOT NULL,
    customer_id        UUID        NOT NULL,
    provenance         TEXT        NOT NULL,
    rotation_cadence   INTERVAL    NOT NULL DEFAULT '90 days',
    last_rotated_at    TIMESTAMPTZ NULL,
    last_accessed_at   TIMESTAMPTZ NULL,
    allowed_role_slugs TEXT[]      NOT NULL DEFAULT '{}',
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (secret_path, customer_id)
);

-- Section 4 — denorm sync trigger (FR-413a).
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION rebuild_secret_metadata_role_slugs()
RETURNS TRIGGER AS $$
DECLARE
    affected_paths TEXT[];
BEGIN
    -- Collect affected (secret_path, customer_id) tuples from OLD and/or NEW.
    IF (TG_OP = 'INSERT') THEN
        UPDATE secret_metadata
           SET allowed_role_slugs = (
               SELECT COALESCE(array_agg(DISTINCT role_slug), '{}')
                 FROM agent_role_secrets
                WHERE secret_path = NEW.secret_path AND customer_id = NEW.customer_id
           ),
               updated_at = now()
         WHERE secret_path = NEW.secret_path AND customer_id = NEW.customer_id;
        RETURN NEW;
    ELSIF (TG_OP = 'UPDATE') THEN
        -- Rebuild both OLD and NEW tuples (they may be the same).
        UPDATE secret_metadata
           SET allowed_role_slugs = (
               SELECT COALESCE(array_agg(DISTINCT role_slug), '{}')
                 FROM agent_role_secrets
                WHERE secret_path = NEW.secret_path AND customer_id = NEW.customer_id
           ),
               updated_at = now()
         WHERE secret_path = NEW.secret_path AND customer_id = NEW.customer_id;
        IF (OLD.secret_path, OLD.customer_id) IS DISTINCT FROM (NEW.secret_path, NEW.customer_id) THEN
            UPDATE secret_metadata
               SET allowed_role_slugs = (
                   SELECT COALESCE(array_agg(DISTINCT role_slug), '{}')
                     FROM agent_role_secrets
                    WHERE secret_path = OLD.secret_path AND customer_id = OLD.customer_id
               ),
                   updated_at = now()
             WHERE secret_path = OLD.secret_path AND customer_id = OLD.customer_id;
        END IF;
        RETURN NEW;
    ELSIF (TG_OP = 'DELETE') THEN
        UPDATE secret_metadata
           SET allowed_role_slugs = (
               SELECT COALESCE(array_agg(DISTINCT role_slug), '{}')
                 FROM agent_role_secrets
                WHERE secret_path = OLD.secret_path AND customer_id = OLD.customer_id
           ),
               updated_at = now()
         WHERE secret_path = OLD.secret_path AND customer_id = OLD.customer_id;
        RETURN OLD;
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER agent_role_secrets_allowed_role_slugs_sync
AFTER INSERT OR UPDATE OR DELETE ON agent_role_secrets
FOR EACH ROW EXECUTE FUNCTION rebuild_secret_metadata_role_slugs();

-- Section 5 — grant the supervisor's existing role SELECT on the
-- new tables. The supervisor's primary connection already has full
-- ownership; the read-only role garrison_agent_ro does NOT receive
-- access to vault tables — the vault is opaque to agents, so the
-- per-agent Postgres connection must not see vault_access_log or
-- agent_role_secrets. M3's dashboard (future) uses a new role.

-- +goose Down
DROP TRIGGER IF EXISTS agent_role_secrets_allowed_role_slugs_sync ON agent_role_secrets;
DROP FUNCTION IF EXISTS rebuild_secret_metadata_role_slugs();
DROP INDEX IF EXISTS idx_vault_access_log_ticket;
DROP INDEX IF EXISTS idx_vault_access_log_agent_instance;
DROP INDEX IF EXISTS idx_agent_role_secrets_secret_path;
DROP TABLE IF EXISTS secret_metadata;
DROP TABLE IF EXISTS vault_access_log;
DROP TABLE IF EXISTS agent_role_secrets;
```

### sqlc query files

#### `migrations/queries/agent_role_secrets.sql` (new)

```sql
-- name: ListGrantsForRole :many
-- Rule 2 per-role grant query (FR-409). Returns zero rows for a role
-- with no grants; callers skip the Infisical fetch in that case.
SELECT env_var_name, secret_path, customer_id
  FROM agent_role_secrets
 WHERE role_slug = $1 AND customer_id = $2
 ORDER BY env_var_name;

-- name: InsertGrant :exec
-- Used only by migrations at M2.3; M4 will layer on top. Trigger
-- rebuilds secret_metadata.allowed_role_slugs automatically.
INSERT INTO agent_role_secrets
    (role_slug, secret_path, env_var_name, customer_id, granted_by)
VALUES ($1, $2, $3, $4, $5);
```

#### `migrations/queries/vault_access_log.sql` (new)

```sql
-- name: InsertVaultAccessLog :exec
-- FR-412: append-only; no UPDATE / DELETE paths. customer_id carried
-- for future multi-tenant partitioning.
INSERT INTO vault_access_log
    (agent_instance_id, ticket_id, secret_path, customer_id, outcome, timestamp)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: ListVaultAccessByTicket :many
-- Exists specifically for M3's dashboard; M2.3 doesn't call it.
SELECT id, agent_instance_id, secret_path, outcome, timestamp
  FROM vault_access_log
 WHERE ticket_id = $1
 ORDER BY timestamp DESC;
```

#### `migrations/queries/secret_metadata.sql` (new)

```sql
-- name: UpsertSecretMetadata :exec
-- M2.3 operator-driven seeding path (ops-checklist snippets). Trigger
-- on agent_role_secrets keeps allowed_role_slugs in sync.
INSERT INTO secret_metadata
    (secret_path, customer_id, provenance, rotation_cadence, last_rotated_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (secret_path, customer_id) DO UPDATE
    SET provenance       = EXCLUDED.provenance,
        rotation_cadence = EXCLUDED.rotation_cadence,
        last_rotated_at  = EXCLUDED.last_rotated_at,
        updated_at       = now();

-- name: TouchSecretLastAccessed :exec
-- Called by vault.WriteAuditRow on OutcomeGranted to populate
-- secret_metadata.last_accessed_at (D3.4). Same tx as the access log
-- INSERT.
UPDATE secret_metadata
   SET last_accessed_at = $3, updated_at = now()
 WHERE secret_path = $1 AND customer_id = $2;

-- name: ListStaleSecrets :many
-- Exists for M3 dashboard's "stale secrets" view. M2.3 does not call.
SELECT secret_path, customer_id, provenance, rotation_cadence,
       last_rotated_at, last_accessed_at, allowed_role_slugs
  FROM secret_metadata
 WHERE last_rotated_at IS NOT NULL
   AND rotation_cadence <> 'never'
   AND (now() - last_rotated_at) > rotation_cadence;
```

### Deployment — `supervisor/docker-compose.yml` additions

Three new services land on the existing compose network, with all bindings internal:

```yaml
  # --- Infisical stack (M2.3) ---
  infisical-postgres:
    image: postgres:15-alpine
    container_name: garrison-infisical-postgres
    environment:
      POSTGRES_DB: infisical
      POSTGRES_USER: infisical
      POSTGRES_PASSWORD: ${GARRISON_INFISICAL_PG_PASSWORD:?GARRISON_INFISICAL_PG_PASSWORD required}
    volumes:
      - infisical-postgres-data:/var/lib/postgresql/data
    restart: unless-stopped

  infisical-redis:
    image: redis:7-alpine
    container_name: garrison-infisical-redis
    restart: unless-stopped

  infisical:
    # Production pins by digest via GARRISON_INFISICAL_IMAGE; :latest
    # is a dev default.
    image: ${GARRISON_INFISICAL_IMAGE:-infisical/infisical:latest}
    container_name: garrison-infisical
    depends_on:
      infisical-postgres: { condition: service_started }
      infisical-redis:    { condition: service_started }
    environment:
      ENCRYPTION_KEY: ${GARRISON_INFISICAL_ENCRYPTION_KEY:?GARRISON_INFISICAL_ENCRYPTION_KEY required}
      AUTH_SECRET:    ${GARRISON_INFISICAL_AUTH_SECRET:?GARRISON_INFISICAL_AUTH_SECRET required}
      DB_CONNECTION_URI: postgres://infisical:${GARRISON_INFISICAL_PG_PASSWORD}@infisical-postgres:5432/infisical
      REDIS_URL: redis://infisical-redis:6379
      SITE_URL: http://garrison-infisical:8080
    # No ports published. Internal network only.
    restart: unless-stopped

volumes:
  infisical-postgres-data:
```

The supervisor service gains three new env-var references for its own Infisical client:

```yaml
  supervisor:
    environment:
      # ... existing ...
      GARRISON_INFISICAL_ADDR:          http://garrison-infisical:8080
      GARRISON_INFISICAL_CLIENT_ID:     ${GARRISON_INFISICAL_CLIENT_ID:?GARRISON_INFISICAL_CLIENT_ID required}
      GARRISON_INFISICAL_CLIENT_SECRET: ${GARRISON_INFISICAL_CLIENT_SECRET:?GARRISON_INFISICAL_CLIENT_SECRET required}
    depends_on:
      # ... existing ...
      infisical: { condition: service_started }
```

### Ops checklist additions — `docs/ops-checklist.md`

A new "M2.3 — Infisical deployment" section covers:

1. **Bootstrap secret generation**: `openssl rand -base64 32` twice, one for `ENCRYPTION_KEY`, one for `AUTH_SECRET`. Store in the operator's password manager. Never commit `.env`.
2. **Image digest pinning**: before production deploy, resolve `infisical/infisical:<version>` to a digest with `docker inspect --format='{{index .RepoDigests 0}}' infisical/infisical:<version>` and set `GARRISON_INFISICAL_IMAGE=infisical/infisical@sha256:...` in Coolify.
3. **Post-deploy Machine Identity creation**: log into the Infisical UI as the first admin (local-network access only), create two Machine Identities (`garrison-supervisor` read-only, `garrison-dashboard` read+write), generate client_id + client_secret for each, store both in the operator's password manager, set `GARRISON_INFISICAL_CLIENT_ID` + `GARRISON_INFISICAL_CLIENT_SECRET` in Coolify for the supervisor service. `garrison-dashboard` credentials are created-and-parked until M4.
4. **Seeding a secret**: in the Infisical UI, add the secret at path `/<customer_id>/<provenance>/<name>`. Then run the ops-checklist INSERT snippet to create the corresponding `secret_metadata` row in Garrison's Postgres (until M4 automates this).
5. **Adding a grant**: a new migration `migrations/<timestamp>_m2_3_grant_<role>_<name>.sql` with an `INSERT INTO agent_role_secrets` row. The trigger rebuilds `secret_metadata.allowed_role_slugs` automatically.
6. **ML credential rotation**: rotate `garrison-supervisor`'s client_secret in the Infisical UI; update `GARRISON_INFISICAL_CLIENT_SECRET` in Coolify; restart the supervisor service. The auto-re-auth path handles the transition on next spawn.

---

## Phase 2 — testing plan

### Unit tests (new files)

**`internal/vault/secretvalue_test.go`**
- `TestSecretValueLogValueReturnsRedacted` — `slog.StringValue("[REDACTED]")` from `LogValue()` regardless of the underlying bytes.
- `TestSecretValueStringFallbackIsSafe` — `fmt.Sprintf("%s", v)` produces the Go-reflection fallback string, not the raw value.
- `TestSecretValueZeroIdempotent` — calling `Zero()` twice succeeds; backing slice is `nil` after.
- `TestSecretValueEmptyIsZeroValue` — `Empty()` returns true for the zero value.
- `TestSecretValueNewCopiesSource` — mutating the input slice after `New` does not mutate the SecretValue.

**`internal/vault/errors_test.go`**
- `TestClassifyExitReasonTable` — table-driven: each of the 6 sentinels returns the correct string; wrapped error via `fmt.Errorf("%w", ErrX)` still classifies correctly; `nil` returns `""`; unknown error returns `ExitClaudeError`.

**`internal/vault/scanner_test.go`**
- `TestScanAndRedact_SKPrefix` through `TestScanAndRedact_BearerShape` — one positive + one negative per pattern (10 × 2 = 20 cases).
- `TestScanAndRedact_MultipleMatchesInOneString` — input containing both a `sk-` and a `ghp_` match produces both labels in `matched` and both substrings redacted.
- `TestScanAndRedact_NoMatch` — plain English text produces no redaction; `matched` is nil.
- `TestScanAndRedact_FalsePositiveShortString` — a 3-char string containing `sk-` does NOT match (minimum-length gate on the regex prevents over-redaction of prose like `"sk-"`).

**`internal/vault/leakscan_test.go`**
- `TestRuleOneLeakScanEmptyGrantSet` — nil/empty map returns nil leaked.
- `TestRuleOneLeakScanPositiveMatch` — agent.md contains a literal secret value → returns the env_var_name.
- `TestRuleOneLeakScanEnvVarNameOnly` — agent.md references the env_var name but not the value → no match.
- `TestRuleOneLeakScanMultipleSecretsOneMatch` — 3 secrets in grant, only 2nd's value appears in agent.md → returns only that env_var_name.
- `TestRuleOneLeakScanZeroValueSecretSkipped` — grant contains an `Empty()` SecretValue → skip, no spurious match on empty string.

**`internal/vault/client_test.go`**
- `TestClientFetchHappyPath` — mock SDK, one path, one value; assert returned `SecretValue` carries the value.
- `TestClientFetchOn401ReauthenticatesOnceThenSucceeds` — mock SDK returns 401 on first call, 200 on second; assert Fetch returns the value, exactly one `reauthenticate` call observed.
- `TestClientFetchOn401TwiceReturnsErrVaultAuthExpired` — mock SDK returns 401 on both calls; assert `errors.Is(err, ErrVaultAuthExpired)`.
- `TestClientFetchConcurrentSafe` — 100 concurrent `Fetch` calls with a mock that simulates a 401 mid-burst; assert no panic, no data race (`go test -race`), exactly one reauth ever issued even under concurrency.
- `TestClientFetchClassifiesAllFailureModes` — table-driven: HTTP 403, 429, 404, unreachable each map to the correct sentinel error.

**`internal/vault/audit_test.go`**
- `TestWriteAuditRowGranted` — in-tx INSERT + `TouchSecretLastAccessed` both observed.
- `TestWriteAuditRowDeniedNoGrant` — INSERT only; no secret_metadata update.
- `TestWriteAuditRowErrorAuditing` — simulated INSERT failure returns `ErrVaultAuditFailed`.

### Unit tests (modified files)

**`internal/spawn/exitreason_test.go`**
- Extend the constant-set assertion to include the 8 new values.

**`internal/mcpconfig/mcpconfig_test.go`**
- `TestRejectVaultServers_NoMatch` — baseline config (postgres, mempalace, finalize) passes.
- `TestRejectVaultServers_NamedVault` — config with `"vault": {...}` server rejected.
- `TestRejectVaultServers_CaseInsensitive` — `"VaultClient"` rejected.
- `TestRejectVaultServers_SubstringMatch` — `"my-infisical-bridge"` rejected.
- `TestRejectVaultServers_BaselineServersNotBanned` — assert none of `postgres`/`mempalace`/`finalize` triggers the filter (regression guard).

**`internal/finalize/handler_test.go`**
- `TestFinalizeHandlerRedactsSecretPatternsInDiary` — input payload with `sk-abc...` in `rationale`; observe written payload has `[REDACTED:sk_prefix]`; `hygiene_status` set to `suspected_secret_emitted`.
- `TestFinalizeHandlerRedactsInKGTriples` — same as above but matching `kg_triples[].object`.
- `TestFinalizeHandlerCleanPayloadUnchanged` — non-matching payload passes through byte-identical; `hygiene_status='clean'`.

### Integration tests

**`internal/vault/vault_integration_test.go`** (`//go:build integration`)
- `TestInfisicalTestcontainerBootstrap` — start Infisical testcontainer, create ML, authenticate, seed one secret, fetch it via `vault.Client`. Baseline smoke test.
- `TestVaultFetchRoundTripWithAudit` — one secret, one ticket, one agent_instance; fetch → audit row written → secret_metadata.last_accessed_at populated; grep-clean slog.

**`supervisor/integration_m2_3_vault_test.go`** (`//go:build integration`) — covers all 9 user stories per D10.6:

- `TestVaultSpawnWithSingleSecret` — US1 / SC-401. Full stack + mockclaude: ticket inserted, supervisor spawns engineer with grant, fetched env var visible to mockclaude fixture, one `vault_access_log` row with `outcome=granted`, grep-clean argv + slog + MCP config + stream-json.
- `TestVaultRule1BlocksSpawnOnLeakedValue` — US2 / SC-402. Seed agent.md with literal secret value; assert `agent_instances.exit_reason='secret_leaked_in_agent_md'`, no subprocess invocation.
- `TestVaultRule2ZeroGrantsZeroSecrets` — US3 / SC-403. Role without grants spawns; no Infisical testcontainer request logged; no `vault_access_log` row.
- `TestVaultRule3BlocksSpawnOnVaultMcp` — US4 / SC-404. `agents.mcp_config` seeded with a banned-pattern entry; assert `exit_reason='vault_mcp_in_config'`; no subprocess.
- `TestVaultFailureMode_Unavailable` — US5 / SC-405. Infisical container stopped mid-test; assert `vault_unavailable`.
- `TestVaultFailureMode_AuthExpired` — US5 / SC-405. ML credentials rotated out-of-band mid-test; auto-reauth also fails; assert `vault_auth_expired`.
- `TestVaultFailureMode_PermissionDenied` — US5 / SC-405. ML lacks access to the seeded path; assert `vault_permission_denied`, `outcome='denied_infisical'` in audit log.
- `TestVaultFailureMode_RateLimited` — US5 / SC-405. Burst of requests hits Infisical's rate limit; assert `vault_rate_limited`.
- `TestVaultFailureMode_SecretNotFound` — US5 / SC-405. Grant references unseeded path; assert `vault_secret_not_found`.
- `TestVaultDualAuditRecord` — US6 / SC-406. Post-spawn, both `vault_access_log` and Infisical's native audit API return matching records; neither carries the value.
- `TestSecretPatternScanRedactsBeforeMemPalaceWrite` — US7 / SC-407. Mockclaude emits finalize payload with `sk-abc...` in rationale; MemPalace-written drawer content carries `[REDACTED:sk_prefix]`; `hygiene_status='suspected_secret_emitted'`; transition committed.
- `TestVaultAuditFailureFailClosed` — US8 / SC-408. Simulated `vault_access_log` INSERT failure (e.g., drop the table before spawn); assert `exit_reason='vault_audit_failed'`, no subprocess, Infisical log shows the fetch but Garrison has no row.
- `TestSecretMetadataPopulatedAtBootstrap` — US9 / SC-409. Post-bootstrap assertion: one `secret_metadata` row per seeded Infisical secret; `last_accessed_at` updated after a successful spawn.

### Regression guard

- `TestAllM2_2_XIntegrationTestsStillPass` — the existing M2.1/M2.2/M2.2.1/M2.2.2 integration suites run unchanged under their existing build tags. Any failure indicates M2.3 broke a prior-milestone assertion; FR-428 forbids this.

### Custom vet analyzer

**`supervisor/tools/vaultlog/analyzer.go`** (new) — AST-walking analyzer built on `golang.org/x/tools/go/analysis` (one of M2.3's two new direct Go deps; see Technical context). Reports a diagnostic whenever a call to `slog.Info`, `slog.Error`, `slog.Warn`, `slog.Debug`, `slog.Log`, `slog.LogAttrs`, `fmt.Printf`, `fmt.Sprintf`, `fmt.Fprintf`, or any `Logger.*` method takes an argument of type `vault.SecretValue` OR the return of `vault.SecretValue.UnsafeBytes()`.

**`supervisor/tools/vaultlog/analyzer_test.go`** (new)
- Positive case: `slog.Info("fetched", "value", sv)` flagged.
- Positive case: `slog.Info("fetched", "value", sv.UnsafeBytes())` flagged.
- Positive case: `fmt.Sprintf("%s", sv)` flagged.
- Negative case: `slog.Info("fetched", "path", "/production/stripe/key")` not flagged.
- Negative case: `slog.Info("fetched", "redacted", sv.LogValue())` not flagged.

**Hookup**: `supervisor/Makefile` (or `justfile`) gains a `vet-vaultlog` target running `go run ./tools/vaultlog/cmd/vaultlog ./...`. CI runs it as part of the test workflow alongside `go vet`, `go test`, `staticcheck`.

### Live-acceptance test

No new `live_acceptance`-tagged test at M2.3. The M2.2.2 compliance matrix is a prompt-and-model correctness check; M2.3 is plumbing verified via integration tests with testcontainers + mockclaude. A real-Claude acceptance run happens once during operator-led ship validation, recorded in `acceptance-evidence.md` (Phase 4), but is not automated as a live-acceptance test in the suite.

---

## Phase 3 — deployment

No new deployment target outside the compose stack. Operator-facing rollout steps land in `docs/ops-checklist.md` (see Ops checklist additions above). The supervisor binary build path, Dockerfile, and CI workflow are unchanged.

**Rollout sequence** (per ops checklist):
1. Generate `ENCRYPTION_KEY` + `AUTH_SECRET` + `INFISICAL_PG_PASSWORD`.
2. Resolve Infisical image to a digest; set `GARRISON_INFISICAL_IMAGE`.
3. Deploy the compose stack — Infisical comes up first; supervisor depends on it.
4. Admin-access Infisical UI over the internal Coolify network, create two Machine Identities.
5. Set `GARRISON_INFISICAL_CLIENT_ID` + `GARRISON_INFISICAL_CLIENT_SECRET` in Coolify.
6. Restart the supervisor service; observe clean startup (fail-fast on missing env vars is the guard).
7. Seed initial secrets in the Infisical UI; insert corresponding `secret_metadata` rows via the ops-checklist SQL snippets.
8. At first grant addition: PR the grant migration; the trigger syncs `allowed_role_slugs`.

---

## Open questions

- **OQ-1 (resolved in slate)**: custom `go vet` analyzer shipped per D10.8 + SC-410. Analyzer is ~100 LOC using `golang.org/x/tools/go/analysis` (stdlib-adjacent, not a locked-list dep addition).
- **OQ-2 (resolved in slate)**: `customer_id` cached once at startup from `SELECT id FROM companies LIMIT 1`. Acceptable at M2.3 single-tenant. Flagged in retro as the cache that becomes stale if/when multi-company activates (additive migration).
- **OQ-3 (resolved in slate)**: Image pin via `GARRISON_INFISICAL_IMAGE` env var. Production sets to digest; dev defaults to `:latest`. Operator-friendly and not hidden in compose.
- **Open, for plan phase follow-up during implementation**: the exact pinned version of `infisical/infisical` to start from. Resolve to a digest at implementation time against the latest stable release; document the chosen version + digest in `acceptance-evidence.md`.
- **Open, implementation-time**: the exact Go SDK version in `go.mod`. Resolve to the latest stable `github.com/infisical/go-sdk` at implementation time; flag any surprising SDK behavior for clarify rather than working around it quietly.

---

## Complexity tracking

*Empty. Constitution check passes without violations.*

---

## Exit criteria (summary)

The plan is complete when every SC-401 – SC-415 maps to a concrete named test function or named deployment artifact in Phase 2 / Phase 3 above. Specifically:

| SC | Tracked by |
|---|---|
| SC-401 | `TestVaultSpawnWithSingleSecret` |
| SC-402 | `TestVaultRule1BlocksSpawnOnLeakedValue` |
| SC-403 | `TestVaultRule2ZeroGrantsZeroSecrets` |
| SC-404 | `TestVaultRule3BlocksSpawnOnVaultMcp` |
| SC-405 | `TestVaultFailureMode_{Unavailable,AuthExpired,PermissionDenied,RateLimited,SecretNotFound}` |
| SC-406 | `TestVaultDualAuditRecord` |
| SC-407 | `TestSecretPatternScanRedactsBeforeMemPalaceWrite` |
| SC-408 | `TestVaultAuditFailureFailClosed` |
| SC-409 | `TestSecretMetadataPopulatedAtBootstrap` |
| SC-410 | `supervisor/tools/vaultlog/` analyzer run as part of CI vet step |
| SC-411 | `git diff --stat origin/main..HEAD -- supervisor/go.mod supervisor/go.sum` shows exactly two direct deps added (infisical Go SDK + golang.org/x/tools/go/analysis) |
| SC-412 | `docs/ops-checklist.md` "M2.3 — Infisical deployment" section |
| SC-413 | All M1/M2.1/M2.2/M2.2.1/M2.2.2 tests pass unchanged under `go test ./...` and each existing build tag |
| SC-414 | `goose up → goose down → goose up` cycle under testcontainer Postgres produces no drift |
| SC-415 (headline) | `acceptance-evidence.md` compiled at Phase 4 |

`/garrison-tasks` is the next step — break this plan into executable tasks ordered by dependency, with the convention Garrison has established (one task per FR or SC where cleanly separable, otherwise grouped by file-surface).
