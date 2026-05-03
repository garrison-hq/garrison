// Package migrate7 carries the one-shot grandfathering migration that
// moves M2.x-seeded agents (engineer, qa-engineer, tech-writer) from
// the supervisor's direct-exec path to per-agent containers at the
// M7 cutover. Run() is invoked at supervisor startup; it's idempotent
// (a second run is a no-op because no row matches the predicate).
//
// Per spec FR-112 / decision #6: existing M2.x agents are grandfathered
// with a single audit row recording the cutover, NOT routed through
// the propose → approve flow. The audit shape is uniform with the
// chat-driven approve_hire path so a forensic walk surfaces both as
// "operator approved this agent at <timestamp>".
package migrate7

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/garrison-hq/garrison/supervisor/internal/agentcontainer"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Deps wraps the runtime collaborators Run consumes. Pool drives both
// the SELECT/UPDATE batch + the audit-row INSERT; Controller manages
// per-agent container lifecycle; ImageRef + UIDRange come from
// internal/config.
type Deps struct {
	Pool        *pgxpool.Pool
	Queries     *store.Queries
	Controller  agentcontainer.Controller
	Logger      *slog.Logger
	ImageRef    string // e.g. "garrison-claude:m5"; resolved → digest by Controller.ImageDigest
	UIDStart    int    // per-customer host_uid range start (cfg.AgentDefaults.UIDRangeStart)
	UIDEnd      int    // ceiling
	WorkspaceFS string // host base dir for /var/lib/garrison/workspaces/<id>:/workspace
	SkillsFS    string // host base dir for /var/lib/garrison/skills/<id>:/workspace/.claude/skills
	Memory      string // "512m"
	CPUs        string // "1.0"
	PIDsLimit   int    // 200
	OnComplete  func(flipUseDirectExec bool)
}

// Run grandfathers every agent with last_grandfathered_at IS NULL.
// Per the test plan in tasks.md T014: idempotent (second run is a
// no-op); mid-spawn-safe (in-flight direct-exec spawns finish under
// direct-exec; new spawns post-migration use docker-exec).
func Run(ctx context.Context, deps Deps) error {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	q := deps.Queries

	rows, err := q.ListAgentsNotGrandfathered(ctx)
	if err != nil {
		return fmt.Errorf("migrate7: list pending agents: %w", err)
	}
	if len(rows) == 0 {
		logger.Info("migrate7: no pending agents; nothing to do (idempotent no-op)")
		if deps.OnComplete != nil {
			deps.OnComplete(false)
		}
		return nil
	}

	digest, err := deps.Controller.ImageDigest(ctx, deps.ImageRef)
	if err != nil {
		return fmt.Errorf("migrate7: resolve image digest %q: %w", deps.ImageRef, err)
	}

	logger.Info("migrate7: starting grandfathering",
		"pending_count", len(rows), "image_ref", deps.ImageRef, "image_digest", digest)

	for _, agent := range rows {
		if err := grandfatherOne(ctx, deps, agent, digest, logger); err != nil {
			return fmt.Errorf("migrate7: grandfather agent %q: %w", agent.RoleSlug, err)
		}
	}
	logger.Info("migrate7: complete; flipping cfg.UseDirectExec=false")
	if deps.OnComplete != nil {
		deps.OnComplete(true)
	}
	return nil
}

func grandfatherOne(ctx context.Context, deps Deps, agent store.Agent, digest string, logger *slog.Logger) error {
	tx, err := deps.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := deps.Queries.WithTx(tx)

	uid, err := q.AllocateNextHostUID(ctx, store.AllocateNextHostUIDParams{
		Column1: int32(deps.UIDStart),
		Column2: int32(deps.UIDEnd),
	})
	if err != nil {
		return fmt.Errorf("allocate host uid: %w", err)
	}
	if uid > int32(deps.UIDEnd) {
		return fmt.Errorf("host_uid range exhausted (%d > %d)", uid, deps.UIDEnd)
	}

	containerName := "garrison-agent-" + agent.RoleSlug
	spec := agentcontainer.ContainerSpec{
		AgentID:     uuidString(agent.ID),
		Image:       digest,
		HostUID:     int(uid),
		Workspace:   deps.WorkspaceFS + "/" + agent.RoleSlug,
		Skills:      deps.SkillsFS + "/" + agent.RoleSlug,
		NetworkName: "none",
		Memory:      deps.Memory,
		CPUs:        deps.CPUs,
		PIDsLimit:   deps.PIDsLimit,
	}
	containerID, err := deps.Controller.Create(ctx, spec)
	if err != nil {
		return fmt.Errorf("controller.Create %s: %w", containerName, err)
	}
	if err := deps.Controller.Start(ctx, containerID); err != nil {
		return fmt.Errorf("controller.Start %s: %w", containerName, err)
	}

	if _, err := q.InsertAgentContainerEvent(ctx, store.InsertAgentContainerEventParams{
		AgentID:     agent.ID,
		Kind:        "migrated",
		ImageDigest: stringPtr(digest),
	}); err != nil {
		return fmt.Errorf("insert agent_container_events: %w", err)
	}

	auditBody, _ := json.Marshal(map[string]any{
		"agent_id":     uuidString(agent.ID),
		"role_slug":    agent.RoleSlug,
		"image_digest": digest,
		"host_uid":     uid,
		"container_id": containerID,
		"reason":       "M7 cutover: direct-exec → per-agent container",
	})
	if _, err := q.InsertChatMutationAudit(ctx, store.InsertChatMutationAuditParams{
		ChatSessionID:        pgtype.UUID{Valid: false},
		ChatMessageID:        pgtype.UUID{Valid: false},
		Verb:                 "grandfathered_at_m7",
		ArgsJsonb:            auditBody,
		Outcome:              "success",
		ReversibilityClass:   3,
		AffectedResourceID:   stringPtr(uuidString(agent.ID)),
		AffectedResourceType: stringPtr("agent_role"),
	}); err != nil {
		return fmt.Errorf("insert chat_mutation_audit: %w", err)
	}

	if err := q.SetAgentLastGrandfathered(ctx, store.SetAgentLastGrandfatheredParams{
		ImageDigest: stringPtr(digest),
		HostUid:     int32Ptr(uid),
		ID:          agent.ID,
	}); err != nil {
		return fmt.Errorf("set last_grandfathered_at: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	logger.Info("migrate7: grandfathered",
		"agent_id", uuidString(agent.ID),
		"role_slug", agent.RoleSlug,
		"container_id", containerID,
		"host_uid", uid,
		"image_digest", digest,
	)
	return nil
}

func uuidString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	v, err := u.Value()
	if err != nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func stringPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func int32Ptr(v int32) *int32 { return &v }
