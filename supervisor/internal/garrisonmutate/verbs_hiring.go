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
	RoleTitle        string `json:"role_title"`
	DepartmentSlug   string `json:"department_slug"`
	JustificationMD  string `json:"justification_md"`
	SkillsSummaryMD  string `json:"skills_summary_md,omitempty"`
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

func init() {
	handleProposeHire = realProposeHireHandler
}
