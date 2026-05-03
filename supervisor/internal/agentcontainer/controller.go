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
	"io"
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

	// Remove issues DELETE /containers/<id> with force=true.
	Remove(ctx context.Context, containerID string) error

	// Exec runs cmd inside the existing container, piping stdin in
	// and reading stdout/stderr out. Used by T011's spawn swap; the
	// returned readers stream the docker exec hijacked connection
	// line-by-line (verified compatible with internal/claudeproto's
	// NDJSON scanner per spike §8 P2).
	//
	// If ctx is cancelled while the exec is in flight, the
	// implementation issues POST /exec/<id>/kill (or the equivalent)
	// and returns ctx.Err().
	Exec(ctx context.Context, containerID string, cmd []string, stdin io.Reader) (stdout, stderr io.ReadCloser, err error)

	// ConnectNetwork attaches the container to a named Docker network
	// (opt-in sidecar reach per sandbox Rule 3). Called after Create
	// for each network in spec.Networks beyond --network=none.
	ConnectNetwork(ctx context.Context, containerID, networkName string) error

	// Reconcile compares the expected set of containers against the
	// Docker daemon's actual state and reports drift. Called once at
	// supervisor startup (FR-214) before normal lifecycle resumes.
	Reconcile(ctx context.Context, expected []ExpectedContainer) (ReconcileReport, error)

	// ImageDigest returns the pinned RepoDigest for the given image
	// reference. Used by migrate7 + approve helpers (decision #22):
	// agents.image_digest is set to this value at activation, and
	// every spawn re-validates against the running container's
	// recorded digest.
	ImageDigest(ctx context.Context, imageRef string) (string, error)
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
	Memory      string // "512m" — parsed by Docker
	CPUs        string // "1.0" — converted to NanoCpus
	PIDsLimit   int    // 200 by default
	EnvVars     []string
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

// Sentinel errors. Callers route on errors.Is.
var (
	ErrContainerNotFound = errors.New("agentcontainer: container not found")
	ErrSocketProxyDown   = errors.New("agentcontainer: socket-proxy unreachable")
	ErrInvalidSpec       = errors.New("agentcontainer: invalid container spec")
	ErrImageNotFound     = errors.New("agentcontainer: image not found")
)
