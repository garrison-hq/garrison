package agentcontainer

import (
	"bytes"
	"context"
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
type containerCreateBody struct {
	Image      string            `json:"Image"`
	User       string            `json:"User,omitempty"`
	Env        []string          `json:"Env,omitempty"`
	Labels     map[string]string `json:"Labels,omitempty"`
	HostConfig hostConfigBody    `json:"HostConfig"`
}

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
		Env:   append([]string(nil), spec.EnvVars...),
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
				"/tmp":     "rw,size=64m",
				"/var/run": "",
			},
			NetworkMode: "none",
			CapDrop:     []string{"ALL"},
			Memory:      mem,
			NanoCpus:    nanoCPUs,
			PidsLimit:   int64(spec.PIDsLimit),
			Privileged:  false,
		},
	}
	if spec.HostUID > 0 {
		body.User = fmt.Sprintf("%d:%d", spec.HostUID, spec.HostUID)
	}
	if spec.NetworkName != "" {
		body.HostConfig.NetworkMode = spec.NetworkName
	}
	return body, nil
}

func (c *socketProxyController) Create(ctx context.Context, spec ContainerSpec) (string, error) {
	body, err := buildCreateBody(spec)
	if err != nil {
		return "", err
	}
	buf, _ := json.Marshal(body)
	name := "garrison-agent-" + shortID(spec.AgentID)

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
	resp, err := c.do(ctx, http.MethodPost, "/containers/"+id+"/start", nil)
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
	resp, err := c.do(ctx, http.MethodPost, "/containers/"+id+"/stop?t=10", nil)
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
	resp, err := c.do(ctx, http.MethodDelete, "/containers/"+id+"?force=true", nil)
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

// Exec runs cmd inside the container using docker's exec API. The
// streaming surface (hijacked stdout/stderr) is set up here as a
// scaffolded shape; T011 wires the full stdin-pipe + line-buffered
// stdout consumer for the spawn replacement. For T004 the method
// shape is verified against the fake (TestExecPreservesNDJSON in the
// fake-impl tests below) and against the request body shape
// (TestExecCreatesExecInstance).
func (c *socketProxyController) Exec(ctx context.Context, id string, cmd []string, stdin io.Reader) (io.ReadCloser, io.ReadCloser, error) {
	createBody, _ := json.Marshal(map[string]any{
		"AttachStdin":  stdin != nil,
		"AttachStdout": true,
		"AttachStderr": true,
		"Tty":          false,
		"Cmd":          cmd,
	})
	resp, err := c.do(ctx, http.MethodPost, "/containers/"+id+"/exec", bytes.NewReader(createBody))
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return nil, nil, c.statusErr(resp, "exec-create")
	}
	var out struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, nil, fmt.Errorf("agentcontainer: parse exec create: %w", err)
	}

	// Start the exec — the docker-engine wire format multiplexes
	// stdout/stderr over a single hijacked connection (8-byte frame
	// header per chunk). T011 ships the demultiplexer; for T004 the
	// scaffolded shape returns the raw stream as stdout and an
	// empty stderr reader.
	startBody, _ := json.Marshal(map[string]any{"Detach": false, "Tty": false})
	startResp, err := c.do(ctx, http.MethodPost, "/exec/"+out.ID+"/start", bytes.NewReader(startBody))
	if err != nil {
		return nil, nil, err
	}
	if startResp.StatusCode != http.StatusOK {
		_ = startResp.Body.Close()
		return nil, nil, c.statusErr(startResp, "exec-start")
	}
	// Spawn an stdin pump if the caller provided stdin. The hijacked
	// connection direction here is request-only; for T011 we'll need
	// a TCP-hijack equivalent. Scaffolded as a one-way drain.
	if stdin != nil {
		go func() { _, _ = io.Copy(io.Discard, stdin) }()
	}
	return startResp.Body, io.NopCloser(strings.NewReader("")), nil
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
		RepoDigests []string `json:"RepoDigests"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("agentcontainer: parse image inspect: %w", err)
	}
	if len(out.RepoDigests) == 0 {
		return "", fmt.Errorf("%w: image has no RepoDigests (was it pulled?)", ErrImageNotFound)
	}
	return out.RepoDigests[0], nil
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
