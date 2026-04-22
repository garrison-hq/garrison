package concurrency_test

import (
	"context"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/concurrency"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5/pgtype"
)

type stubQuerier struct {
	cap     int32
	running int64
}

func (s *stubQuerier) GetDepartmentByID(ctx context.Context, id pgtype.UUID) (store.Department, error) {
	return store.Department{ConcurrencyCap: s.cap}, nil
}

func (s *stubQuerier) CountRunningByDepartment(ctx context.Context, id pgtype.UUID) (int64, error) {
	return s.running, nil
}

func TestCheckCapAllowsUnderCap(t *testing.T) {
	q := &stubQuerier{cap: 3, running: 2}

	allowed, capOut, running, err := concurrency.CheckCap(context.Background(), q, pgtype.UUID{})
	if err != nil {
		t.Fatalf("CheckCap: unexpected error: %v", err)
	}
	if !allowed {
		t.Errorf("allowed = false, want true (cap=3, running=2)")
	}
	if capOut != 3 || running != 2 {
		t.Errorf("got (cap=%d, running=%d), want (3, 2)", capOut, running)
	}
}

func TestCheckCapBlocksAtCap(t *testing.T) {
	q := &stubQuerier{cap: 3, running: 3}

	allowed, capOut, running, err := concurrency.CheckCap(context.Background(), q, pgtype.UUID{})
	if err != nil {
		t.Fatalf("CheckCap: unexpected error: %v", err)
	}
	if allowed {
		t.Errorf("allowed = true, want false (cap=3, running=3)")
	}
	if capOut != 3 || running != 3 {
		t.Errorf("got (cap=%d, running=%d), want (3, 3)", capOut, running)
	}
}

func TestCheckCapBlocksAtZero(t *testing.T) {
	// FR-003: cap=0 is the pause signal; events should defer, not spawn.
	q := &stubQuerier{cap: 0, running: 0}

	allowed, capOut, running, err := concurrency.CheckCap(context.Background(), q, pgtype.UUID{})
	if err != nil {
		t.Fatalf("CheckCap: unexpected error: %v", err)
	}
	if allowed {
		t.Errorf("allowed = true, want false (cap=0 is FR-003 pause)")
	}
	if capOut != 0 || running != 0 {
		t.Errorf("got (cap=%d, running=%d), want (0, 0)", capOut, running)
	}
}
