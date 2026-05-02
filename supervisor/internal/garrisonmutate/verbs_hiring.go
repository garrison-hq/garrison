package garrisonmutate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5"
)

// ProposeHireArgs is the JSON input shape for propose_hire. Field
// length bounds match the FR-422 / plan §13.8 spec; the M5.3 stopgap
// page renders these fields directly.
type ProposeHireArgs struct {
	RoleTitle       string `json:"role_title"`
	DepartmentSlug  string `json:"department_slug"`
	JustificationMD string `json:"justification_md"`
	SkillsSummaryMD string `json:"skills_summary_md,omitempty"`
}

// realProposeHireHandler implements garrison-mutate.propose_hire. Tier 3
// reversibility (M7's review flow can mark it rejected/superseded but
// the row persists). Always sets proposed_via='ceo_chat' and
// proposed_by_chat_session_id to the calling session.
func realProposeHireHandler(ctx context.Context, deps Deps, raw json.RawMessage) (Result, error) {
	var args ProposeHireArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return validationFailure("propose_hire: parse args: " + err.Error()), nil
	}
	args.RoleTitle = strings.TrimSpace(args.RoleTitle)
	args.DepartmentSlug = strings.TrimSpace(args.DepartmentSlug)
	args.JustificationMD = strings.TrimSpace(args.JustificationMD)
	if args.RoleTitle == "" {
		return validationFailure("propose_hire: role_title is required"), nil
	}
	if len(args.RoleTitle) > 100 {
		return validationFailure("propose_hire: role_title exceeds 100 chars"), nil
	}
	if args.DepartmentSlug == "" {
		return validationFailure("propose_hire: department_slug is required"), nil
	}
	if args.JustificationMD == "" {
		return validationFailure("propose_hire: justification_md is required"), nil
	}
	if len(args.JustificationMD) > 10000 {
		return validationFailure("propose_hire: justification_md exceeds 10000 chars"), nil
	}
	if len(args.SkillsSummaryMD) > 10000 {
		return validationFailure("propose_hire: skills_summary_md exceeds 10000 chars"), nil
	}

	tx, err := deps.Pool.Begin(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("propose_hire: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)

	// Validate department_slug exists; the FK on hiring_proposals
	// would catch this at INSERT time but the validation gives a
	// cleaner typed error for the chip layer.
	if _, err := q.SelectDepartmentIDBySlug(ctx, args.DepartmentSlug); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return resourceNotFound("propose_hire: department %q not found", args.DepartmentSlug),
				writeFailureAudit(ctx, deps, "propose_hire", args, ErrResourceNotFound, 3, "")
		}
		return Result{}, fmt.Errorf("propose_hire: lookup department: %w", err)
	}

	row, err := q.InsertHiringProposal(ctx, store.InsertHiringProposalParams{
		RoleTitle:               args.RoleTitle,
		DepartmentSlug:          args.DepartmentSlug,
		JustificationMd:         args.JustificationMD,
		SkillsSummaryMd:         stringPtrOrNil(args.SkillsSummaryMD),
		ProposedVia:             "ceo_chat",
		ProposedByChatSessionID: deps.ChatSessionID,
	})
	if err != nil {
		return Result{}, fmt.Errorf("propose_hire: insert proposal: %w", err)
	}

	resourceID := uuidString(row.ID)
	rt := "hiring_proposal"
	if _, err := WriteAudit(ctx, q, AuditWriteParams{
		ChatSessionID:        deps.ChatSessionID,
		ChatMessageID:        deps.ChatMessageID,
		Verb:                 "propose_hire",
		Args:                 args,
		Outcome:              "success",
		ReversibilityClass:   3,
		AffectedResourceID:   &resourceID,
		AffectedResourceType: &rt,
	}); err != nil {
		return Result{}, fmt.Errorf("propose_hire: write audit: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, fmt.Errorf("propose_hire: commit: %w", err)
	}

	emitNotifyBestEffort(deps, "hiring.proposed", chatNotifyPayload{
		ChatSessionID:        uuidString(deps.ChatSessionID),
		ChatMessageID:        uuidString(deps.ChatMessageID),
		Verb:                 "propose_hire",
		AffectedResourceID:   resourceID,
		AffectedResourceType: rt,
		Extras: map[string]string{
			"department_slug": args.DepartmentSlug,
			"role_title":      args.RoleTitle,
		},
	})
	return Result{
		Success:             true,
		AffectedResourceID:  resourceID,
		AffectedResourceURL: "/hiring/proposals/" + resourceID,
		Message:             fmt.Sprintf("Proposed hire: %s in %s", args.RoleTitle, args.DepartmentSlug),
	}, nil
}

// SkillEntry is the shape of one element inside ProposeSkillChangeArgs.Add /
// .Remove / .Bump and BumpSkillVersionArgs. Fields:
//   - Package: registry-qualified name (e.g. "skills.sh/sqlmigrate-reader").
//   - Version: semver-ish or registry-defined version tag.
//   - Digest: SHA-256 digest captured at propose time. Required for Add/
//     Bump.NewVersion entries; the supervisor re-verifies at install time
//     per FR-106 / HR-7.
type SkillEntry struct {
	Package string `json:"package"`
	Version string `json:"version,omitempty"`
	Digest  string `json:"digest,omitempty"`
}

// SkillBumpEntry pairs prior + new digest for a version-bump line in a
// skill_change proposal. BumpSkillVersionArgs uses the dedicated
// before/after fields directly; this type appears in the diff jsonb only.
type SkillBumpEntry struct {
	Package     string `json:"package"`
	FromVersion string `json:"from_version,omitempty"`
	ToVersion   string `json:"to_version,omitempty"`
	FromDigest  string `json:"from_digest,omitempty"`
	ToDigest    string `json:"to_digest,omitempty"`
}

// ProposeSkillChangeArgs is the JSON input shape for the chat verb
// `propose_skill_change` (FR-103). Mirrors the propose_hire bound shape
// — bounded text + bounded list lengths so a runaway chat turn cannot
// blow up the audit row.
//
// Add / Remove / Bump are independently optional but at least one must
// be non-empty (a no-op proposal is rejected as validation_failed).
type ProposeSkillChangeArgs struct {
	AgentRoleSlug   string           `json:"agent_role_slug"`
	JustificationMD string           `json:"justification_md"`
	Add             []SkillEntry     `json:"add,omitempty"`
	Remove          []SkillEntry     `json:"remove,omitempty"`
	Bump            []SkillBumpEntry `json:"bump,omitempty"`
}

// BumpSkillVersionArgs is the JSON input shape for the chat verb
// `bump_skill_version` (FR-103). FR-110a: simultaneous bumps for the
// same (agent, package) both succeed at propose time; supersession
// resolves at approve time.
type BumpSkillVersionArgs struct {
	AgentRoleSlug   string `json:"agent_role_slug"`
	Package         string `json:"package"`
	FromVersion     string `json:"from_version,omitempty"`
	ToVersion       string `json:"to_version"`
	FromDigest      string `json:"from_digest,omitempty"`
	ToDigest        string `json:"to_digest"`
	JustificationMD string `json:"justification_md,omitempty"`
}

const (
	// maxSkillEntriesPerProposal caps the combined count of
	// add/remove/bump entries on one propose_skill_change call.
	// Defends the audit row against pathological argument shape.
	maxSkillEntriesPerProposal = 32
	// maxSkillPackageLen mirrors the registry's package-name budget.
	maxSkillPackageLen = 200
	// maxSkillVersionLen mirrors registry version-tag conventions.
	maxSkillVersionLen = 64
	// skillDigestHexLen pins SHA-256 hex (64 chars). Empty digests
	// are accepted at propose-time for Remove entries; required for
	// Add/Bump.
	skillDigestHexLen = 64
)

// realProposeSkillChangeHandler implements `propose_skill_change`. The
// chat verb writes a skill_change row in `hiring_proposals`; the
// dashboard /admin/hires surface (T015) renders the diff with operator
// approve/reject. Approve queues the install actuator (T010) which
// atomically swaps the agent's skill bind-mount; the next spawn picks
// up the new skill.
func realProposeSkillChangeHandler(ctx context.Context, deps Deps, raw json.RawMessage) (Result, error) {
	args, vRes := parseSkillChangeArgs(raw)
	if vRes != nil {
		return *vRes, nil
	}
	return runSkillProposalTx(ctx, deps, "propose_skill_change", args.AgentRoleSlug, "skill_change",
		args.JustificationMD, skillChangeDiffJSON(args), skillChangeSnapshotJSON(args), "")
}

// realBumpSkillVersionHandler implements `bump_skill_version`. Records
// both pre- and post-bump digests so the dashboard can render the diff
// (FR-101 skill_diff_jsonb shape). FR-110a supersession resolves at
// approve time.
func realBumpSkillVersionHandler(ctx context.Context, deps Deps, raw json.RawMessage) (Result, error) {
	args, vRes := parseBumpSkillVersionArgs(raw)
	if vRes != nil {
		return *vRes, nil
	}
	diff, _ := json.Marshal(map[string]any{
		"bump": []SkillBumpEntry{{
			Package:     args.Package,
			FromVersion: args.FromVersion,
			ToVersion:   args.ToVersion,
			FromDigest:  args.FromDigest,
			ToDigest:    args.ToDigest,
		}},
	})
	snap, _ := json.Marshal(args)
	return runSkillProposalTx(ctx, deps, "bump_skill_version", args.AgentRoleSlug, "version_bump",
		args.JustificationMD, diff, snap, args.ToDigest)
}

// parseSkillChangeArgs unmarshals + validates a ProposeSkillChange
// payload. Returns the args struct on success, or a Result describing
// the validation failure for the caller to return verbatim.
func parseSkillChangeArgs(raw json.RawMessage) (ProposeSkillChangeArgs, *Result) {
	var args ProposeSkillChangeArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		r := validationFailure("propose_skill_change: parse args: " + err.Error())
		return args, &r
	}
	args.AgentRoleSlug = strings.TrimSpace(args.AgentRoleSlug)
	args.JustificationMD = strings.TrimSpace(args.JustificationMD)
	if args.AgentRoleSlug == "" {
		r := validationFailure("propose_skill_change: agent_role_slug is required")
		return args, &r
	}
	if args.JustificationMD == "" {
		r := validationFailure("propose_skill_change: justification_md is required")
		return args, &r
	}
	if len(args.JustificationMD) > 10000 {
		r := validationFailure("propose_skill_change: justification_md exceeds 10000 chars")
		return args, &r
	}
	total := len(args.Add) + len(args.Remove) + len(args.Bump)
	if total == 0 {
		r := validationFailure("propose_skill_change: at least one of add/remove/bump must be non-empty")
		return args, &r
	}
	if total > maxSkillEntriesPerProposal {
		r := validationFailure(fmt.Sprintf("propose_skill_change: too many entries (max %d)", maxSkillEntriesPerProposal))
		return args, &r
	}
	if r := validateSkillEntries("add", args.Add, true); r != nil {
		return args, r
	}
	if r := validateSkillEntries("remove", args.Remove, false); r != nil {
		return args, r
	}
	if r := validateSkillBumps(args.Bump); r != nil {
		return args, r
	}
	return args, nil
}

// parseBumpSkillVersionArgs unmarshals + validates a BumpSkillVersion
// payload. Mirrors parseSkillChangeArgs in shape so both validation
// paths read uniformly.
func parseBumpSkillVersionArgs(raw json.RawMessage) (BumpSkillVersionArgs, *Result) {
	var args BumpSkillVersionArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		r := validationFailure("bump_skill_version: parse args: " + err.Error())
		return args, &r
	}
	args.AgentRoleSlug = strings.TrimSpace(args.AgentRoleSlug)
	args.Package = strings.TrimSpace(args.Package)
	args.ToVersion = strings.TrimSpace(args.ToVersion)
	args.ToDigest = strings.TrimSpace(args.ToDigest)
	args.JustificationMD = strings.TrimSpace(args.JustificationMD)
	if args.AgentRoleSlug == "" {
		r := validationFailure("bump_skill_version: agent_role_slug is required")
		return args, &r
	}
	if args.Package == "" {
		r := validationFailure("bump_skill_version: package is required")
		return args, &r
	}
	if len(args.Package) > maxSkillPackageLen {
		r := validationFailure(fmt.Sprintf("bump_skill_version: package exceeds %d chars", maxSkillPackageLen))
		return args, &r
	}
	if args.ToVersion == "" {
		r := validationFailure("bump_skill_version: to_version is required")
		return args, &r
	}
	if len(args.ToVersion) > maxSkillVersionLen {
		r := validationFailure(fmt.Sprintf("bump_skill_version: to_version exceeds %d chars", maxSkillVersionLen))
		return args, &r
	}
	if args.ToDigest == "" {
		r := validationFailure("bump_skill_version: to_digest is required")
		return args, &r
	}
	if !looksLikeSHA256Hex(args.ToDigest) {
		r := validationFailure("bump_skill_version: to_digest must be 64-char SHA-256 hex")
		return args, &r
	}
	if args.FromDigest != "" && !looksLikeSHA256Hex(args.FromDigest) {
		r := validationFailure("bump_skill_version: from_digest must be 64-char SHA-256 hex when supplied")
		return args, &r
	}
	if len(args.JustificationMD) > 10000 {
		r := validationFailure("bump_skill_version: justification_md exceeds 10000 chars")
		return args, &r
	}
	return args, nil
}

func validateSkillEntries(field string, entries []SkillEntry, digestRequired bool) *Result {
	for i, e := range entries {
		pkg := strings.TrimSpace(e.Package)
		if pkg == "" {
			r := validationFailure(fmt.Sprintf("propose_skill_change: %s[%d].package is required", field, i))
			return &r
		}
		if len(pkg) > maxSkillPackageLen {
			r := validationFailure(fmt.Sprintf("propose_skill_change: %s[%d].package exceeds %d chars", field, i, maxSkillPackageLen))
			return &r
		}
		if len(e.Version) > maxSkillVersionLen {
			r := validationFailure(fmt.Sprintf("propose_skill_change: %s[%d].version exceeds %d chars", field, i, maxSkillVersionLen))
			return &r
		}
		if digestRequired {
			if e.Digest == "" {
				r := validationFailure(fmt.Sprintf("propose_skill_change: %s[%d].digest is required", field, i))
				return &r
			}
			if !looksLikeSHA256Hex(e.Digest) {
				r := validationFailure(fmt.Sprintf("propose_skill_change: %s[%d].digest must be 64-char SHA-256 hex", field, i))
				return &r
			}
		} else if e.Digest != "" && !looksLikeSHA256Hex(e.Digest) {
			r := validationFailure(fmt.Sprintf("propose_skill_change: %s[%d].digest must be 64-char SHA-256 hex when supplied", field, i))
			return &r
		}
	}
	return nil
}

func validateSkillBumps(bumps []SkillBumpEntry) *Result {
	for i, b := range bumps {
		pkg := strings.TrimSpace(b.Package)
		if pkg == "" {
			r := validationFailure(fmt.Sprintf("propose_skill_change: bump[%d].package is required", i))
			return &r
		}
		if len(pkg) > maxSkillPackageLen {
			r := validationFailure(fmt.Sprintf("propose_skill_change: bump[%d].package exceeds %d chars", i, maxSkillPackageLen))
			return &r
		}
		if b.ToDigest == "" {
			r := validationFailure(fmt.Sprintf("propose_skill_change: bump[%d].to_digest is required", i))
			return &r
		}
		if !looksLikeSHA256Hex(b.ToDigest) {
			r := validationFailure(fmt.Sprintf("propose_skill_change: bump[%d].to_digest must be 64-char SHA-256 hex", i))
			return &r
		}
		if b.FromDigest != "" && !looksLikeSHA256Hex(b.FromDigest) {
			r := validationFailure(fmt.Sprintf("propose_skill_change: bump[%d].from_digest must be 64-char SHA-256 hex when supplied", i))
			return &r
		}
	}
	return nil
}

// looksLikeSHA256Hex reports whether s is exactly 64 lowercase-hex
// characters. We don't accept upper-case here so the digest column
// stays comparable byte-for-byte with the SHA-256 hex skillregistry
// emits.
func looksLikeSHA256Hex(s string) bool {
	if len(s) != skillDigestHexLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') {
			continue
		}
		return false
	}
	return true
}

func skillChangeDiffJSON(args ProposeSkillChangeArgs) []byte {
	body, _ := json.Marshal(map[string]any{
		"add":    args.Add,
		"remove": args.Remove,
		"bump":   args.Bump,
	})
	return body
}

func skillChangeSnapshotJSON(args ProposeSkillChangeArgs) []byte {
	body, _ := json.Marshal(args)
	return body
}

// runSkillProposalTx is the shared INSERT path for `propose_skill_change`
// and `bump_skill_version`. Both verbs walk the same shape: lookup the
// target agent by role_slug; INSERT a hiring_proposals row with the M7
// columns populated; write the audit row in the same tx; emit the
// chat-namespaced notify post-commit.
func runSkillProposalTx(
	ctx context.Context,
	deps Deps,
	verb, agentRoleSlug, proposalType, justification string,
	skillDiffJSONB, snapshotJSONB []byte,
	digestAtPropose string,
) (Result, error) {
	target, lookupRes, lookupErr := resolveTargetAgent(ctx, deps, verb, agentRoleSlug)
	if lookupRes != nil || lookupErr != nil {
		return resultOrEmpty(lookupRes), lookupErr
	}

	tx, err := deps.Pool.Begin(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("%s: begin tx: %w", verb, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)

	dept, err := q.GetDepartmentByID(ctx, target.DepartmentID)
	if err != nil {
		return Result{}, fmt.Errorf("%s: lookup department: %w", verb, err)
	}

	row, err := q.InsertHiringProposalM7(ctx, store.InsertHiringProposalM7Params{
		RoleTitle:               target.RoleSlug,
		DepartmentSlug:          dept.Slug,
		JustificationMd:         justificationOrPlaceholder(justification, verb),
		SkillsSummaryMd:         nil,
		ProposedVia:             "ceo_chat",
		ProposedByChatSessionID: deps.ChatSessionID,
		TargetAgentID:           target.ID,
		ProposalType:            proposalType,
		SkillDiffJsonb:          skillDiffJSONB,
		ProposalSnapshotJsonb:   snapshotJSONB,
		SkillDigestAtPropose:    stringPtrOrNil(digestAtPropose),
	})
	if err != nil {
		return Result{}, fmt.Errorf("%s: insert proposal: %w", verb, err)
	}

	resourceID := uuidString(row.ID)
	rt := "hiring_proposal"
	if _, err := WriteAudit(ctx, q, AuditWriteParams{
		ChatSessionID:        deps.ChatSessionID,
		ChatMessageID:        deps.ChatMessageID,
		Verb:                 verb,
		Args:                 json.RawMessage(snapshotJSONB),
		Outcome:              "success",
		ReversibilityClass:   3,
		AffectedResourceID:   &resourceID,
		AffectedResourceType: &rt,
	}); err != nil {
		return Result{}, fmt.Errorf("%s: write audit: %w", verb, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, fmt.Errorf("%s: commit: %w", verb, err)
	}

	emitNotifyBestEffort(deps, "hiring.proposed", chatNotifyPayload{
		ChatSessionID:        uuidString(deps.ChatSessionID),
		ChatMessageID:        uuidString(deps.ChatMessageID),
		Verb:                 verb,
		AffectedResourceID:   resourceID,
		AffectedResourceType: rt,
		Extras: map[string]string{
			"agent_role_slug": agentRoleSlug,
			"proposal_type":   proposalType,
		},
	})
	return Result{
		Success:             true,
		AffectedResourceID:  resourceID,
		AffectedResourceURL: "/admin/hires/" + resourceID,
		Message:             fmt.Sprintf("Proposed %s for %s", strings.ReplaceAll(proposalType, "_", "-"), agentRoleSlug),
	}, nil
}

// resolveTargetAgent runs the count-then-find ambiguity check
// established by verbs_agents.go's setAgentStatus. Returns the matched
// agent on a single hit; a pre-baked Result + audit-write error on
// not-found / ambiguous.
func resolveTargetAgent(
	ctx context.Context, deps Deps, verb, roleSlug string,
) (store.FindAgentByRoleSlugRow, *Result, error) {
	q := store.New(deps.Pool)
	count, err := q.CountAgentsByRoleSlug(ctx, roleSlug)
	if err != nil {
		return store.FindAgentByRoleSlugRow{}, nil, fmt.Errorf("%s: count agents: %w", verb, err)
	}
	switch {
	case count == 0:
		r := resourceNotFound("%s: agent role %q not found", verb, roleSlug)
		return store.FindAgentByRoleSlugRow{}, &r,
			writeFailureAudit(ctx, deps, verb, map[string]string{"agent_role_slug": roleSlug}, ErrResourceNotFound, 3, "")
	case count > 1:
		r := validationFailure(fmt.Sprintf("%s: agent role %q is ambiguous (%d matches across departments)", verb, roleSlug, count))
		return store.FindAgentByRoleSlugRow{}, &r,
			writeFailureAudit(ctx, deps, verb, map[string]string{"agent_role_slug": roleSlug}, ErrValidationFailed, 3, "")
	}
	row, err := q.FindAgentByRoleSlug(ctx, roleSlug)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			r := resourceNotFound("%s: agent role %q not found", verb, roleSlug)
			return store.FindAgentByRoleSlugRow{}, &r,
				writeFailureAudit(ctx, deps, verb, map[string]string{"agent_role_slug": roleSlug}, ErrResourceNotFound, 3, "")
		}
		return store.FindAgentByRoleSlugRow{}, nil, fmt.Errorf("%s: find agent: %w", verb, err)
	}
	return row, nil, nil
}

func justificationOrPlaceholder(s, verb string) string {
	if strings.TrimSpace(s) != "" {
		return s
	}
	// bump_skill_version's justification is optional; callers commonly
	// omit it when the operator's chat already explained intent. Keep
	// the column NOT NULL satisfied by recording the verb name.
	return "(omitted; recorded via " + verb + ")"
}

func resultOrEmpty(r *Result) Result {
	if r == nil {
		return Result{}
	}
	return *r
}

func init() {
	handleProposeHire = realProposeHireHandler
	handleProposeSkillChange = realProposeSkillChangeHandler
	handleBumpSkillVersion = realBumpSkillVersionHandler
}
