package garrisonmutate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/throttle"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// ticketURLPrefix is the dashboard route prefix for ticket detail
// pages. Centralised so the chat-mutation chip's deep-link target
// stays consistent across all three ticket verbs (create / edit /
// transition) and so the M3 dashboard's tickets-router base can
// shift here in one place if it ever does.
const ticketURLPrefix = "/tickets/"

// resourceTypeTicket is the affected_resource_type CHECK enum value
// for ticket-domain verbs. Mirrored from the migration's CHECK
// constraint; centralised so the three ticket verbs can pass a
// pointer to this constant rather than allocating a fresh string
// per call.
const resourceTypeTicket = "ticket"

// CreateTicketArgs is the JSON-encoded input shape for create_ticket.
// Field names match the chat-side tool schema; tags drive both
// json.Unmarshal and the eventual MCP tools/list inputSchema.
type CreateTicketArgs struct {
	Objective          string         `json:"objective"`
	DepartmentSlug     string         `json:"department_slug"`
	AcceptanceCriteria string         `json:"acceptance_criteria,omitempty"`
	ColumnSlug         string         `json:"column_slug,omitempty"`
	Metadata           map[string]any `json:"metadata,omitempty"`
	// M6 T010: parent_ticket_id (UUID-shaped string) links this child
	// to a parent ticket. Validation: parent must exist, share the
	// child's department_id, and not be in column_slug='done'.
	ParentTicketID string `json:"parent_ticket_id,omitempty"`
	// M8 FR-101: cross-dept dependency. When set, the ticket cannot
	// spawn until the predecessor is in one of the predecessor's
	// department's dependency_satisfaction_columns (default
	// {"qa_review","done"}). Optional; null = no dependency.
	DependsOnTicketID string `json:"depends_on_ticket_id,omitempty"`
}

// dependencyCycleErr is the typed error_kind for graph cycles.
func dependencyCycleErr(msg string) Result {
	return Result{Success: false, ErrorKind: string(ErrDependencyCycle), Message: msg}
}

// dependencyChainTooDeepErr is the typed error_kind for depth-cap
// rejections.
func dependencyChainTooDeepErr(msg string) Result {
	return Result{Success: false, ErrorKind: string(ErrDependencyChainTooDeep), Message: msg}
}

// deptWeeklyBudgetExceededErr is the typed error_kind for runaway
// gate rejections.
func deptWeeklyBudgetExceededErr(msg string) Result {
	return Result{Success: false, ErrorKind: string(ErrDeptWeeklyBudgetExceeded), Message: msg}
}

// assertExactlyOneCallerAnchor is the wiring-invariant guard for the
// M8 agent-caller surface. The supervisor's MCP server constructs a
// per-spawn Deps for the agent's garrison-mutate connection, leaving
// ChatSessionID zero-valued and setting AgentInstanceID. The chat
// path does the opposite. Both anchors set is a supervisor wiring
// bug; the verb refuses to write the audit row in that shape.
//
// Both anchors NULL is legal for Server-Action verbs (register_mcp_-
// server's worker writes such rows); for create_ticket specifically
// at least one must be set. This helper is called only from
// create_ticket — other verbs may legitimately leave both NULL.
func assertExactlyOneCallerAnchor(deps Deps) error {
	if deps.ChatSessionID.Valid && deps.AgentInstanceID.Valid {
		return errors.New("create_ticket: both chat_session_id and agent_instance_id set; supervisor wiring bug")
	}
	if !deps.ChatSessionID.Valid && !deps.AgentInstanceID.Valid {
		return errors.New("create_ticket: neither chat_session_id nor agent_instance_id set; supervisor wiring bug")
	}
	return nil
}

// realCreateTicketHandler implements garrison-mutate.create_ticket.
// Tier 3 reversibility per chat-threat-model.md §5: full pre-state
// args captured in args_jsonb; ticket can be deleted but downstream
// effects survive.
//
// M8 added the agent-caller surface: when Deps.AgentInstanceID is
// set, the verb auto-inherits the agent's current ticket as parent
// (FR-006), tags cross-dept creates in args_jsonb (FR-007), runs the
// cycle/depth walker on depends_on_ticket_id (FR-103), and runs the
// per-department weekly ticket budget gate (FR-201). The audit row
// anchors on agent_instance_id rather than chat_session_id.
func realCreateTicketHandler(ctx context.Context, deps Deps, raw json.RawMessage) (Result, error) {
	if err := assertExactlyOneCallerAnchor(deps); err != nil {
		return validationFailure("create_ticket: " + err.Error()), nil
	}
	args, validationRes := parseCreateTicketArgs(raw)
	if validationRes != nil {
		return *validationRes, nil
	}

	tx, err := deps.Pool.Begin(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("create_ticket: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)

	prep, res, prepErr := prepCreateTicket(ctx, q, deps, &args)
	if prepErr != nil {
		return Result{}, prepErr
	}
	if res != nil {
		return *res, nil
	}

	ticket, err := q.InsertTicketM8(ctx, store.InsertTicketM8Params{
		DepartmentID:            prep.dept.ID,
		Objective:               args.Objective,
		AcceptanceCriteria:      stringPtrOrNil(args.AcceptanceCriteria),
		ColumnSlug:              prep.columnSlug,
		Metadata:                prep.metadataJSON,
		Origin:                  pickTicketOrigin(deps),
		CreatedViaChatSessionID: deps.ChatSessionID,
		ParentTicketID:          prep.parentTicketID,
		DependsOnTicketID:       prep.dependsOnTicketID,
	})
	if err != nil {
		return Result{}, fmt.Errorf("create_ticket: insert ticket: %w", err)
	}

	resourceID := uuidString(ticket.ID)
	rt := resourceTypeTicket
	if _, err := WriteAudit(ctx, q, AuditWriteParams{
		ChatSessionID:        deps.ChatSessionID,
		ChatMessageID:        deps.ChatMessageID,
		AgentInstanceID:      deps.AgentInstanceID,
		Verb:                 "create_ticket",
		Args:                 args,
		Outcome:              "success",
		ReversibilityClass:   3,
		AffectedResourceID:   &resourceID,
		AffectedResourceType: &rt,
	}); err != nil {
		return Result{}, fmt.Errorf("create_ticket: write audit: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Result{}, fmt.Errorf("create_ticket: commit: %w", err)
	}

	// Post-commit notify (Rule 6 backstop: IDs only, no chat content).
	emitNotifyBestEffort(deps, "ticket.created", chatNotifyPayload{
		ChatSessionID:        uuidString(deps.ChatSessionID),
		ChatMessageID:        uuidString(deps.ChatMessageID),
		Verb:                 "create_ticket",
		AffectedResourceID:   resourceID,
		AffectedResourceType: rt,
	})

	// FR-104: emit dependency_added when the new ticket carries a
	// non-NULL depends_on_ticket_id. Best-effort; the listener
	// fallback is the existing transition-listener seam.
	if prep.dependsOnTicketID.Valid {
		emitDependencyAddedNotify(deps, prep.dept.Slug, resourceID, uuidString(prep.dependsOnTicketID))
	}

	return Result{
		Success:             true,
		AffectedResourceID:  resourceID,
		AffectedResourceURL: ticketURLPrefix + resourceID,
		Message: fmt.Sprintf(
			"Created ticket %s in %s at column %q. The supervisor's spawn loop dispatches the appropriate agent automatically — do NOT call transition_ticket on this ticket as a follow-up unless you intend to override the agent's work.",
			resourceID, args.DepartmentSlug, prep.columnSlug,
		),
	}, nil
}

// createTicketPrep bundles every per-call value the handler resolves
// before the INSERT. Extracted to keep realCreateTicketHandler's
// cognitive complexity under Sonar S3776's threshold (15) by pulling
// the dept/parent/depends-on/gate/metadata chain out of the main
// function's branch tree.
type createTicketPrep struct {
	dept              store.Department
	parentTicketID    pgtype.UUID
	dependsOnTicketID pgtype.UUID
	metadataJSON      []byte
	columnSlug        string
}

// prepCreateTicket runs every pre-INSERT step that can yield a
// caller-facing Result or an internal error. Returns (zero, *Result,
// nil) on user-facing rejection (caller returns *res), (zero, nil,
// err) on transport-level failure, or (prep, nil, nil) on success.
func prepCreateTicket(ctx context.Context, q *store.Queries, deps Deps, args *CreateTicketArgs) (createTicketPrep, *Result, error) {
	dept, deptRes, err := lookupCreateTicketDeptFull(ctx, q, deps, *args)
	if err != nil {
		return createTicketPrep{}, nil, err
	}
	if deptRes != nil {
		return createTicketPrep{}, deptRes, nil
	}
	if res, err := applyAutoInheritParent(ctx, q, deps, args, dept.ID); err != nil {
		return createTicketPrep{}, nil, err
	} else if res != nil {
		return createTicketPrep{}, res, nil
	}
	parentTicketID, parentRes, err := resolveParentTicketID(ctx, q, args.ParentTicketID, dept.ID)
	if err != nil {
		return createTicketPrep{}, nil, err
	}
	if parentRes != nil {
		return createTicketPrep{}, parentRes, nil
	}
	dependsOnTicketID, depRes, err := resolveDependsOn(ctx, q, args.DependsOnTicketID)
	if err != nil {
		return createTicketPrep{}, nil, err
	}
	if depRes != nil {
		return createTicketPrep{}, depRes, nil
	}
	if res, err := runDeptWeeklyGate(ctx, q, deps, *args, dept); err != nil {
		return createTicketPrep{}, nil, err
	} else if res != nil {
		return createTicketPrep{}, res, nil
	}
	metadataJSON, metaRes := buildCreateTicketMetadata(*args)
	if metaRes != nil {
		return createTicketPrep{}, metaRes, nil
	}
	metadataJSON = tagCrossDeptCreate(ctx, q, deps, dept, metadataJSON)
	columnSlug := strings.TrimSpace(args.ColumnSlug)
	if columnSlug == "" {
		columnSlug = "todo"
	}
	return createTicketPrep{
		dept:              dept,
		parentTicketID:    parentTicketID,
		dependsOnTicketID: dependsOnTicketID,
		metadataJSON:      metadataJSON,
		columnSlug:        columnSlug,
	}, nil, nil
}

// lookupCreateTicketDeptFull is M8's replacement for
// lookupCreateTicketDept. Returns the full Department row so the dept-
// weekly gate can read company_id + weekly_ticket_budget without a
// second query.
func lookupCreateTicketDeptFull(ctx context.Context, q *store.Queries, deps Deps, args CreateTicketArgs) (store.Department, *Result, error) {
	dept, err := q.GetDepartmentBySlug(ctx, args.DepartmentSlug)
	if err == nil {
		return dept, nil, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		r := resourceNotFound("create_ticket: department %q not found", args.DepartmentSlug)
		return store.Department{}, &r, writeFailureAudit(ctx, deps, "create_ticket", args, ErrResourceNotFound, 3, "")
	}
	return store.Department{}, nil, fmt.Errorf("create_ticket: lookup department: %w", err)
}

// applyAutoInheritParent fills args.ParentTicketID from the agent's
// current spawn's ticket_id when the agent caller omits it (FR-006).
// No-op for chat callers. Skip auto-inherit when the inherited
// parent is in a different department from the target — that's a
// cross-dept create where the parent link would fail
// resolveParentTicketID's same-dept invariant.
//
// Returns a non-nil *Result if the lookup fails in a user-facing way
// (e.g. agent_instance_id orphaned).
func applyAutoInheritParent(ctx context.Context, q *store.Queries, deps Deps, args *CreateTicketArgs, targetDeptID pgtype.UUID) (*Result, error) {
	if !deps.AgentInstanceID.Valid {
		return nil, nil
	}
	if strings.TrimSpace(args.ParentTicketID) != "" {
		return nil, nil
	}
	parentID, err := q.GetAgentInstanceTicketID(ctx, deps.AgentInstanceID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("create_ticket: GetAgentInstanceTicketID: %w", err)
	}
	if !parentID.Valid {
		return nil, nil
	}
	parent, err := q.GetTicketByID(ctx, parentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("create_ticket: lookup auto-inherit parent: %w", err)
	}
	if parent.DepartmentID != targetDeptID {
		// Cross-dept create: skip auto-inherit (the cross_dept_create
		// tag still lands via tagCrossDeptCreate).
		return nil, nil
	}
	args.ParentTicketID = uuidString(parentID)
	return nil, nil
}

// resolveDependsOn parses args.DependsOnTicketID and runs the cycle +
// depth-cap walker (FR-103). Returns the parsed UUID, a non-nil
// *Result on user-facing rejection, or an err on transport failure.
func resolveDependsOn(ctx context.Context, q *store.Queries, raw string) (pgtype.UUID, *Result, error) {
	var zero pgtype.UUID
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return zero, nil, nil
	}
	var depID pgtype.UUID
	if err := depID.Scan(trimmed); err != nil {
		r := validationFailure("create_ticket: depends_on_ticket_id is not a valid UUID")
		return zero, &r, nil
	}
	const depthCap = 32
	chain, err := q.GetTicketDependencyChain(ctx, store.GetTicketDependencyChainParams{
		StartID:  depID,
		DepthCap: depthCap,
	})
	if err != nil {
		return zero, nil, fmt.Errorf("create_ticket: GetTicketDependencyChain: %w", err)
	}
	visited := map[[16]byte]bool{depID.Bytes: true}
	for _, row := range chain {
		if row.Depth >= depthCap {
			r := dependencyChainTooDeepErr(fmt.Sprintf("create_ticket: dependency chain exceeds %d hops (cap)", depthCap))
			return zero, &r, nil
		}
		if visited[row.ChainID.Bytes] {
			r := dependencyCycleErr("create_ticket: dependency chain forms a cycle")
			return zero, &r, nil
		}
		visited[row.ChainID.Bytes] = true
	}
	return depID, nil, nil
}

// runDeptWeeklyGate consults throttle.CheckDeptWeekly against the
// target department. On allowed: returns (nil, nil) and the caller
// proceeds with the main tx. On rejected: rolls back the caller's
// main tx and writes the throttle_events row + agent-anchored audit
// row in a separate tx, then returns the typed Result. The separate
// tx is necessary because the caller's defer rolls back its tx on
// early return, which would erase the bookkeeping rows.
func runDeptWeeklyGate(ctx context.Context, q *store.Queries, deps Deps, args CreateTicketArgs, dept store.Department) (*Result, error) {
	decision, err := throttle.CheckDeptWeekly(ctx, q, dept.ID)
	if err != nil {
		return nil, fmt.Errorf("create_ticket: CheckDeptWeekly: %w", err)
	}
	if decision.Allowed {
		return nil, nil
	}
	if err := writeDeptWeeklyRejection(ctx, deps, args, dept, decision); err != nil {
		return nil, err
	}
	r := deptWeeklyBudgetExceededErr(fmt.Sprintf(
		"create_ticket: department %q has hit its weekly ticket budget (current=%d, budget=%d)",
		dept.Slug, decision.CurrentCount, deref32(decision.Budget),
	))
	return &r, nil
}

// writeDeptWeeklyRejection commits the throttle_events row + the
// agent-anchored audit row for a dept-weekly gate rejection in a
// fresh tx, isolated from the caller's main-flow tx (which rolls
// back on the rejection path).
func writeDeptWeeklyRejection(ctx context.Context, deps Deps, args CreateTicketArgs, dept store.Department, decision throttle.DeptWeeklyDecision) error {
	tx, err := deps.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("create_ticket: begin rejection tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)
	callerID := uuidString(deps.AgentInstanceID)
	if callerID == "" {
		callerID = uuidString(deps.ChatSessionID)
	}
	if err := throttle.FireDeptWeekly(ctx, q, dept.CompanyID, decision, dept.ID, callerID); err != nil {
		return fmt.Errorf("create_ticket: FireDeptWeekly: %w", err)
	}
	rt := resourceTypeTicket
	resourceID := ""
	if _, err := WriteAudit(ctx, q, AuditWriteParams{
		ChatSessionID:        deps.ChatSessionID,
		ChatMessageID:        deps.ChatMessageID,
		AgentInstanceID:      deps.AgentInstanceID,
		Verb:                 "create_ticket",
		Args:                 args,
		Outcome:              throttle.KindDeptWeeklyBudgetExceeded,
		ReversibilityClass:   3,
		AffectedResourceID:   &resourceID,
		AffectedResourceType: &rt,
	}); err != nil {
		return fmt.Errorf("create_ticket: write throttle audit: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("create_ticket: commit rejection tx: %w", err)
	}
	return nil
}

// tagCrossDeptCreate annotates the metadata JSONB with
// `cross_dept_create: true` when the agent caller's department
// differs from the target department (FR-007). No-op for chat
// callers. Best-effort: any lookup failure returns the metadata
// unchanged.
func tagCrossDeptCreate(ctx context.Context, q *store.Queries, deps Deps, target store.Department, metadata []byte) []byte {
	if !deps.AgentInstanceID.Valid {
		return metadata
	}
	parentID, err := q.GetAgentInstanceTicketID(ctx, deps.AgentInstanceID)
	if err != nil || !parentID.Valid {
		return metadata
	}
	parent, err := q.GetTicketByID(ctx, parentID)
	if err != nil {
		return metadata
	}
	if parent.DepartmentID == target.ID {
		return metadata
	}
	// Splice the flag into the existing object. Simplest path:
	// unmarshal → set → remarshal; if any step fails, leave the
	// metadata unchanged.
	var m map[string]any
	if err := json.Unmarshal(metadata, &m); err != nil || m == nil {
		m = map[string]any{}
	}
	m["cross_dept_create"] = true
	if out, err := json.Marshal(m); err == nil {
		return out
	}
	return metadata
}

// pickTicketOrigin chooses the tickets.origin enum value based on the
// caller anchor. Agent caller → 'agent'; chat caller → 'chat'.
func pickTicketOrigin(deps Deps) string {
	if deps.AgentInstanceID.Valid {
		return "agent"
	}
	return "chat"
}

// emitDependencyAddedNotify fires work.ticket.dependency_added.<dept>
// (FR-104) post-commit. Best-effort; the listener handles missed
// notifications via the existing transition-event poll fallback.
func emitDependencyAddedNotify(deps Deps, deptSlug, ticketID, dependsOnID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	channel := "work.ticket.dependency_added." + deptSlug
	payload := fmt.Sprintf(
		`{"ticket_id":%q,"depends_on_ticket_id":%q,"dept":%q,"created_at":%q}`,
		ticketID, dependsOnID, deptSlug, time.Now().UTC().Format(time.RFC3339Nano),
	)
	if _, err := deps.Pool.Exec(ctx, "SELECT pg_notify($1, $2)", channel, payload); err != nil && deps.Logger != nil {
		deps.Logger.Warn("garrison-mutate: dependency_added notify failed",
			"channel", channel, "ticket_id", ticketID, "err", err)
	}
}

// deref32 returns *p, or 0 for nil. Used to render Budget in
// rejection messages without panicking on the unlimited case (which
// shouldn't reach this code path but defensively guard anyway).
func deref32(p *int32) int32 {
	if p == nil {
		return 0
	}
	return *p
}

// EditTicketArgs is the JSON input shape for edit_ticket. Pointer-typed
// fields signal "set if present, leave unchanged if nil"; this lets the
// verb implement partial-update semantics.
type EditTicketArgs struct {
	TicketID           string         `json:"ticket_id"`
	Objective          *string        `json:"objective,omitempty"`
	AcceptanceCriteria *string        `json:"acceptance_criteria,omitempty"`
	Metadata           map[string]any `json:"metadata,omitempty"`
}

// realEditTicketHandler implements garrison-mutate.edit_ticket. Tier 2
// reversibility: diff captured in audit (before/after for each changed
// field).
// parseCreateTicketArgs unmarshals + trims + validates the create_ticket
// args. Returns (args, nil) on a valid input or (zero, validation Result)
// when the caller should short-circuit. Pulled out of
// realCreateTicketHandler to keep that function below the SonarCloud
// cognitive-complexity threshold per the M6 retro § "what the spec got
// wrong".
func parseCreateTicketArgs(raw json.RawMessage) (CreateTicketArgs, *Result) {
	var args CreateTicketArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		r := validationFailure("create_ticket: parse args: " + err.Error())
		return CreateTicketArgs{}, &r
	}
	args.Objective = strings.TrimSpace(args.Objective)
	args.DepartmentSlug = strings.TrimSpace(args.DepartmentSlug)
	if args.Objective == "" {
		r := validationFailure("create_ticket: objective is required")
		return CreateTicketArgs{}, &r
	}
	if len(args.Objective) > 10000 {
		r := validationFailure("create_ticket: objective exceeds 10000 chars")
		return CreateTicketArgs{}, &r
	}
	if args.DepartmentSlug == "" {
		r := validationFailure("create_ticket: department_slug is required")
		return CreateTicketArgs{}, &r
	}
	return args, nil
}

// lookupCreateTicketDept resolves the department slug to an ID,
// returning a not-found Result + a transport error from any unexpected
// SQL failure. Pulled out for cognitive-complexity reasons.
func lookupCreateTicketDept(ctx context.Context, q *store.Queries, deps Deps, args CreateTicketArgs) (pgtype.UUID, *Result, error) {
	deptID, err := q.SelectDepartmentIDBySlug(ctx, args.DepartmentSlug)
	if err == nil {
		return deptID, nil, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		r := resourceNotFound("create_ticket: department %q not found", args.DepartmentSlug)
		// writeFailureAudit returns its own error, which the caller
		// propagates as the verb's transport-error return.
		return pgtype.UUID{}, &r, writeFailureAudit(ctx, deps, "create_ticket", args, ErrResourceNotFound, 3, "")
	}
	return pgtype.UUID{}, nil, fmt.Errorf("create_ticket: lookup department: %w", err)
}

// buildCreateTicketMetadata json-marshals args.Metadata. Returns
// (jsonBytes, nil) on success or (nil, validation Result) on a marshal
// failure (operator-typed structures that don't round-trip through
// encoding/json).
func buildCreateTicketMetadata(args CreateTicketArgs) ([]byte, *Result) {
	if len(args.Metadata) == 0 {
		return []byte("{}"), nil
	}
	b, err := json.Marshal(args.Metadata)
	if err != nil {
		r := validationFailure("create_ticket: invalid metadata: " + err.Error())
		return nil, &r
	}
	return b, nil
}

// resolveParentTicketID validates the M6 T010 parent_ticket_id arg.
// Returns:
//   - parentTicketID (pgtype.UUID): valid (or zero-value) UUID for the
//     create_ticket INSERT.
//   - validationResult (*Result): non-nil on a user-facing validation
//     failure (UUID parse / not found / cross-dept / closed parent).
//   - err (error): non-nil only on a transport-level lookup failure
//     (caller surfaces as %w).
//
// Pulled out of realCreateTicketHandler to keep that function below
// SonarCloud's cognitive-complexity threshold per the M6 retro § "what
// the spec got wrong".
func resolveParentTicketID(ctx context.Context, q *store.Queries, raw string, deptID pgtype.UUID) (pgtype.UUID, *Result, error) {
	var zero pgtype.UUID
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return zero, nil, nil
	}
	var parentTicketID pgtype.UUID
	if err := parentTicketID.Scan(trimmed); err != nil {
		r := validationFailure("create_ticket: parent_ticket_id is not a valid UUID")
		return zero, &r, nil
	}
	parent, err := q.GetTicketByID(ctx, parentTicketID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			r := validationFailure("create_ticket: parent_ticket_id refers to a ticket that does not exist")
			return zero, &r, nil
		}
		return zero, nil, fmt.Errorf("create_ticket: lookup parent ticket: %w", err)
	}
	if parent.DepartmentID != deptID {
		r := validationFailure("create_ticket: parent_ticket_id is in a different department")
		return zero, &r, nil
	}
	if parent.ColumnSlug == "done" {
		r := validationFailure("create_ticket: parent_ticket_id is already closed")
		return zero, &r, nil
	}
	return parentTicketID, nil, nil
}

func realEditTicketHandler(ctx context.Context, deps Deps, raw json.RawMessage) (Result, error) {
	var args EditTicketArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return validationFailure("edit_ticket: parse args: " + err.Error()), nil
	}
	if args.TicketID == "" {
		return validationFailure("edit_ticket: ticket_id is required"), nil
	}
	if args.Objective == nil && args.AcceptanceCriteria == nil && len(args.Metadata) == 0 {
		return validationFailure("edit_ticket: at least one of objective / acceptance_criteria / metadata required"), nil
	}
	var ticketID pgtype.UUID
	if err := ticketID.Scan(args.TicketID); err != nil {
		return validationFailure("edit_ticket: invalid ticket_id: " + err.Error()), nil
	}

	tx, err := deps.Pool.Begin(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("edit_ticket: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)

	if _, lockErr := q.LockTicketForUpdate(ctx, ticketID); lockErr != nil {
		if r, auditErr, ok := classifyTicketLockErr(ctx, deps, "edit_ticket", args, 2, args.TicketID, lockErr); !ok {
			return r, auditErr
		}
	}

	// Fetch the before-state for the audit diff.
	before, err := q.GetTicketByID(ctx, ticketID)
	if err != nil {
		return Result{}, fmt.Errorf("edit_ticket: get before: %w", err)
	}

	finalObjective, finalAcceptance, finalMetadata, vErr := mergeTicketFields(before, args)
	if vErr != nil {
		return *vErr, nil
	}
	if err := q.UpdateTicketEditableFields(ctx, store.UpdateTicketEditableFieldsParams{
		ID:                 ticketID,
		Objective:          finalObjective,
		AcceptanceCriteria: finalAcceptance,
		Metadata:           finalMetadata,
	}); err != nil {
		return Result{}, fmt.Errorf("edit_ticket: update: %w", err)
	}

	diff := map[string]any{
		"before": map[string]any{
			"objective":           before.Objective,
			"acceptance_criteria": derefOrNil(before.AcceptanceCriteria),
			"metadata":            json.RawMessage(before.Metadata),
		},
		"after": args,
	}

	rt := "ticket"
	resourceID := args.TicketID
	if _, err := WriteAudit(ctx, q, AuditWriteParams{
		ChatSessionID:        deps.ChatSessionID,
		ChatMessageID:        deps.ChatMessageID,
		Verb:                 "edit_ticket",
		Args:                 diff,
		Outcome:              "success",
		ReversibilityClass:   2,
		AffectedResourceID:   &resourceID,
		AffectedResourceType: &rt,
	}); err != nil {
		return Result{}, fmt.Errorf("edit_ticket: write audit: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, fmt.Errorf("edit_ticket: commit: %w", err)
	}

	emitNotifyBestEffort(deps, "ticket.edited", chatNotifyPayload{
		ChatSessionID:        uuidString(deps.ChatSessionID),
		ChatMessageID:        uuidString(deps.ChatMessageID),
		Verb:                 "edit_ticket",
		AffectedResourceID:   resourceID,
		AffectedResourceType: rt,
	})

	return Result{
		Success:             true,
		AffectedResourceID:  resourceID,
		AffectedResourceURL: ticketURLPrefix + resourceID,
		Message:             "Edited ticket " + resourceID,
	}, nil
}

// TransitionTicketArgs is the JSON input shape for transition_ticket.
//
// ExpectedFromColumn is an optional optimistic-concurrency check
// (added live 2026-05-01 after the M5.4 A→Z smoke surfaced a
// CEO/engineer race). When supplied, the verb verifies the ticket's
// current column matches before transitioning; on mismatch it returns
// outcome=ticket_state_changed so the caller can reconcile against
// fresh state instead of clobbering another actor's work. Empty =
// skip the check (M5.3 back-compat for callers that haven't been
// updated to send the field).
type TransitionTicketArgs struct {
	TicketID           string `json:"ticket_id"`
	ToColumn           string `json:"to_column"`
	ExpectedFromColumn string `json:"expected_from_column,omitempty"`
	Reason             string `json:"reason,omitempty"`
}

// realTransitionTicketHandler implements garrison-mutate.transition_ticket.
// Tier 1 reversibility: a paired call moves the ticket back to the
// previous column. Hooks into the existing M2.x ticket_transitions
// event-bus AND emits the chat-namespaced work.chat.ticket.transitioned
// channel.
func realTransitionTicketHandler(ctx context.Context, deps Deps, raw json.RawMessage) (Result, error) {
	args, ticketID, vRes := parseTransitionArgs(raw)
	if vRes != nil {
		return *vRes, nil
	}

	tx, err := deps.Pool.Begin(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("transition_ticket: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)

	if _, lockErr := q.LockTicketForUpdate(ctx, ticketID); lockErr != nil {
		if r, auditErr, ok := classifyTicketLockErr(ctx, deps, "transition_ticket", args, 1, args.TicketID, lockErr); !ok {
			return r, auditErr
		}
	}

	cur, err := q.GetTicketColumnAndDept(ctx, ticketID)
	if err != nil {
		return Result{}, fmt.Errorf("transition_ticket: get current: %w", err)
	}
	// Optimistic-concurrency check: when the caller supplied an
	// expected_from_column, verify it matches what's actually on the
	// ticket right now. Discovered live during the M5.4-ship A→Z smoke
	// (2026-05-01): the CEO LLM raced with an agent — the engineer
	// finalized in_dev → qa_review within seconds of create_ticket,
	// the CEO (still holding stale state) issued
	// transition_ticket(to_column=in_dev) and clobbered the engineer's
	// work because there was no concurrency check at all.
	//
	// Same shape as the chat-threat-model.md "ticket_state_changed"
	// outcome: when the caller-observed state diverges from current
	// state, fail with a typed error so the CEO can reconcile (or
	// just acknowledge the agent already did the work).
	//
	// Backwards transitions (e.g. QA bouncing back to in_dev) remain
	// fully allowed — they're a legitimate workflow primitive. This
	// check is purely about staleness, not direction.
	if args.ExpectedFromColumn != "" && args.ExpectedFromColumn != cur.ColumnSlug {
		writeErr := writeFailureAudit(ctx, deps, "transition_ticket", args, ErrTicketStateChanged, 1, args.TicketID)
		return Result{
			Success:   false,
			ErrorKind: string(ErrTicketStateChanged),
			Message: fmt.Sprintf(
				"Ticket %s is now at column %q, not %q as you expected — another actor (likely the agent assigned to it) moved it. Check the ticket's current state before deciding what to do next.",
				args.TicketID, cur.ColumnSlug, args.ExpectedFromColumn,
			),
		}, writeErr
	}
	if cur.ColumnSlug == args.ToColumn {
		return commitNoopTransition(ctx, tx, q, deps, args)
	}
	return commitTransition(ctx, tx, q, deps, args, ticketID, cur.ColumnSlug)
}

// parseTransitionArgs unmarshals + validates transition_ticket's input
// shape. Returns a non-nil *Result on validation failure; the caller
// returns it verbatim with nil error per the verb-handler contract.
func parseTransitionArgs(raw json.RawMessage) (TransitionTicketArgs, pgtype.UUID, *Result) {
	var args TransitionTicketArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		r := validationFailure("transition_ticket: parse args: " + err.Error())
		return args, pgtype.UUID{}, &r
	}
	args.ToColumn = strings.TrimSpace(args.ToColumn)
	if args.TicketID == "" {
		r := validationFailure("transition_ticket: ticket_id is required")
		return args, pgtype.UUID{}, &r
	}
	if args.ToColumn == "" {
		r := validationFailure("transition_ticket: to_column is required")
		return args, pgtype.UUID{}, &r
	}
	var ticketID pgtype.UUID
	if err := ticketID.Scan(args.TicketID); err != nil {
		r := validationFailure("transition_ticket: invalid ticket_id: " + err.Error())
		return args, pgtype.UUID{}, &r
	}
	return args, ticketID, nil
}

// commitNoopTransition handles the same-column idempotent branch:
// audit-only commit, no notify, success result describing the no-op.
func commitNoopTransition(ctx context.Context, tx pgx.Tx, q *store.Queries, deps Deps, args TransitionTicketArgs) (Result, error) {
	rt := resourceTypeTicket
	resourceID := args.TicketID
	if _, err := WriteAudit(ctx, q, AuditWriteParams{
		ChatSessionID:        deps.ChatSessionID,
		ChatMessageID:        deps.ChatMessageID,
		Verb:                 "transition_ticket",
		Args:                 args,
		Outcome:              "success",
		ReversibilityClass:   1,
		AffectedResourceID:   &resourceID,
		AffectedResourceType: &rt,
	}); err != nil {
		return Result{}, fmt.Errorf("transition_ticket: write audit: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, fmt.Errorf("transition_ticket: commit: %w", err)
	}
	return Result{
		Success:             true,
		AffectedResourceID:  resourceID,
		AffectedResourceURL: ticketURLPrefix + resourceID,
		Message:             "Ticket " + resourceID + " already in column " + args.ToColumn,
	}, nil
}

// commitTransition handles the non-idempotent path: insert the
// ticket_transitions row, update tickets.column_slug, write the audit
// row, commit, then post-commit notify.
func commitTransition(ctx context.Context, tx pgx.Tx, q *store.Queries, deps Deps, args TransitionTicketArgs, ticketID pgtype.UUID, fromColumn string) (Result, error) {
	if _, err := q.InsertTicketTransition(ctx, store.InsertTicketTransitionParams{
		TicketID:                   ticketID,
		FromColumn:                 &fromColumn,
		ToColumn:                   args.ToColumn,
		TriggeredByAgentInstanceID: pgtype.UUID{},
	}); err != nil {
		return Result{}, fmt.Errorf("transition_ticket: insert transition: %w", err)
	}
	if err := q.UpdateTicketColumnSlug(ctx, store.UpdateTicketColumnSlugParams{
		ID:         ticketID,
		ColumnSlug: args.ToColumn,
	}); err != nil {
		return Result{}, fmt.Errorf("transition_ticket: update column: %w", err)
	}
	rt := resourceTypeTicket
	resourceID := args.TicketID
	if _, err := WriteAudit(ctx, q, AuditWriteParams{
		ChatSessionID:        deps.ChatSessionID,
		ChatMessageID:        deps.ChatMessageID,
		Verb:                 "transition_ticket",
		Args:                 args,
		Outcome:              "success",
		ReversibilityClass:   1,
		AffectedResourceID:   &resourceID,
		AffectedResourceType: &rt,
	}); err != nil {
		return Result{}, fmt.Errorf("transition_ticket: write audit: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, fmt.Errorf("transition_ticket: commit: %w", err)
	}
	emitNotifyBestEffort(deps, "ticket.transitioned", chatNotifyPayload{
		ChatSessionID:        uuidString(deps.ChatSessionID),
		ChatMessageID:        uuidString(deps.ChatMessageID),
		Verb:                 "transition_ticket",
		AffectedResourceID:   resourceID,
		AffectedResourceType: rt,
		Extras: map[string]string{
			"from_column": fromColumn,
			"to_column":   args.ToColumn,
		},
	})
	return Result{
		Success:             true,
		AffectedResourceID:  resourceID,
		AffectedResourceURL: ticketURLPrefix + resourceID,
		Message:             fmt.Sprintf("Transitioned ticket %s: %s → %s", resourceID, fromColumn, args.ToColumn),
	}, nil
}

// helpers shared by ticket verbs

func validationFailure(msg string) Result {
	return Result{Success: false, ErrorKind: string(ErrValidationFailed), Message: msg}
}

func resourceNotFound(format string, args ...any) Result {
	return Result{Success: false, ErrorKind: string(ErrResourceNotFound), Message: fmt.Sprintf(format, args...)}
}

func ticketStateChanged(msg string) Result {
	return Result{Success: false, ErrorKind: string(ErrTicketStateChanged), Message: msg}
}

func concurrencyCapFull(msg string) Result {
	return Result{Success: false, ErrorKind: string(ErrConcurrencyCapFull), Message: msg}
}

func leakScanFailure(msg string) Result {
	return Result{Success: false, ErrorKind: string(ErrLeakScanFailed), Message: msg}
}

// isLockNotAvailable detects PostgreSQL's lock_not_available SQLSTATE
// (55P03), returned by SELECT ... FOR UPDATE NOWAIT when another tx
// holds the lock. Per chat-threat-model.md Rule 4 this is the
// concurrent-mutation conflict signal.
func isLockNotAvailable(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "55P03"
	}
	return false
}

// classifyTicketLockErr maps the LockTicketForUpdate error onto the
// shared (concurrency-conflict / not-found / generic) shape that
// edit_ticket and transition_ticket both need. Returns:
//   - locked=true: the lock was acquired; the caller proceeds.
//   - locked=false + result/audit: the lock failed for a known reason
//     (ErrTicketStateChanged or ErrResourceNotFound); caller returns
//     the result + audit-write error verbatim.
//   - locked=false + result.Success=false + result.ErrorKind=="" +
//     unhandled error: an unexpected pgx error; caller wraps it.
func classifyTicketLockErr(ctx context.Context, deps Deps, verb string, args any, class int16, ticketIDText string, lockErr error) (Result, error, bool) {
	if lockErr == nil {
		return Result{}, nil, true
	}
	if isLockNotAvailable(lockErr) {
		return ticketStateChanged(verb + ": another mutation got there first"),
			writeFailureAudit(ctx, deps, verb, args, ErrTicketStateChanged, class, ticketIDText), false
	}
	if errors.Is(lockErr, pgx.ErrNoRows) {
		return resourceNotFound("%s: ticket %q not found", verb, ticketIDText),
			writeFailureAudit(ctx, deps, verb, args, ErrResourceNotFound, class, ticketIDText), false
	}
	return Result{}, fmt.Errorf("%s: lock: %w", verb, lockErr), false
}

// mergeTicketFields applies edit_ticket's Go-side COALESCE merge:
// nil-pointer args mean "leave unchanged"; populated args overwrite.
// Returns the resolved final values plus an optional validation
// failure when args.Metadata fails to JSON-encode.
func mergeTicketFields(before store.Ticket, args EditTicketArgs) (string, *string, []byte, *Result) {
	finalObjective := before.Objective
	if args.Objective != nil {
		finalObjective = *args.Objective
	}
	finalAcceptance := before.AcceptanceCriteria
	if args.AcceptanceCriteria != nil {
		finalAcceptance = args.AcceptanceCriteria
	}
	finalMetadata := []byte(before.Metadata)
	if len(args.Metadata) > 0 {
		raw, err := json.Marshal(args.Metadata)
		if err != nil {
			r := validationFailure("edit_ticket: invalid metadata: " + err.Error())
			return "", nil, nil, &r
		}
		finalMetadata = raw
	}
	return finalObjective, finalAcceptance, finalMetadata, nil
}

// stringPtrOrNil returns a *string pointing at s if non-empty, nil
// otherwise. Used to satisfy sqlc-generated nullable text columns.
func stringPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// derefOrNil returns *p as a value, or nil for nil pointers. Used
// in audit diffs.
func derefOrNil(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

// emitNotifyBestEffort is the post-commit notify emitter. Runs
// inline in the tool-call path under a 5-second background ctx; if
// pg_notify drops a payload, the activity feed picks it up via the
// existing event_outbox poll fallback (M1 retro precedent).
func emitNotifyBestEffort(deps Deps, channelSuffix string, payload chatNotifyPayload) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := EmitChatMutationNotify(ctx, deps.Pool, channelSuffix, payload); err != nil && deps.Logger != nil {
		deps.Logger.Warn("garrison-mutate: post-commit notify failed",
			"channel_suffix", channelSuffix,
			"error", err,
		)
	}
}

// writeFailureAudit opens a separate audit-only transaction to record a
// failed verb call. Best-effort: the verb's main path returns the
// failure result regardless of whether this audit row commits. Per
// chat-threat-model.md Rule 3 + plan §4.5.
func writeFailureAudit(ctx context.Context, deps Deps, verb string, args any, kind MutateErrorKind, class int16, resourceID string) error {
	if deps.Pool == nil {
		return nil
	}
	q := store.New(deps.Pool)
	rt := ""
	switch v := FindVerb(verb); {
	case v != nil:
		rt = v.AffectedResourceType
	}
	_, err := WriteAudit(ctx, q, AuditWriteParams{
		ChatSessionID:        deps.ChatSessionID,
		ChatMessageID:        deps.ChatMessageID,
		Verb:                 verb,
		Args:                 args,
		Outcome:              kind.String(),
		ReversibilityClass:   class,
		AffectedResourceID:   stringPtrOrNil(resourceID),
		AffectedResourceType: stringPtrOrNil(rt),
	})
	if err != nil && deps.Logger != nil {
		deps.Logger.Warn("garrison-mutate: failure audit insert failed",
			"verb", verb, "error", err)
	}
	return err
}

// init wires the per-verb handlers into the registry, replacing the
// stubHandler placeholders verbs.go declares at package init.
func init() {
	handleCreateTicket = realCreateTicketHandler
	handleEditTicket = realEditTicketHandler
	handleTransitionTicket = realTransitionTicketHandler
}
