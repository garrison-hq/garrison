//go:build integration

// M7 T018 / US2 — migration integration test for the M2.x → M7
// grandfathering flow. Exercises spec US2 in two phases:
//   1. Seed two M2.x-shape agents (engineer + qa-engineer) with
//      last_grandfathered_at IS NULL.
//   2. Invoke migrate7.Run with a fake Controller; assert each
//      row's last_grandfathered_at is now non-NULL, the audit row
//      lands under verb='grandfathered_at_m7', and a second invocation
//      is a no-op (idempotence).

package supervisor_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/agentcontainer"
	"github.com/garrison-hq/garrison/supervisor/internal/migrate7"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/jackc/pgx/v5/pgtype"
)

type fakeMigrationController struct {
	createdIDs []string
	startedIDs []string
}

func (f *fakeMigrationController) Create(ctx context.Context, spec agentcontainer.ContainerSpec) (string, error) {
	id := "fake-" + spec.AgentID
	f.createdIDs = append(f.createdIDs, id)
	return id, nil
}
func (f *fakeMigrationController) Start(ctx context.Context, id string) error {
	f.startedIDs = append(f.startedIDs, id)
	return nil
}
func (f *fakeMigrationController) Stop(ctx context.Context, id string) error   { return nil }
func (f *fakeMigrationController) Remove(ctx context.Context, id string) error { return nil }
func (f *fakeMigrationController) Exec(ctx context.Context, id string, cmd []string, stdin io.Reader) (io.ReadCloser, io.ReadCloser, error) {
	return nil, nil, nil
}
func (f *fakeMigrationController) ConnectNetwork(ctx context.Context, id, name string) error {
	return nil
}
func (f *fakeMigrationController) Reconcile(ctx context.Context, expected []agentcontainer.ExpectedContainer) (agentcontainer.ReconcileReport, error) {
	return agentcontainer.ReconcileReport{}, nil
}
func (f *fakeMigrationController) ImageDigest(ctx context.Context, ref string) (string, error) {
	return "sha256:fakedigest", nil
}

func TestM7MigrationGrandfathersM2xAgentsAndIsIdempotent(t *testing.T) {
	pool := testdb.Start(t)
	ctx := context.Background()

	if _, err := pool.Exec(ctx,
		`TRUNCATE chat_mutation_audit, hiring_proposals, chat_messages, chat_sessions,
		         agent_install_journal, agent_container_events, agents, departments, companies CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	var companyID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO companies (name) VALUES ('m7 migration co') RETURNING id`).Scan(&companyID); err != nil {
		t.Fatalf("seed company: %v", err)
	}
	var deptID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO departments (company_id, slug, name, workspace_path)
		 VALUES ($1, 'engineering', 'Engineering', '/tmp/m7-mig')
		 RETURNING id`, companyID).Scan(&deptID); err != nil {
		t.Fatalf("seed dept: %v", err)
	}

	insertAgent := func(role string) {
		if _, err := pool.Exec(ctx,
			`INSERT INTO agents (department_id, role_slug, listens_for, agent_md, model, status)
			 VALUES ($1, $2, '["work.ticket.created.engineering.todo"]'::jsonb, 'agent prose', 'claude-x', 'active')`,
			deptID, role); err != nil {
			t.Fatalf("seed agent %s: %v", role, err)
		}
	}
	insertAgent("engineer")
	insertAgent("qa-engineer")

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctrl := &fakeMigrationController{}
	deps := migrate7.Deps{
		Pool:        pool,
		Queries:     store.New(pool),
		Controller:  ctrl,
		Logger:      logger,
		ImageRef:    "garrison-claude:m5",
		UIDStart:    1000,
		UIDEnd:      1999,
		WorkspaceFS: "/var/lib/garrison/workspaces",
		SkillsFS:    "/var/lib/garrison/skills",
		Memory:      "512m",
		CPUs:        "1.0",
		PIDsLimit:   200,
	}

	if err := migrate7.Run(ctx, deps); err != nil {
		t.Fatalf("first migrate7.Run: %v", err)
	}
	if len(ctrl.createdIDs) != 2 {
		t.Errorf("Create call count = %d; want 2", len(ctrl.createdIDs))
	}
	if len(ctrl.startedIDs) != 2 {
		t.Errorf("Start call count = %d; want 2", len(ctrl.startedIDs))
	}

	// Both agents now have last_grandfathered_at set + image_digest set.
	for _, role := range []string{"engineer", "qa-engineer"} {
		var (
			lgAt        pgtype.Timestamptz
			imageDigest *string
			hostUID     *int32
		)
		if err := pool.QueryRow(ctx,
			`SELECT last_grandfathered_at, image_digest, host_uid FROM agents WHERE role_slug = $1`,
			role).Scan(&lgAt, &imageDigest, &hostUID); err != nil {
			t.Fatalf("readback %s: %v", role, err)
		}
		if !lgAt.Valid {
			t.Errorf("%s: last_grandfathered_at not set", role)
		}
		if imageDigest == nil || *imageDigest == "" {
			t.Errorf("%s: image_digest empty", role)
		}
		if hostUID == nil || *hostUID < 1000 || *hostUID > 1999 {
			t.Errorf("%s: host_uid out of range: %v", role, hostUID)
		}
	}

	// Audit rows: two grandfathered_at_m7 rows.
	var audCount int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM chat_mutation_audit WHERE verb = 'grandfathered_at_m7'`).Scan(&audCount); err != nil {
		t.Fatalf("audit count: %v", err)
	}
	if audCount != 2 {
		t.Errorf("audit count = %d; want 2", audCount)
	}

	// agent_container_events rows: two 'migrated' events.
	var eventCount int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM agent_container_events WHERE kind = 'migrated'`).Scan(&eventCount); err != nil {
		t.Fatalf("event count: %v", err)
	}
	if eventCount != 2 {
		t.Errorf("agent_container_events count = %d; want 2", eventCount)
	}

	// Idempotence: second invocation is a no-op (no extra container
	// creates, no extra audit rows).
	priorCreate := len(ctrl.createdIDs)
	if err := migrate7.Run(ctx, deps); err != nil {
		t.Fatalf("second migrate7.Run: %v", err)
	}
	if len(ctrl.createdIDs) != priorCreate {
		t.Errorf("idempotence broken: Create called %d times on second run", len(ctrl.createdIDs)-priorCreate)
	}
}
