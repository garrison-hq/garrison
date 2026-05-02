package agentcontainer

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
)

// FakeController is the in-memory test implementation. Records every
// call so unit tests can assert request shapes without a real Docker;
// the spawn-side integration tests in T011 use this to drive
// deterministic exec sequences.
//
// FakeController is safe for concurrent use; the supervisor's spawn
// dispatcher invokes Controller methods from multiple goroutines.
type FakeController struct {
	mu sync.Mutex

	// Calls records every method invocation in order.
	Calls []FakeCall

	// Containers tracks the in-memory state machine. Keyed by ID.
	Containers map[string]*FakeContainerState

	// ExecOutputs configurable per container — when Exec is called,
	// the fake reads the queue's next entry and returns it as stdout.
	// If empty, the fake echoes stdin.
	ExecOutputs map[string][]string

	// CreateError, StartError, etc. let tests inject failures.
	CreateError      error
	StartError       error
	StopError        error
	RemoveError      error
	ExecError        error
	ImageDigestValue string
	ImageDigestError error

	nextID int
}

type FakeCall struct {
	Method string
	Spec   ContainerSpec // for Create
	ID     string        // for Start / Stop / Remove / Exec / ConnectNetwork
	Cmd    []string      // for Exec
	Stdin  string        // for Exec — read from the input reader at call time
	NetID  string        // for ConnectNetwork
}

// FakeContainerState tracks per-container state in the fake.
type FakeContainerState struct {
	Spec     ContainerSpec
	State    string // "created", "running", "stopped", "removed"
	Networks []string
}

// NewFakeController constructs an empty fake.
func NewFakeController() *FakeController {
	return &FakeController{
		Containers:       map[string]*FakeContainerState{},
		ExecOutputs:      map[string][]string{},
		ImageDigestValue: "sha256:fake-digest-deadbeef",
	}
}

func (f *FakeController) Create(_ context.Context, spec ContainerSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.CreateError != nil {
		return "", f.CreateError
	}
	f.nextID++
	id := fmt.Sprintf("fake-container-%d", f.nextID)
	f.Containers[id] = &FakeContainerState{Spec: spec, State: "created"}
	f.Calls = append(f.Calls, FakeCall{Method: "Create", Spec: spec, ID: id})
	return id, nil
}

func (f *FakeController) Start(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.StartError != nil {
		return f.StartError
	}
	st, ok := f.Containers[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrContainerNotFound, id)
	}
	st.State = "running"
	f.Calls = append(f.Calls, FakeCall{Method: "Start", ID: id})
	return nil
}

func (f *FakeController) Stop(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.StopError != nil {
		return f.StopError
	}
	st, ok := f.Containers[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrContainerNotFound, id)
	}
	st.State = "stopped"
	f.Calls = append(f.Calls, FakeCall{Method: "Stop", ID: id})
	return nil
}

func (f *FakeController) Remove(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.RemoveError != nil {
		return f.RemoveError
	}
	if _, ok := f.Containers[id]; !ok {
		return fmt.Errorf("%w: %s", ErrContainerNotFound, id)
	}
	delete(f.Containers, id)
	f.Calls = append(f.Calls, FakeCall{Method: "Remove", ID: id})
	return nil
}

func (f *FakeController) ConnectNetwork(_ context.Context, id, network string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, ok := f.Containers[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrContainerNotFound, id)
	}
	st.Networks = append(st.Networks, network)
	f.Calls = append(f.Calls, FakeCall{Method: "ConnectNetwork", ID: id, NetID: network})
	return nil
}

// Exec reads stdin into a captured buffer (so tests can assert what
// the supervisor wrote) and returns either the queued ExecOutputs
// or echoes stdin back as stdout.
func (f *FakeController) Exec(ctx context.Context, id string, cmd []string, stdin io.Reader) (io.ReadCloser, io.ReadCloser, error) {
	f.mu.Lock()
	if f.ExecError != nil {
		f.mu.Unlock()
		return nil, nil, f.ExecError
	}
	if _, ok := f.Containers[id]; !ok {
		f.mu.Unlock()
		return nil, nil, fmt.Errorf("%w: %s", ErrContainerNotFound, id)
	}
	queue := f.ExecOutputs[id]
	f.mu.Unlock()

	stdinCapture := ""
	if stdin != nil {
		buf, _ := io.ReadAll(stdin)
		stdinCapture = string(buf)
	}

	f.mu.Lock()
	f.Calls = append(f.Calls, FakeCall{Method: "Exec", ID: id, Cmd: cmd, Stdin: stdinCapture})
	if len(queue) > 0 {
		out := queue[0]
		f.ExecOutputs[id] = queue[1:]
		f.mu.Unlock()
		return io.NopCloser(bytes.NewReader([]byte(out))), io.NopCloser(bytes.NewReader(nil)), nil
	}
	f.mu.Unlock()

	// Echo stdin back as stdout — the simplest line-buffering
	// preserve check.
	return io.NopCloser(bytes.NewReader([]byte(stdinCapture))), io.NopCloser(bytes.NewReader(nil)), nil
}

// Reconcile uses the in-memory Containers map as "actual state".
// Compares against expected and produces a report identical in
// shape to the production impl.
func (f *FakeController) Reconcile(_ context.Context, expected []ExpectedContainer) (ReconcileReport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	report := ReconcileReport{}
	expectedByID := map[string]ExpectedContainer{}
	expectedByAgent := map[string]ExpectedContainer{}
	for _, e := range expected {
		expectedByID[e.ContainerID] = e
		expectedByAgent[e.AgentID] = e
	}

	// Walk actual containers; classify each.
	for cid, state := range f.Containers {
		exp, ok := expectedByID[cid]
		if !ok {
			// Orphan — no agents row references this container.
			delete(f.Containers, cid)
			report.GarbageCollected = append(report.GarbageCollected, cid)
			continue
		}
		switch exp.State {
		case ExpectedRunning:
			if state.State == "running" {
				report.AdoptedRunning = append(report.AdoptedRunning, exp.AgentID)
			} else {
				state.State = "running"
				report.Restarted = append(report.Restarted, exp.AgentID)
			}
		case ExpectedStopped, ExpectedRemoved:
			if state.State != "stopped" && state.State != "removed" {
				report.Mismatches = append(report.Mismatches, ReconcileMismatch{
					AgentID:    exp.AgentID,
					Expected:   exp.State,
					ActualKind: state.State,
					Reason:     "actual state does not match expected",
				})
			}
		}
	}
	return report, nil
}

func (f *FakeController) ImageDigest(_ context.Context, _ string) (string, error) {
	if f.ImageDigestError != nil {
		return "", f.ImageDigestError
	}
	return f.ImageDigestValue, nil
}

// ResetCalls clears the recorded call history (used between subtests).
func (f *FakeController) ResetCalls() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = nil
}

// Compile-time assert: FakeController implements Controller.
var _ Controller = (*FakeController)(nil)
