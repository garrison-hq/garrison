package spawn

import (
	"context"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/agents"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5/pgtype"
)

// stubAgentsQuerier feeds canned rows into agents.NewCache so the slot
// gate can resolve (department, role) → agent ID without a database.
type stubAgentsQuerier struct {
	rows []store.Agent
}

func (s stubAgentsQuerier) ListActiveAgents(_ context.Context) ([]store.Agent, error) {
	return s.rows, nil
}

func inflightUUID(b byte) pgtype.UUID {
	var u pgtype.UUID
	u.Bytes[15] = b
	u.Valid = true
	return u
}

func inflightAgentRow(id, deptID pgtype.UUID, roleSlug string) store.Agent {
	return store.Agent{ID: id, DepartmentID: deptID, RoleSlug: roleSlug, Status: "active"}
}

// inflightTestDeps builds Deps with the container path selected
// explicitly (UseDirectExec=false + non-nil controller): the gate is
// inert under the still-default direct-exec, so these tests must opt
// into the configuration T012 ships.
func inflightTestDeps(t *testing.T, rows []store.Agent) Deps {
	t.Helper()
	cache, err := agents.NewCache(context.Background(), stubAgentsQuerier{rows: rows})
	if err != nil {
		t.Fatalf("agents.NewCache: %v", err)
	}
	return Deps{
		Logger:         testLogger(),
		UseDirectExec:  false,
		AgentContainer: &fakeController{},
		Inflight:       NewAgentInflight(),
		AgentsCache:    cache,
	}
}

// TestPerAgentSlotDefersSecondEvent — FR-017: a second concurrent event
// for the same agent fails the slot acquire. prepareSpawn maps ok=false
// to spawnPrep{done:true} after rolling back the dedupe tx and before
// InsertRunningInstance ever runs, so the deferred event carries no
// instance row and stays unprocessed (cap-full semantics).
func TestPerAgentSlotDefersSecondEvent(t *testing.T) {
	ctx := context.Background()
	deptID := inflightUUID(1)
	deps := inflightTestDeps(t, []store.Agent{
		inflightAgentRow(inflightUUID(0xA1), deptID, "engineer"),
	})
	dept := store.Department{ID: deptID}

	release, ok := acquireAgentSlot(ctx, deps, dept, "engineer")
	if !ok || release == nil {
		t.Fatalf("first acquire: ok=%v releaseNil=%v; want held slot", ok, release == nil)
	}

	secondRelease, secondOK := acquireAgentSlot(ctx, deps, dept, "engineer")
	if secondOK {
		t.Errorf("second acquire for same agent: ok=true; want defer")
	}
	if secondRelease != nil {
		t.Errorf("second acquire for same agent: release non-nil; want nil")
	}
}

// TestPerAgentSlotReleasedAfterTerminalWrite — the release handle rides
// spawnPrep and Spawn defers it until the run branch returns, i.e. past
// the terminal write. This pins the handle's contract: while held the
// agent's slot stays closed; once released (idempotently — Spawn's
// defer must be safe on every exit path) the next event for the same
// agent proceeds.
func TestPerAgentSlotReleasedAfterTerminalWrite(t *testing.T) {
	ctx := context.Background()
	deptID := inflightUUID(1)
	deps := inflightTestDeps(t, []store.Agent{
		inflightAgentRow(inflightUUID(0xA1), deptID, "engineer"),
	})
	dept := store.Department{ID: deptID}

	release, ok := acquireAgentSlot(ctx, deps, dept, "engineer")
	if !ok || release == nil {
		t.Fatalf("first acquire: ok=%v releaseNil=%v; want held slot", ok, release == nil)
	}
	if _, blocked := acquireAgentSlot(ctx, deps, dept, "engineer"); blocked {
		t.Fatalf("acquire while slot held: ok=true; want defer")
	}

	release()
	release() // idempotent: the deferred call may race other exit paths

	reacquire, reacquireOK := acquireAgentSlot(ctx, deps, dept, "engineer")
	if !reacquireOK || reacquire == nil {
		t.Fatalf("acquire after release: ok=%v releaseNil=%v; want held slot", reacquireOK, reacquire == nil)
	}
}

// TestPerAgentSlotIndependentOfDepartmentCap — two different agents in
// one department each hold their own slot concurrently: the per-agent
// gate never serializes a department. Department-level parallelism is
// bounded only by concurrency.CheckCap, which is untouched
// (Constitution X).
func TestPerAgentSlotIndependentOfDepartmentCap(t *testing.T) {
	ctx := context.Background()
	deptID := inflightUUID(1)
	deps := inflightTestDeps(t, []store.Agent{
		inflightAgentRow(inflightUUID(0xA1), deptID, "engineer"),
		inflightAgentRow(inflightUUID(0xB2), deptID, "qa-engineer"),
	})
	dept := store.Department{ID: deptID}

	engRelease, engOK := acquireAgentSlot(ctx, deps, dept, "engineer")
	if !engOK || engRelease == nil {
		t.Fatalf("engineer acquire: ok=%v releaseNil=%v; want held slot", engOK, engRelease == nil)
	}
	qaRelease, qaOK := acquireAgentSlot(ctx, deps, dept, "qa-engineer")
	if !qaOK || qaRelease == nil {
		t.Fatalf("qa-engineer acquire while engineer held: ok=%v releaseNil=%v; want held slot", qaOK, qaRelease == nil)
	}
}
