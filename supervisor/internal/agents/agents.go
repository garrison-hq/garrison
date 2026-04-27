// Package agents owns the supervisor's in-memory cache of agent rows.
//
// M2.1 loaded every status='active' row from the `agents` table once at
// startup and never reloaded. M4 (T014) adds a Reset path: the dashboard
// emits pg_notify('agents.changed', role_slug) on every successful
// agents-row write, and the supervisor's StartChangeListener (listener.go)
// drives Cache.Reset on receipt. The startup-once invariant is preserved
// for the no-edits common case (no Reset calls means no rebuilds); only
// edits trigger re-reads. Concurrent Get + Reset are safe via the cache's
// RWMutex.
package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

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
// `mcp_config` is carried for M2.3 T013: Rule 3 checks agent-specific
// MCP servers before spawn (FR-410 / D2.4).
type Agent struct {
	ID           pgtype.UUID
	DepartmentID pgtype.UUID
	Role         string
	AgentMD      string
	Model        string
	ListensFor   []string
	PalaceWing   *string
	McpConfig    []byte
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
// Concurrent Get + Reset are safe via mu; the M4 Reset path takes
// the write lock briefly while swapping the map.
type Cache struct {
	q          Querier
	mu         sync.RWMutex
	byDeptRole map[key]Agent
}

// NewCache runs a single SELECT via q.ListActiveAgents, deserialises the
// JSONB listens_for payload, and indexes the result by (department, role).
// An error here is fatal at supervisor startup — the dispatcher cannot
// register handlers without the agent rows.
func NewCache(ctx context.Context, q Querier) (*Cache, error) {
	c := &Cache{q: q}
	if err := c.refresh(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

// refresh reloads all active-agent rows and atomically swaps them into
// the cache. Holds the write lock only while assigning the new map —
// the SELECT itself runs without the lock so a slow query doesn't
// stall concurrent Get callers reading the previous snapshot.
func (c *Cache) refresh(ctx context.Context) error {
	rows, err := c.q.ListActiveAgents(ctx)
	if err != nil {
		return fmt.Errorf("agents: list active: %w", err)
	}
	m := make(map[key]Agent, len(rows))
	for _, r := range rows {
		var listensFor []string
		if len(r.ListensFor) > 0 {
			if err := json.Unmarshal(r.ListensFor, &listensFor); err != nil {
				return fmt.Errorf("agents: decode listens_for for role=%s: %w", r.RoleSlug, err)
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
			McpConfig:    r.McpConfig,
		}
	}
	c.mu.Lock()
	c.byDeptRole = m
	c.mu.Unlock()
	return nil
}

// Reset re-fetches all active agents and atomically swaps the cache map.
// Called by listener.go on every agents.changed notification (M4 / T014).
//
// roleSlug is accepted for logging + observability — the implementation
// re-fetches all active agents rather than per-role partial updates,
// which keeps the consistency model simple at solo-operator scale (rare
// events, small agent count). Future milestones may optimise to a
// per-role refresh if scale demands it.
func (c *Cache) Reset(ctx context.Context, _ string) error {
	return c.refresh(ctx)
}

// GetForDepartmentAndRole returns the cached Agent for the given
// department/role pair. Returns ErrAgentNotFound if no active row matched
// the latest cache snapshot; callers (spawn.go) turn that into an
// exit_reason='agent_missing' terminal per plan.md §"internal/spawn".
//
// ctx is accepted for interface-consistency with hot-reload paths;
// the current implementation does no I/O on the read path.
func (c *Cache) GetForDepartmentAndRole(_ context.Context, deptID pgtype.UUID, role string) (Agent, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	a, ok := c.byDeptRole[key{dept: deptID, role: role}]
	if !ok {
		return Agent{}, ErrAgentNotFound
	}
	return a, nil
}

// Len reports the number of cached agents. Useful for startup logging
// and for the concurrency test that wants to assert the cache is
// populated without reaching in for the map directly.
func (c *Cache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.byDeptRole)
}
