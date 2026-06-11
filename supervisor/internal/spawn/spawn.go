package spawn

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/agentcontainer"
	"github.com/garrison-hq/garrison/supervisor/internal/agents"
	"github.com/garrison-hq/garrison/supervisor/internal/concurrency"
	"github.com/garrison-hq/garrison/supervisor/internal/mcpconfig"
	"github.com/garrison-hq/garrison/supervisor/internal/mempalace"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/throttle"
	"github.com/garrison-hq/garrison/supervisor/internal/vault"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ShutdownSignalGrace is the NFR-005 fixed window between the SIGTERM
// forwarded to a running subprocess on supervisor shutdown and the SIGKILL
// escalation that follows. Not operator-tunable.
const ShutdownSignalGrace = 5 * time.Second

// TerminalWriteGrace bounds the final terminal-row update after the
// subprocess exits. The root ctx is already cancelled on shutdown, so the
// terminal writers run against a detached ctx — this cap keeps a stuck DB
// from holding the supervisor past the operator's shutdown budget.
const TerminalWriteGrace = 5 * time.Second

// MCPBailSignalGrace bounds the SIGTERM→SIGKILL escalation window on the
// MCP-bail path. NFR-106 budget is 2 s from init-observation to the group
// being signalled; the first SIGTERM lands within microseconds of the
// observation, so this grace governs only how long we wait before
// escalating. 2 seconds is a deliberate parity with the total NFR budget.
const MCPBailSignalGrace = 2 * time.Second

const (
	roleQAEngineer     = "qa-engineer"
	errBeginTerminalTx = "spawn: begin terminal tx: %w"
	errMarkEventProcd  = "spawn: MarkEventProcessed: %w"
	errCommitTerminal  = "spawn: commit terminal: %w"
)

// spawnPayload is the JSON shape every event payload carries. Extracted
// to a named type so the anonymous struct doesn't have to be redeclared
// at every site that reads it (lock-tx body, runFakeAgent, runRealClaude).
type spawnPayload struct {
	TicketID     string `json:"ticket_id"`
	DepartmentID string `json:"department_id"`
	ColumnSlug   string `json:"column_slug"`
}

// realClaudeInvocation bundles the per-event identifiers + role context
// runRealClaude needs. Kept as a struct so the function signature stays
// readable (S107: ≤ 7 params) without losing the fact that all four IDs
// + dept + payload + role refer to the same spawn invocation.
type realClaudeInvocation struct {
	InstanceID pgtype.UUID
	EventID    pgtype.UUID
	TicketUUID pgtype.UUID
	Dept       store.Department
	Payload    spawnPayload
	RoleSlug   string
}

// terminalWriteParams bundles every column writeTerminalCostAndWakeup
// updates inside its single transaction. Extracted to keep the function
// at S107-compliant arity while making call-site intent explicit.
type terminalWriteParams struct {
	InstanceID       pgtype.UUID
	EventID          pgtype.UUID
	TicketID         pgtype.UUID
	Status           string
	ExitReason       string
	Cost             pgtype.Numeric
	WakeUpStatus     string
	InsertTransition bool
	FromCol          string
	ToCol            string

	// SkipMarkProcessed leaves the event_outbox row unprocessed so the
	// M1 poll fallback retries the event. Container-path exec-create
	// failures set it (FR-019: container missing/stopped at spawn is
	// retryable; the boot reconciler is the repair path). Every other
	// terminal write marks the event processed as before.
	SkipMarkProcessed bool
}

// Deps bundles Spawn's runtime collaborators. Constructed once in
// cmd/supervisor and handed to the dispatcher so every event invocation
// shares the same pool, logger, and config.
type Deps struct {
	Pool              *pgxpool.Pool
	Queries           *store.Queries
	Logger            *slog.Logger
	SubprocessTimeout time.Duration

	// SigkillEscalations counts subprocesses the supervisor had to escalate
	// from SIGTERM to SIGKILL on shutdown (NFR-005). nil is tolerated
	// (tests skip tracking); production wires an atomic counter so
	// cmd/supervisor can return exit code 5 per contracts/cli.md when
	// counter > 0 at shutdown.
	SigkillEscalations *atomic.Int64

	// Fake-agent escape hatch. FakeAgentCmd being non-empty implicitly
	// flips UseFakeAgent true so M1 integration tests and chaos_test.go
	// keep working without touching every call site. Production daemons
	// set UseFakeAgent explicitly from config.
	FakeAgentCmd string
	UseFakeAgent bool

	// M2.1 real-Claude path collaborators. Unused when UseFakeAgent is
	// true. AgentRODSN is composed by config.AgentRODSN() and handed in
	// whole (the unexported password never leaves config).
	AgentsCache     *agents.Cache
	ClaudeBin       string
	ClaudeModel     string
	ClaudeBudgetUSD float64
	MCPConfigDir    string
	SupervisorBin   string
	AgentRODSN      string

	// DatabaseURL is the supervisor's main (write-capable) DSN, used
	// only for the M8 agent-caller garrison-mutate MCP entry — its
	// verbs INSERT tickets + audit rows, which agent_ro cannot. Empty
	// disables the entry (M2.x test harnesses keep the 3-entry shape).
	DatabaseURL string

	// M2.2 additions — mempalace entry in the per-invocation MCP config,
	// plus wake-up/hygiene collaborators that land in spawn.go via T013.
	DockerBin          string
	MempalaceContainer string
	PalacePath         string
	DockerHost         string

	// M2.2.1 T011: finalize-flow collaborators. Palace is the shared
	// client (same instance the hygiene goroutine uses — one
	// docker-exec pool per process). FinalizeWriteTimeout bounds the
	// atomic write's wall clock per FR-261 / Clarify Q5; 30s default
	// comes from config.DefaultFinalizeWriteTimeout.
	Palace               *mempalace.Client
	FinalizeWriteTimeout time.Duration

	// M6 T006: result-event grace window. Post-finalize-commit, the
	// pipeline defers the onCommit callback for up to this duration
	// to give Claude time to emit its `result` event so total_cost_usd
	// is populated when the row is written. Closes the cost-telemetry
	// blind-spot documented at docs/issues/cost-telemetry-blind-spot.md.
	// Zero preserves M2.2.1 synchronous-commit semantics; production
	// wires from config.FinalizeResultGrace (default 3s).
	FinalizeResultGrace time.Duration

	// M6 T007: throttle gate dependencies. Constructed once at
	// supervisor boot (cmd/supervisor/main.go) and shared with the
	// pipeline OnRateLimit observer (T008). Zero-value Deps disables
	// the gate (Pool=nil short-circuits Check); used by M2.x
	// integration tests + chaos suites that don't seed company rows.
	Throttle throttle.Deps

	// M2.3: vault client (Fetcher interface so tests can inject a mock).
	// CustomerID scopes grant queries and audit rows to this deployment's
	// company row (D6.3 / OQ-2). Both are wired from config in cmd/supervisor.
	// nil Vault is tolerated — vault steps are skipped (zero-grants fast path).
	Vault      vault.Fetcher
	CustomerID pgtype.UUID

	// GrantsListerFn overrides the default ListGrantsForRole DB call when
	// set. Tests inject mock grants without a real database. Production
	// leaves this nil and the live deps.Queries path is used.
	GrantsListerFn func(ctx context.Context, roleSlug string, customerID pgtype.UUID) ([]vault.GrantRow, error)

	// AuditFn overrides the full vault audit write (Begin + WriteAuditRow + Commit)
	// when set. Tests inject failure scenarios (e.g. returning ErrVaultAuditFailed)
	// without needing a real pool. Production leaves this nil.
	AuditFn func(ctx context.Context, row vault.AuditRow) error

	// M7 T011 / decision #5: feature flag selecting the spawn execution
	// path. UseDirectExec=true (default at M7 ship) runs the legacy
	// exec.CommandContext flow for M2.x-shipped agents pre-grandfathering.
	// migrate7 (T014) flips this to false after the M2.x agents move to
	// per-agent containers; from then on every spawn flows through
	// AgentContainer.Exec. The flag is removed entirely in the post-M7
	// polish PR after a soak window at false.
	UseDirectExec bool

	// AgentContainer is the M7 controller used when UseDirectExec=false.
	// nil tolerated only when UseDirectExec=true; otherwise spawn fails
	// fast with ExitSpawnFailed.
	AgentContainer agentcontainer.Controller

	// M7.1 T010 / FR-017: per-agent in-flight slot. The container path
	// serializes spawns per agent — one claude exec per per-agent
	// container at a time. Enforcement is active only when the container
	// path is selected (!UseDirectExec && AgentContainer != nil); nil
	// disables it (direct-exec/test back-compat). Production wires one
	// shared instance from cmd/supervisor.
	Inflight *AgentInflight

	// M7.1 T011: container-path collaborators. EgressProxyURL lands in
	// claude execs as HTTPS_PROXY (FR-009/FR-011 — the only route out
	// of the agents network); AgentWorkspaceFS is the host base dir of
	// the agent-ID-keyed workspace binds, used for the acceptance-gate
	// path <WorkspaceFS>/<agent-uuid> (FR-006). Both wired from config
	// in cmd/supervisor (T012); unused on the direct-exec path.
	EgressProxyURL   string
	AgentWorkspaceFS string

	// terminalReasonOverride, when non-nil, remaps the adjudicated
	// (status, exit_reason) pair just before the terminal write. Set
	// per-spawn on a Deps COPY by the container path only (plan D21):
	// a coreutils-timeout wrapper failure (exit 125–127, no result
	// frame → adjudicated no_result) lands in the spawn_failed class.
	// nil everywhere else — zero behavior change for direct-exec.
	terminalReasonOverride func(status, exitReason string) (status2, exitReason2 string)

	// terminalWriteFn replaces the terminal-write transaction in unit
	// tests (m7_test.go) so container-path terminal contracts are
	// assertable without a database. nil in production.
	terminalWriteFn func(ctx context.Context, p terminalWriteParams) error
}

// UseFake decides which branch Spawn runs. UseFakeAgent wins if set;
// otherwise a non-empty FakeAgentCmd implies fake mode for back-compat
// with existing M1 tests that predate the explicit flag. Exported so
// tests can pin the dispatch contract without reaching for reflection.
func (d Deps) UseFake() bool {
	return d.UseFakeAgent || d.FakeAgentCmd != ""
}

// spawnPrep is the result of Spawn's first short transaction. When
// done is true, Spawn returns immediately (already-processed dedupe,
// missing-department mark-and-skip, or capacity-deferred path) — there
// is no agent_instances row to drive. Otherwise the caller proceeds to
// the fake-agent or real-Claude branch with the populated fields.
type spawnPrep struct {
	done       bool
	instanceID pgtype.UUID
	ticketUUID pgtype.UUID
	dept       store.Department
	payload    spawnPayload

	// release frees the per-agent in-flight slot (M7.1 T010 / FR-017).
	// nil when enforcement was inert. Spawn defers it so the slot is
	// held until after the run branch returns — past the terminal write.
	release func()
}

// Spawn handles one work.ticket.created event end-to-end. Idempotent: a
// second call with the same event_id is a no-op via the
// LockEventForProcessing dedupe check, which is the guard against the
// LISTEN/poll race described in plan §"Dedupe on handling".
//
// The first short transaction (LockEventForProcessing → department lookup
// → CheckCap → InsertRunningInstance → commit) is shared by both the
// fake-agent and real-Claude paths. Only after that commit do the two
// paths diverge — the fake path runs the M1 exec-and-scan loop; the real
// path runs the M2.1 agent-resolve + MCP-config + NDJSON-pipeline flow.
func Spawn(ctx context.Context, deps Deps, eventID pgtype.UUID, roleSlug string) error {
	if roleSlug == "" {
		roleSlug = "engineer" // M1/M2.1 back-compat default for fake-agent test paths
	}
	prep, err := prepareSpawn(ctx, deps, eventID, roleSlug)
	// M6 T007: throttle deferral is not an error from the dispatcher's
	// perspective — it's a "leave event_outbox unprocessed and try again
	// next poll" signal that mirrors the concurrency-cap-deferred path.
	if errors.Is(err, ErrSpawnDeferred) {
		return nil
	}
	if err != nil || prep.done {
		return err
	}
	// M7.1 T010: the per-agent slot (when held) is released only after
	// the run branch returns — i.e. past the terminal write — so a
	// retried event for the same agent cannot interleave with the tail
	// of this spawn.
	if prep.release != nil {
		defer prep.release()
	}
	if deps.UseFake() {
		return runFakeAgent(ctx, deps, prep.instanceID, eventID, prep.ticketUUID, prep.payload)
	}
	return runRealClaude(ctx, deps, realClaudeInvocation{
		InstanceID: prep.instanceID,
		EventID:    eventID,
		TicketUUID: prep.ticketUUID,
		Dept:       prep.dept,
		Payload:    prep.payload,
		RoleSlug:   roleSlug,
	})
}

// prepareSpawn runs the dedupe transaction shared by the fake-agent and
// real-Claude paths: LockEventForProcessing → decode payload → resolve
// department → CheckCap → InsertRunningInstance → commit. Returns
// spawnPrep{done:true} for the three terminal-without-agent_instance
// outcomes (already-processed, deleted department, capacity-cap
// deferred); a populated prep otherwise.
func prepareSpawn(ctx context.Context, deps Deps, eventID pgtype.UUID, roleSlug string) (spawnPrep, error) {
	tx, err := deps.Pool.Begin(ctx)
	if err != nil {
		return spawnPrep{}, fmt.Errorf("spawn: begin dedupe tx: %w", err)
	}
	q := deps.Queries.WithTx(tx)

	evt, err := q.LockEventForProcessing(ctx, eventID)
	if err != nil {
		_ = tx.Rollback(ctx)
		return spawnPrep{}, fmt.Errorf("spawn: LockEventForProcessing: %w", err)
	}
	if evt.ProcessedAt.Valid {
		_ = tx.Rollback(ctx)
		return spawnPrep{done: true}, nil
	}

	payload, ticketUUID, deptUUID, err := decodeSpawnPayload(evt.Payload)
	if err != nil {
		_ = tx.Rollback(ctx)
		return spawnPrep{}, err
	}

	dept, missing, err := resolveDeptOrSkip(ctx, q, deptUUID, eventID, payload, deps.Logger, tx)
	if err != nil {
		return spawnPrep{}, err
	}
	if missing {
		return spawnPrep{done: true}, nil
	}

	allowed, capN, running, err := concurrency.CheckCap(ctx, q, deptUUID)
	if err != nil {
		_ = tx.Rollback(ctx)
		return spawnPrep{}, fmt.Errorf("spawn: CheckCap: %w", err)
	}
	if !allowed {
		_ = tx.Rollback(ctx)
		deps.Logger.Info("defer: concurrency cap reached",
			"event_id", formatUUID(eventID),
			"department_id", payload.DepartmentID,
			"cap", capN,
			"running", running,
		)
		return spawnPrep{done: true}, nil
	}

	// M7.1 T010 / FR-017: per-agent in-flight slot. A second event for
	// the same agent defers exactly like cap-full: rollback, event left
	// unprocessed, retried on the next poll sweep. Acquired before
	// InsertRunningInstance so a deferred event never carries an
	// instance row.
	releaseSlot, slotFree := acquireAgentSlot(ctx, deps, dept, roleSlug)
	if !slotFree {
		_ = tx.Rollback(ctx)
		deps.Logger.Info("defer: agent spawn already in flight",
			"event_id", formatUUID(eventID),
			"department_id", payload.DepartmentID,
			"role_slug", roleSlug,
		)
		return spawnPrep{done: true}, nil
	}
	slotHandedOff := false
	if releaseSlot != nil {
		// Every later failure/defer path in this function releases the
		// slot; the success return hands it to Spawn via spawnPrep.
		defer func() {
			if !slotHandedOff {
				releaseSlot()
			}
		}()
	}

	// M6 T007: throttle gate. Companies opt-in via daily_budget_usd
	// (set by the operator); pause_until is set by OnRateLimit (T008).
	// Both are NULL by default so the gate is fully back-compat — the
	// CheckThrottle helper short-circuits to "allow" when the deps
	// pool is nil (test/back-compat path).
	if dept.CompanyID.Valid && deps.Throttle.Pool != nil {
		decision, err := throttle.Check(ctx, deps.Throttle, q, dept.CompanyID)
		if err != nil {
			_ = tx.Rollback(ctx)
			return spawnPrep{}, fmt.Errorf("spawn: throttle.Check: %w", err)
		}
		if !decision.Allowed {
			// Audit: budget defer fires the throttle_events row + notify
			// here; rate-limit pause was already audited by OnRateLimit
			// when the rate-limit event landed (T008 wires that path).
			if decision.Kind == throttle.KindCompanyBudgetExceeded {
				if err := throttle.FireBudgetDefer(ctx, q, dept.CompanyID,
					/* current */ pgtype.Numeric{},
					/* estimated */ deps.Throttle.DefaultSpawnCostUSD,
					/* budget */ pgtype.Numeric{},
				); err != nil {
					deps.Logger.Warn("throttle: FireBudgetDefer failed; deferring without audit",
						"event_id", formatUUID(eventID),
						"company_id", formatUUID(dept.CompanyID),
						"err", err,
					)
				}
			}
			if err := tx.Commit(ctx); err != nil {
				return spawnPrep{}, fmt.Errorf("spawn: commit throttle audit: %w", err)
			}
			deps.Logger.Info("defer: throttle gate",
				"event_id", formatUUID(eventID),
				"company_id", formatUUID(dept.CompanyID),
				"kind", decision.Kind,
			)
			return spawnPrep{}, ErrSpawnDeferred
		}
	}

	// M8 FR-102: dependency satisfaction gate. If the candidate
	// ticket carries a `depends_on_ticket_id` and that predecessor
	// isn't yet in one of its dept's
	// `dependency_satisfaction_columns`, defer the spawn (event stays
	// unprocessed; the transition listener's NotifyBlockedDependents
	// callback re-enqueues on predecessor advancement).
	if ok, err := checkDependencySatisfied(ctx, q, ticketUUID); err != nil {
		_ = tx.Rollback(ctx)
		return spawnPrep{}, fmt.Errorf("spawn: checkDependencySatisfied: %w", err)
	} else if !ok {
		_ = tx.Rollback(ctx)
		deps.Logger.Info("defer: dependency not satisfied",
			"event_id", formatUUID(eventID),
			"ticket_id", payload.TicketID,
		)
		return spawnPrep{done: true}, nil
	}

	instanceID, err := q.InsertRunningInstance(ctx, store.InsertRunningInstanceParams{
		DepartmentID: deptUUID,
		TicketID:     ticketUUID,
		RoleSlug:     roleSlug,
	})
	if err != nil {
		_ = tx.Rollback(ctx)
		return spawnPrep{}, fmt.Errorf("spawn: InsertRunningInstance: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return spawnPrep{}, fmt.Errorf("spawn: commit dedupe+running: %w", err)
	}
	slotHandedOff = true
	return spawnPrep{
		instanceID: instanceID,
		ticketUUID: ticketUUID,
		dept:       dept,
		payload:    payload,
		release:    releaseSlot,
	}, nil
}

// acquireAgentSlot claims the per-agent in-flight slot for the agent
// resolved by (department, role) via the in-memory AgentsCache. ok=false
// means a spawn for the same agent is already in flight — the caller
// defers exactly like cap-full. A nil release with ok=true means
// enforcement was inert: the gate is active only when the container
// path is selected (!UseDirectExec && AgentContainer != nil) and its
// collaborators are wired (Inflight, AgentsCache). Agent-resolution
// failure also passes through open — runRealClaude resolves again and
// writes the agent_missing terminal, so gating here would only change
// which surface reports it.
func acquireAgentSlot(ctx context.Context, deps Deps, dept store.Department, roleSlug string) (release func(), ok bool) {
	if deps.UseDirectExec || deps.AgentContainer == nil || deps.Inflight == nil || deps.AgentsCache == nil {
		return nil, true
	}
	agent, err := deps.AgentsCache.GetForDepartmentAndRole(ctx, dept.ID, roleSlug)
	if err != nil {
		return nil, true
	}
	return deps.Inflight.TryAcquire(formatUUID(agent.ID))
}

// decodeSpawnPayload unmarshals the event payload and parses both
// referenced UUIDs. All three errors share the same caller-side handling
// (tx rollback + return), so they're collapsed here.
func decodeSpawnPayload(raw []byte) (spawnPayload, pgtype.UUID, pgtype.UUID, error) {
	var payload spawnPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return spawnPayload{}, pgtype.UUID{}, pgtype.UUID{}, fmt.Errorf("spawn: decode payload: %w", err)
	}
	ticketUUID, err := parseUUID(payload.TicketID)
	if err != nil {
		return spawnPayload{}, pgtype.UUID{}, pgtype.UUID{}, fmt.Errorf("spawn: ticket_id: %w", err)
	}
	deptUUID, err := parseUUID(payload.DepartmentID)
	if err != nil {
		return spawnPayload{}, pgtype.UUID{}, pgtype.UUID{}, fmt.Errorf("spawn: department_id: %w", err)
	}
	return payload, ticketUUID, deptUUID, nil
}

// resolveDeptOrSkip looks up the department row, handling the deleted-
// department edge: mark the event processed, commit the tx, log once,
// and signal the caller via missing=true to bail out cleanly.
func resolveDeptOrSkip(
	ctx context.Context,
	q *store.Queries,
	deptUUID pgtype.UUID,
	eventID pgtype.UUID,
	payload spawnPayload,
	logger *slog.Logger,
	tx pgx.Tx,
) (store.Department, bool, error) {
	dept, err := q.GetDepartmentByID(ctx, deptUUID)
	if err == nil {
		return dept, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(ctx)
		return store.Department{}, false, fmt.Errorf("spawn: GetDepartmentByID: %w", err)
	}
	if err := q.MarkEventProcessed(ctx, eventID); err != nil {
		_ = tx.Rollback(ctx)
		return store.Department{}, false, fmt.Errorf("spawn: MarkEventProcessed missing-dept: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return store.Department{}, false, fmt.Errorf("spawn: commit missing-dept: %w", err)
	}
	logger.Error("department missing for event",
		"event_id", formatUUID(eventID),
		"ticket_id", payload.TicketID,
		"department_id", payload.DepartmentID,
		"reason", ExitDepartmentMissing,
	)
	return store.Department{}, true, nil
}

// -----------------------------------------------------------------------
// Fake-agent path (M1 lifecycle, preserved verbatim for test suites)
// -----------------------------------------------------------------------

// runFakeAgent is the M1 lifecycle: BuildCommand → cmd.Start → line-scan
// stdout/stderr → wait → Classify → writeTerminal. PID-level signalling is
// sufficient here because the fake agent does not spawn children; keeping
// this path identical to M1 is what lets every M1 integration test pass
// unchanged.
func runFakeAgent(
	ctx context.Context,
	deps Deps,
	instanceID, eventID, _ pgtype.UUID,
	payload spawnPayload,
) error {
	execCtx, execCancel := context.WithTimeout(context.WithoutCancel(ctx), deps.SubprocessTimeout)
	defer execCancel()

	cmd, err := BuildCommand(execCtx, deps.FakeAgentCmd, payload.TicketID, payload.DepartmentID)
	if err != nil {
		return writeTerminal(ctx, deps, instanceID, eventID, "failed", "build_command_error")
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return writeTerminal(ctx, deps, instanceID, eventID, "failed", "stdout_pipe_error")
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return writeTerminal(ctx, deps, instanceID, eventID, "failed", "stderr_pipe_error")
	}
	if err := cmd.Start(); err != nil {
		return writeTerminal(ctx, deps, instanceID, eventID, "failed", "start_error")
	}

	logger := deps.Logger.With(
		"event_id", formatUUID(eventID),
		"ticket_id", payload.TicketID,
		"department_id", payload.DepartmentID,
		"pid", cmd.Process.Pid,
	)

	var wg sync.WaitGroup
	wg.Add(2)
	go scanStream(&wg, stdout, logger, "stdout")
	go scanStream(&wg, stderr, logger, "stderr")

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	var (
		exitErr           error
		shutdownCtxErr    error
		shutdownSigkilled bool
	)
	select {
	case err := <-waitDone:
		exitErr = err
	case <-ctx.Done():
		shutdownCtxErr = ctx.Err()
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-waitDone:
			exitErr = err
		case <-time.After(ShutdownSignalGrace):
			_ = cmd.Process.Kill()
			shutdownSigkilled = true
			exitErr = <-waitDone
		}
	}
	wg.Wait()
	_ = exitErr

	var ctxErr error
	switch {
	case shutdownCtxErr != nil:
		ctxErr = context.Canceled
	case errors.Is(execCtx.Err(), context.DeadlineExceeded):
		ctxErr = context.DeadlineExceeded
	}

	exitCode, sigName := extractExit(cmd.ProcessState)
	cls := Classify(exitCode, sigName, ctxErr, shutdownSigkilled)

	if shutdownSigkilled && deps.SigkillEscalations != nil {
		deps.SigkillEscalations.Add(1)
	}

	termCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), TerminalWriteGrace)
	defer cancel()
	return writeTerminal(termCtx, deps, instanceID, eventID, cls.Status, cls.ExitReason)
}

// -----------------------------------------------------------------------
// Real-Claude path (M2.1 — agents cache + MCP config + NDJSON pipeline)
// -----------------------------------------------------------------------

// runRealClaude implements steps 3–12 of plan §internal/spawn for the real
// Claude Code subprocess. The 12-step sequence is preserved in the code
// layout below; each block is annotated with its plan step.
func runRealClaude(
	ctx context.Context,
	deps Deps,
	inv realClaudeInvocation,
) error {
	instanceID := inv.InstanceID
	eventID := inv.EventID
	ticketUUID := inv.TicketUUID
	dept := inv.Dept
	payload := inv.Payload
	roleSlug := inv.RoleSlug

	logger := deps.Logger.With(
		"event_id", formatUUID(eventID),
		"instance_id", formatUUID(instanceID),
		"ticket_id", payload.TicketID,
		"department_id", payload.DepartmentID,
		"role_slug", roleSlug,
	)
	instanceIDText := formatUUID(instanceID)
	ticketIDText := payload.TicketID

	// M2.2 terminal-write helper closure: always captures wake_up_status
	// (possibly "" for pre-wake-up bailouts) + fromColumn/toColumn pair.
	writeFail := func(exitReason string) error {
		return writeTerminalCostAndWakeup(ctx, deps, terminalWriteParams{
			InstanceID: instanceID,
			EventID:    eventID,
			TicketID:   ticketUUID,
			Status:     "failed",
			ExitReason: exitReason,
		})
	}

	// Step 3: resolve agent row from the startup cache. Role-slug
	// parameterized per T013 — no longer hardcoded to "engineer".
	if deps.AgentsCache == nil {
		logger.Error("agents cache not wired; cannot resolve agent", "role_slug", roleSlug)
		return writeFail(ExitAgentMissing)
	}
	agent, err := deps.AgentsCache.GetForDepartmentAndRole(ctx, dept.ID, roleSlug)
	if err != nil {
		logger.Error("no agent for department+role", "role_slug", roleSlug, "err", err)
		return writeFail(ExitAgentMissing)
	}

	// M7 FR-303 / FR-304 / FR-305: every spawn records the immutable
	// preamble's hash + the cwd CLAUDE.md hash + the per-agent container
	// image digest so a forensic walk can reconstruct exactly which
	// preamble version + skill content + image bytes were active for any
	// historical run. preamble_hash always populates (the const is
	// embedded in the binary); claude_md_hash is NULL when no CLAUDE.md
	// exists in the workspace; image_digest is "" until grandfathering
	// (T014) flips the agent into a per-agent container.
	if errH := recordM7HashesForInstance(ctx, deps, instanceID, dept, agent); errH != nil {
		logger.Warn("M7 hash record best-effort failed", "err", errH)
	}

	// --- M2.3 vault steps V1–V5 (D4.5 / FR-409 / FR-405 / FR-407 / FR-412) ---
	// Rule 3 pre-check: run BEFORE the vault fetch so no Infisical request is
	// made when the agent's mcp_config carries a banned server name (T013 /
	// D4.5 ordering). mcpconfig.Write also checks Rule 3 inside WriteWithOps
	// against the full composed config as a defence-in-depth guard.
	if err := mcpconfig.CheckExtraServers(agent.McpConfig); err != nil {
		if errors.Is(err, mcpconfig.ErrVaultMCPBanned) {
			logger.Error("Rule 3 pre-check: vault-pattern server in agents.mcp_config", "err", err)
			return writeFail(ExitVaultMCPInConfig)
		}
		logger.Error("Rule 3 pre-check: parse agents.mcp_config failed", "err", err)
		return writeFail(ExitSpawnFailed)
	}
	fetched, vaultExit := vaultOrchestrate(ctx, deps, roleSlug, instanceID, ticketUUID, agent.AgentMD, logger)
	if vaultExit != "" {
		return writeFail(vaultExit)
	}
	// Ensure all fetched secrets are zeroed on any exit path from this point.
	defer func() {
		for k := range fetched {
			sv := fetched[k]
			sv.Zero()
		}
	}()
	// --- end M2.3 vault steps ---

	// Step 3a: wake-up context capture. Non-blocking on failure per FR-207b.
	var wakeUpStdout string
	wakeUpStatus := mempalace.StatusSkipped // M2.2 never writes 'skipped' in
	// practice, but this sentinel tracks "no wake-up attempted" distinctly
	// from StatusOK ("tried, got empty output"). It only surfaces to the DB
	// if the agent.PalaceWing is nil (no wing configured → skip wake-up).
	if agent.PalaceWing != nil && *agent.PalaceWing != "" {
		wakeUpCtx, wakeUpCancel := context.WithTimeout(ctx, 2*time.Second)
		stdout, status, elapsed, werr := mempalace.Wakeup(wakeUpCtx, mempalace.WakeupConfig{
			DockerBin:          deps.DockerBin,
			MempalaceContainer: deps.MempalaceContainer,
			PalacePath:         deps.PalacePath,
			Timeout:            2 * time.Second,
			Logger:             logger,
		}, *agent.PalaceWing)
		wakeUpCancel()
		_ = werr // non-blocking: Wakeup returns nil err on every failure mode
		wakeUpStdout = stdout
		wakeUpStatus = status
		logger.Info("wake_up_complete",
			"palace_wing", *agent.PalaceWing,
			"wake_up_status", string(status),
			"elapsed_ms", elapsed.Milliseconds(),
		)
	}

	// M7.1 T011: the transport dispatch happens here — after the shared
	// steps 1–3 (prepare, hashes, wake-up, vault fetch, Rule-3
	// pre-check) and before the host-side MCP config write: the
	// container path renders the config itself, writes it inside the
	// container, and builds argv with the in-container path. The legacy
	// direct-exec path below stays load-bearing through the soak window
	// (GARRISON_USE_DIRECT_EXEC=true is the rollback lever, FR-018).
	if !deps.UseDirectExec && deps.AgentContainer != nil {
		return runRealClaudeViaContainer(ctx, deps, containerRunInputs{
			InstanceID:   instanceID,
			EventID:      eventID,
			TicketUUID:   ticketUUID,
			Dept:         dept,
			Payload:      payload,
			RoleSlug:     roleSlug,
			Agent:        agent,
			Fetched:      fetched,
			WakeUpStdout: wakeUpStdout,
			WakeUpStatus: wakeUpStatus,
			Logger:       logger,
		})
	}

	// Step 4: write per-invocation MCP config. Disk errors here land in
	// the spawn_failed terminal (clarify-session Q2) — dispatcher continues
	// onto the next event.
	// M2.2.1 T011: populate FinalizeParams with the just-INSERTed
	// agent_instance_id. The mcpconfig writer emits the `finalize` MCP
	// entry with GARRISON_AGENT_INSTANCE_ID + GARRISON_DATABASE_URL env
	// vars so the subprocess server scopes its already-committed check
	// to this specific spawn per FR-260.
	mcpPath, err := mcpconfig.Write(ctx, mcpconfig.WriteParams{
		Dir:           deps.MCPConfigDir,
		InstanceID:    instanceID,
		SupervisorBin: deps.SupervisorBin,
		DSN:           deps.AgentRODSN,
		Mempalace: mcpconfig.MempalaceParams{
			DockerBin:          deps.DockerBin,
			MempalaceContainer: deps.MempalaceContainer,
			PalacePath:         deps.PalacePath,
			DockerHost:         deps.DockerHost,
		},
		Finalize: mcpconfig.FinalizeParams{
			SupervisorBin:   deps.SupervisorBin,
			AgentInstanceID: formatUUID(instanceID),
			DatabaseURL:     deps.AgentRODSN,
		},
		// M8 FR-005: agent-callable create_ticket via the in-tree
		// garrison-mutate server in agent mode (audit anchors on
		// agent_instance_id per FR-401; verb surface restricted in
		// garrisonmutate.listToolsFor/dispatch).
		GarrisonMutate: mcpconfig.GarrisonMutateParams{
			SupervisorBin:   deps.SupervisorBin,
			AgentInstanceID: formatUUID(instanceID),
			DatabaseURL:     deps.DatabaseURL,
		},
		ExtraServersJSON: agent.McpConfig, // M2.3 T013: agent-specific servers; Rule 3 checks these
	})
	if err != nil {
		if errors.Is(err, mcpconfig.ErrVaultMCPBanned) {
			logger.Error("mcpconfig: Rule 3 violation — vault-pattern MCP server", "err", err)
			return writeFail(ExitVaultMCPInConfig)
		}
		logger.Error("mcpconfig.Write failed; recording spawn_failed", "err", err)
		return writeFail(ExitSpawnFailed)
	}
	// Remove the MCP config file on every exit path.
	defer func() {
		if rmErr := mcpconfig.Remove(mcpPath); rmErr != nil {
			logger.Warn("mcpconfig.Remove failed; continuing", "path", mcpPath, "err", rmErr)
		}
	}()

	// Step 5 + 6: build argv + configure exec.Cmd.
	execCtx, execCancel := context.WithTimeout(context.WithoutCancel(ctx), deps.SubprocessTimeout)
	defer execCancel()

	argvIn := claudeArgvInputs(deps, agent, roleSlug, ticketIDText, instanceIDText, wakeUpStdout)
	argvIn.MCPConfigPath = mcpPath
	argv := buildClaudeArgv(argvIn)

	cmd := exec.CommandContext(execCtx, deps.ClaudeBin, argv...)
	if dept.WorkspacePath != nil && *dept.WorkspacePath != "" {
		// The per-department workspace only exists if something created
		// it: the Dockerfile bakes the base dir, not per-dept subdirs,
		// and container recreation wipes runtime-created ones. A missing
		// cmd.Dir makes cmd.Start fail with a misleading
		// "fork/exec <claude>: no such file or directory" (the ENOENT is
		// for the dir, not the binary). Ensure it exists per spawn;
		// hired departments (M7) get their workspace on first dispatch.
		if err := os.MkdirAll(*dept.WorkspacePath, 0o755); err != nil {
			logger.Error("workspace MkdirAll failed; recording spawn_failed",
				"workspace_path", *dept.WorkspacePath, "err", err)
			return writeFail(ExitSpawnFailed)
		}
		cmd.Dir = *dept.WorkspacePath
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// exec.CommandContext's default Cancel kills the PID only. Override
	// so the timeout path signals the whole process group.
	cmd.Cancel = func() error { return killProcessGroup(cmd, syscall.SIGTERM) }
	// WaitDelay escalates to a second Cancel (which we steer to SIGKILL
	// via a latch) if the process is still alive after 5 seconds.
	cmd.WaitDelay = ShutdownSignalGrace
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		logger.Error("open /dev/null failed", "err", err)
		return writeFail(ExitSpawnFailed)
	}
	defer stdin.Close()
	cmd.Stdin = stdin
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return writeFail(ExitSpawnFailed)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return writeFail(ExitSpawnFailed)
	}

	// V6: inject secrets as env vars (FR-416). Secrets are appended to the
	// subprocess environment; they are never logged or placed in argv.
	// Zero() is called in the deferred cleanup above (after cmd.Start copies
	// the values into the OS process).
	if len(fetched) > 0 {
		cmd.Env = appendSecretEnv(os.Environ(), fetched)
	}

	// Step 7: cmd.Start.
	if err := cmd.Start(); err != nil {
		logger.Error("claude cmd.Start failed; recording spawn_failed", "err", err)
		return writeFail(ExitSpawnFailed)
	}

	logger = logger.With("pid", cmd.Process.Pid, "session_prep_model", argvIn.Model)
	logger.Info("claude subprocess started")

	// Step 8: backfill pid (M1 retro §4 fix).
	if err := deps.Queries.UpdatePID(ctx, store.UpdatePIDParams{
		ID:  instanceID,
		Pid: int32Ptr(int32(cmd.Process.Pid)),
	}); err != nil {
		logger.Warn("UpdatePID failed; continuing without backfill", "err", err)
	}

	// Bail hook: the pipeline calls this on MCP-health failure or parse
	// error. The handler signals the whole group and latches a flag so
	// the wait-loop can escalate to SIGKILL after the grace.
	var bailed atomic.Bool
	onBail := func(_ string) {
		if !bailed.CompareAndSwap(false, true) {
			return
		}
		if err := killProcessGroup(cmd, syscall.SIGTERM); err != nil {
			logger.Warn("killProcessGroup on bail returned error", "err", err)
		}
	}

	// Steps 9–12 live in runClaudeSession (runner.go) since M7.1 T009:
	// the NDJSON pipeline + stderr mirror + shutdown sequencing +
	// adjudication + terminal write are transport-independent. The
	// direct-exec transport below wraps the process-group kill ladder
	// and the drain-then-cmd.Wait + extractExit reap exactly as the
	// pre-T009 inline block did.
	directTransport := transport{
		Stdout: stdout,
		Stderr: stderr,
		Terminate: func(escalate bool) error {
			if escalate {
				return killProcessGroup(cmd, syscall.SIGKILL)
			}
			return killProcessGroup(cmd, syscall.SIGTERM)
		},
		ExitDetail: func(context.Context) WaitDetail {
			// cmd.Wait closes stdout/stderr pipes, but the runner only
			// calls ExitDetail after both are drained (concurrency rule
			// 8), so no data is lost; it just returns the ProcessState.
			_ = cmd.Wait()
			exitCode, sigName := extractExit(cmd.ProcessState)
			wait := WaitDetail{ExitCode: exitCode}
			if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
				wait.ContextErr = context.DeadlineExceeded
			}
			if sigName != "" {
				wait.Signaled = true
				wait.Signal = signalFromName(sigName)
			}
			return wait
		},
	}
	workspacePath := ""
	if dept.WorkspacePath != nil {
		workspacePath = *dept.WorkspacePath
	}
	fromCol, toCol := transitionColumns(roleSlug, payload.ColumnSlug)
	return runClaudeSession(ctx, execCtx, deps, directTransport, sessionParams{
		InstanceID:       instanceID,
		EventID:          eventID,
		TicketUUID:       ticketUUID,
		TicketIDText:     payload.TicketID,
		RoleSlug:         roleSlug,
		OriginColumn:     payload.ColumnSlug,
		Agent:            agent,
		Dept:             dept,
		WakeUpStatus:     wakeUpStatus,
		FinalizeExpected: finalizeExpectedForRole(roleSlug, payload.ColumnSlug),
		FromCol:          fromCol,
		ToCol:            toCol,
		WorkspacePath:    workspacePath,
		Bailed:           &bailed,
		OnBail:           onBail,
		Logger:           logger,
	})
}

// argvParams carries the per-invocation inputs buildClaudeArgv renders
// into the claude CLI flag sequence. Both transports use it; only
// MCPConfigPath differs between direct-exec (host path under
// deps.MCPConfigDir) and the container path (in-container /tmp path,
// T011).
type argvParams struct {
	TaskDescription string
	Model           string
	BudgetUSD       float64
	MCPConfigPath   string
	SystemPrompt    string
}

// claudeArgvInputs derives the transport-independent argv inputs: the
// model fallback, the M2.2 task description (names both ticket_id and
// instance_id so the agent has them without querying; the full
// instance_id also appears in the system-prompt "This turn" block via
// mempalace.ComposeSystemPrompt — M2.2 Session 2026-04-23 Q2), the
// NFR-201 budget default, and the composed system prompt. Both
// transports call it so the container path provably runs the same
// invocation contract (FR-013); MCPConfigPath is left empty for the
// caller to fill (host path vs in-container /tmp path).
func claudeArgvInputs(deps Deps, agent agents.Agent, roleSlug, ticketIDText, instanceIDText, wakeUpStdout string) argvParams {
	model := agent.Model
	if model == "" {
		model = deps.ClaudeModel
	}
	budget := deps.ClaudeBudgetUSD
	if budget <= 0 {
		budget = 0.10 // M2.2 default per NFR-201
	}
	return argvParams{
		TaskDescription: fmt.Sprintf(
			"You are the %s on ticket %s (agent_instance %s). Read it, then execute your completion protocol from the system prompt.",
			roleSlug, ticketIDText, instanceIDText,
		),
		Model:        model,
		BudgetUSD:    budget,
		SystemPrompt: mempalace.ComposeSystemPrompt(agent.AgentMD, wakeUpStdout, ticketIDText, instanceIDText),
	}
}

// buildClaudeArgv renders the legacy direct-exec claude argv (M2.1 → M7
// shape, byte-for-byte). TestBuildClaudeArgvGoldenLegacy pins the exact
// flag sequence so the container path (T011) provably runs the same
// invocation contract (FR-013).
func buildClaudeArgv(p argvParams) []string {
	return []string{
		"-p", p.TaskDescription,
		"--output-format", "stream-json",
		"--verbose",
		"--no-session-persistence",
		"--model", p.Model,
		"--max-budget-usd", strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.6f", p.BudgetUSD), "0"), "."),
		"--mcp-config", p.MCPConfigPath,
		"--strict-mcp-config",
		"--system-prompt", p.SystemPrompt,
		"--permission-mode", "bypassPermissions",
	}
}

// appendSecretEnv appends NAME=value pairs for every fetched vault secret
// to env and returns it. This is the single sanctioned production
// UnsafeBytes env-injection call site (AGENTS.md vaultlog rule: spawn env
// injection + Rule 1 leak scan are the only two); the container path
// (T011) reuses this helper rather than adding a third site. Values are
// never logged or placed in argv; the caller owns Zero()ing the fetched
// map after the env is handed to the subprocess/exec.
func appendSecretEnv(env []string, fetched map[string]vault.SecretValue) []string {
	for envVar, sv := range fetched {
		//nolint:vaultlog // UnsafeBytes is the only safe path for env-var injection
		env = append(env, envVar+"="+string(sv.UnsafeBytes()))
	}
	return env
}

// acceptanceGateSatisfied returns true for (role, origin-column) pairs
// where the supervisor should skip the M1 hello.txt check. M2.2's
// engineer@in_dev and qa-engineer@qa_review trust the terminal result
// event + mempalace writes (the hygiene checker's concern). M2.1's
// engineer@todo still exercises the hello.txt gate because the M2.1
// integration suite asserts that codepath directly
// (TestM21AcceptanceFailedWhenHelloTxt*). Unknown roles / blank column
// default to false so the M1 safety net stays in place.
func acceptanceGateSatisfied(roleSlug, fromColumn string) bool {
	switch roleSlug {
	case roleQAEngineer:
		return true
	default:
		// The in_dev column implies the M2.2 finalize workflow for ANY
		// role — engineer or M7 hire alike (a seo-writer run was
		// misclassified acceptance_failed by the M1 hello.txt gate in
		// the 2026-06-10 acceptance run). M2.1 engineer@todo and any
		// call without column info fall through to the M1 hello.txt
		// check, preserving the TestM21AcceptanceFailed* contract.
		return fromColumn == "in_dev"
	}
}

// finalizeExpectedForRole returns true for the (role, origin-column)
// combinations where M2.2.1's finalize_ticket flow is expected. The
// engineer role is finalize-expected only on the M2.2 in_dev column
// (the M2.1 todo path predates finalize and uses the M1 acceptance
// gate instead). qa-engineer is always finalize-expected. Any other
// role or column combination leaves FinalizeState.Expected=false so
// the pipeline's finalize observer + Adjudicate's finalize branches
// all short-circuit per plan §"Decisions baked into this plan" item 7.
func finalizeExpectedForRole(roleSlug, fromColumn string) bool {
	switch roleSlug {
	case roleQAEngineer:
		return true
	default:
		// Same generalization as acceptanceGateSatisfied: in_dev means
		// the M2.2 finalize workflow regardless of role, so M7 hires
		// get the finalize observer + Adjudicate branches. todo stays
		// M2.1/fake-agent territory (finalize unexpected).
		return fromColumn == "in_dev"
	}
}

// transitionColumns maps role_slug + origin column to the (from, to)
// pair the supervisor inserts into ticket_transitions on a succeeded
// run. fromColumn is the ticket's column at spawn time (carried on the
// event payload's "column_slug"). The engineer role is polymorphic:
// on M2.1's todo column it lands at done (single-transition workflow);
// on M2.2's in_dev column it lands at qa_review (two-transition
// workflow with qa-engineer picking up the qa_review → done leg).
// Unknown roles / blank fromColumn fall back to the M2.1 default so
// the fake-agent path and any future role the migration adds remain
// write-safe.
func transitionColumns(roleSlug, fromColumn string) (from, to string) {
	switch roleSlug {
	case roleQAEngineer:
		return "qa_review", "done"
	default:
		// Any role working the in_dev column lands at qa_review — for
		// M7-hired single-role departments that column is the
		// operator's HITL review parking spot (drag to done). todo
		// keeps the M2.1 single-transition fallback for the fake-agent
		// path and legacy tests.
		if fromColumn == "in_dev" {
			return "in_dev", "qa_review"
		}
		return "todo", "done"
	}
}

// checkHelloTxt reads workspace/hello.txt and returns true iff the contents,
// stripped of at most one trailing newline, equal ticketID exactly. Any
// read error (file missing, permission denied, etc.) yields false — the
// acceptance check is fail-closed.
func checkHelloTxt(workspacePath, ticketID string) bool {
	b, err := os.ReadFile(filepath.Join(workspacePath, "hello.txt"))
	if err != nil {
		return false
	}
	got := strings.TrimRight(string(b), "\n")
	return got == ticketID
}

// parseCostToNumeric converts Claude's json.Number-preserved decimal string
// into pgtype.Numeric via the pgx Scan path. Returns the zero Numeric and
// the error on failure so the caller can decide whether to surface or
// swallow it. An empty cost string yields the zero Numeric with nil error.
func parseCostToNumeric(cost string) (pgtype.Numeric, error) {
	if cost == "" {
		return pgtype.Numeric{}, nil
	}
	var n pgtype.Numeric
	if err := n.Scan(cost); err != nil {
		return pgtype.Numeric{}, err
	}
	return n, nil
}

// signalFromName maps the canonical SIG* string that signalName() emits
// back to syscall.Signal so FormatSignalled can render the exit_reason.
// Unknown strings return 0 and FormatSignalled falls through to its
// numeric shim ("signaled_signal_N").
func signalFromName(s string) syscall.Signal {
	switch s {
	case "SIGKILL":
		return syscall.SIGKILL
	case "SIGTERM":
		return syscall.SIGTERM
	case "SIGINT":
		return syscall.SIGINT
	case "SIGHUP":
		return syscall.SIGHUP
	case "SIGQUIT":
		return syscall.SIGQUIT
	case "SIGSEGV":
		return syscall.SIGSEGV
	case "SIGABRT":
		return syscall.SIGABRT
	case "SIGPIPE":
		return syscall.SIGPIPE
	default:
		return 0
	}
}

func int32Ptr(n int32) *int32 { return &n }

// -----------------------------------------------------------------------
// Terminal writers
// -----------------------------------------------------------------------

// Classification is the (status, exit_reason) pair written to agent_instances
// by the M1 fake-agent path. The M2.1 real path uses pipeline.Adjudicate
// instead; both paths share the underlying column vocabulary.
type Classification struct {
	Status     string
	ExitReason string
}

// Classify is the pure subprocess-exit → (status, exit_reason) function used
// by the M1 fake-agent path. Inputs intentionally avoid *os.ProcessState so
// unit tests exercise the mapping without fork/exec.
func Classify(exitCode int, sigName string, ctxErr error, shutdownSigkilled bool) Classification {
	switch {
	case errors.Is(ctxErr, context.DeadlineExceeded):
		return Classification{Status: "timeout", ExitReason: "timeout"}
	case errors.Is(ctxErr, context.Canceled):
		if shutdownSigkilled {
			return Classification{Status: "failed", ExitReason: "supervisor_shutdown_sigkill"}
		}
		return Classification{Status: "failed", ExitReason: "supervisor_shutdown"}
	case sigName != "":
		return Classification{Status: "failed", ExitReason: "signal_" + sigName}
	case exitCode == 0:
		return Classification{Status: "succeeded", ExitReason: "exit_code_0"}
	default:
		return Classification{Status: "failed", ExitReason: fmt.Sprintf("exit_code_%d", exitCode)}
	}
}

// writeTerminal is the M1 two-statement terminal tx (UpdateInstanceTerminal
// + MarkEventProcessed). Preserved verbatim so the fake-agent path stays
// byte-identical with M1.
func writeTerminal(ctx context.Context, deps Deps, instanceID, eventID pgtype.UUID, status, exitReason string) error {
	tx, err := deps.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf(errBeginTerminalTx, err)
	}
	q := deps.Queries.WithTx(tx)
	reason := exitReason
	if err := q.UpdateInstanceTerminal(ctx, store.UpdateInstanceTerminalParams{
		ID:         instanceID,
		Status:     status,
		ExitReason: &reason,
	}); err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("spawn: UpdateInstanceTerminal: %w", err)
	}
	if err := q.MarkEventProcessed(ctx, eventID); err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf(errMarkEventProcd, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf(errCommitTerminal, err)
	}
	return nil
}

// writeTerminalCostAndWakeup is the M2.2 widened terminal tx. Writes
// wake_up_status alongside the M2.1 terminal (status, exit_reason, cost),
// supports role-slug-configurable transition column pairs, and commits
// everything in one tx with MarkEventProcessed.
//
// p.WakeUpStatus is a typed string ("ok" / "failed" / "skipped"); empty
// string writes NULL to the column (pre-wake-up bailout paths).
//
// p.FromCol / p.ToCol: empty strings skip the transition writes entirely.
// Non-empty on the succeeded path insert a ticket_transitions row and
// update tickets.column_slug to p.ToCol.
func writeTerminalCostAndWakeup(ctx context.Context, deps Deps, p terminalWriteParams) error {
	// M7.1 T011: container-transport remap hook (plan D21). Runs before
	// the test seam so both observe the final (status, exit_reason).
	if deps.terminalReasonOverride != nil {
		p.Status, p.ExitReason = deps.terminalReasonOverride(p.Status, p.ExitReason)
	}
	if deps.terminalWriteFn != nil {
		return deps.terminalWriteFn(ctx, p)
	}
	tx, err := deps.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf(errBeginTerminalTx, err)
	}
	q := deps.Queries.WithTx(tx)
	reason := p.ExitReason
	var wakeUpPtr *string
	if p.WakeUpStatus != "" {
		wakeUpPtr = &p.WakeUpStatus
	}
	if err := q.UpdateInstanceTerminalWithCostAndWakeup(ctx, store.UpdateInstanceTerminalWithCostAndWakeupParams{
		ID:           p.InstanceID,
		Status:       p.Status,
		ExitReason:   &reason,
		TotalCostUsd: p.Cost,
		WakeUpStatus: wakeUpPtr,
	}); err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("spawn: UpdateInstanceTerminalWithCostAndWakeup: %w", err)
	}
	if p.InsertTransition && p.TicketID.Valid && p.FromCol != "" && p.ToCol != "" {
		from := p.FromCol
		if _, err := q.InsertTicketTransition(ctx, store.InsertTicketTransitionParams{
			TicketID:                   p.TicketID,
			FromColumn:                 &from,
			ToColumn:                   p.ToCol,
			TriggeredByAgentInstanceID: p.InstanceID,
		}); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("spawn: InsertTicketTransition: %w", err)
		}
		if err := q.UpdateTicketColumnSlug(ctx, store.UpdateTicketColumnSlugParams{
			ID:         p.TicketID,
			ColumnSlug: p.ToCol,
		}); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("spawn: UpdateTicketColumnSlug: %w", err)
		}
	}
	if !p.SkipMarkProcessed {
		if err := q.MarkEventProcessed(ctx, p.EventID); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf(errMarkEventProcd, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf(errCommitTerminal, err)
	}
	return nil
}

// -----------------------------------------------------------------------
// Shared helpers (unchanged from M1 except for location)
// -----------------------------------------------------------------------

func scanStream(wg *sync.WaitGroup, r io.ReadCloser, logger *slog.Logger, stream string) {
	defer wg.Done()
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		logger.Info(scanner.Text(), "stream", stream)
	}
}

func extractExit(ps *os.ProcessState) (int, string) {
	if ps == nil {
		return -1, ""
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return -1, signalName(ws.Signal())
	}
	return ps.ExitCode(), ""
}

func signalName(sig syscall.Signal) string {
	switch sig {
	case syscall.SIGKILL:
		return "SIGKILL"
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGHUP:
		return "SIGHUP"
	case syscall.SIGQUIT:
		return "SIGQUIT"
	case syscall.SIGSEGV:
		return "SIGSEGV"
	case syscall.SIGABRT:
		return "SIGABRT"
	case syscall.SIGPIPE:
		return "SIGPIPE"
	default:
		return fmt.Sprintf("signal_%d", int(sig))
	}
}

func parseUUID(s string) (pgtype.UUID, error) {
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		return u, err
	}
	return u, nil
}

func formatUUID(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x",
		u.Bytes[0:4],
		u.Bytes[4:6],
		u.Bytes[6:8],
		u.Bytes[8:10],
		u.Bytes[10:16],
	)
}

// vaultAuditErrParams groups the per-fetch error context for auditVaultError,
// keeping the function signature within the S107 parameter-count limit.
type vaultAuditErrParams struct {
	instanceID pgtype.UUID
	ticketID   pgtype.UUID
	secretPath string
	customerID pgtype.UUID
	outcome    vault.Outcome
}

// vaultOrchestrate executes vault steps V1 (list grants), V3 (fetch secrets),
// V4 (Rule 1 leak scan), and V5 (audit). Extracted so unit tests can exercise
// the vault logic without a real subprocess or DB connection.
//
// Returns (fetched, exitReason). exitReason=="" means "continue"; the caller
// owns the returned map and must defer Zero() on every value. On any non-empty
// exitReason the returned map is nil (secrets were zeroed internally).
func vaultOrchestrate(
	ctx context.Context,
	deps Deps,
	roleSlug string,
	instanceID, ticketID pgtype.UUID,
	agentMD string,
	logger *slog.Logger,
) (map[string]vault.SecretValue, string) {
	if deps.Vault == nil {
		return nil, ""
	}

	// V1: list grants (Rule 2). Zero rows → skip fetch entirely (FR-409).
	grants, exitReason := listGrantsForRole(ctx, deps, roleSlug, logger)
	if exitReason != "" {
		return nil, exitReason
	}
	if len(grants) == 0 {
		return nil, ""
	}

	// V3: fetch secrets.
	fetched, ferr := deps.Vault.Fetch(ctx, grants)
	if ferr != nil {
		errOutcome := vault.OutcomeErrorFetching
		if errors.Is(ferr, vault.ErrVaultPermissionDenied) {
			errOutcome = vault.OutcomeDeniedInfisical
		}
		auditVaultError(ctx, deps.Pool, vaultAuditErrParams{
			instanceID: instanceID,
			ticketID:   ticketID,
			secretPath: grants[0].SecretPath,
			customerID: deps.CustomerID,
			outcome:    errOutcome,
		}, logger)
		return nil, vault.ClassifyExitReason(ferr)
	}

	// V4: Rule 1 leak scan — reject if literal secret value found in agent.md.
	if len(fetched) > 0 {
		if leaked := vault.RuleOneLeakScan(agentMD, fetched); len(leaked) > 0 {
			zeroFetched(fetched)
			logger.Error("Rule 1: secret value leaked in agent.md; aborting spawn",
				"leaked_env_vars", leaked,
			)
			return nil, ExitSecretLeakedInAgentMd
		}
	}

	// V5: audit log write (fail-closed per Q9).
	row := vault.AuditRow{
		AgentInstanceID: instanceID,
		TicketID:        ticketID,
		SecretPath:      grants[0].SecretPath,
		CustomerID:      deps.CustomerID,
		Outcome:         vault.OutcomeGranted,
		Timestamp:       time.Now().UTC(),
	}
	if exitReason = writeAuditGranted(ctx, deps, row, fetched, logger); exitReason != "" {
		return nil, exitReason
	}

	return fetched, ""
}

// listGrantsForRole runs V1: lists vault grants for the role, using the
// test override if available. Returns (grants, exitReason); non-empty
// exitReason means the caller must abort.
func listGrantsForRole(ctx context.Context, deps Deps, roleSlug string, logger *slog.Logger) ([]vault.GrantRow, string) {
	if deps.GrantsListerFn != nil {
		grants, err := deps.GrantsListerFn(ctx, roleSlug, deps.CustomerID)
		if err != nil {
			logger.Error("ListGrantsForRole failed", "err", err)
			return nil, ExitSpawnFailed
		}
		return grants, ""
	}
	grantRows, err := deps.Queries.ListGrantsForRole(ctx, store.ListGrantsForRoleParams{
		RoleSlug:   roleSlug,
		CustomerID: deps.CustomerID,
	})
	if err != nil {
		logger.Error("ListGrantsForRole failed", "err", err)
		return nil, ExitSpawnFailed
	}
	var grants []vault.GrantRow
	for _, r := range grantRows {
		grants = append(grants, vault.GrantRow{
			EnvVarName: r.EnvVarName,
			SecretPath: r.SecretPath,
			CustomerID: r.CustomerID,
		})
	}
	return grants, ""
}

// writeAuditGranted runs V5: writes the success audit row (fail-closed per Q9).
// Uses deps.AuditFn override if set (unit tests), otherwise uses the DB pool.
// Returns non-empty exitReason on failure; zeroes fetched before returning.
func writeAuditGranted(ctx context.Context, deps Deps, row vault.AuditRow, fetched map[string]vault.SecretValue, logger *slog.Logger) string {
	if deps.AuditFn != nil {
		if err := deps.AuditFn(ctx, row); err != nil {
			logger.Error("vault audit: AuditFn failed; fail-closed", "err", err)
			zeroFetched(fetched)
			return ExitVaultAuditFailed
		}
		return ""
	}
	tx, err := deps.Pool.Begin(ctx)
	if err != nil {
		logger.Error("vault audit: begin tx failed; fail-closed", "err", err)
		zeroFetched(fetched)
		return ExitVaultAuditFailed
	}
	if err := vault.WriteAuditRow(ctx, tx, row); err != nil {
		_ = tx.Rollback(ctx)
		logger.Error("vault audit: WriteAuditRow failed; fail-closed", "err", err)
		zeroFetched(fetched)
		return ExitVaultAuditFailed
	}
	if err := tx.Commit(ctx); err != nil {
		logger.Error("vault audit: commit failed; fail-closed", "err", err)
		zeroFetched(fetched)
		return ExitVaultAuditFailed
	}
	return ""
}

// zeroFetched zeroes every SecretValue in the map to prevent secrets from
// outliving the current call frame.
func zeroFetched(fetched map[string]vault.SecretValue) {
	for k := range fetched {
		sv := fetched[k]
		sv.Zero()
	}
}

// auditVaultError writes a best-effort vault_access_log row when Fetch fails.
// Non-blocking on pool or write errors; caller still fails the spawn.
func auditVaultError(ctx context.Context, pool *pgxpool.Pool, p vaultAuditErrParams, logger *slog.Logger) {
	if pool == nil {
		return
	}
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	tx, err := pool.Begin(auditCtx)
	if err != nil {
		logger.Warn("vault audit: begin tx failed during error audit", "err", err)
		return
	}
	if wErr := vault.WriteAuditRow(auditCtx, tx, vault.AuditRow{
		AgentInstanceID: p.instanceID,
		TicketID:        p.ticketID,
		SecretPath:      p.secretPath,
		CustomerID:      p.customerID,
		Outcome:         p.outcome,
		Timestamp:       time.Now().UTC(),
	}); wErr != nil {
		_ = tx.Rollback(auditCtx)
		logger.Warn("vault audit: WriteAuditRow failed during error audit", "err", wErr)
		return
	}
	if cErr := tx.Commit(auditCtx); cErr != nil {
		logger.Warn("vault audit: commit failed during error audit", "err", cErr)
	}
}
