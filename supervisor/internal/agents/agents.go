// Package agents owns the supervisor's in-memory cache of agent rows.
//
// M2.1 loads every status='active' row from the `agents` table once at
// startup and never reloads. Agent edits therefore require a supervisor
// restart — consistent with the M1 stance on config being process-lifetime
// (plan.md §"internal/agents", §"Deferred to later milestones").
// MemPalace-era flows in M2.2+ will revisit hot-reload; the hook is
// intentionally absent for M2.1.
package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5/pgtype"
)

// ErrAgentNotFound is returned by Cache.GetForDepartmentAndRole when no
// cached active agent matches the (department, role) key.
var ErrAgentNotFound = errors.New("agents: no active agent for department+role")

// Agent is the cache-shaped view of a row from the `agents` table.
//
// The JSONB `skills` and `mcp_tools` columns are intentionally omitted —
// M2.1 does not act on either (M3+ territory). `listens_for` is carried
// because main.go wires event-handler registration off it in T014.
type Agent struct {
	ID           pgtype.UUID
	DepartmentID pgtype.UUID
	Role         string
	AgentMD      string
	Model        string
	ListensFor   []string
	PalaceWing   *string
}

// Querier is the narrow seam the cache depends on. *store.Queries satisfies
// it directly; unit tests substitute a stub that returns canned rows.
type Querier interface {
	ListActiveAgents(ctx context.Context) ([]store.Agent, error)
}

type key struct {
	dept pgtype.UUID
	role string
}

// Cache holds the loaded agent rows keyed by (department, role).
// Concurrent GetForDepartmentAndRole calls are safe; the map is only
// written once inside NewCache before the Cache is returned.
type Cache struct {
	byDeptRole map[key]Agent
}

// NewCache runs a single SELECT via q.ListActiveAgents, deserialises the
// JSONB listens_for payload, and indexes the result by (department, role).
// An error here is fatal at supervisor startup — the dispatcher cannot
// register handlers without the agent rows.
func NewCache(ctx context.Context, q Querier) (*Cache, error) {
	rows, err := q.ListActiveAgents(ctx)
	if err != nil {
		return nil, fmt.Errorf("agents: list active: %w", err)
	}
	m := make(map[key]Agent, len(rows))
	for _, r := range rows {
		var listensFor []string
		if len(r.ListensFor) > 0 {
			if err := json.Unmarshal(r.ListensFor, &listensFor); err != nil {
				return nil, fmt.Errorf("agents: decode listens_for for role=%s: %w", r.RoleSlug, err)
			}
		}
		m[key{dept: r.DepartmentID, role: r.RoleSlug}] = Agent{
			ID:           r.ID,
			DepartmentID: r.DepartmentID,
			Role:         r.RoleSlug,
			AgentMD:      r.AgentMd,
			Model:        r.Model,
			ListensFor:   listensFor,
			PalaceWing:   r.PalaceWing,
		}
	}
	return &Cache{byDeptRole: m}, nil
}

// GetForDepartmentAndRole returns the cached Agent for the given
// department/role pair. Returns ErrAgentNotFound if no active row matched
// at NewCache time; callers (spawn.go) turn that into an
// exit_reason='agent_missing' terminal per plan.md §"internal/spawn".
//
// ctx is accepted for interface-consistency with the hot-reload shape
// M2.2+ may introduce; the current implementation does no I/O.
func (c *Cache) GetForDepartmentAndRole(_ context.Context, deptID pgtype.UUID, role string) (Agent, error) {
	a, ok := c.byDeptRole[key{dept: deptID, role: role}]
	if !ok {
		return Agent{}, ErrAgentNotFound
	}
	return a, nil
}

// Len reports the number of cached agents. Useful for startup logging
// and for the concurrency test that wants to assert the cache is
// populated without reaching in for the map directly.
func (c *Cache) Len() int { return len(c.byDeptRole) }
