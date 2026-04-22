package agents_test

import (
	"context"
	"errors"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/agents"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5/pgtype"
)

// stubQuerier feeds canned store.Agent rows into NewCache so unit tests
// exercise the cache without a Postgres container. An err field lets
// failure-path tests force ListActiveAgents to surface a synthetic error.
type stubQuerier struct {
	rows []store.Agent
	err  error
}

func (s stubQuerier) ListActiveAgents(_ context.Context) ([]store.Agent, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.rows, nil
}

// uuid returns a deterministic pgtype.UUID from the given 16-byte seed so
// table-driven assertions can look up the exact row later.
func uuid(b byte) pgtype.UUID {
	var u pgtype.UUID
	for i := range u.Bytes {
		u.Bytes[i] = b
	}
	u.Valid = true
	return u
}

func TestCachePopulatesFromQuerier(t *testing.T) {
	engDept := uuid(0xA1)
	marDept := uuid(0xB2)
	palace := "engineering.wing.alpha"

	q := stubQuerier{rows: []store.Agent{
		{
			ID:           uuid(0x01),
			DepartmentID: engDept,
			RoleSlug:     "engineer",
			AgentMd:      "# Engineer\n...",
			Model:        "claude-haiku-4-5-20251001",
			ListensFor:   []byte(`["work.ticket.created.engineering.todo"]`),
			PalaceWing:   &palace,
			Status:       "active",
		},
		{
			ID:           uuid(0x02),
			DepartmentID: marDept,
			RoleSlug:     "marketer",
			AgentMd:      "# Marketer\n...",
			Model:        "claude-haiku-4-5-20251001",
			ListensFor:   []byte(`["work.ticket.created.marketing.todo","work.ticket.created.marketing.draft"]`),
			PalaceWing:   nil,
			Status:       "active",
		},
	}}

	c, err := agents.NewCache(context.Background(), q)
	if err != nil {
		t.Fatalf("NewCache(): unexpected error: %v", err)
	}
	if c.Len() != 2 {
		t.Fatalf("Len() = %d; want 2", c.Len())
	}

	eng, err := c.GetForDepartmentAndRole(context.Background(), engDept, "engineer")
	if err != nil {
		t.Fatalf("GetForDepartmentAndRole(engineer): %v", err)
	}
	if eng.Role != "engineer" {
		t.Errorf("eng.Role = %q; want engineer", eng.Role)
	}
	if eng.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("eng.Model = %q; want claude-haiku-4-5-20251001", eng.Model)
	}
	if eng.AgentMD == "" {
		t.Error("eng.AgentMD empty; expected seed text")
	}
	if len(eng.ListensFor) != 1 || eng.ListensFor[0] != "work.ticket.created.engineering.todo" {
		t.Errorf("eng.ListensFor = %v; want [work.ticket.created.engineering.todo]", eng.ListensFor)
	}
	if eng.PalaceWing == nil || *eng.PalaceWing != palace {
		t.Errorf("eng.PalaceWing = %v; want &%q", eng.PalaceWing, palace)
	}

	mar, err := c.GetForDepartmentAndRole(context.Background(), marDept, "marketer")
	if err != nil {
		t.Fatalf("GetForDepartmentAndRole(marketer): %v", err)
	}
	if len(mar.ListensFor) != 2 {
		t.Errorf("mar.ListensFor len = %d; want 2", len(mar.ListensFor))
	}
	if mar.PalaceWing != nil {
		t.Errorf("mar.PalaceWing = %v; want nil", mar.PalaceWing)
	}
}

func TestCacheReturnsNotFoundForMissing(t *testing.T) {
	engDept := uuid(0xA1)
	q := stubQuerier{rows: []store.Agent{{
		ID:           uuid(0x01),
		DepartmentID: engDept,
		RoleSlug:     "engineer",
		AgentMd:      "# Engineer",
		Model:        "claude-haiku-4-5-20251001",
		ListensFor:   []byte(`[]`),
		Status:       "active",
	}}}

	c, err := agents.NewCache(context.Background(), q)
	if err != nil {
		t.Fatalf("NewCache(): %v", err)
	}

	// Wrong department, right role.
	if _, err := c.GetForDepartmentAndRole(context.Background(), uuid(0xFF), "engineer"); !errors.Is(err, agents.ErrAgentNotFound) {
		t.Errorf("wrong-dept lookup: got %v; want ErrAgentNotFound", err)
	}

	// Right department, wrong role.
	if _, err := c.GetForDepartmentAndRole(context.Background(), engDept, "scientist"); !errors.Is(err, agents.ErrAgentNotFound) {
		t.Errorf("wrong-role lookup: got %v; want ErrAgentNotFound", err)
	}
}

func TestCachePropagatesQuerierError(t *testing.T) {
	want := errors.New("synthetic db failure")
	q := stubQuerier{err: want}

	_, err := agents.NewCache(context.Background(), q)
	if err == nil {
		t.Fatal("NewCache(): want error, got nil")
	}
	if !errors.Is(err, want) {
		t.Errorf("NewCache() error chain = %v; want it to wrap %v", err, want)
	}
}

func TestCacheRejectsMalformedListensFor(t *testing.T) {
	engDept := uuid(0xA1)
	q := stubQuerier{rows: []store.Agent{{
		ID:           uuid(0x01),
		DepartmentID: engDept,
		RoleSlug:     "engineer",
		AgentMd:      "# Engineer",
		Model:        "claude-haiku-4-5-20251001",
		ListensFor:   []byte(`{"not":"an array"}`),
		Status:       "active",
	}}}

	_, err := agents.NewCache(context.Background(), q)
	if err == nil {
		t.Fatal("NewCache(): want error for malformed listens_for, got nil")
	}
}

func TestCacheTreatsEmptyListensForAsEmptySlice(t *testing.T) {
	engDept := uuid(0xA1)
	q := stubQuerier{rows: []store.Agent{{
		ID:           uuid(0x01),
		DepartmentID: engDept,
		RoleSlug:     "engineer",
		AgentMd:      "# Engineer",
		Model:        "claude-haiku-4-5-20251001",
		ListensFor:   []byte(`[]`),
		Status:       "active",
	}}}

	c, err := agents.NewCache(context.Background(), q)
	if err != nil {
		t.Fatalf("NewCache(): %v", err)
	}
	got, err := c.GetForDepartmentAndRole(context.Background(), engDept, "engineer")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.ListensFor) != 0 {
		t.Errorf("ListensFor len = %d; want 0", len(got.ListensFor))
	}
}
