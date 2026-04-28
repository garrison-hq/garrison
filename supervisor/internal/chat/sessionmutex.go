package chat

import (
	"sync"

	"github.com/jackc/pgx/v5/pgtype"
)

// sessionMutexRegistry is an in-process per-session mutex map. Single-
// operator + per-session serial processing means low contention; an
// in-process mutex is sufficient (no Postgres advisory lock needed).
//
// AcquireForSession returns a *sync.Mutex keyed by session_id. The
// caller MUST call Lock before any work and Unlock when done. The
// registry holds mutexes indefinitely; on a multi-day chat the map
// grows by N sessions but each entry is small (a few words).
type sessionMutexRegistry struct {
	mu  sync.Mutex
	per map[[16]byte]*sync.Mutex
}

func newSessionMutexRegistry() *sessionMutexRegistry {
	return &sessionMutexRegistry{per: make(map[[16]byte]*sync.Mutex, 4)}
}

func (r *sessionMutexRegistry) AcquireForSession(id pgtype.UUID) *sync.Mutex {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !id.Valid {
		// Defensive: a zero UUID still gets a mutex; multiple
		// callers with zero UUIDs serialize together.
		return r.getOrCreateLocked([16]byte{})
	}
	return r.getOrCreateLocked(id.Bytes)
}

func (r *sessionMutexRegistry) getOrCreateLocked(key [16]byte) *sync.Mutex {
	if m, ok := r.per[key]; ok {
		return m
	}
	m := &sync.Mutex{}
	r.per[key] = m
	return m
}
