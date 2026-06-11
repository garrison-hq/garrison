// Package agentcontainer is the M7 Docker lifecycle controller.
// One persistent container per agents row (sandbox Rule 1); per-spawn
// invocation is docker exec rather than docker run (spike §8 P1's 10×
// cold-start gap drove the design).
//
// Controller is the abstraction the supervisor calls; socketproxy.go
// is the real implementation that talks to the M2.2 docker-socket-proxy
// over HTTP (no github.com/docker/docker/client dependency — locked-deps
// streak preserved). fake.go is the in-memory test impl.
//
// All Docker control-plane calls go through the proxy; the agent
// container itself does NOT mount the docker socket (sandbox Rule 6).
package agentcontainer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// Controller is the consumer-side surface every agentcontainer impl
// satisfies. The supervisor's spawn (T011), garrisonmutate.approve
// (T010), and migrate7 (T014) all call through this interface.
type Controller interface {
	// Create issues POST /containers/create with the spec's mount /
	// network / cap / resource shape. Returns the new container ID.
	// Does NOT start the container — Start is a separate call so the
	// supervisor can write its created event row inside the same tx
	// before the container starts.
	Create(ctx context.Context, spec ContainerSpec) (containerID string, err error)

	// Start issues POST /containers/<id>/start.
	Start(ctx context.Context, containerID string) error

	// Stop issues POST /containers/<id>/stop with a 10s grace window.
	Stop(ctx context.Context, containerID string) error

	// Restart issues POST /containers/<id>/restart with a 5s grace
	// window. The M7.1 SIGKILL analog (FR-016 backstop): restarting
	// the container kills every in-flight exec and returns the idle
	// `sleep infinity` PID 1. Requires ALLOW_RESTARTS on the proxy.
	Restart(ctx context.Context, containerID string) error

	// Remove issues DELETE /containers/<id> with force=true.
	Remove(ctx context.Context, containerID string) error

	// Exec runs spec.Cmd inside the existing container and returns an
	// ExecSession streaming the demuxed stdout/stderr. No stdin attach
	// and no connection hijacking (FR-004): the exec-start response is
	// a normal chunked application/vnd.docker.raw-stream demultiplexed
	// in-process (spike F2). Per-exec spec.Env is the ONLY transit for
	// secrets/runtime env (FR-002).
	//
	// Exec-create against a missing or stopped container maps to
	// ErrContainerNotFound so spawn lands in spawn_failed (FR-019).
	Exec(ctx context.Context, containerID string, spec ExecSpec) (*ExecSession, error)

	// ConnectNetwork attaches the container to a named Docker network
	// (opt-in sidecar reach per sandbox Rule 3). Called after Create
	// for each network in spec.Networks beyond --network=none.
	ConnectNetwork(ctx context.Context, containerID, networkName string) error

	// Reconcile compares the expected set of containers against the
	// Docker daemon's actual state and reports drift. Called once at
	// supervisor startup (FR-214) before normal lifecycle resumes.
	Reconcile(ctx context.Context, expected []ExpectedContainer) (ReconcileReport, error)

	// ReconcileShape converges every container addressed by
	// ContainerName(spec.AgentID) to the desired create shape (FR-007,
	// M7.1 boot reconcile): missing → create+start; shape-hash label
	// absent or stale → stop+remove+create+start; hash match but
	// stopped → start; hash match and running → no-op. Containers it
	// doesn't address by name (chat, compose services) are never
	// touched. Per-spec failures accumulate into the returned error
	// while the walk continues; the report covers what succeeded.
	ReconcileShape(ctx context.Context, specs []ContainerSpec) (ShapeReport, error)

	// ImageDigest returns the pinned RepoDigest for the given image
	// reference. Used by migrate7 + approve helpers (decision #22):
	// agents.image_digest is set to this value at activation, and
	// every spawn re-validates against the running container's
	// recorded digest.
	ImageDigest(ctx context.Context, imageRef string) (string, error)
}

// ExecSpec is the input to Exec. Fields map to the Docker Engine
// API's /containers/<id>/exec body.
type ExecSpec struct {
	Cmd        []string // full argv, argv[0] absolute
	Env        []string // per-exec env — the ONLY transit for secrets/runtime env (FR-002)
	WorkingDir string   // "/workspace" for claude execs (FR-006)
}

// Exit-code polling knobs (plan §1): after the demuxed stream EOFs the
// exec is usually already done, so the first inspect fires immediately
// and at most execExitPollBudget inspects run, execExitPollInterval
// apart, before ExitCode gives up with (-1, error).
const (
	execExitPollBudget   = 10
	execExitPollInterval = 200 * time.Millisecond
)

// ExecSession is one in-flight (or finished) exec. Stdout/Stderr are
// the demuxed raw-stream sides; closing Stdout tears down the raw
// response body and with it the demux goroutine (concurrency rule 1).
type ExecSession struct {
	ID     string
	Stdout io.ReadCloser // demuxed stream 1
	Stderr io.ReadCloser // demuxed stream 2

	// inspect reports the exec's Running flag + exit code. The
	// production impl binds GET /exec/<id>/json; the fake scripts it.
	inspect func(ctx context.Context) (running bool, exitCode int, err error)
	// pollInterval overrides execExitPollInterval when > 0 (tests).
	pollInterval time.Duration
}

// ExitCode polls the exec's inspect endpoint until Running=false, then
// returns its ExitCode. If the exec is still running after the poll
// budget, returns (-1, error) — the caller's adjudication falls back
// to result-frame evidence.
func (s *ExecSession) ExitCode(ctx context.Context) (int, error) {
	if s.inspect == nil {
		return -1, errors.New("agentcontainer: exec session has no inspect binding")
	}
	interval := s.pollInterval
	if interval <= 0 {
		interval = execExitPollInterval
	}
	for attempt := 0; attempt < execExitPollBudget; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return -1, ctx.Err()
			case <-time.After(interval):
			}
		}
		running, code, err := s.inspect(ctx)
		if err != nil {
			return -1, err
		}
		if !running {
			return code, nil
		}
	}
	return -1, fmt.Errorf("agentcontainer: exec %s still running after %d exit-code polls", s.ID, execExitPollBudget)
}

// ContainerSpec is the input to Create. Fields map 1:1 to Docker
// Engine API request body fields under HostConfig + top-level. The
// supervisor builds this from agents row fields (host_uid,
// runtime_caps, image_digest) + per-customer config defaults.
type ContainerSpec struct {
	AgentID     string // for the container Name + label
	Image       string // typically "garrison-claude@sha256:<digest>"
	HostUID     int    // FR-206a allocator output
	Workspace   string // host path bind-mounted at /workspace:rw
	Skills      string // host path bind-mounted at /workspace/.claude/skills:ro
	NetworkName string // optional initial network attach (default: "none" for sandbox Rule 3)
	// SupervisorBin is the host path of the CGO_ENABLED=0 supervisor
	// binary, bind-mounted read-only at /usr/local/bin/garrison-supervisor
	// so in-container stdio MCP servers run from the same binary
	// (spike F6, FR-014). Empty = no mount (pre-M7.1 callers).
	SupervisorBin string
	Memory        string // "512m" — parsed by Docker
	CPUs          string // "1.0" — converted to NanoCpus
	PIDsLimit     int    // 200 by default
	// Container-level env is deliberately absent: per-exec ExecSpec.Env
	// is the ONLY transit for secrets/runtime env (FR-002 structural).
	// Networks are applied via ConnectNetwork after Create — listed
	// here for completeness; Create itself uses NetworkName=none.
	Networks []string
}

// ExpectedContainer is one row of the reconcile input set.
type ExpectedContainer struct {
	AgentID     string
	ContainerID string
	Image       string
	State       ExpectedState
}

type ExpectedState string

const (
	ExpectedRunning ExpectedState = "should_be_running"
	ExpectedStopped ExpectedState = "should_be_stopped"
	ExpectedRemoved ExpectedState = "should_be_removed"
)

// ReconcileReport summarises drift between expected and actual state.
type ReconcileReport struct {
	AdoptedRunning   []string // agent IDs whose container was already running and matched expectation
	Restarted        []string // agent IDs whose container was stopped but should run; impl restarted
	GarbageCollected []string // container IDs that had no matching agents row; impl removed
	Mismatches       []ReconcileMismatch
}

type ReconcileMismatch struct {
	AgentID    string
	Expected   ExpectedState
	ActualKind string // from the Docker State.Status (e.g. "exited")
	Reason     string
}

// ShapeReport summarises one boot shape reconcile pass (FR-007). Each
// slice carries agent IDs. Event recording stays out of this package:
// the caller in cmd/supervisor writes the agent_container_events rows
// from the report — a `removed`+`created` pair per Recreated agent,
// `created` per Created, `started` per Restarted; Unchanged writes
// nothing (SC-005: a repeat boot with no shape change is all-Unchanged
// and performs zero container mutations).
type ShapeReport struct {
	Created   []string // no container existed; created + started
	Recreated []string // shape-hash label absent or stale; removed, recreated, started
	Restarted []string // hash matched but container was stopped; started
	Unchanged []string // hash matched, running; untouched
}

// Sentinel errors. Callers route on errors.Is.
var (
	ErrContainerNotFound = errors.New("agentcontainer: container not found")
	ErrSocketProxyDown   = errors.New("agentcontainer: socket-proxy unreachable")
	ErrInvalidSpec       = errors.New("agentcontainer: invalid container spec")
	ErrImageNotFound     = errors.New("agentcontainer: image not found")
)
