package agentcontainer

import (
	"context"
	"fmt"
	"io"
	"strings"
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

	// ExecResults scripts Exec per container: each call pops the
	// queue's next entry. An empty queue yields a default session
	// (empty streams, exit 0).
	ExecResults map[string][]FakeExecResult

	// CreateError, StartError, etc. let tests inject failures.
	CreateError      error
	StartError       error
	StopError        error
	RemoveError      error
	ExecError        error
	RestartError     error
	ImageDigestValue string
	ImageDigestError error

	nextID     int
	nextExecID int
}

type FakeCall struct {
	Method string
	Spec   ContainerSpec // for Create
	ID     string        // for Start / Stop / Restart / Remove / Exec / ConnectNetwork
	Exec   ExecSpec      // for Exec — the full per-exec spec (Cmd/Env/WorkingDir)
	NetID  string        // for ConnectNetwork
}

// FakeExecResult scripts one Exec invocation: the streamed output, the
// exit code ExitCode reports once the streams drain, and the optional
// errors for the Exec call itself or the exit-code inspect.
type FakeExecResult struct {
	Stdout     string
	Stderr     string
	ExitCode   int
	InspectErr error // ExitCode returns (-1, InspectErr)
	Err        error // Exec itself fails; no session is returned
}

// FakeContainerState tracks per-container state in the fake.
type FakeContainerState struct {
	Spec     ContainerSpec
	State    string // "created", "running", "stopped", "removed"
	Networks []string
	// Labels mirrors the create body's label set (including the shape
	// hash) so ReconcileShape can classify recreate-vs-noop. Tests
	// seed old-shape containers by clearing or staling the map.
	Labels map[string]string
}

// NewFakeController constructs an empty fake.
func NewFakeController() *FakeController {
	return &FakeController{
		Containers:       map[string]*FakeContainerState{},
		ExecResults:      map[string][]FakeExecResult{},
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
	st := &FakeContainerState{Spec: spec, State: "created"}
	// Best-effort label mirror: specs too sparse for buildCreateBody
	// (older tests) simply get no labels, which ReconcileShape treats
	// as old-shape — exactly like the real unlabeled fleet.
	if body, err := buildCreateBody(spec); err == nil {
		st.Labels = body.Labels
	}
	f.Containers[id] = st
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
		return errContainerNotFoundf(id)
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
		return errContainerNotFoundf(id)
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
		return errContainerNotFoundf(id)
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
		return errContainerNotFoundf(id)
	}
	st.Networks = append(st.Networks, network)
	f.Calls = append(f.Calls, FakeCall{Method: "ConnectNetwork", ID: id, NetID: network})
	return nil
}

// Exec pops the next scripted FakeExecResult for the container (or a
// zero default) and returns it as a finished ExecSession with the
// scripted exit code.
func (f *FakeController) Exec(_ context.Context, id string, spec ExecSpec) (*ExecSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ExecError != nil {
		return nil, f.ExecError
	}
	if _, ok := f.Containers[id]; !ok {
		return nil, errContainerNotFoundf(id)
	}
	res := FakeExecResult{}
	if queue := f.ExecResults[id]; len(queue) > 0 {
		res = queue[0]
		f.ExecResults[id] = queue[1:]
	}
	f.Calls = append(f.Calls, FakeCall{Method: "Exec", ID: id, Exec: spec})
	if res.Err != nil {
		return nil, res.Err
	}
	f.nextExecID++
	return &ExecSession{
		ID:     fmt.Sprintf("fake-exec-%d", f.nextExecID),
		Stdout: io.NopCloser(strings.NewReader(res.Stdout)),
		Stderr: io.NopCloser(strings.NewReader(res.Stderr)),
		inspect: func(context.Context) (bool, int, error) {
			if res.InspectErr != nil {
				return false, -1, res.InspectErr
			}
			return false, res.ExitCode, nil
		},
	}, nil
}

// Restart records the call and leaves the container running — the
// fake analog of the idle `sleep infinity` PID 1 coming back.
func (f *FakeController) Restart(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.RestartError != nil {
		return f.RestartError
	}
	st, ok := f.Containers[id]
	if !ok {
		return errContainerNotFoundf(id)
	}
	st.State = "running"
	f.Calls = append(f.Calls, FakeCall{Method: "Restart", ID: id})
	return nil
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

// ReconcileShape mirrors the production algorithm against the
// in-memory state: per spec it finds the container whose Spec.AgentID
// matches, compares the stored label set's shape hash to the desired
// one, and converges via the fake's own Create/Start/Stop/Remove so
// the Calls log records the same sequence the real impl would issue.
func (f *FakeController) ReconcileShape(ctx context.Context, specs []ContainerSpec) (ShapeReport, error) {
	report := ShapeReport{}
	for _, spec := range specs {
		desired, err := buildCreateBody(spec)
		if err != nil {
			return report, err
		}
		desiredHash := desired.Labels[shapeHashLabel]

		id, labels, state, found := f.findByAgentID(spec.AgentID)
		switch {
		case !found:
			if err := f.fakeCreateAndStart(ctx, spec); err != nil {
				return report, err
			}
			report.Created = append(report.Created, spec.AgentID)
		case labels[shapeHashLabel] != desiredHash:
			if err := f.Stop(ctx, id); err != nil {
				return report, err
			}
			if err := f.Remove(ctx, id); err != nil {
				return report, err
			}
			if err := f.fakeCreateAndStart(ctx, spec); err != nil {
				return report, err
			}
			report.Recreated = append(report.Recreated, spec.AgentID)
		case state != "running":
			if err := f.Start(ctx, id); err != nil {
				return report, err
			}
			report.Restarted = append(report.Restarted, spec.AgentID)
		default:
			report.Unchanged = append(report.Unchanged, spec.AgentID)
		}
	}
	return report, nil
}

// findByAgentID snapshots the matching container's identity and state
// under the lock; ReconcileShape's mutations go through the public
// methods, which take the lock per call.
func (f *FakeController) findByAgentID(agentID string) (id string, labels map[string]string, state string, found bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for cid, st := range f.Containers {
		if st.Spec.AgentID == agentID {
			return cid, st.Labels, st.State, true
		}
	}
	return "", nil, "", false
}

func (f *FakeController) fakeCreateAndStart(ctx context.Context, spec ContainerSpec) error {
	id, err := f.Create(ctx, spec)
	if err != nil {
		return err
	}
	return f.Start(ctx, id)
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

// errContainerNotFoundf wraps ErrContainerNotFound with the offending
// container id. Centralised so the "not found: <id>" string isn't
// duplicated across every method (Sonar S1192).
func errContainerNotFoundf(id string) error {
	return fmt.Errorf("%w: %s", ErrContainerNotFound, id)
}
