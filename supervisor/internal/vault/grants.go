package vault

import (
	"context"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5/pgtype"
)

// GrantLister is the narrow seam vault's grant-resolution code depends
// on. *store.Queries satisfies it; unit tests substitute a stub.
type GrantLister interface {
	ListGrantsForRoleAndAgent(ctx context.Context, arg store.ListGrantsForRoleAndAgentParams) ([]store.ListGrantsForRoleAndAgentRow, error)
}

// ListGrantsForRoleAndAgent returns both role-scoped grants
// (role_slug=<role> AND agent_id IS NULL) and agent-scoped grants
// (agent_id=<agent>). Used at spawn time to fetch every vault path the
// agent is authorised to read. M8 extension of the M2.3 grant lookup;
// the agent_id discriminator scopes MCPJungle bearer tokens to specific
// agent instances per FR-403.
//
// Returns an empty slice (not nil) when no grants resolve so callers
// can range over the result unconditionally.
func ListGrantsForRoleAndAgent(
	ctx context.Context,
	q GrantLister,
	roleSlug string,
	customerID pgtype.UUID,
	agentID pgtype.UUID,
) ([]GrantRow, error) {
	rows, err := q.ListGrantsForRoleAndAgent(ctx, store.ListGrantsForRoleAndAgentParams{
		RoleSlug:   roleSlug,
		CustomerID: customerID,
		AgentID:    agentID,
	})
	if err != nil {
		return nil, err
	}
	out := make([]GrantRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, GrantRow{
			EnvVarName: r.EnvVarName,
			SecretPath: r.SecretPath,
			CustomerID: r.CustomerID,
			AgentID:    r.AgentID,
		})
	}
	return out, nil
}
