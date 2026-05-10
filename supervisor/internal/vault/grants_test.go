package vault

import (
	"context"
	"errors"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5/pgtype"
)

// fakeGrantLister captures the ListGrantsForRoleAndAgent params and
// returns canned rows. Used to verify the query-param shape + result
// transformation without a real DB.
type fakeGrantLister struct {
	gotParams store.ListGrantsForRoleAndAgentParams
	rows      []store.ListGrantsForRoleAndAgentRow
	err       error
}

func (f *fakeGrantLister) ListGrantsForRoleAndAgent(ctx context.Context, arg store.ListGrantsForRoleAndAgentParams) ([]store.ListGrantsForRoleAndAgentRow, error) {
	f.gotParams = arg
	return f.rows, f.err
}

func TestListGrantsForRoleAndAgentPassesParamsThrough(t *testing.T) {
	customerID := pgtype.UUID{Valid: true, Bytes: [16]byte{0xc0, 0xcf, 0xff}}
	agentID := pgtype.UUID{Valid: true, Bytes: [16]byte{0xa9, 0xe1, 0x07}}
	f := &fakeGrantLister{}
	_, err := ListGrantsForRoleAndAgent(context.Background(), f, "engineer", customerID, agentID)
	if err != nil {
		t.Fatalf("ListGrantsForRoleAndAgent: %v", err)
	}
	if f.gotParams.RoleSlug != "engineer" {
		t.Errorf("RoleSlug = %q; want engineer", f.gotParams.RoleSlug)
	}
	if f.gotParams.CustomerID != customerID {
		t.Errorf("CustomerID mismatch: %v vs %v", f.gotParams.CustomerID, customerID)
	}
	if f.gotParams.AgentID != agentID {
		t.Errorf("AgentID mismatch: %v vs %v", f.gotParams.AgentID, agentID)
	}
}

func TestListGrantsForRoleAndAgentReturnsAgentScopedRows(t *testing.T) {
	customerID := pgtype.UUID{Valid: true, Bytes: [16]byte{0xc0, 0xcf, 0xff}}
	agentID := pgtype.UUID{Valid: true, Bytes: [16]byte{0xa9, 0xe1, 0x07}}
	roleID := pgtype.UUID{Valid: false}
	f := &fakeGrantLister{
		rows: []store.ListGrantsForRoleAndAgentRow{
			// Role-scoped grant: agent_id IS NULL.
			{
				EnvVarName: "OPENAI_API_KEY",
				SecretPath: "/role/engineer/openai",
				CustomerID: customerID,
				AgentID:    roleID,
			},
			// Agent-scoped grant: agent_id = <agent>.
			{
				EnvVarName: "MCPJUNGLE_BEARER_TOKEN",
				SecretPath: "/mcpjungle/agents/<id>",
				CustomerID: customerID,
				AgentID:    agentID,
			},
		},
	}
	grants, err := ListGrantsForRoleAndAgent(context.Background(), f, "engineer", customerID, agentID)
	if err != nil {
		t.Fatalf("ListGrantsForRoleAndAgent: %v", err)
	}
	if len(grants) != 2 {
		t.Fatalf("len(grants) = %d; want 2", len(grants))
	}
	// First row is role-scoped (AgentID.Valid=false).
	if grants[0].EnvVarName != "OPENAI_API_KEY" || grants[0].AgentID.Valid {
		t.Errorf("first grant: want role-scoped OPENAI_API_KEY, got %+v", grants[0])
	}
	// Second row is agent-scoped (AgentID.Valid=true and matches the queried agent).
	if grants[1].EnvVarName != "MCPJUNGLE_BEARER_TOKEN" || !grants[1].AgentID.Valid || grants[1].AgentID != agentID {
		t.Errorf("second grant: want agent-scoped MCPJUNGLE_BEARER_TOKEN for agent, got %+v", grants[1])
	}
}

func TestListGrantsForRoleAndAgentSurfacesQueryError(t *testing.T) {
	f := &fakeGrantLister{err: errors.New("db hiccup")}
	_, err := ListGrantsForRoleAndAgent(context.Background(), f, "engineer", pgtype.UUID{}, pgtype.UUID{})
	if err == nil || err.Error() != "db hiccup" {
		t.Errorf("err = %v; want unwrapped query error", err)
	}
}

func TestListGrantsForRoleAndAgentEmptyResultIsEmptySlice(t *testing.T) {
	f := &fakeGrantLister{rows: nil}
	grants, err := ListGrantsForRoleAndAgent(context.Background(), f, "engineer", pgtype.UUID{}, pgtype.UUID{})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if grants == nil {
		t.Error("grants is nil; want empty slice for safe ranging")
	}
	if len(grants) != 0 {
		t.Errorf("len(grants) = %d; want 0", len(grants))
	}
}
