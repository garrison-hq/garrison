package chat

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

// TestSessionMutex_SameUUIDReturnsSamePointer pins the per-session
// serialization invariant: two AcquireForSession calls for the same
// UUID return the same *sync.Mutex. A regression here would let two
// concurrent operator messages on the same session race the spawn
// path and produce duplicate assistant turn_index INSERT failures.
func TestSessionMutex_SameUUIDReturnsSamePointer(t *testing.T) {
	r := newSessionMutexRegistry()
	var u pgtype.UUID
	if err := u.Scan("11111111-2222-4333-8444-555555555555"); err != nil {
		t.Fatalf("scan: %v", err)
	}
	m1 := r.AcquireForSession(u)
	m2 := r.AcquireForSession(u)
	if m1 != m2 {
		t.Errorf("same UUID returned different mutexes: %p vs %p", m1, m2)
	}
}

// TestSessionMutex_DifferentUUIDsReturnDifferentMutexes: distinct
// session_ids must NOT share a mutex — that would block independent
// chat sessions on each other. The map cardinality assertion guards
// the bucketing logic.
func TestSessionMutex_DifferentUUIDsReturnDifferentMutexes(t *testing.T) {
	r := newSessionMutexRegistry()
	var a, b pgtype.UUID
	if err := a.Scan("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"); err != nil {
		t.Fatalf("scan a: %v", err)
	}
	if err := b.Scan("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"); err != nil {
		t.Fatalf("scan b: %v", err)
	}
	mA := r.AcquireForSession(a)
	mB := r.AcquireForSession(b)
	if mA == mB {
		t.Errorf("distinct UUIDs collapsed onto a single mutex: %p", mA)
	}
	if got := len(r.per); got != 2 {
		t.Errorf("registry size = %d; want 2", got)
	}
}

// TestSessionMutex_InvalidUUIDFallsBackToZeroKey pins the defensive
// branch: callers passing a zero pgtype.UUID still get a usable
// mutex (a single shared "no-uuid" mutex). This mainly exists so
// HandleMessageInSession's nil-uuid edge doesn't crash.
func TestSessionMutex_InvalidUUIDFallsBackToZeroKey(t *testing.T) {
	r := newSessionMutexRegistry()
	m1 := r.AcquireForSession(pgtype.UUID{})
	m2 := r.AcquireForSession(pgtype.UUID{})
	if m1 == nil || m2 == nil {
		t.Fatal("invalid UUID returned a nil mutex")
	}
	if m1 != m2 {
		t.Errorf("two zero-UUID acquires returned different mutexes")
	}
}

// TestSessionMutex_LockUnlockDoesNotPanic exercises the actual
// Lock/Unlock cycle so the test isn't just pointer-comparison theatre.
// A double-acquire-then-double-release pattern (sequential, not
// concurrent) is enough — concurrent semantics are sync.Mutex's
// concern and not under test here.
func TestSessionMutex_LockUnlockDoesNotPanic(t *testing.T) {
	r := newSessionMutexRegistry()
	var u pgtype.UUID
	if err := u.Scan("12345678-1234-4234-8234-123456789012"); err != nil {
		t.Fatalf("scan: %v", err)
	}
	m := r.AcquireForSession(u)
	m.Lock()
	m.Unlock()
	m.Lock()
	m.Unlock()
}
