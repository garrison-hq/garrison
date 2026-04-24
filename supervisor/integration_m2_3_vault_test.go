//go:build integration

// M2.3 vault integration tests.
//
// T012: TestVaultSpawnWithSingleSecret — golden path; one secret injected,
//       SC-401 log-clean assertions, vault_access_log + secret_metadata
//       assertions, workspace env-var-injection proof.
//
// Requires the spike-mempalace + spike-docker-proxy containers running
// (same prerequisite as integration_m2_2_happy_path_test.go). The Infisical
// stack is started automatically via testcontainers-go.
//
// Run:
//   go test -tags=integration -count=1 -timeout=600s \
//           -run='TestVaultSpawnWithSingleSecret' .

package supervisor_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/garrison-hq/garrison/supervisor/internal/vault"
	"github.com/jackc/pgx/v5/pgtype"
)

// TestVaultSpawnWithSingleSecret is the M2.3 golden-path integration test
// (T012). It boots the full stack — testdb + spike MemPalace + docker-proxy +
// Infisical testcontainer — seeds one secret and one grant, spawns the
// engineer role, and asserts all SC-401 vault invariants.
func TestVaultSpawnWithSingleSecret(t *testing.T) {
	requireSpikeStack(t)

	pool := testdb.Start(t)
	workspace := t.TempDir()
	_, _ = testdb.SeedM22(t, workspace)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	// Vault tables are not included in testdb's default TRUNCATE. Clean them
	// so state from other tests does not interfere.
	_, _ = pool.Exec(ctx, "TRUNCATE vault_access_log, agent_role_secrets, secret_metadata")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "TRUNCATE vault_access_log, agent_role_secrets, secret_metadata")
	})

	var companyID pgtype.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM companies LIMIT 1`).Scan(&companyID); err != nil {
		t.Fatalf("lookup company: %v", err)
	}
	companyUUID := uuidString(companyID)

	var deptID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`SELECT id FROM departments WHERE slug='engineering'`,
	).Scan(&deptID); err != nil {
		t.Fatalf("lookup dept: %v", err)
	}

	// Boot Infisical testcontainer.
	harness := vault.StartInfisical(t)

	// Create a Machine Identity for the supervisor.
	mlClientID, mlClientSecret, err := harness.CreateMachineIdentity("garrison-m23-test-ml")
	if err != nil {
		t.Fatalf("CreateMachineIdentity: %v", err)
	}

	// Seed the Infisical secret.
	const secretValue = "infisical-golden-path-secret-abc123xyz789"
	folderPath := "/" + companyUUID + "/operator"
	const secretKey = "EXAMPLE_API_KEY"
	if err := harness.SeedSecret(folderPath, secretKey, secretValue); err != nil {
		t.Fatalf("SeedSecret: %v", err)
	}
	fullSecretPath := folderPath + "/" + secretKey

	// Seed vault tables.
	if _, err := pool.Exec(ctx,
		`INSERT INTO agent_role_secrets (role_slug, env_var_name, secret_path, customer_id, granted_by)
		 VALUES ('engineer', 'EXAMPLE_API_KEY', $1, $2, 'test-operator')`,
		fullSecretPath, companyID,
	); err != nil {
		t.Fatalf("insert agent_role_secrets: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO secret_metadata (secret_path, customer_id, provenance, rotation_cadence)
		 VALUES ($1, $2, 'test-operator', '90 days')`,
		fullSecretPath, companyID,
	); err != nil {
		t.Fatalf("insert secret_metadata: %v", err)
	}

	// Set DB passwords required by the supervisor's subcomponents.
	mempalacePw := "m23-vault-test-pw"
	testdb.SetAgentMempalacePassword(t, mempalacePw)
	testdb.SetAgentROPassword(t, "m23-vault-ro-test-pw")

	bin := buildSupervisorBinary(t)
	mockBin := buildMockClaudeBinary(t)
	engineerScript := mockClaudeScriptPath(t, "m2_3_vault_uses_env_var.ndjson")

	mcpDir := t.TempDir()
	if err := os.Chmod(mcpDir, 0o750); err != nil {
		t.Fatalf("chmod mcp dir: %v", err)
	}

	logSink := newLogSink()
	healthPort := mustFreePort(t)
	env := append(os.Environ(),
		"GARRISON_DATABASE_URL="+testdb.URL(t),
		fmt.Sprintf("GARRISON_HEALTH_PORT=%d", healthPort),
		"GARRISON_CLAUDE_BIN="+mockBin,
		"GARRISON_AGENT_RO_PASSWORD=m23-vault-ro-test-pw",
		"GARRISON_AGENT_MEMPALACE_PASSWORD="+mempalacePw,
		"GARRISON_MCP_CONFIG_DIR="+mcpDir,
		"GARRISON_MEMPALACE_CONTAINER="+m22MempalaceContainer,
		"GARRISON_PALACE_PATH=/palace",
		"DOCKER_HOST="+m22DockerProxyHost,
		"GARRISON_MOCK_CLAUDE_SCRIPT_ENGINEER="+engineerScript,
		"GARRISON_SUBPROCESS_TIMEOUT=30s",
		"GARRISON_HYGIENE_DELAY=1s",
		"GARRISON_HYGIENE_SWEEP_INTERVAL=3s",
		"GARRISON_FINALIZE_WRITE_TIMEOUT=30s",
		"GARRISON_CLAUDE_BUDGET_USD=0.10",
		"GARRISON_LOG_LEVEL=info",
		// M2.3 Infisical credentials from the testcontainer harness.
		"GARRISON_INFISICAL_ADDR="+harness.URL(),
		"GARRISON_INFISICAL_CLIENT_ID="+mlClientID,
		"GARRISON_INFISICAL_CLIENT_SECRET="+mlClientSecret,
		"GARRISON_INFISICAL_PROJECT_ID="+harness.ProjectID(),
		"GARRISON_INFISICAL_ENVIRONMENT="+harness.Environment(),
		// Override customer_id resolution to avoid the DB query at startup.
		"GARRISON_CUSTOMER_ID="+companyUUID,
	)

	cmd := exec.CommandContext(ctx, bin)
	cmd.Env = env
	cmd.Dir = workspace
	var stdout safeBuffer
	cmd.Stdout = io.MultiWriter(os.Stdout, logSink, &stdout)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("supervisor start: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		_ = cmd.Wait()
		t.Logf("supervisor stdout tail:\n%s", tail(stdout.String(), 5000))
	})

	if err := waitForHealth(healthPort, 20*time.Second); err != nil {
		t.Fatalf("supervisor health: %v", err)
	}

	// Insert ticket at in_dev — the engineer listens on
	// work.ticket.created.engineering.in_dev.
	var ticketID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO tickets (id, department_id, objective, column_slug)
		VALUES (gen_random_uuid(), $1, 'M2.3 vault golden path', 'in_dev')
		RETURNING id`, deptID,
	).Scan(&ticketID); err != nil {
		t.Fatalf("insert ticket: %v", err)
	}
	t.Logf("ticket: %s", uuidString(ticketID))

	// Wait for the engineer agent_instance to reach a terminal state.
	waitForAgentInstanceCount(ctx, t, pool, ticketID, 1, 90*time.Second)

	// (1) Assert agent_instances.status='succeeded', exit_reason='completed'.
	var status string
	var exitReason *string
	if err := pool.QueryRow(ctx, `
		SELECT status, exit_reason FROM agent_instances
		WHERE ticket_id=$1`, ticketID,
	).Scan(&status, &exitReason); err != nil {
		t.Fatalf("query agent_instances: %v", err)
	}
	if status != "succeeded" {
		t.Errorf("agent_instances.status=%q; want succeeded (exit_reason=%v)", status, exitReason)
	}
	if exitReason != nil && *exitReason != "completed" {
		t.Errorf("agent_instances.exit_reason=%q; want 'completed'", *exitReason)
	}

	// (2) Assert vault_access_log: one row, outcome='granted', correct path.
	var logOutcome, logPath string
	var logCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*), outcome, secret_path
		FROM vault_access_log
		WHERE ticket_id=$1
		GROUP BY outcome, secret_path`, ticketID,
	).Scan(&logCount, &logOutcome, &logPath); err != nil {
		t.Fatalf("query vault_access_log: %v (want one row for ticket)", err)
	}
	if logCount != 1 {
		t.Errorf("vault_access_log row count=%d; want 1", logCount)
	}
	if logOutcome != "granted" {
		t.Errorf("vault_access_log.outcome=%q; want 'granted'", logOutcome)
	}
	if logPath != fullSecretPath {
		t.Errorf("vault_access_log.secret_path=%q; want %q", logPath, fullSecretPath)
	}

	// (3) Assert secret_metadata.last_accessed_at updated after OutcomeGranted.
	var lastAccessed pgtype.Timestamptz
	if err := pool.QueryRow(ctx, `
		SELECT last_accessed_at FROM secret_metadata WHERE secret_path=$1`,
		fullSecretPath,
	).Scan(&lastAccessed); err != nil {
		t.Fatalf("query secret_metadata: %v", err)
	}
	if !lastAccessed.Valid {
		t.Error("secret_metadata.last_accessed_at is NULL after OutcomeGranted; want non-NULL")
	}

	// (4) Assert hygiene_status on the ticket transition. The finalize
	// handler's pattern scanner should find no secrets in the diary entry
	// → hygiene_status='clean'. This is best-effort: if MemPalace is
	// unreachable the status may be 'missing_diary'; assert it is NOT
	// 'suspected_secret_emitted' (the scanner must not false-positive on
	// a clean diary).
	var hygieneStatus *string
	_ = waitFor(ctx, 15*time.Second, func() (bool, error) {
		var h *string
		err := pool.QueryRow(ctx, `
			SELECT hygiene_status FROM ticket_transitions
			WHERE ticket_id=$1 ORDER BY at DESC LIMIT 1`, ticketID,
		).Scan(&h)
		if err != nil || h == nil {
			return false, nil
		}
		hygieneStatus = h
		return true, nil
	})
	if hygieneStatus != nil {
		t.Logf("ticket_transitions.hygiene_status=%q", *hygieneStatus)
		if *hygieneStatus == "suspected_secret_emitted" {
			t.Errorf("hygiene_status='suspected_secret_emitted'; want 'clean' (scanner false-positive on clean diary?)")
		}
	} else {
		t.Log("ticket_transitions.hygiene_status not yet set (MemPalace timing); best-effort assertion skipped")
	}

	// (5) SC-401: supervisor stdout must contain ZERO occurrences of the raw
	// secret value. This is the core SC-401 assertion — the vault package's
	// SecretValue.LogValue() returns "[REDACTED]" so no slog call can emit
	// the raw bytes.
	if strings.Contains(stdout.String(), secretValue) {
		t.Errorf("SC-401 FAIL: supervisor stdout contains raw secret value %q; check slog call sites", secretValue)
	}

	// (6) MCP config file: supervisor cleans up the per-invocation config
	// file after spawn. Any residual file must not contain the raw value.
	if entries, err := os.ReadDir(mcpDir); err == nil {
		for _, e := range entries {
			data, _ := os.ReadFile(filepath.Join(mcpDir, e.Name()))
			if strings.Contains(string(data), secretValue) {
				t.Errorf("MCP config file %s contains raw secret value (SC-401)", e.Name())
			}
		}
	}

	// (7) Workspace output file written by mockclaude's #write-env-to-file
	// directive contains the raw secret value, proving the supervisor
	// injected EXAMPLE_API_KEY into the subprocess environment.
	outFile := filepath.Join(workspace, "vault_secret_used.txt")
	got, err := os.ReadFile(outFile)
	if err != nil {
		t.Errorf("vault_secret_used.txt missing from workspace %s: %v (env var injection not proven)", workspace, err)
	} else if string(got) != secretValue {
		t.Errorf("vault_secret_used.txt=%q; want %q (env var injection failed)", string(got), secretValue)
	}
}
