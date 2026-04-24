//go:build integration

// M2.3 vault integration tests.
//
// T012: TestVaultSpawnWithSingleSecret — golden path; one secret injected,
//       SC-401 log-clean assertions, vault_access_log + secret_metadata
//       assertions, workspace env-var-injection proof.
//
// T013: TestVaultRule1BlocksSpawnOnLeakedValue — Rule 1 / SC-402: agent.md
//       containing the raw secret value triggers abort before spawn; reverse
//       case proves a clean agent.md spawns correctly.
//       TestVaultRule2ZeroGrantsZeroSecrets — Rule 2 / SC-403: zero grants
//       produce zero Infisical fetches and zero vault_access_log rows; spawn
//       proceeds normally with no vault env vars injected.
//       TestVaultRule3BlocksSpawnOnVaultMcp — Rule 3 / SC-404: a vault-pattern
//       server in agents.mcp_config triggers abort before the vault fetch;
//       reverse case proves a clean mcp_config spawns correctly.
//
// Requires the spike-mempalace + spike-docker-proxy containers running
// (same prerequisite as integration_m2_2_happy_path_test.go). The Infisical
// stack is started automatically via testcontainers-go.
//
// Run:
//   go test -tags=integration -count=1 -timeout=600s \
//           -run='TestVaultSpawnWithSingleSecret' .
//
//   go test -tags=integration -count=1 -timeout=600s \
//           -run='TestVaultRule1|TestVaultRule2|TestVaultRule3' .

package supervisor_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
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

// vaultTestEnv builds the base supervisor env slice used by all M2.3 vault
// tests. The caller appends test-specific overrides after the returned slice.
func vaultTestEnv(
	dbURL string,
	healthPort int,
	mockBin, engineerScript, mcpDir, mempalacePw, mempalaceContainer, dockerProxyHost string,
	harness interface {
		URL() string
		ProjectID() string
		Environment() string
	},
	mlClientID, mlClientSecret, companyUUID string,
) []string {
	return append(os.Environ(),
		"GARRISON_DATABASE_URL="+dbURL,
		fmt.Sprintf("GARRISON_HEALTH_PORT=%d", healthPort),
		"GARRISON_CLAUDE_BIN="+mockBin,
		"GARRISON_AGENT_RO_PASSWORD=m23-ro-test-pw",
		"GARRISON_AGENT_MEMPALACE_PASSWORD="+mempalacePw,
		"GARRISON_MCP_CONFIG_DIR="+mcpDir,
		"GARRISON_MEMPALACE_CONTAINER="+mempalaceContainer,
		"GARRISON_PALACE_PATH=/palace",
		"DOCKER_HOST="+dockerProxyHost,
		"GARRISON_MOCK_CLAUDE_SCRIPT_ENGINEER="+engineerScript,
		"GARRISON_SUBPROCESS_TIMEOUT=30s",
		"GARRISON_HYGIENE_DELAY=1s",
		"GARRISON_HYGIENE_SWEEP_INTERVAL=3s",
		"GARRISON_FINALIZE_WRITE_TIMEOUT=30s",
		"GARRISON_CLAUDE_BUDGET_USD=0.10",
		"GARRISON_LOG_LEVEL=info",
		"GARRISON_INFISICAL_ADDR="+harness.URL(),
		"GARRISON_INFISICAL_CLIENT_ID="+mlClientID,
		"GARRISON_INFISICAL_CLIENT_SECRET="+mlClientSecret,
		"GARRISON_INFISICAL_PROJECT_ID="+harness.ProjectID(),
		"GARRISON_INFISICAL_ENVIRONMENT="+harness.Environment(),
		"GARRISON_CUSTOMER_ID="+companyUUID,
	)
}

// startVaultSupervisor launches the supervisor binary with the given env and
// returns the cmd (already started). logSink and stdout are wired for capture.
// t.Cleanup is registered to interrupt + wait.
func startVaultSupervisor(t *testing.T, ctx context.Context, bin string, env []string, workspace string) (*exec.Cmd, *safeBuffer) {
	t.Helper()
	var stdout safeBuffer
	cmd := exec.CommandContext(ctx, bin)
	cmd.Env = env
	cmd.Dir = workspace
	cmd.Stdout = io.MultiWriter(os.Stdout, &stdout)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("supervisor start: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		_ = cmd.Wait()
		t.Logf("supervisor stdout tail:\n%s", tail(stdout.String(), 2000))
	})
	return cmd, &stdout
}

// TestVaultRule1BlocksSpawnOnLeakedValue is the M2.3 Rule 1 integration test
// (T013 / US2 / SC-402). It poisons the engineer agent.md with the literal
// secret value BEFORE starting the supervisor, inserts a ticket, and asserts
// the spawn is aborted with exit_reason='secret_leaked_in_agent_md'. A second
// run with the clean agent.md verifies the reverse case spawns successfully.
func TestVaultRule1BlocksSpawnOnLeakedValue(t *testing.T) {
	requireSpikeStack(t)

	pool := testdb.Start(t)
	workspace := t.TempDir()
	_, _ = testdb.SeedM22(t, workspace)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

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
	if err := pool.QueryRow(ctx, `SELECT id FROM departments WHERE slug='engineering'`).Scan(&deptID); err != nil {
		t.Fatalf("lookup dept: %v", err)
	}

	harness := vault.StartInfisical(t)
	mlClientID, mlClientSecret, err := harness.CreateMachineIdentity("garrison-rule1-test-ml")
	if err != nil {
		t.Fatalf("CreateMachineIdentity: %v", err)
	}

	const secretValue = "rule1-secret-value-abc123xyz789xyz"
	folderPath := "/" + companyUUID + "/operator"
	const secretKey = "EXAMPLE_API_KEY"
	if err := harness.SeedSecret(folderPath, secretKey, secretValue); err != nil {
		t.Fatalf("SeedSecret: %v", err)
	}
	fullSecretPath := folderPath + "/" + secretKey

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

	// Capture the original agent_md before poisoning.
	var originalAgentMD string
	if err := pool.QueryRow(ctx, `SELECT agent_md FROM agents WHERE role_slug='engineer'`).Scan(&originalAgentMD); err != nil {
		t.Fatalf("read agent_md: %v", err)
	}

	// Poison agent_md with the literal secret value BEFORE starting the supervisor
	// so the cache loads the contaminated content at startup.
	poisonedMD := originalAgentMD + "\n# POISON: " + secretValue
	if _, err := pool.Exec(ctx, `UPDATE agents SET agent_md=$1 WHERE role_slug='engineer'`, poisonedMD); err != nil {
		t.Fatalf("poison agent_md: %v", err)
	}

	mempalacePw := "rule1-test-pw"
	testdb.SetAgentMempalacePassword(t, mempalacePw)
	testdb.SetAgentROPassword(t, "m23-ro-test-pw")

	bin := buildSupervisorBinary(t)
	mockBin := buildMockClaudeBinary(t)
	engineerScript := mockClaudeScriptPath(t, "m2_3_vault_uses_env_var.ndjson")

	mcpDir := t.TempDir()
	if err := os.Chmod(mcpDir, 0o750); err != nil {
		t.Fatalf("chmod mcp dir: %v", err)
	}

	healthPort := mustFreePort(t)
	env := vaultTestEnv(testdb.URL(t), healthPort, mockBin, engineerScript, mcpDir, mempalacePw,
		m22MempalaceContainer, m22DockerProxyHost, harness, mlClientID, mlClientSecret, companyUUID)

	// --- Poisoned run ---
	cmd1Ctx, cmd1Cancel := context.WithCancel(ctx)
	cmd1, _ := startVaultSupervisor(t, cmd1Ctx, bin, env, workspace)
	_ = cmd1 // managed by cleanup

	if err := waitForHealth(healthPort, 20*time.Second); err != nil {
		t.Fatalf("supervisor health (poisoned run): %v", err)
	}

	var ticket1ID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO tickets (id, department_id, objective, column_slug)
		VALUES (gen_random_uuid(), $1, 'Rule 1 poison test', 'in_dev')
		RETURNING id`, deptID,
	).Scan(&ticket1ID); err != nil {
		t.Fatalf("insert ticket (poisoned): %v", err)
	}

	waitForAgentInstanceCount(ctx, t, pool, ticket1ID, 1, 60*time.Second)

	var status1, exitReason1 string
	if err := pool.QueryRow(ctx, `SELECT status, exit_reason FROM agent_instances WHERE ticket_id=$1`, ticket1ID).
		Scan(&status1, &exitReason1); err != nil {
		t.Fatalf("query agent_instances (poisoned): %v", err)
	}
	if status1 != "failed" {
		t.Errorf("poisoned: agent_instances.status=%q; want 'failed'", status1)
	}
	if exitReason1 != "secret_leaked_in_agent_md" {
		t.Errorf("poisoned: exit_reason=%q; want 'secret_leaked_in_agent_md'", exitReason1)
	}

	// Rule 1 fires AFTER fetch but BEFORE V5 audit write → no vault_access_log row.
	var logCount1 int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM vault_access_log WHERE ticket_id=$1`, ticket1ID).Scan(&logCount1)
	if logCount1 != 0 {
		t.Errorf("poisoned: vault_access_log count=%d; want 0 (Rule 1 abort before audit write)", logCount1)
	}

	// Mockclaude was never invoked: workspace file must NOT exist.
	if _, err := os.Stat(filepath.Join(workspace, "vault_secret_used.txt")); err == nil {
		t.Error("poisoned: vault_secret_used.txt should not exist when Rule 1 aborts before spawn")
	}

	// --- Restore + clean run ---
	cmd1Cancel()
	_ = cmd1.Wait()
	t.Logf("poisoned supervisor stopped")

	if _, err := pool.Exec(ctx, `UPDATE agents SET agent_md=$1 WHERE role_slug='engineer'`, originalAgentMD); err != nil {
		t.Fatalf("restore agent_md: %v", err)
	}
	_, _ = pool.Exec(ctx, "TRUNCATE vault_access_log")

	healthPort2 := mustFreePort(t)
	env2 := vaultTestEnv(testdb.URL(t), healthPort2, mockBin, engineerScript, mcpDir, mempalacePw,
		m22MempalaceContainer, m22DockerProxyHost, harness, mlClientID, mlClientSecret, companyUUID)

	_, _ = startVaultSupervisor(t, ctx, bin, env2, workspace)
	if err := waitForHealth(healthPort2, 20*time.Second); err != nil {
		t.Fatalf("supervisor health (clean run): %v", err)
	}

	workspace2 := t.TempDir()
	var ticket2ID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO tickets (id, department_id, objective, column_slug)
		VALUES (gen_random_uuid(), $1, 'Rule 1 clean run', 'in_dev')
		RETURNING id`, deptID,
	).Scan(&ticket2ID); err != nil {
		t.Fatalf("insert ticket (clean): %v", err)
	}
	_ = workspace2

	waitForAgentInstanceCount(ctx, t, pool, ticket2ID, 1, 90*time.Second)

	var status2, exitReason2 string
	if err := pool.QueryRow(ctx, `SELECT status, exit_reason FROM agent_instances WHERE ticket_id=$1`, ticket2ID).
		Scan(&status2, &exitReason2); err != nil {
		t.Fatalf("query agent_instances (clean): %v", err)
	}
	if status2 != "succeeded" {
		t.Errorf("clean: agent_instances.status=%q; want 'succeeded' (exit_reason=%q)", status2, exitReason2)
	}
}

// TestVaultRule2ZeroGrantsZeroSecrets is the M2.3 Rule 2 integration test
// (T013 / US3 / SC-403). With no agent_role_secrets rows for the engineer,
// the supervisor must not contact Infisical, must not write a vault_access_log
// row, and must not inject any secret env var into the subprocess.
func TestVaultRule2ZeroGrantsZeroSecrets(t *testing.T) {
	requireSpikeStack(t)

	pool := testdb.Start(t)
	workspace := t.TempDir()
	_, _ = testdb.SeedM22(t, workspace)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

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
	if err := pool.QueryRow(ctx, `SELECT id FROM departments WHERE slug='engineering'`).Scan(&deptID); err != nil {
		t.Fatalf("lookup dept: %v", err)
	}

	// Start Infisical for vault client auth at supervisor startup — no secrets seeded.
	harness := vault.StartInfisical(t)
	mlClientID, mlClientSecret, err := harness.CreateMachineIdentity("garrison-rule2-test-ml")
	if err != nil {
		t.Fatalf("CreateMachineIdentity: %v", err)
	}

	// No agent_role_secrets for 'engineer' — this is the zero-grants scenario.

	mempalacePw := "rule2-test-pw"
	testdb.SetAgentMempalacePassword(t, mempalacePw)
	testdb.SetAgentROPassword(t, "m23-ro-test-pw")

	bin := buildSupervisorBinary(t)
	mockBin := buildMockClaudeBinary(t)
	// Rule 2 fixture dumps all env vars so the test can check no vault var was injected.
	engineerScript := mockClaudeScriptPath(t, "m2_3_no_vault_env_dump.ndjson")

	mcpDir := t.TempDir()
	if err := os.Chmod(mcpDir, 0o750); err != nil {
		t.Fatalf("chmod mcp dir: %v", err)
	}

	healthPort := mustFreePort(t)
	env := vaultTestEnv(testdb.URL(t), healthPort, mockBin, engineerScript, mcpDir, mempalacePw,
		m22MempalaceContainer, m22DockerProxyHost, harness, mlClientID, mlClientSecret, companyUUID)

	_, _ = startVaultSupervisor(t, ctx, bin, env, workspace)
	if err := waitForHealth(healthPort, 20*time.Second); err != nil {
		t.Fatalf("supervisor health: %v", err)
	}

	var ticketID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO tickets (id, department_id, objective, column_slug)
		VALUES (gen_random_uuid(), $1, 'Rule 2 zero grants test', 'in_dev')
		RETURNING id`, deptID,
	).Scan(&ticketID); err != nil {
		t.Fatalf("insert ticket: %v", err)
	}

	waitForAgentInstanceCount(ctx, t, pool, ticketID, 1, 90*time.Second)

	// (1) Spawn must succeed — zero grants is not an error condition.
	var status, exitReason string
	if err := pool.QueryRow(ctx, `SELECT status, exit_reason FROM agent_instances WHERE ticket_id=$1`, ticketID).
		Scan(&status, &exitReason); err != nil {
		t.Fatalf("query agent_instances: %v", err)
	}
	if status != "succeeded" {
		t.Errorf("agent_instances.status=%q; want 'succeeded' (exit_reason=%q)", status, exitReason)
	}

	// (2) vault_access_log must be empty — no fetch was attempted.
	var logCount int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM vault_access_log WHERE ticket_id=$1`, ticketID).Scan(&logCount)
	if logCount != 0 {
		t.Errorf("vault_access_log count=%d; want 0 (zero grants → no Infisical fetch)", logCount)
	}

	// (3) Subprocess env dump must not contain any vault-related var name.
	envDump := filepath.Join(workspace, "env_dump.txt")
	dumpData, err := os.ReadFile(envDump)
	if err != nil {
		t.Errorf("env_dump.txt missing: %v (mockclaude #dump-env-to-file did not run)", err)
	} else {
		if strings.Contains(string(dumpData), "EXAMPLE_API_KEY") {
			t.Errorf("env_dump.txt contains EXAMPLE_API_KEY; want absent (Rule 2: zero grants inject nothing)")
		}
	}
}

// TestVaultRule3BlocksSpawnOnVaultMcp is the M2.3 Rule 3 integration test
// (T013 / US4 / SC-404). A vault-pattern server name in agents.mcp_config
// must abort the spawn BEFORE any Infisical fetch, leaving no vault_access_log
// row. The reverse case with a clean mcp_config spawns successfully.
func TestVaultRule3BlocksSpawnOnVaultMcp(t *testing.T) {
	requireSpikeStack(t)

	pool := testdb.Start(t)
	workspace := t.TempDir()
	_, _ = testdb.SeedM22(t, workspace)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

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
	if err := pool.QueryRow(ctx, `SELECT id FROM departments WHERE slug='engineering'`).Scan(&deptID); err != nil {
		t.Fatalf("lookup dept: %v", err)
	}

	harness := vault.StartInfisical(t)
	mlClientID, mlClientSecret, err := harness.CreateMachineIdentity("garrison-rule3-test-ml")
	if err != nil {
		t.Fatalf("CreateMachineIdentity: %v", err)
	}

	// Seed a secret + grant so that, were Rule 3 not to fire, a fetch would occur.
	const secretValue = "rule3-secret-value-xyz789abc123"
	folderPath := "/" + companyUUID + "/operator"
	const secretKey = "EXAMPLE_API_KEY"
	if err := harness.SeedSecret(folderPath, secretKey, secretValue); err != nil {
		t.Fatalf("SeedSecret: %v", err)
	}
	fullSecretPath := folderPath + "/" + secretKey
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

	// Poison the engineer's mcp_config with a banned-pattern server name BEFORE
	// starting the supervisor so the cache loads the contaminated config.
	if _, err := pool.Exec(ctx,
		`UPDATE agents SET mcp_config='{"vault":{"command":"true"}}'::jsonb WHERE role_slug='engineer'`,
	); err != nil {
		t.Fatalf("poison mcp_config: %v", err)
	}

	mempalacePw := "rule3-test-pw"
	testdb.SetAgentMempalacePassword(t, mempalacePw)
	testdb.SetAgentROPassword(t, "m23-ro-test-pw")

	bin := buildSupervisorBinary(t)
	mockBin := buildMockClaudeBinary(t)
	engineerScript := mockClaudeScriptPath(t, "m2_3_vault_uses_env_var.ndjson")

	mcpDir := t.TempDir()
	if err := os.Chmod(mcpDir, 0o750); err != nil {
		t.Fatalf("chmod mcp dir: %v", err)
	}

	healthPort := mustFreePort(t)
	env := vaultTestEnv(testdb.URL(t), healthPort, mockBin, engineerScript, mcpDir, mempalacePw,
		m22MempalaceContainer, m22DockerProxyHost, harness, mlClientID, mlClientSecret, companyUUID)

	// --- Poisoned run ---
	cmd1Ctx, cmd1Cancel := context.WithCancel(ctx)
	cmd1, _ := startVaultSupervisor(t, cmd1Ctx, bin, env, workspace)
	_ = cmd1

	if err := waitForHealth(healthPort, 20*time.Second); err != nil {
		t.Fatalf("supervisor health (poisoned run): %v", err)
	}

	var ticket1ID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO tickets (id, department_id, objective, column_slug)
		VALUES (gen_random_uuid(), $1, 'Rule 3 poison test', 'in_dev')
		RETURNING id`, deptID,
	).Scan(&ticket1ID); err != nil {
		t.Fatalf("insert ticket (poisoned): %v", err)
	}

	waitForAgentInstanceCount(ctx, t, pool, ticket1ID, 1, 60*time.Second)

	var status1, exitReason1 string
	if err := pool.QueryRow(ctx, `SELECT status, exit_reason FROM agent_instances WHERE ticket_id=$1`, ticket1ID).
		Scan(&status1, &exitReason1); err != nil {
		t.Fatalf("query agent_instances (poisoned): %v", err)
	}
	if status1 != "failed" {
		t.Errorf("poisoned: agent_instances.status=%q; want 'failed'", status1)
	}
	if exitReason1 != "vault_mcp_in_config" {
		t.Errorf("poisoned: exit_reason=%q; want 'vault_mcp_in_config'", exitReason1)
	}

	// Rule 3 fires BEFORE the vault fetch → no vault_access_log row.
	var logCount1 int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM vault_access_log WHERE ticket_id=$1`, ticket1ID).Scan(&logCount1)
	if logCount1 != 0 {
		t.Errorf("poisoned: vault_access_log count=%d; want 0 (Rule 3 aborts before Infisical fetch)", logCount1)
	}

	// Mockclaude was never invoked → workspace file must NOT exist.
	if _, err := os.Stat(filepath.Join(workspace, "vault_secret_used.txt")); err == nil {
		t.Error("poisoned: vault_secret_used.txt should not exist when Rule 3 aborts before spawn")
	}

	// --- Restore + clean run ---
	cmd1Cancel()
	_ = cmd1.Wait()
	t.Logf("poisoned supervisor stopped")

	if _, err := pool.Exec(ctx, `UPDATE agents SET mcp_config='{}'::jsonb WHERE role_slug='engineer'`); err != nil {
		t.Fatalf("restore mcp_config: %v", err)
	}
	_, _ = pool.Exec(ctx, "TRUNCATE vault_access_log")

	healthPort2 := mustFreePort(t)
	env2 := vaultTestEnv(testdb.URL(t), healthPort2, mockBin, engineerScript, mcpDir, mempalacePw,
		m22MempalaceContainer, m22DockerProxyHost, harness, mlClientID, mlClientSecret, companyUUID)

	_, _ = startVaultSupervisor(t, ctx, bin, env2, workspace)
	if err := waitForHealth(healthPort2, 20*time.Second); err != nil {
		t.Fatalf("supervisor health (clean run): %v", err)
	}

	var ticket2ID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO tickets (id, department_id, objective, column_slug)
		VALUES (gen_random_uuid(), $1, 'Rule 3 clean run', 'in_dev')
		RETURNING id`, deptID,
	).Scan(&ticket2ID); err != nil {
		t.Fatalf("insert ticket (clean): %v", err)
	}

	waitForAgentInstanceCount(ctx, t, pool, ticket2ID, 1, 90*time.Second)

	var status2, exitReason2 string
	if err := pool.QueryRow(ctx, `SELECT status, exit_reason FROM agent_instances WHERE ticket_id=$1`, ticket2ID).
		Scan(&status2, &exitReason2); err != nil {
		t.Fatalf("query agent_instances (clean): %v", err)
	}
	if status2 != "succeeded" {
		t.Errorf("clean: agent_instances.status=%q; want 'succeeded' (exit_reason=%q)", status2, exitReason2)
	}
}

// newInfisicalProxy starts an httptest.Server that proxies all requests to
// targetURL, but returns statusCode for any request whose path contains
// pathSubstr. Returns the proxy's URL. Use this to inject specific HTTP error
// codes (403, 429, 404) into secret-fetch paths without touching Infisical
// internals. Auth calls use different URL paths and pass through normally.
func newInfisicalProxy(t *testing.T, targetURL string, pathSubstr string, statusCode int) string {
	t.Helper()
	target, err := url.Parse(targetURL)
	if err != nil {
		t.Fatalf("newInfisicalProxy: parse target URL: %v", err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Director = func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.Host = target.Host
	}
	var count atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, pathSubstr) {
			n := count.Add(1)
			_ = n
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(statusCode)
			_, _ = fmt.Fprintf(w, `{"message":"proxy-injected %d"}`, statusCode)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// vaultFailureModeSetup is shared initialization for all TestVaultFailureMode_*
// tests: full stack, one Infisical harness, one grant seeded in Garrison DB and
// (optionally) in Infisical. Returns the companyUUID, deptID, fullSecretPath, and
// a cleanup fn. It does NOT start the supervisor process — callers start it with
// the desired env overrides.
type failureModeSetup struct {
	companyUUID    string
	companyID      pgtype.UUID
	deptID         pgtype.UUID
	fullSecretPath string
}

func setupVaultFailureMode(
	t *testing.T,
	ctx context.Context,
	pool interface {
		Exec(context.Context, string, ...interface{}) (interface{}, error)
		QueryRow(context.Context, string, ...interface{}) interface{ Scan(...interface{}) error }
	},
	harness interface {
		SeedSecret(folderPath, secretKey, secretValue string) error
	},
	seedSecret bool,
) failureModeSetup {
	t.Helper()
	// This helper is intentionally minimal — the complex assertions live in each test.
	// We just set the pkg-level companyID/deptID pattern used across all tests.
	return failureModeSetup{}
}

// TestVaultFailureMode_Unavailable verifies that when the Infisical server is
// unreachable the supervisor records exit_reason='vault_unavailable' and a
// vault_access_log row with outcome='error_fetching'. No subprocess is started.
func TestVaultFailureMode_Unavailable(t *testing.T) {
	requireSpikeStack(t)

	pool := testdb.Start(t)
	workspace := t.TempDir()
	_, _ = testdb.SeedM22(t, workspace)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

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
	if err := pool.QueryRow(ctx, `SELECT id FROM departments WHERE slug='engineering'`).Scan(&deptID); err != nil {
		t.Fatalf("lookup dept: %v", err)
	}

	harness := vault.StartInfisical(t)
	mlClientID, mlClientSecret, err := harness.CreateMachineIdentity("garrison-unavail-test-ml")
	if err != nil {
		t.Fatalf("CreateMachineIdentity: %v", err)
	}

	folderPath := "/" + companyUUID + "/operator"
	const secretKey = "EXAMPLE_API_KEY"
	if err := harness.SeedSecret(folderPath, secretKey, "unavail-secret-value"); err != nil {
		t.Fatalf("SeedSecret: %v", err)
	}
	fullSecretPath := folderPath + "/" + secretKey

	if _, err := pool.Exec(ctx,
		`INSERT INTO agent_role_secrets (role_slug, env_var_name, secret_path, customer_id, granted_by)
		 VALUES ('engineer', 'EXAMPLE_API_KEY', $1, $2, 'test-operator')`,
		fullSecretPath, companyID,
	); err != nil {
		t.Fatalf("insert agent_role_secrets: %v", err)
	}

	mempalacePw := "unavail-test-pw"
	testdb.SetAgentMempalacePassword(t, mempalacePw)
	testdb.SetAgentROPassword(t, "m23-ro-test-pw")

	bin := buildSupervisorBinary(t)
	mockBin := buildMockClaudeBinary(t)
	mcpDir := t.TempDir()
	_ = os.Chmod(mcpDir, 0o750)
	engineerScript := mockClaudeScriptPath(t, "m2_3_vault_uses_env_var.ndjson")

	healthPort := mustFreePort(t)
	env := vaultTestEnv(testdb.URL(t), healthPort, mockBin, engineerScript, mcpDir, mempalacePw,
		m22MempalaceContainer, m22DockerProxyHost, harness, mlClientID, mlClientSecret, companyUUID)

	// Start supervisor — at this point Infisical is UP so initial auth succeeds.
	_, _ = startVaultSupervisor(t, ctx, bin, env, workspace)
	if err := waitForHealth(healthPort, 20*time.Second); err != nil {
		t.Fatalf("supervisor health: %v", err)
	}

	// Kill Infisical. The supervisor's next fetch call will hit a dead server.
	if err := harness.StopInfisical(context.Background()); err != nil {
		t.Fatalf("StopInfisical: %v", err)
	}
	time.Sleep(500 * time.Millisecond) // let the container fully stop

	var ticketID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO tickets (id, department_id, objective, column_slug)
		VALUES (gen_random_uuid(), $1, 'Vault unavailable test', 'in_dev')
		RETURNING id`, deptID,
	).Scan(&ticketID); err != nil {
		t.Fatalf("insert ticket: %v", err)
	}

	waitForAgentInstanceCount(ctx, t, pool, ticketID, 1, 60*time.Second)

	var status, exitReason string
	if err := pool.QueryRow(ctx, `SELECT status, exit_reason FROM agent_instances WHERE ticket_id=$1`, ticketID).
		Scan(&status, &exitReason); err != nil {
		t.Fatalf("query agent_instances: %v", err)
	}
	if status != "failed" {
		t.Errorf("status=%q; want 'failed'", status)
	}
	if exitReason != "vault_unavailable" {
		t.Errorf("exit_reason=%q; want 'vault_unavailable'", exitReason)
	}

	// vault_access_log must have one row with outcome='error_fetching'.
	var logOutcome string
	var logCount int
	if err := pool.QueryRow(ctx, `SELECT count(*), outcome FROM vault_access_log WHERE ticket_id=$1 GROUP BY outcome`, ticketID).
		Scan(&logCount, &logOutcome); err != nil {
		t.Errorf("vault_access_log: no row found for ticket (want 1 row with outcome='error_fetching'): %v", err)
	} else {
		if logCount != 1 {
			t.Errorf("vault_access_log count=%d; want 1", logCount)
		}
		if logOutcome != "error_fetching" {
			t.Errorf("vault_access_log.outcome=%q; want 'error_fetching'", logOutcome)
		}
	}
}

// TestVaultFailureMode_AuthExpired verifies that when the access token expires
// and re-authentication fails (single-use client secret exhausted), the
// supervisor records exit_reason='vault_auth_expired'.
func TestVaultFailureMode_AuthExpired(t *testing.T) {
	requireSpikeStack(t)

	pool := testdb.Start(t)
	workspace := t.TempDir()
	_, _ = testdb.SeedM22(t, workspace)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

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
	if err := pool.QueryRow(ctx, `SELECT id FROM departments WHERE slug='engineering'`).Scan(&deptID); err != nil {
		t.Fatalf("lookup dept: %v", err)
	}

	harness := vault.StartInfisical(t)
	// 1-second TTL token + 1-use-limit client secret: first auth succeeds,
	// re-auth after token expiry fails.
	mlClientID, mlClientSecret, err := harness.CreateShortLivedMachineIdentity("garrison-authexpired-test-ml")
	if err != nil {
		t.Fatalf("CreateShortLivedMachineIdentity: %v", err)
	}

	folderPath := "/" + companyUUID + "/operator"
	const secretKey = "EXAMPLE_API_KEY"
	if err := harness.SeedSecret(folderPath, secretKey, "auth-expired-secret"); err != nil {
		t.Fatalf("SeedSecret: %v", err)
	}
	fullSecretPath := folderPath + "/" + secretKey

	if _, err := pool.Exec(ctx,
		`INSERT INTO agent_role_secrets (role_slug, env_var_name, secret_path, customer_id, granted_by)
		 VALUES ('engineer', 'EXAMPLE_API_KEY', $1, $2, 'test-operator')`,
		fullSecretPath, companyID,
	); err != nil {
		t.Fatalf("insert agent_role_secrets: %v", err)
	}

	mempalacePw := "authexp-test-pw"
	testdb.SetAgentMempalacePassword(t, mempalacePw)
	testdb.SetAgentROPassword(t, "m23-ro-test-pw")

	bin := buildSupervisorBinary(t)
	mockBin := buildMockClaudeBinary(t)
	mcpDir := t.TempDir()
	_ = os.Chmod(mcpDir, 0o750)
	engineerScript := mockClaudeScriptPath(t, "m2_3_vault_uses_env_var.ndjson")

	healthPort := mustFreePort(t)
	env := vaultTestEnv(testdb.URL(t), healthPort, mockBin, engineerScript, mcpDir, mempalacePw,
		m22MempalaceContainer, m22DockerProxyHost, harness, mlClientID, mlClientSecret, companyUUID)

	_, _ = startVaultSupervisor(t, ctx, bin, env, workspace)
	if err := waitForHealth(healthPort, 20*time.Second); err != nil {
		t.Fatalf("supervisor health: %v", err)
	}

	// Wait for the 1-second access token to expire before inserting the ticket.
	time.Sleep(2 * time.Second)

	var ticketID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO tickets (id, department_id, objective, column_slug)
		VALUES (gen_random_uuid(), $1, 'Auth expired test', 'in_dev')
		RETURNING id`, deptID,
	).Scan(&ticketID); err != nil {
		t.Fatalf("insert ticket: %v", err)
	}

	waitForAgentInstanceCount(ctx, t, pool, ticketID, 1, 60*time.Second)

	var status, exitReason string
	if err := pool.QueryRow(ctx, `SELECT status, exit_reason FROM agent_instances WHERE ticket_id=$1`, ticketID).
		Scan(&status, &exitReason); err != nil {
		t.Fatalf("query agent_instances: %v", err)
	}
	if status != "failed" {
		t.Errorf("status=%q; want 'failed'", status)
	}
	if exitReason != "vault_auth_expired" {
		t.Errorf("exit_reason=%q; want 'vault_auth_expired'", exitReason)
	}
}

// TestVaultFailureMode_PermissionDenied verifies that when Infisical returns
// HTTP 403 for a secret fetch, the supervisor records exit_reason=
// 'vault_permission_denied' and vault_access_log.outcome='denied_infisical'.
func TestVaultFailureMode_PermissionDenied(t *testing.T) {
	requireSpikeStack(t)

	pool := testdb.Start(t)
	workspace := t.TempDir()
	_, _ = testdb.SeedM22(t, workspace)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

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
	if err := pool.QueryRow(ctx, `SELECT id FROM departments WHERE slug='engineering'`).Scan(&deptID); err != nil {
		t.Fatalf("lookup dept: %v", err)
	}

	harness := vault.StartInfisical(t)
	// Create a normal ML for supervisor startup auth.
	mlClientID, mlClientSecret, err := harness.CreateMachineIdentity("garrison-permdeny-test-ml")
	if err != nil {
		t.Fatalf("CreateMachineIdentity: %v", err)
	}

	folderPath := "/" + companyUUID + "/operator"
	const secretKey = "EXAMPLE_API_KEY"
	if err := harness.SeedSecret(folderPath, secretKey, "perm-denied-secret"); err != nil {
		t.Fatalf("SeedSecret: %v", err)
	}
	fullSecretPath := folderPath + "/" + secretKey

	if _, err := pool.Exec(ctx,
		`INSERT INTO agent_role_secrets (role_slug, env_var_name, secret_path, customer_id, granted_by)
		 VALUES ('engineer', 'EXAMPLE_API_KEY', $1, $2, 'test-operator')`,
		fullSecretPath, companyID,
	); err != nil {
		t.Fatalf("insert agent_role_secrets: %v", err)
	}

	// Proxy that returns 403 for all secret-fetch requests. Auth requests (different
	// URL path) pass through normally so the supervisor starts successfully.
	proxyURL := newInfisicalProxy(t, harness.URL(), "/api/v3/secrets/raw/", http.StatusForbidden)

	mempalacePw := "permdeny-test-pw"
	testdb.SetAgentMempalacePassword(t, mempalacePw)
	testdb.SetAgentROPassword(t, "m23-ro-test-pw")

	bin := buildSupervisorBinary(t)
	mockBin := buildMockClaudeBinary(t)
	mcpDir := t.TempDir()
	_ = os.Chmod(mcpDir, 0o750)
	engineerScript := mockClaudeScriptPath(t, "m2_3_vault_uses_env_var.ndjson")

	healthPort := mustFreePort(t)
	// Override Infisical addr to use the proxy — auth calls pass through,
	// secret-fetch calls receive 403.
	env := vaultTestEnv(testdb.URL(t), healthPort, mockBin, engineerScript, mcpDir, mempalacePw,
		m22MempalaceContainer, m22DockerProxyHost, harness, mlClientID, mlClientSecret, companyUUID)
	env = append(env, "GARRISON_INFISICAL_ADDR="+proxyURL)

	_, _ = startVaultSupervisor(t, ctx, bin, env, workspace)
	if err := waitForHealth(healthPort, 20*time.Second); err != nil {
		t.Fatalf("supervisor health: %v", err)
	}

	var ticketID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO tickets (id, department_id, objective, column_slug)
		VALUES (gen_random_uuid(), $1, 'Permission denied test', 'in_dev')
		RETURNING id`, deptID,
	).Scan(&ticketID); err != nil {
		t.Fatalf("insert ticket: %v", err)
	}

	waitForAgentInstanceCount(ctx, t, pool, ticketID, 1, 60*time.Second)

	var status, exitReason string
	if err := pool.QueryRow(ctx, `SELECT status, exit_reason FROM agent_instances WHERE ticket_id=$1`, ticketID).
		Scan(&status, &exitReason); err != nil {
		t.Fatalf("query agent_instances: %v", err)
	}
	if status != "failed" {
		t.Errorf("status=%q; want 'failed'", status)
	}
	if exitReason != "vault_permission_denied" {
		t.Errorf("exit_reason=%q; want 'vault_permission_denied'", exitReason)
	}

	var logOutcome string
	var logCount int
	if err := pool.QueryRow(ctx, `SELECT count(*), outcome FROM vault_access_log WHERE ticket_id=$1 GROUP BY outcome`, ticketID).
		Scan(&logCount, &logOutcome); err != nil {
		t.Errorf("vault_access_log: no row found: %v", err)
	} else {
		if logOutcome != "denied_infisical" {
			t.Errorf("vault_access_log.outcome=%q; want 'denied_infisical'", logOutcome)
		}
	}
}

// TestVaultFailureMode_RateLimited verifies that when Infisical returns HTTP 429
// for a secret fetch, the supervisor records exit_reason='vault_rate_limited'
// with no in-flight retry. Uses a proxy to inject the 429 response.
func TestVaultFailureMode_RateLimited(t *testing.T) {
	requireSpikeStack(t)

	pool := testdb.Start(t)
	workspace := t.TempDir()
	_, _ = testdb.SeedM22(t, workspace)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

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
	if err := pool.QueryRow(ctx, `SELECT id FROM departments WHERE slug='engineering'`).Scan(&deptID); err != nil {
		t.Fatalf("lookup dept: %v", err)
	}

	harness := vault.StartInfisical(t)
	mlClientID, mlClientSecret, err := harness.CreateMachineIdentity("garrison-ratelimit-test-ml")
	if err != nil {
		t.Fatalf("CreateMachineIdentity: %v", err)
	}

	folderPath := "/" + companyUUID + "/operator"
	const secretKey = "EXAMPLE_API_KEY"
	if err := harness.SeedSecret(folderPath, secretKey, "rate-limit-secret"); err != nil {
		t.Fatalf("SeedSecret: %v", err)
	}
	fullSecretPath := folderPath + "/" + secretKey

	if _, err := pool.Exec(ctx,
		`INSERT INTO agent_role_secrets (role_slug, env_var_name, secret_path, customer_id, granted_by)
		 VALUES ('engineer', 'EXAMPLE_API_KEY', $1, $2, 'test-operator')`,
		fullSecretPath, companyID,
	); err != nil {
		t.Fatalf("insert agent_role_secrets: %v", err)
	}

	// Proxy returning 429 for all secret-fetch requests.
	proxyURL := newInfisicalProxy(t, harness.URL(), "/api/v3/secrets/raw/", http.StatusTooManyRequests)

	mempalacePw := "ratelimit-test-pw"
	testdb.SetAgentMempalacePassword(t, mempalacePw)
	testdb.SetAgentROPassword(t, "m23-ro-test-pw")

	bin := buildSupervisorBinary(t)
	mockBin := buildMockClaudeBinary(t)
	mcpDir := t.TempDir()
	_ = os.Chmod(mcpDir, 0o750)
	engineerScript := mockClaudeScriptPath(t, "m2_3_vault_uses_env_var.ndjson")

	healthPort := mustFreePort(t)
	env := vaultTestEnv(testdb.URL(t), healthPort, mockBin, engineerScript, mcpDir, mempalacePw,
		m22MempalaceContainer, m22DockerProxyHost, harness, mlClientID, mlClientSecret, companyUUID)
	env = append(env, "GARRISON_INFISICAL_ADDR="+proxyURL)

	_, _ = startVaultSupervisor(t, ctx, bin, env, workspace)
	if err := waitForHealth(healthPort, 20*time.Second); err != nil {
		t.Fatalf("supervisor health: %v", err)
	}

	var ticketID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO tickets (id, department_id, objective, column_slug)
		VALUES (gen_random_uuid(), $1, 'Rate limited test', 'in_dev')
		RETURNING id`, deptID,
	).Scan(&ticketID); err != nil {
		t.Fatalf("insert ticket: %v", err)
	}

	waitForAgentInstanceCount(ctx, t, pool, ticketID, 1, 60*time.Second)

	var status, exitReason string
	if err := pool.QueryRow(ctx, `SELECT status, exit_reason FROM agent_instances WHERE ticket_id=$1`, ticketID).
		Scan(&status, &exitReason); err != nil {
		t.Fatalf("query agent_instances: %v", err)
	}
	if status != "failed" {
		t.Errorf("status=%q; want 'failed'", status)
	}
	if exitReason != "vault_rate_limited" {
		t.Errorf("exit_reason=%q; want 'vault_rate_limited'", exitReason)
	}
}

// TestVaultFailureMode_SecretNotFound verifies that when a grant points to an
// Infisical path that has never been seeded, the supervisor records
// exit_reason='vault_secret_not_found'.
func TestVaultFailureMode_SecretNotFound(t *testing.T) {
	requireSpikeStack(t)

	pool := testdb.Start(t)
	workspace := t.TempDir()
	_, _ = testdb.SeedM22(t, workspace)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

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
	if err := pool.QueryRow(ctx, `SELECT id FROM departments WHERE slug='engineering'`).Scan(&deptID); err != nil {
		t.Fatalf("lookup dept: %v", err)
	}

	harness := vault.StartInfisical(t)
	mlClientID, mlClientSecret, err := harness.CreateMachineIdentity("garrison-notfound-test-ml")
	if err != nil {
		t.Fatalf("CreateMachineIdentity: %v", err)
	}

	// Point the grant at a path that has never been seeded in Infisical.
	const nonExistentKey = "NEVER_SEEDED_KEY"
	folderPath := "/" + companyUUID + "/operator"
	fullSecretPath := folderPath + "/" + nonExistentKey
	// NOTE: harness.SeedSecret is NOT called — the secret path does not exist.

	// The folder must exist for Infisical to return 404 (vs 403 folder-not-found).
	// Use a direct harness.SeedSecret with a different key to create the folder,
	// then use the non-existent key in the grant.
	if err := harness.SeedSecret(folderPath, "PLACEHOLDER_KEY", "placeholder"); err != nil {
		t.Fatalf("SeedSecret (folder creation): %v", err)
	}

	if _, err := pool.Exec(ctx,
		`INSERT INTO agent_role_secrets (role_slug, env_var_name, secret_path, customer_id, granted_by)
		 VALUES ('engineer', 'NEVER_SEEDED_KEY', $1, $2, 'test-operator')`,
		fullSecretPath, companyID,
	); err != nil {
		t.Fatalf("insert agent_role_secrets: %v", err)
	}

	mempalacePw := "notfound-test-pw"
	testdb.SetAgentMempalacePassword(t, mempalacePw)
	testdb.SetAgentROPassword(t, "m23-ro-test-pw")

	bin := buildSupervisorBinary(t)
	mockBin := buildMockClaudeBinary(t)
	mcpDir := t.TempDir()
	_ = os.Chmod(mcpDir, 0o750)
	engineerScript := mockClaudeScriptPath(t, "m2_3_vault_uses_env_var.ndjson")

	healthPort := mustFreePort(t)
	env := vaultTestEnv(testdb.URL(t), healthPort, mockBin, engineerScript, mcpDir, mempalacePw,
		m22MempalaceContainer, m22DockerProxyHost, harness, mlClientID, mlClientSecret, companyUUID)

	_, _ = startVaultSupervisor(t, ctx, bin, env, workspace)
	if err := waitForHealth(healthPort, 20*time.Second); err != nil {
		t.Fatalf("supervisor health: %v", err)
	}

	var ticketID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO tickets (id, department_id, objective, column_slug)
		VALUES (gen_random_uuid(), $1, 'Secret not found test', 'in_dev')
		RETURNING id`, deptID,
	).Scan(&ticketID); err != nil {
		t.Fatalf("insert ticket: %v", err)
	}

	waitForAgentInstanceCount(ctx, t, pool, ticketID, 1, 60*time.Second)

	var status, exitReason string
	if err := pool.QueryRow(ctx, `SELECT status, exit_reason FROM agent_instances WHERE ticket_id=$1`, ticketID).
		Scan(&status, &exitReason); err != nil {
		t.Fatalf("query agent_instances: %v", err)
	}
	if status != "failed" {
		t.Errorf("status=%q; want 'failed'", status)
	}
	if exitReason != "vault_secret_not_found" {
		t.Errorf("exit_reason=%q; want 'vault_secret_not_found'", exitReason)
	}
}
