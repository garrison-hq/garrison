package spawn

import "sync"

// AgentInflight tracks which agents currently have a spawn in flight.
//
// M7.1 T010 / FR-017: the container path serializes spawns per agent —
// each per-agent container hosts one claude exec at a time, so a second
// event for an agent whose slot is held must defer (cap-full semantics)
// instead of racing two execs into one container. Keyed by the agent's
// UUID text; one instance is shared process-wide via spawn.Deps.
// Department caps are independent and unchanged (Constitution X).
type AgentInflight struct {
	mu       sync.Mutex
	inflight map[string]struct{}
}

// NewAgentInflight returns a ready-to-share slot tracker.
func NewAgentInflight() *AgentInflight {
	return &AgentInflight{inflight: make(map[string]struct{})}
}

// TryAcquire claims agentID's slot. ok=false means a spawn for this
// agent is already in flight (release is nil). On success the returned
// release frees the slot; it is idempotent so callers can defer it on
// every exit path without double-release bookkeeping.
func (a *AgentInflight) TryAcquire(agentID string) (release func(), ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.inflight == nil {
		a.inflight = make(map[string]struct{})
	}
	if _, held := a.inflight[agentID]; held {
		return nil, false
	}
	a.inflight[agentID] = struct{}{}
	var once sync.Once
	return func() {
		once.Do(func() {
			a.mu.Lock()
			delete(a.inflight, agentID)
			a.mu.Unlock()
		})
	}, true
}
