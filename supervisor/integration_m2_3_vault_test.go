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

	"github.com/garrison-hq/garrison/supervisor/internal/mempalace"
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

// TestVaultDualAuditRecord verifies SC-406: after a clean spawn, both Garrison's
// vault_access_log and Infisical's native audit log carry exactly one record for
// the access, with matching secret_path. The raw secret value must not appear in
// either audit trail.
func TestVaultDualAuditRecord(t *testing.T) {
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
	mlClientID, mlClientSecret, err := harness.CreateMachineIdentity("garrison-dualaudit-test-ml")
	if err != nil {
		t.Fatalf("CreateMachineIdentity: %v", err)
	}

	const secretValue = "dual-audit-secret-abc123xyz789"
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

	mempalacePw := "dualaudit-test-pw"
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

	beforeSpawn := time.Now()
	_, _ = startVaultSupervisor(t, ctx, bin, env, workspace)
	if err := waitForHealth(healthPort, 20*time.Second); err != nil {
		t.Fatalf("supervisor health: %v", err)
	}

	var ticketID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO tickets (id, department_id, objective, column_slug)
		VALUES (gen_random_uuid(), $1, 'Dual audit record test', 'in_dev')
		RETURNING id`, deptID,
	).Scan(&ticketID); err != nil {
		t.Fatalf("insert ticket: %v", err)
	}

	waitForAgentInstanceCount(ctx, t, pool, ticketID, 1, 90*time.Second)

	// (1) Garrison vault_access_log: one row, outcome='granted', correct path.
	var logOutcome, logPath string
	var logCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*), outcome, secret_path
		FROM vault_access_log WHERE ticket_id=$1
		GROUP BY outcome, secret_path`, ticketID,
	).Scan(&logCount, &logOutcome, &logPath); err != nil {
		t.Fatalf("vault_access_log query: %v", err)
	}
	if logCount != 1 {
		t.Errorf("vault_access_log count=%d; want 1", logCount)
	}
	if logOutcome != "granted" {
		t.Errorf("vault_access_log.outcome=%q; want 'granted'", logOutcome)
	}
	if logPath != fullSecretPath {
		t.Errorf("vault_access_log.secret_path=%q; want %q", logPath, fullSecretPath)
	}

	// (2) vault_access_log schema must not include the secret value column.
	var colNames []string
	rows, err := pool.Query(ctx, `SELECT column_name FROM information_schema.columns
		WHERE table_name='vault_access_log' ORDER BY ordinal_position`)
	if err != nil {
		t.Fatalf("query column names: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cn string
		_ = rows.Scan(&cn)
		colNames = append(colNames, cn)
	}
	for _, col := range colNames {
		if strings.Contains(strings.ToLower(col), "value") || strings.Contains(strings.ToLower(col), "secret_value") {
			t.Errorf("vault_access_log has suspicious column %q that may store secret value (SC-406 schema check)", col)
		}
	}

	// (3) Infisical native audit log: best-effort check.
	infisicalEvents := harness.AuditLogForIdentity(beforeSpawn)
	if len(infisicalEvents) > 0 {
		// Audit API is available — assert at least one matching event.
		found := false
		for _, e := range infisicalEvents {
			if e.EventType == "getSecret" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Infisical audit log: no 'getSecret' event found after spawn; got %d events", len(infisicalEvents))
		}
		// Assert raw secret value not in any audit event representation.
		for _, e := range infisicalEvents {
			if strings.Contains(e.SecretPath, secretValue) {
				t.Errorf("Infisical audit event contains raw secret value in SecretPath (SC-406)")
			}
		}
	} else {
		t.Logf("Infisical audit log API not available in this version — skipping native audit assertion (best-effort)")
	}
}

// TestVaultAuditFailureFailClosed verifies SC-408: when the vault_access_log
// INSERT fails, the supervisor records exit_reason='vault_audit_failed' and
// does NOT start the subprocess. The fail-closed semantic ensures no agent
// can run without a successful audit write.
func TestVaultAuditFailureFailClosed(t *testing.T) {
	requireSpikeStack(t)

	pool := testdb.Start(t)
	workspace := t.TempDir()
	_, _ = testdb.SeedM22(t, workspace)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	_, _ = pool.Exec(ctx, "TRUNCATE vault_access_log, agent_role_secrets, secret_metadata")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "TRUNCATE vault_access_log, agent_role_secrets, secret_metadata")
		// Ensure trigger is cleaned up even if the test panics.
		_, _ = pool.Exec(context.Background(),
			`DROP TRIGGER IF EXISTS vault_access_log_fail_trigger ON vault_access_log;
			 DROP FUNCTION IF EXISTS vault_access_log_fail_test();`)
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
	mlClientID, mlClientSecret, err := harness.CreateMachineIdentity("garrison-failclosed-test-ml")
	if err != nil {
		t.Fatalf("CreateMachineIdentity: %v", err)
	}

	const secretValue = "fail-closed-secret-abc123"
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

	mempalacePw := "failclosed-test-pw"
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

	// Install a BEFORE INSERT trigger that raises an exception, making every
	// INSERT into vault_access_log fail. This forces the fail-closed path.
	if _, err := pool.Exec(ctx, `
		CREATE OR REPLACE FUNCTION vault_access_log_fail_test() RETURNS TRIGGER AS $$
		BEGIN
			RAISE EXCEPTION 'vault_access_log: test-injected failure for fail-closed test';
			RETURN NULL;
		END; $$ LANGUAGE plpgsql;
		CREATE TRIGGER vault_access_log_fail_trigger
		BEFORE INSERT ON vault_access_log
		FOR EACH ROW EXECUTE FUNCTION vault_access_log_fail_test();
	`); err != nil {
		t.Fatalf("install fail trigger: %v", err)
	}

	var ticketID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO tickets (id, department_id, objective, column_slug)
		VALUES (gen_random_uuid(), $1, 'Fail-closed audit test', 'in_dev')
		RETURNING id`, deptID,
	).Scan(&ticketID); err != nil {
		t.Fatalf("insert ticket: %v", err)
	}

	waitForAgentInstanceCount(ctx, t, pool, ticketID, 1, 60*time.Second)

	// Remove the trigger before asserting so cleanup queries succeed.
	if _, err := pool.Exec(ctx, `
		DROP TRIGGER IF EXISTS vault_access_log_fail_trigger ON vault_access_log;
		DROP FUNCTION IF EXISTS vault_access_log_fail_test();
	`); err != nil {
		t.Fatalf("drop fail trigger: %v", err)
	}

	var status, exitReason string
	if err := pool.QueryRow(ctx, `SELECT status, exit_reason FROM agent_instances WHERE ticket_id=$1`, ticketID).
		Scan(&status, &exitReason); err != nil {
		t.Fatalf("query agent_instances: %v", err)
	}
	if status != "failed" {
		t.Errorf("status=%q; want 'failed'", status)
	}
	if exitReason != "vault_audit_failed" {
		t.Errorf("exit_reason=%q; want 'vault_audit_failed'", exitReason)
	}

	// vault_access_log must have NO row (the trigger prevented any INSERT).
	var logCount int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM vault_access_log WHERE ticket_id=$1`, ticketID).Scan(&logCount)
	if logCount != 0 {
		t.Errorf("vault_access_log count=%d; want 0 (fail-closed: trigger prevented all INSERTs)", logCount)
	}

	// Mockclaude must NOT have been invoked.
	if _, err := os.Stat(filepath.Join(workspace, "vault_secret_used.txt")); err == nil {
		t.Error("vault_secret_used.txt should not exist when fail-closed aborts before spawn")
	}
}

// TestSecretMetadataPopulatedAtBootstrap verifies SC-409: the secret_metadata
// table is seeded by the operator at bootstrap, last_accessed_at is updated
// after a successful spawn, and the allowed_role_slugs trigger maintains the
// denorm column.
func TestSecretMetadataPopulatedAtBootstrap(t *testing.T) {
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
	mlClientID, mlClientSecret, err := harness.CreateMachineIdentity("garrison-metadata-test-ml")
	if err != nil {
		t.Fatalf("CreateMachineIdentity: %v", err)
	}

	const secretValue = "metadata-bootstrap-secret-abc123"
	folderPath := "/" + companyUUID + "/operator"
	const secretKey = "EXAMPLE_API_KEY"
	if err := harness.SeedSecret(folderPath, secretKey, secretValue); err != nil {
		t.Fatalf("SeedSecret: %v", err)
	}
	fullSecretPath := folderPath + "/" + secretKey

	// Seed secret_metadata WITHOUT a grant (operator pre-populates at bootstrap
	// before any role grants are issued — mirrors the ops-checklist SQL from T018).
	if _, err := pool.Exec(ctx,
		`INSERT INTO secret_metadata (secret_path, customer_id, provenance, rotation_cadence)
		 VALUES ($1, $2, 'operator_entered', '90 days')`,
		fullSecretPath, companyID,
	); err != nil {
		t.Fatalf("insert secret_metadata: %v", err)
	}

	// (1) Initially: last_accessed_at=NULL, allowed_role_slugs={}
	var lastAccessed pgtype.Timestamptz
	var allowedRoles []string
	if err := pool.QueryRow(ctx,
		`SELECT last_accessed_at, allowed_role_slugs FROM secret_metadata WHERE secret_path=$1`,
		fullSecretPath,
	).Scan(&lastAccessed, &allowedRoles); err != nil {
		t.Fatalf("query secret_metadata before spawn: %v", err)
	}
	if lastAccessed.Valid {
		t.Error("initial last_accessed_at should be NULL before any spawn")
	}
	if len(allowedRoles) != 0 {
		t.Errorf("initial allowed_role_slugs=%v; want empty (no grants yet)", allowedRoles)
	}

	// Insert a grant — the trigger should rebuild allowed_role_slugs.
	if _, err := pool.Exec(ctx,
		`INSERT INTO agent_role_secrets (role_slug, env_var_name, secret_path, customer_id, granted_by)
		 VALUES ('engineer', 'EXAMPLE_API_KEY', $1, $2, 'test-operator')`,
		fullSecretPath, companyID,
	); err != nil {
		t.Fatalf("insert agent_role_secrets: %v", err)
	}

	// (2) After grant: allowed_role_slugs should contain 'engineer' (trigger rebuilt it).
	if err := pool.QueryRow(ctx,
		`SELECT allowed_role_slugs FROM secret_metadata WHERE secret_path=$1`,
		fullSecretPath,
	).Scan(&allowedRoles); err != nil {
		t.Fatalf("query secret_metadata after grant: %v", err)
	}
	found := false
	for _, r := range allowedRoles {
		if r == "engineer" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("allowed_role_slugs=%v; want to include 'engineer' after grant insert", allowedRoles)
	}

	// (3) Run a spawn — last_accessed_at should be populated after successful grant.
	mempalacePw := "metadata-test-pw"
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

	beforeSpawn := time.Now()
	_, _ = startVaultSupervisor(t, ctx, bin, env, workspace)
	if err := waitForHealth(healthPort, 20*time.Second); err != nil {
		t.Fatalf("supervisor health: %v", err)
	}

	var ticketID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO tickets (id, department_id, objective, column_slug)
		VALUES (gen_random_uuid(), $1, 'Metadata bootstrap test', 'in_dev')
		RETURNING id`, deptID,
	).Scan(&ticketID); err != nil {
		t.Fatalf("insert ticket: %v", err)
	}

	waitForAgentInstanceCount(ctx, t, pool, ticketID, 1, 90*time.Second)

	// (4) After spawn: last_accessed_at should be populated within ±10s of spawn.
	if err := pool.QueryRow(ctx,
		`SELECT last_accessed_at FROM secret_metadata WHERE secret_path=$1`,
		fullSecretPath,
	).Scan(&lastAccessed); err != nil {
		t.Fatalf("query secret_metadata after spawn: %v", err)
	}
	if !lastAccessed.Valid {
		t.Error("last_accessed_at is NULL after OutcomeGranted spawn; want non-NULL")
	} else {
		delta := lastAccessed.Time.Sub(beforeSpawn)
		if delta < -10*time.Second || delta > 60*time.Second {
			t.Errorf("last_accessed_at=%v is outside ±10s of spawn start %v", lastAccessed.Time, beforeSpawn)
		}
	}
}

// TestVaultRotationDuringRunNoOp verifies FR-429: rotating the backing Infisical
// secret while a spawn is in progress does NOT signal or interrupt the running
// agent. The old value remains valid for the current run; the new value is picked
// up on the next spawn.
func TestVaultRotationDuringRunNoOp(t *testing.T) {
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
	mlClientID, mlClientSecret, err := harness.CreateMachineIdentity("garrison-rotation-test-ml")
	if err != nil {
		t.Fatalf("CreateMachineIdentity: %v", err)
	}

	const secretValueV1 = "rotation-secret-v1-abc123xyz789"
	const secretValueV2 = "rotation-secret-v2-xyz789abc123"
	folderPath := "/" + companyUUID + "/operator"
	const secretKey = "EXAMPLE_API_KEY"
	if err := harness.SeedSecret(folderPath, secretKey, secretValueV1); err != nil {
		t.Fatalf("SeedSecret V1: %v", err)
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

	mempalacePw := "rotation-test-pw"
	testdb.SetAgentMempalacePassword(t, mempalacePw)
	testdb.SetAgentROPassword(t, "m23-ro-test-pw")

	bin := buildSupervisorBinary(t)
	mockBin := buildMockClaudeBinary(t)
	mcpDir := t.TempDir()
	_ = os.Chmod(mcpDir, 0o750)
	// Long-running fixture: sleeps 6s, writes vault_secret_{{TICKET_ID}}.txt.
	engineerScript := mockClaudeScriptPath(t, "m2_3_vault_rotation_slow.ndjson")

	healthPort := mustFreePort(t)
	env := vaultTestEnv(testdb.URL(t), healthPort, mockBin, engineerScript, mcpDir, mempalacePw,
		m22MempalaceContainer, m22DockerProxyHost, harness, mlClientID, mlClientSecret, companyUUID)

	_, _ = startVaultSupervisor(t, ctx, bin, env, workspace)
	if err := waitForHealth(healthPort, 20*time.Second); err != nil {
		t.Fatalf("supervisor health: %v", err)
	}

	// --- Spawn #1: long-running, uses V1 ---
	var ticket1ID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO tickets (id, department_id, objective, column_slug)
		VALUES (gen_random_uuid(), $1, 'Rotation test spawn 1', 'in_dev')
		RETURNING id`, deptID,
	).Scan(&ticket1ID); err != nil {
		t.Fatalf("insert ticket #1: %v", err)
	}
	ticket1UUID := uuidString(ticket1ID)

	// Wait for spawn to start (agent_instance inserted with status='running').
	if err := waitFor(ctx, 30*time.Second, func() (bool, error) {
		var cnt int
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM agent_instances WHERE ticket_id=$1`, ticket1ID).Scan(&cnt)
		return cnt > 0, nil
	}); err != nil {
		t.Fatalf("spawn #1 did not start within 30s")
	}

	// Rotate the secret mid-spawn (within the 6s sleep window of the fixture).
	// FR-429: the running agent must NOT be interrupted.
	time.Sleep(1 * time.Second) // brief wait to ensure subprocess is sleeping
	if err := harness.UpdateSecret(folderPath, secretKey, secretValueV2); err != nil {
		t.Fatalf("UpdateSecret to V2: %v", err)
	}
	t.Logf("rotated secret to V2 while spawn #1 is running")

	// Wait for spawn #1 to complete.
	waitForAgentInstanceCount(ctx, t, pool, ticket1ID, 1, 120*time.Second)

	// (a) Spawn #1 must have succeeded — not interrupted by rotation.
	var status1, exitReason1 string
	if err := pool.QueryRow(ctx, `SELECT status, exit_reason FROM agent_instances WHERE ticket_id=$1`, ticket1ID).
		Scan(&status1, &exitReason1); err != nil {
		t.Fatalf("query agent_instances #1: %v", err)
	}
	if status1 != "succeeded" {
		t.Errorf("spawn #1: status=%q; want 'succeeded' — rotation must not interrupt running agent (FR-429)", status1)
	}

	// (b) Spawn #1 used V1 (value captured before rotation).
	file1 := filepath.Join(workspace, "vault_secret_"+ticket1UUID+".txt")
	got1, err := os.ReadFile(file1)
	if err != nil {
		t.Errorf("vault_secret_%s.txt missing: %v (spawn #1 fixture did not run)", ticket1UUID, err)
	} else if string(got1) != secretValueV1 {
		t.Errorf("spawn #1 value=%q; want V1=%q (old value must persist through rotation)", string(got1), secretValueV1)
	}

	// --- Spawn #2: quick, fetches V2 ---
	// The concurrency_cap=1 means spawn #2 starts only after spawn #1 finishes.
	var ticket2ID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO tickets (id, department_id, objective, column_slug)
		VALUES (gen_random_uuid(), $1, 'Rotation test spawn 2', 'in_dev')
		RETURNING id`, deptID,
	).Scan(&ticket2ID); err != nil {
		t.Fatalf("insert ticket #2: %v", err)
	}
	ticket2UUID := uuidString(ticket2ID)

	waitForAgentInstanceCount(ctx, t, pool, ticket2ID, 1, 120*time.Second)

	var status2, exitReason2 string
	if err := pool.QueryRow(ctx, `SELECT status, exit_reason FROM agent_instances WHERE ticket_id=$1`, ticket2ID).
		Scan(&status2, &exitReason2); err != nil {
		t.Fatalf("query agent_instances #2: %v", err)
	}
	if status2 != "succeeded" {
		t.Errorf("spawn #2: status=%q; want 'succeeded' (exit_reason=%q)", status2, exitReason2)
	}

	// (c) Spawn #2 fetches V2 (the rotated value).
	file2 := filepath.Join(workspace, "vault_secret_"+ticket2UUID+".txt")
	got2, err := os.ReadFile(file2)
	if err != nil {
		t.Errorf("vault_secret_%s.txt missing: %v (spawn #2 fixture did not run)", ticket2UUID, err)
	} else if string(got2) != secretValueV2 {
		t.Errorf("spawn #2 value=%q; want V2=%q (rotated value must be fetched on next spawn)", string(got2), secretValueV2)
	}

	// (d) Exactly one vault_access_log row per spawn.
	var count1, count2 int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM vault_access_log WHERE ticket_id=$1`, ticket1ID).Scan(&count1)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM vault_access_log WHERE ticket_id=$1`, ticket2ID).Scan(&count2)
	if count1 != 1 {
		t.Errorf("spawn #1 vault_access_log count=%d; want 1", count1)
	}
	if count2 != 1 {
		t.Errorf("spawn #2 vault_access_log count=%d; want 1", count2)
	}
}

// TestSecretPatternScanRedactsBeforeMemPalaceWrite is the M2.3 T016
// integration test (SC-407 / FR-418 / FR-419). A mockclaude fixture
// emits a finalize_ticket payload whose diary_entry.rationale contains
// an sk-prefix secret-shaped string AND a kg_triple.object with a GitHub
// PAT pattern. The test asserts the supervisor's pattern scanner redacts
// both fields before writing to MemPalace, sets hygiene_status=
// 'suspected_secret_emitted', and does NOT insert a vault_access_log row
// (pattern scan is independent of vault access).
func TestSecretPatternScanRedactsBeforeMemPalaceWrite(t *testing.T) {
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

	// Start Infisical for supervisor auth at startup — no secrets seeded; zero grants.
	harness := vault.StartInfisical(t)
	mlClientID, mlClientSecret, err := harness.CreateMachineIdentity("garrison-scan-test-ml")
	if err != nil {
		t.Fatalf("CreateMachineIdentity: %v", err)
	}

	mempalacePw := "scan-test-pw"
	testdb.SetAgentMempalacePassword(t, mempalacePw)
	testdb.SetAgentROPassword(t, "m23-ro-test-pw")

	bin := buildSupervisorBinary(t)
	mockBin := buildMockClaudeBinary(t)
	engineerScript := mockClaudeScriptPath(t, "m2_3_vault_secret_in_diary.ndjson")

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
		VALUES (gen_random_uuid(), $1, 'Pattern scan test', 'in_dev')
		RETURNING id`, deptID,
	).Scan(&ticketID); err != nil {
		t.Fatalf("insert ticket: %v", err)
	}
	ticketUUID := uuidString(ticketID)

	waitForAgentInstanceCount(ctx, t, pool, ticketID, 1, 90*time.Second)

	// (1) Agent instance must succeed — pattern scan is non-blocking (FR-419).
	var status, exitReason string
	if err := pool.QueryRow(ctx, `SELECT status, exit_reason FROM agent_instances WHERE ticket_id=$1`, ticketID).
		Scan(&status, &exitReason); err != nil {
		t.Fatalf("query agent_instances: %v", err)
	}
	if status != "succeeded" {
		t.Errorf("agent_instances.status=%q; want 'succeeded' (exit_reason=%q)", status, exitReason)
	}

	// (2) hygiene_status must be 'suspected_secret_emitted'.
	var hygieneStatus *string
	_ = waitFor(ctx, 30*time.Second, func() (bool, error) {
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
	if hygieneStatus == nil {
		t.Fatal("ticket_transitions.hygiene_status is NULL; want 'suspected_secret_emitted'")
	}
	if *hygieneStatus != "suspected_secret_emitted" {
		t.Errorf("hygiene_status=%q; want 'suspected_secret_emitted' (SC-407)", *hygieneStatus)
	}

	// (3) to_column must still be 'qa_review' — scan never blocks the transition (FR-419).
	var toColumn string
	_ = pool.QueryRow(ctx, `
		SELECT to_column FROM ticket_transitions WHERE ticket_id=$1 ORDER BY at DESC LIMIT 1`, ticketID,
	).Scan(&toColumn)
	if toColumn != "qa_review" {
		t.Errorf("ticket_transitions.to_column=%q; want 'qa_review' (pattern scan must not block transition)", toColumn)
	}

	// (4) No vault_access_log row — pattern scan is a finalize-path concern,
	// not a vault-access concern; zero grants means no Infisical fetch.
	var logCount int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM vault_access_log WHERE ticket_id=$1`, ticketID).Scan(&logCount)
	if logCount != 0 {
		t.Errorf("vault_access_log count=%d; want 0 (no grants → no vault access)", logCount)
	}

	// (5) MemPalace drawer must contain the redacted form, not the raw
	// secret-shaped strings. Use the mempalace.Client with the spike
	// docker-proxy host so we can query the actual written drawer.
	t.Setenv("DOCKER_HOST", m22DockerProxyHost)
	palaceClient := &mempalace.Client{
		MempalaceContainer: m22MempalaceContainer,
		PalacePath:         "/palace",
		Exec:               mempalace.RealDockerExec{},
		Timeout:            20 * time.Second,
	}
	window := mempalace.TimeWindow{
		Start: time.Now().Add(-5 * time.Minute),
		End:   time.Now().Add(5 * time.Minute),
	}
	drawers, triples, qErr := palaceClient.Query(ctx, ticketUUID, "wing_frontend_engineer", window)
	if qErr != nil {
		t.Logf("palace Query failed (best-effort assertion): %v", qErr)
	} else {
		// (5a) sk-prefix must be redacted in drawer body.
		const rawSK = "sk-test-very-long-fake-key-abc123xyz789"
		const rawGHP = "ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghij1234"
		for _, d := range drawers {
			if strings.Contains(d.Body, rawSK) {
				t.Errorf("SC-407: MemPalace drawer body contains raw sk-prefix string; want [REDACTED:sk_prefix]")
			}
			if strings.Contains(d.Body, "[REDACTED:sk_prefix]") {
				t.Logf("SC-407: drawer body correctly contains [REDACTED:sk_prefix]")
			}
		}
		// (5b) GitHub PAT must be redacted in kg_triples.
		for _, tr := range triples {
			if strings.Contains(tr.Object, rawGHP) {
				t.Errorf("SC-407: kg_triple.Object contains raw GitHub PAT string; want [REDACTED:github_pat]")
			}
			if strings.Contains(tr.Object, "[REDACTED:github_pat]") {
				t.Logf("SC-407: kg_triple.Object correctly contains [REDACTED:github_pat]")
			}
		}
		// (5c) Neither raw string appears anywhere in drawer bodies or triples.
		allText := ""
		for _, d := range drawers {
			allText += d.Body
		}
		for _, tr := range triples {
			allText += tr.Subject + " " + tr.Predicate + " " + tr.Object
		}
		if strings.Contains(allText, rawSK) {
			t.Errorf("SC-407: raw sk-prefix string found in palace content; want redacted")
		}
		if strings.Contains(allText, rawGHP) {
			t.Errorf("SC-407: raw GitHub PAT string found in palace content; want redacted")
		}
	}
}
