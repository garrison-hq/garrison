package agentcontainer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// containersPathPrefix is the docker engine API root for container
// operations. Centralised here so per-call site doesn't repeat the
// literal (Sonar S1192).
const containersPathPrefix = "/containers/"

// socketProxyController is the production Controller implementation.
// HTTP requests against the M2.2 docker-socket-proxy; no
// github.com/docker/docker/client dependency. JSON request bodies
// are constructed inline against the Docker Engine API shape.
//
// The proxy enforces the body-filter allow-list from
// deploy/socket-proxy/socket-proxy.yaml (decision #21). Requests that
// don't conform — wrong Image prefix, mounts outside
// /var/lib/garrison/{workspaces,skills}, NetworkMode != none/custom,
// CapAdd non-empty — are rejected at the proxy layer with 403. This
// is belt-and-suspenders against a compromised supervisor process.
type socketProxyController struct {
	baseURL string // e.g. "http://garrison-docker-proxy:2375"
	http    *http.Client
	logger  *slog.Logger
}

// NewSocketProxyController constructs a production Controller.
func NewSocketProxyController(baseURL string, httpClient *http.Client, logger *slog.Logger) Controller {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &socketProxyController{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    httpClient,
		logger:  logger,
	}
}

// containerCreateBody is the subset of the Docker Engine API's
// /containers/create body that Garrison populates. Field names match
// the Docker JSON schema; unset fields are omitted via omitempty.
// Deliberately no Env field: per-exec ExecSpec.Env is the only
// secret/runtime-env transit (FR-002 structural).
type containerCreateBody struct {
	Image      string            `json:"Image"`
	User       string            `json:"User,omitempty"`
	Entrypoint []string          `json:"Entrypoint,omitempty"`
	Cmd        []string          `json:"Cmd,omitempty"`
	Labels     map[string]string `json:"Labels,omitempty"`
	HostConfig hostConfigBody    `json:"HostConfig"`
}

// shapeHashLabel carries the hex SHA-256 of the marshaled create body
// (every per-agent field), computed after all fields are set and
// before the label itself is added. The boot reconcile (FR-007)
// compares it to decide recreate-vs-noop; any future shape edit or a
// per-agent workspace/image/uid change flips the hash for exactly the
// affected containers.
const shapeHashLabel = "garrison.shape_hash"

// supervisorBinContainerPath is where the host supervisor binary is
// bind-mounted read-only inside every agent container (spike F6,
// FR-014) so the in-container stdio MCP servers run from it.
const supervisorBinContainerPath = "/usr/local/bin/garrison-supervisor"

type hostConfigBody struct {
	Binds          []string          `json:"Binds,omitempty"`
	ReadonlyRootfs bool              `json:"ReadonlyRootfs"`
	Tmpfs          map[string]string `json:"Tmpfs,omitempty"`
	NetworkMode    string            `json:"NetworkMode,omitempty"`
	CapDrop        []string          `json:"CapDrop,omitempty"`
	CapAdd         []string          `json:"CapAdd,omitempty"`
	Memory         int64             `json:"Memory,omitempty"`
	NanoCpus       int64             `json:"NanoCpus,omitempty"`
	PidsLimit      int64             `json:"PidsLimit,omitempty"`
	Privileged     bool              `json:"Privileged"`
}

// buildCreateBody is exported via lower-case for tests in the same
// package; mounts the host paths at the spec's locations, sets the
// hard-coded sandbox Rule 2/3/4/5 fields, and converts the spec's
// human-readable Memory + CPUs strings to Docker's int64 fields.
func buildCreateBody(spec ContainerSpec) (containerCreateBody, error) {
	if spec.Image == "" || spec.Workspace == "" || spec.Skills == "" {
		return containerCreateBody{}, fmt.Errorf("%w: missing image/workspace/skills", ErrInvalidSpec)
	}
	mem, err := parseHumanBytes(spec.Memory)
	if err != nil {
		return containerCreateBody{}, fmt.Errorf("%w: memory %q: %v", ErrInvalidSpec, spec.Memory, err)
	}
	nanoCPUs, err := parseCPUs(spec.CPUs)
	if err != nil {
		return containerCreateBody{}, fmt.Errorf("%w: cpus %q: %v", ErrInvalidSpec, spec.CPUs, err)
	}

	body := containerCreateBody{
		Image: spec.Image,
		// The image's entrypoint is claude, which exits(1) without a
		// prompt (spike F1 — the Exited(1) fleet). Idle `sleep
		// infinity` PID 1 keeps the container standing between execs
		// (FR-005).
		Entrypoint: []string{"/bin/sleep"},
		Cmd:        []string{"infinity"},
		Labels: map[string]string{
			"garrison.agent_id": spec.AgentID,
			"garrison.managed":  "true",
		},
		HostConfig: hostConfigBody{
			Binds: []string{
				spec.Workspace + ":/workspace:rw",
				spec.Skills + ":/workspace/.claude/skills:ro",
			},
			ReadonlyRootfs: true,
			Tmpfs: map[string]string{
				"/tmp": "rw,size=64m",
				// claude writes session state under ~/.claude; HOME
				// on the read-only rootfs needs a tmpfs (spike F5).
				"/home/node": "rw,size=64m",
				"/var/run":   "",
			},
			NetworkMode: "none",
			CapDrop:     []string{"ALL"},
			Memory:      mem,
			NanoCpus:    nanoCPUs,
			PidsLimit:   int64(spec.PIDsLimit),
			Privileged:  false,
		},
	}
	if spec.SupervisorBin != "" {
		body.HostConfig.Binds = append(body.HostConfig.Binds,
			spec.SupervisorBin+":"+supervisorBinContainerPath+":ro")
	}
	if spec.HostUID > 0 {
		body.User = fmt.Sprintf("%d:%d", spec.HostUID, spec.HostUID)
	}
	if spec.NetworkName != "" {
		body.HostConfig.NetworkMode = spec.NetworkName
	}
	// Shape hash last: it covers every field above (FR-007). Marshal
	// is deterministic — fixed struct field order, sorted map keys.
	hashable, err := json.Marshal(body)
	if err != nil {
		return containerCreateBody{}, fmt.Errorf("%w: marshal for shape hash: %v", ErrInvalidSpec, err)
	}
	sum := sha256.Sum256(hashable)
	body.Labels[shapeHashLabel] = hex.EncodeToString(sum[:])
	return body, nil
}

func (c *socketProxyController) Create(ctx context.Context, spec ContainerSpec) (string, error) {
	body, err := buildCreateBody(spec)
	if err != nil {
		return "", err
	}
	buf, _ := json.Marshal(body)
	name := ContainerName(spec.AgentID)

	resp, err := c.do(ctx, http.MethodPost,
		"/containers/create?name="+name, bytes.NewReader(buf))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return "", c.statusErr(resp, "create")
	}
	var out struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("agentcontainer: parse create response: %w", err)
	}
	return out.ID, nil
}

func (c *socketProxyController) Start(ctx context.Context, id string) error {
	resp, err := c.do(ctx, http.MethodPost, containersPathPrefix+id+"/start", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotModified {
		return c.statusErr(resp, "start")
	}
	return nil
}

func (c *socketProxyController) Stop(ctx context.Context, id string) error {
	resp, err := c.do(ctx, http.MethodPost, containersPathPrefix+id+"/stop?t=10", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotModified {
		return c.statusErr(resp, "stop")
	}
	return nil
}

func (c *socketProxyController) Remove(ctx context.Context, id string) error {
	resp, err := c.do(ctx, http.MethodDelete, containersPathPrefix+id+"?force=true", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return c.statusErr(resp, "remove")
	}
	return nil
}

func (c *socketProxyController) ConnectNetwork(ctx context.Context, id, network string) error {
	body, _ := json.Marshal(map[string]string{"Container": id})
	resp, err := c.do(ctx, http.MethodPost, "/networks/"+network+"/connect", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return c.statusErr(resp, "connect")
	}
	return nil
}

// Exec runs spec.Cmd inside the container using docker's exec API.
// No stdin attach, no connection hijacking (FR-004): the exec-start
// response body is a normal chunked application/vnd.docker.raw-stream
// fed through the in-process demultiplexer (spike F2). Per-exec
// spec.Env rides the exec-create body — the only secret/runtime-env
// transit (FR-002).
func (c *socketProxyController) Exec(ctx context.Context, id string, spec ExecSpec) (*ExecSession, error) {
	createBody, _ := json.Marshal(map[string]any{
		"AttachStdin":  false,
		"AttachStdout": true,
		"AttachStderr": true,
		"Tty":          false,
		"Cmd":          spec.Cmd,
		"Env":          spec.Env,
		"WorkingDir":   spec.WorkingDir,
	})
	resp, err := c.do(ctx, http.MethodPost, containersPathPrefix+id+"/exec", bytes.NewReader(createBody))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// 404 = container missing; 409 = container exists but isn't
	// running. Both mean "no container to exec into" — callers route
	// to spawn_failed and the boot reconciler is the repair path
	// (FR-019).
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusConflict {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("%w: exec-create: %s", ErrContainerNotFound, body)
	}
	if resp.StatusCode != http.StatusCreated {
		return nil, c.statusErr(resp, "exec-create")
	}
	var out struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("agentcontainer: parse exec create: %w", err)
	}

	startBody, _ := json.Marshal(map[string]any{"Detach": false, "Tty": false})
	startResp, err := c.do(ctx, http.MethodPost, "/exec/"+out.ID+"/start", bytes.NewReader(startBody))
	if err != nil {
		return nil, err
	}
	if startResp.StatusCode != http.StatusOK {
		_ = startResp.Body.Close()
		return nil, c.statusErr(startResp, "exec-start")
	}
	stdout, stderr := demuxRawStream(startResp.Body)
	return &ExecSession{
		ID:     out.ID,
		Stdout: stdout,
		Stderr: stderr,
		inspect: func(ctx context.Context) (bool, int, error) {
			return c.execInspect(ctx, out.ID)
		},
	}, nil
}

// execInspect reads GET /exec/<id>/json — the exit-code source for
// ExecSession.ExitCode's poll loop. Allowed under the proxy's EXEC=1.
func (c *socketProxyController) execInspect(ctx context.Context, execID string) (running bool, exitCode int, err error) {
	resp, err := c.do(ctx, http.MethodGet, "/exec/"+execID+"/json", nil)
	if err != nil {
		return false, -1, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, -1, c.statusErr(resp, "exec-inspect")
	}
	var out struct {
		Running  bool `json:"Running"`
		ExitCode int  `json:"ExitCode"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, -1, fmt.Errorf("agentcontainer: parse exec inspect: %w", err)
	}
	return out.Running, out.ExitCode, nil
}

// Restart issues POST /containers/<id>/restart with a 5s grace window
// — the M7.1 SIGKILL analog (FR-016). The idle `sleep infinity` PID 1
// returns and every in-flight exec stream EOFs.
func (c *socketProxyController) Restart(ctx context.Context, id string) error {
	resp, err := c.do(ctx, http.MethodPost, containersPathPrefix+id+"/restart?t=5", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return c.statusErr(resp, "restart")
	}
	return nil
}

// containerJSON is the response shape for GET /containers/json.
type containerJSON struct {
	ID     string            `json:"Id"`
	Image  string            `json:"Image"`
	Names  []string          `json:"Names"`
	Labels map[string]string `json:"Labels"`
	State  string            `json:"State"`
	Status string            `json:"Status"`
}

func (c *socketProxyController) listContainers(ctx context.Context) ([]containerJSON, error) {
	resp, err := c.do(ctx, http.MethodGet, "/containers/json?all=true&filters="+
		urlEncode(`{"label":["garrison.managed=true"]}`), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, c.statusErr(resp, "list")
	}
	var out []containerJSON
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("agentcontainer: parse list: %w", err)
	}
	return out, nil
}

func (c *socketProxyController) ImageDigest(ctx context.Context, imageRef string) (string, error) {
	resp, err := c.do(ctx, http.MethodGet, "/images/"+imageRef+"/json", nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("%w: %s", ErrImageNotFound, imageRef)
	}
	if resp.StatusCode != http.StatusOK {
		return "", c.statusErr(resp, "image-inspect")
	}
	var out struct {
		ID          string   `json:"Id"`
		RepoDigests []string `json:"RepoDigests"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("agentcontainer: parse image inspect: %w", err)
	}
	if len(out.RepoDigests) > 0 {
		return out.RepoDigests[0], nil
	}
	// Locally-built images (docker build / compose build) carry no
	// RepoDigests — only registry-pulled images do. The image ID is
	// the content-addressed config digest, which serves the same
	// pin-for-audit purpose in dev; production deploys that pull a
	// pinned ref still get the registry digest above.
	if out.ID != "" {
		c.logger.Info("agentcontainer: image has no RepoDigests (locally built); using image ID as digest",
			"image_ref", imageRef, "image_id", out.ID)
		return out.ID, nil
	}
	return "", fmt.Errorf("%w: image has neither RepoDigests nor Id", ErrImageNotFound)
}

// do issues an HTTP request to the socket-proxy. Maps connection
// errors to ErrSocketProxyDown so callers can route distinctly from
// per-endpoint errors.
func (c *socketProxyController) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("agentcontainer: build %s %s: %w", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// Distinguish ctx cancel from connection failure for callers.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %v", ErrSocketProxyDown, err)
	}
	return resp, nil
}

func (c *socketProxyController) statusErr(resp *http.Response, op string) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%w: %s: %s", ErrContainerNotFound, op, body)
	}
	return fmt.Errorf("agentcontainer: %s status %d: %s", op, resp.StatusCode, body)
}

// shortID produces an 8-char prefix of the agent UUID for container
// + network names (decision #32). Strips dashes; lower-case hex.
func shortID(uuid string) string {
	stripped := strings.ReplaceAll(uuid, "-", "")
	if len(stripped) > 8 {
		return stripped[:8]
	}
	return stripped
}

// parseHumanBytes converts "512m" / "1g" / "0" to int64 bytes.
// Mirrors Docker's parseSize for the most common shapes; rejects
// unknown suffixes.
func parseHumanBytes(s string) (int64, error) {
	if s == "" || s == "0" {
		return 0, nil
	}
	last := s[len(s)-1]
	num := s
	mult := int64(1)
	switch last {
	case 'k', 'K':
		mult, num = 1024, s[:len(s)-1]
	case 'm', 'M':
		mult, num = 1024*1024, s[:len(s)-1]
	case 'g', 'G':
		mult, num = 1024*1024*1024, s[:len(s)-1]
	}
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil {
		return 0, err
	}
	return n * mult, nil
}

// parseCPUs converts "1.0" / "0.5" to NanoCpus (1.0 = 1e9).
func parseCPUs(s string) (int64, error) {
	if s == "" || s == "0" {
		return 0, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	return int64(f * 1e9), nil
}

func urlEncode(s string) string {
	// minimal escape for the filters param; the proxy accepts the
	// raw JSON for label filters in practice.
	return strings.ReplaceAll(strings.ReplaceAll(s, " ", "%20"), `"`, "%22")
}
