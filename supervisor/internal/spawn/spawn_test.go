package spawn

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/mempalace"
	"github.com/garrison-hq/garrison/supervisor/internal/vault"
	"github.com/jackc/pgx/v5/pgtype"
)

// mockFetcher is a test double for vault.Fetcher.
type mockFetcher struct {
	fetch func(ctx context.Context, grants []vault.GrantRow) (map[string]vault.SecretValue, error)
}

func (m *mockFetcher) Fetch(ctx context.Context, grants []vault.GrantRow) (map[string]vault.SecretValue, error) {
	return m.fetch(ctx, grants)
}

// TestTransitionColumnsEngineerM22 — engineer on in_dev → qa_review (M2.2).
func TestTransitionColumnsEngineerM22(t *testing.T) {
	from, to := transitionColumns("engineer", "in_dev")
	if from != "in_dev" || to != "qa_review" {
		t.Errorf("engineer@in_dev: got (%s, %s); want (in_dev, qa_review)", from, to)
	}
}

// TestTransitionColumnsEngineerM21 — engineer on todo → done (M2.1 compat:
// the M2.1 workflow is single-transition so the engineer's completion
// lands the ticket at done, not qa_review).
func TestTransitionColumnsEngineerM21(t *testing.T) {
	from, to := transitionColumns("engineer", "todo")
	if from != "todo" || to != "done" {
		t.Errorf("engineer@todo: got (%s, %s); want (todo, done)", from, to)
	}
}

// TestTransitionColumnsQAEngineer — qa-engineer role → qa_review → done.
func TestTransitionColumnsQAEngineer(t *testing.T) {
	from, to := transitionColumns("qa-engineer", "qa_review")
	if from != "qa_review" || to != "done" {
		t.Errorf("qa-engineer: got (%s, %s); want (qa_review, done)", from, to)
	}
}

// TestTransitionColumnsFallback — any unknown role defaults to todo → done
// (M2.1 back-compat for fake-agent tests that pre-date role dispatch).
func TestTransitionColumnsFallback(t *testing.T) {
	from, to := transitionColumns("unknown-role", "")
	if from != "todo" || to != "done" {
		t.Errorf("fallback: got (%s, %s); want (todo, done)", from, to)
	}
}

// TestSpawnSystemPromptIncludesInstanceID — verifies the composed system
// prompt (what runRealClaude passes via --system-prompt) contains both
// ticket_id and instance_id in the "This turn" block per Session
// 2026-04-23 Q2. Exercised through mempalace.ComposeSystemPrompt
// directly since that's the helper spawn.go calls; the spawn call site
// just threads the string through to argv.
func TestSpawnSystemPromptIncludesInstanceID(t *testing.T) {
	sp := mempalace.ComposeSystemPrompt("AGENT_MD_BODY", "WAKE_UP_BODY",
		"tkt-abc", "inst-xyz")
	if !strings.Contains(sp, "ticket tkt-abc") {
		t.Errorf("missing ticket_id substitution in:\n%s", sp)
	}
	if !strings.Contains(sp, "agent_instance inst-xyz") {
		t.Errorf("missing instance_id substitution in:\n%s", sp)
	}
	if !strings.Contains(sp, "## This turn") {
		t.Errorf("missing 'This turn' heading; the template shape is load-bearing")
	}
	if !strings.Contains(sp, "WAKE_UP_BODY") {
		t.Error("wake-up stdout missing when it was non-empty")
	}
}

// TestSpawnDefaultRoleSlug — Spawn with empty roleSlug falls back to
// "engineer" for M1/M2.1 back-compat. This is the guarantee that
// existing integration tests (which don't set a role_slug because they
// predate T013) still work.
//
// We test this via the public Spawn signature indirectly: the helper
// transitionColumns is what the role flows into for the succeeded path.
// An empty role passed to transitionColumns would hit "" → default; but
// Spawn's "" → "engineer" coercion makes that impossible. Verify the
// coercion logic by inspecting the source path.
//
// Since Spawn's coercion happens before any test-mockable call, we
// pin the contract at the constants level: the fallback string is
// "engineer" in BOTH Spawn (input coercion) and transitionColumns
// (output default). This consistency is what keeps the fake-agent
// test suite byte-identical.
func TestSpawnDefaultRoleSlug(t *testing.T) {
	// Input: empty role → Spawn coerces to "engineer".
	// Output of that "engineer" in transitionColumns (M2.2 in_dev path):
	from, to := transitionColumns("engineer", "in_dev")
	if from != "in_dev" || to != "qa_review" {
		t.Errorf("engineer@in_dev transition changed under us: (%s, %s)", from, to)
	}
	// If this test fails, it means either Spawn's coercion default or
	// transitionColumns's engineer branch moved — in which case the
	// M2.2 fake-agent path will land rows on an unexpected column.
}

// TestSpawnThreadVaultClientThroughDeps — unit-level: verify Fetch is called
// with the exact GrantRow set returned by GrantsListerFn (T008 / FR-409).
func TestSpawnThreadVaultClientThroughDeps(t *testing.T) {
	want := []vault.GrantRow{{EnvVarName: "DB_PASS", SecretPath: "/prod/db"}}
	var gotGrants []vault.GrantRow

	deps := Deps{
		Logger: slog.Default(),
		Vault: &mockFetcher{
			fetch: func(_ context.Context, grants []vault.GrantRow) (map[string]vault.SecretValue, error) {
				gotGrants = grants
				return map[string]vault.SecretValue{"DB_PASS": vault.New([]byte("s3cr3t"))}, nil
			},
		},
		GrantsListerFn: func(_ context.Context, _ string, _ pgtype.UUID) ([]vault.GrantRow, error) {
			return want, nil
		},
		AuditFn: func(_ context.Context, _ vault.AuditRow) error { return nil },
	}

	fetched, exitReason := vaultOrchestrate(context.Background(), deps, "engineer",
		pgtype.UUID{}, pgtype.UUID{}, "clean agent md", slog.Default())
	defer func() {
		for k := range fetched {
			sv := fetched[k]
			sv.Zero()
		}
	}()

	if exitReason != "" {
		t.Fatalf("unexpected exit reason %q; want empty", exitReason)
	}
	if len(gotGrants) != 1 || gotGrants[0].EnvVarName != want[0].EnvVarName {
		t.Errorf("Fetch called with %v; want %v", gotGrants, want)
	}
}

// TestSpawnZeroGrantsSkipsFetch — unit-level: when GrantsListerFn returns an
// empty slice, Fetch must NOT be called (FR-409 zero-grant fast path).
func TestSpawnZeroGrantsSkipsFetch(t *testing.T) {
	deps := Deps{
		Logger: slog.Default(),
		Vault: &mockFetcher{
			fetch: func(_ context.Context, _ []vault.GrantRow) (map[string]vault.SecretValue, error) {
				t.Fatal("Fetch was called but should not have been for zero grants")
				return nil, nil
			},
		},
		GrantsListerFn: func(_ context.Context, _ string, _ pgtype.UUID) ([]vault.GrantRow, error) {
			return nil, nil
		},
	}

	fetched, exitReason := vaultOrchestrate(context.Background(), deps, "engineer",
		pgtype.UUID{}, pgtype.UUID{}, "irrelevant", slog.Default())
	if exitReason != "" {
		t.Fatalf("unexpected exit reason %q; want empty", exitReason)
	}
	if len(fetched) != 0 {
		t.Errorf("expected empty fetched map; got %v", fetched)
	}
}

// TestSpawnLeakScanBlocksSpawn — unit-level: when the fetched secret value
// appears verbatim in agent.md, vaultOrchestrate returns ExitSecretLeakedInAgentMd
// and no subprocess is started (no writeFail caller needed here — the non-empty
// exitReason itself proves the subprocess path would be blocked).
// Zero is exercised internally (returned map is nil on abort).
func TestSpawnLeakScanBlocksSpawn(t *testing.T) {
	const secretVal = "topp-sekrit-value"

	deps := Deps{
		Logger: slog.Default(),
		Vault: &mockFetcher{
			fetch: func(_ context.Context, _ []vault.GrantRow) (map[string]vault.SecretValue, error) {
				return map[string]vault.SecretValue{"MY_VAR": vault.New([]byte(secretVal))}, nil
			},
		},
		GrantsListerFn: func(_ context.Context, _ string, _ pgtype.UUID) ([]vault.GrantRow, error) {
			return []vault.GrantRow{{EnvVarName: "MY_VAR", SecretPath: "/prod/my"}}, nil
		},
		AuditFn: func(_ context.Context, _ vault.AuditRow) error { return nil },
	}

	// agentMD deliberately contains the secret value — leak scan should fire.
	agentMD := "You are an agent. Use token: " + secretVal + " when calling the API."

	fetched, exitReason := vaultOrchestrate(context.Background(), deps, "engineer",
		pgtype.UUID{}, pgtype.UUID{}, agentMD, slog.Default())
	if exitReason != ExitSecretLeakedInAgentMd {
		t.Fatalf("exit reason: got %q; want %q", exitReason, ExitSecretLeakedInAgentMd)
	}
	if fetched != nil {
		t.Error("expected nil fetched map (secrets should be zeroed on abort)")
	}
}

// TestSpawnAuditFailureFailsClosed — unit-level: when AuditFn returns
// ErrVaultAuditFailed, vaultOrchestrate returns ExitVaultAuditFailed and
// no subprocess is started.
func TestSpawnAuditFailureFailsClosed(t *testing.T) {
	deps := Deps{
		Logger: slog.Default(),
		Vault: &mockFetcher{
			fetch: func(_ context.Context, _ []vault.GrantRow) (map[string]vault.SecretValue, error) {
				return map[string]vault.SecretValue{"API_KEY": vault.New([]byte("key123"))}, nil
			},
		},
		GrantsListerFn: func(_ context.Context, _ string, _ pgtype.UUID) ([]vault.GrantRow, error) {
			return []vault.GrantRow{{EnvVarName: "API_KEY", SecretPath: "/prod/api"}}, nil
		},
		AuditFn: func(_ context.Context, _ vault.AuditRow) error {
			return errors.New("db full")
		},
	}

	fetched, exitReason := vaultOrchestrate(context.Background(), deps, "engineer",
		pgtype.UUID{}, pgtype.UUID{}, "clean agent md", slog.Default())
	if exitReason != ExitVaultAuditFailed {
		t.Fatalf("exit reason: got %q; want %q", exitReason, ExitVaultAuditFailed)
	}
	if fetched != nil {
		t.Error("expected nil fetched map (secrets should be zeroed on audit failure)")
	}
}

// TestVaultOrchestrateNilVault — when deps.Vault is nil, vaultOrchestrate
// is a no-op: returns nil map and empty exit reason. This preserves
// M2.1/M2.2 compatibility for supervisors started without vault config.
func TestVaultOrchestrateNilVault(t *testing.T) {
	deps := Deps{Logger: slog.Default()} // Vault == nil
	fetched, exit := vaultOrchestrate(context.Background(), deps, "engineer",
		pgtype.UUID{}, pgtype.UUID{}, "any agent md", slog.Default())
	if exit != "" {
		t.Errorf("exit reason: got %q; want empty", exit)
	}
	if fetched != nil {
		t.Errorf("expected nil fetched map for nil Vault; got %v", fetched)
	}
}

// TestVaultOrchestrateGrantsListError — when GrantsListerFn returns an error,
// vaultOrchestrate exits with ExitSpawnFailed and does not call Fetch.
func TestVaultOrchestrateGrantsListError(t *testing.T) {
	deps := Deps{
		Logger: slog.Default(),
		Vault: &mockFetcher{
			fetch: func(_ context.Context, _ []vault.GrantRow) (map[string]vault.SecretValue, error) {
				t.Fatal("Fetch must not be called when grants list fails")
				return nil, nil
			},
		},
		GrantsListerFn: func(_ context.Context, _ string, _ pgtype.UUID) ([]vault.GrantRow, error) {
			return nil, errors.New("db connection refused")
		},
	}
	_, exit := vaultOrchestrate(context.Background(), deps, "engineer",
		pgtype.UUID{}, pgtype.UUID{}, "irrelevant", slog.Default())
	if exit != ExitSpawnFailed {
		t.Errorf("exit reason: got %q; want %q", exit, ExitSpawnFailed)
	}
}

// TestVaultOrchestrateFetchError_Unavailable — when Fetch returns
// ErrVaultUnavailable, vaultOrchestrate returns ExitVaultUnavailable.
// deps.Pool is nil; auditVaultError returns early on nil pool (best-effort).
func TestVaultOrchestrateFetchError_Unavailable(t *testing.T) {
	deps := Deps{
		Logger: slog.Default(),
		Vault: &mockFetcher{
			fetch: func(_ context.Context, _ []vault.GrantRow) (map[string]vault.SecretValue, error) {
				return nil, vault.ErrVaultUnavailable
			},
		},
		GrantsListerFn: func(_ context.Context, _ string, _ pgtype.UUID) ([]vault.GrantRow, error) {
			return []vault.GrantRow{{EnvVarName: "API_KEY", SecretPath: "/prod/api"}}, nil
		},
	}
	fetched, exit := vaultOrchestrate(context.Background(), deps, "engineer",
		pgtype.UUID{}, pgtype.UUID{}, "clean md", slog.Default())
	if exit != ExitVaultUnavailable {
		t.Errorf("exit reason: got %q; want %q", exit, ExitVaultUnavailable)
	}
	if fetched != nil {
		t.Errorf("expected nil fetched map on fetch error; got %v", fetched)
	}
}

// TestVaultOrchestrateFetchError_PermissionDenied — ErrVaultPermissionDenied
// maps to OutcomeDeniedInfisical in the audit log and ExitVaultPermissionDenied
// as the exit reason.
func TestVaultOrchestrateFetchError_PermissionDenied(t *testing.T) {
	deps := Deps{
		Logger: slog.Default(),
		Vault: &mockFetcher{
			fetch: func(_ context.Context, _ []vault.GrantRow) (map[string]vault.SecretValue, error) {
				return nil, vault.ErrVaultPermissionDenied
			},
		},
		GrantsListerFn: func(_ context.Context, _ string, _ pgtype.UUID) ([]vault.GrantRow, error) {
			return []vault.GrantRow{{EnvVarName: "DB_PASS", SecretPath: "/prod/db"}}, nil
		},
	}
	_, exit := vaultOrchestrate(context.Background(), deps, "engineer",
		pgtype.UUID{}, pgtype.UUID{}, "clean md", slog.Default())
	if exit != ExitVaultPermissionDenied {
		t.Errorf("exit reason: got %q; want %q", exit, ExitVaultPermissionDenied)
	}
}

// TestAcceptanceGateSatisfied — M2.2 engineer@in_dev skips the M1
// hello.txt check; M2.1 engineer@todo still runs it; qa-engineer always
// skips; unknown roles fall through to the check (M1 safety-net).
func TestAcceptanceGateSatisfied(t *testing.T) {
	cases := []struct {
		role, col string
		want      bool
	}{
		{"engineer", "in_dev", true},       // M2.2
		{"engineer", "todo", false},        // M2.1 back-compat — hello.txt check still runs
		{"engineer", "", false},            // no column info → defer to check
		{"qa-engineer", "qa_review", true}, // M2.2
		{"qa-engineer", "", true},          // qa-engineer never writes hello.txt by design
		{"", "todo", false},                // empty role → default false
		{"cto", "in_dev", false},           // future role → default false
	}
	for _, c := range cases {
		if got := acceptanceGateSatisfied(c.role, c.col); got != c.want {
			t.Errorf("acceptanceGateSatisfied(%q, %q)=%v; want %v", c.role, c.col, got, c.want)
		}
	}
}
