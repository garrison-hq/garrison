package mcpjungle

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/vault"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// VaultWriter is the narrow seam the reconciler uses to write
// per-agent bearer tokens to Infisical. *vault.Client satisfies it via
// a small wrapper; tests inject a fake.
type VaultWriter interface {
	WriteSecret(ctx context.Context, path, value string) error
}

// ReconcileDeps wires the reconciler's collaborators. Constructed once
// at supervisor boot in cmd/supervisor/main.go and called by
// mcpjungle.ReconcileMcpClients after migrate7.Run completes.
type ReconcileDeps struct {
	Client       *Client
	Pool         *pgxpool.Pool
	Queries      *store.Queries
	VaultClient  VaultWriter
	CustomerID   pgtype.UUID
	CustomerSlug string // M8 alpha: "garrison"; beta: read from companies.customer_slug
	Logger       *slog.Logger
}

// ReconcileReport summarises a reconciler run for the supervisor's
// startup log + any subsequent health-check surfacing.
type ReconcileReport struct {
	Created  []pgtype.UUID // agent_ids that gained an McpClient + vault grant
	Existing []pgtype.UUID // agent_ids whose McpClient already existed (409 conflict)
	Failed   []ReconcileFailure
}

// ReconcileFailure captures a single agent's reconciliation failure.
type ReconcileFailure struct {
	AgentID pgtype.UUID
	Reason  string
}

// ReconcileMcpClients ensures every agents.status='active' row has a
// matching MCPJungle McpClient row + an agent-scoped vault grant
// (FR-403) for its bearer token at path mcpjungle/agents/<agent-id>.
// Idempotent — a second invocation produces zero Created entries
// because MCPJungle's CreateMcpClient returns 409 conflict for the
// existing name and the reconciler treats that as "already exists,
// skip."
//
// The reconciler runs in two situations:
//  1. Supervisor startup, after migrate7.Run (closes the M7 grand-
//     fathering substrate; ensures every M2.x agent has an
//     MCPJungle client by M8 ship).
//  2. After every M7 ApproveHire commit (a freshly-hired agent
//     lands without an McpClient until the reconciler picks it up).
//
// Both paths use the same algorithm; the difference is just the
// initial set of agents the SELECT returns.
func ReconcileMcpClients(ctx context.Context, deps ReconcileDeps) (ReconcileReport, error) {
	if deps.Client == nil {
		return ReconcileReport{}, errors.New("mcpjungle: ReconcileMcpClients: Client is nil")
	}
	if deps.VaultClient == nil {
		return ReconcileReport{}, errors.New("mcpjungle: ReconcileMcpClients: VaultClient is nil")
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	agents, err := deps.Queries.ListActiveAgents(ctx)
	if err != nil {
		return ReconcileReport{}, fmt.Errorf("mcpjungle: ListActiveAgents: %w", err)
	}
	report := ReconcileReport{}
	for _, agent := range agents {
		result := reconcileOne(ctx, deps, logger, agent)
		switch result.outcome {
		case outcomeCreated:
			report.Created = append(report.Created, agent.ID)
		case outcomeExisting:
			report.Existing = append(report.Existing, agent.ID)
		case outcomeFailed:
			report.Failed = append(report.Failed, ReconcileFailure{AgentID: agent.ID, Reason: result.reason})
		}
	}
	logger.Info("mcpjungle: reconcile complete",
		"created", len(report.Created),
		"existing", len(report.Existing),
		"failed", len(report.Failed),
	)
	return report, nil
}

type reconcileOutcome int

const (
	outcomeCreated reconcileOutcome = iota
	outcomeExisting
	outcomeFailed
)

type reconcileResult struct {
	outcome reconcileOutcome
	reason  string
}

func reconcileOne(ctx context.Context, deps ReconcileDeps, logger *slog.Logger, agent store.Agent) reconcileResult {
	// Derive the canonical McpClient name per FR-304's customer-prefix
	// convention. Agent UUID is shortened to 8 hex chars for readability
	// + bounded length; uniqueness is preserved because the UUIDs are
	// 122 random bits — 8 hex chars = 32 bits collisions would require
	// ~65k agents per customer to risk.
	clientName := fmt.Sprintf("%s.%s.%s", deps.CustomerSlug, agent.RoleSlug, shortAgentID(agent.ID))

	token, err := generateBearerToken()
	if err != nil {
		return reconcileResult{outcome: outcomeFailed, reason: fmt.Sprintf("generate token: %v", err)}
	}
	var allowList []string
	if len(agent.McpServersJsonb) > 0 {
		// agents.mcp_servers_jsonb is operator-approved JSON; the
		// reconciler trusts it verbatim. Empty array means "no MCP
		// servers reachable yet" — agent can still spawn but its
		// tool_use calls against MCPJungle servers return 403.
		var parsed []string
		if err := decodeAllowList(agent.McpServersJsonb, &parsed); err == nil {
			allowList = parsed
		}
	}

	_, err = deps.Client.CreateMcpClient(ctx, CreateMcpClientParams{
		Name:        clientName,
		AllowList:   allowList,
		AccessToken: token,
	})
	if err != nil && errors.Is(err, ErrServerRegistrationConflict) {
		logger.Debug("mcpjungle: client already exists, skipping", "name", clientName, "agent_id", uuidString(agent.ID))
		return reconcileResult{outcome: outcomeExisting}
	}
	if err != nil {
		return reconcileResult{outcome: outcomeFailed, reason: fmt.Sprintf("CreateMcpClient: %v", err)}
	}

	// Write the bearer token to vault at mcpjungle/agents/<agent-id>.
	vaultPath := fmt.Sprintf("mcpjungle/agents/%s", uuidString(agent.ID))
	if err := deps.VaultClient.WriteSecret(ctx, vaultPath, token); err != nil {
		// Best-effort: try to clean up the orphan McpClient. If
		// cleanup also fails, log + return; idempotent re-run will
		// recover.
		if delErr := deps.Client.DeleteMcpClient(ctx, clientName); delErr != nil {
			logger.Warn("mcpjungle: vault write failed + cleanup also failed",
				"agent_id", uuidString(agent.ID), "vault_err", err, "delete_err", delErr)
		}
		return reconcileResult{outcome: outcomeFailed, reason: fmt.Sprintf("vault write: %v", err)}
	}

	// Insert the agent-scoped grant so the spawn-time vault fetcher
	// resolves the path for this agent.
	if err := deps.Queries.InsertAgentScopedSecret(ctx, store.InsertAgentScopedSecretParams{
		RoleSlug:   agent.RoleSlug,
		AgentID:    agent.ID,
		SecretPath: vaultPath,
		EnvVarName: vault.MCPJungleBearerTokenEnvVar,
		CustomerID: deps.CustomerID,
		GrantedBy:  "mcpjungle.reconciler",
	}); err != nil {
		logger.Warn("mcpjungle: grant insert failed",
			"agent_id", uuidString(agent.ID), "err", err)
		return reconcileResult{outcome: outcomeFailed, reason: fmt.Sprintf("InsertAgentScopedSecret: %v", err)}
	}

	logger.Info("mcpjungle: created McpClient",
		"name", clientName, "agent_id", uuidString(agent.ID), "allow_list_count", len(allowList))
	return reconcileResult{outcome: outcomeCreated}
}

// decodeAllowList parses the agent's mcp_servers JSONB column into a
// []string. The column is operator-approved JSON; if it doesn't decode
// the reconciler treats the agent as having an empty allow list (the
// agent can still spawn but its MCPJungle tool calls return 403 until
// the column is fixed and the reconciler re-runs).
func decodeAllowList(raw []byte, out *[]string) error {
	return json.Unmarshal(raw, out)
}

// generateBearerToken produces a 32-byte (256-bit) cryptographically
// random token hex-encoded to 64 chars. Plenty of entropy for opaque
// bearer auth.
func generateBearerToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// shortAgentID returns the first 8 hex chars of the agent UUID.
// Sufficient discriminator for MCPJungle client names within a single
// customer's namespace.
func shortAgentID(u pgtype.UUID) string {
	if !u.Valid {
		return "00000000"
	}
	return hex.EncodeToString(u.Bytes[:4])
}

// uuidString formats a pgtype.UUID as canonical 36-char hex. Mirrors
// the helper in internal/throttle to avoid cross-package internal
// coupling.
func uuidString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		u.Bytes[0:4], u.Bytes[4:6], u.Bytes[6:8], u.Bytes[8:10], u.Bytes[10:16])
}
