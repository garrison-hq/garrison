package mcpjungle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// Client is the supervisor-side handle for MCPJungle's admin API.
// Operator-only — never embedded in agent containers (agents receive
// per-McpClient bearer tokens via vault, scoped through
// MCPJungle's AllowList).
type Client struct {
	// BaseURL is the MCPJungle server's URL (without trailing slash),
	// e.g. "http://garrison-mcpjungle:8080". M8 alpha runs a single
	// instance; URLForCustomer returns this value unconditionally.
	BaseURL string

	// AdminToken authenticates every admin-API request. Fetched from
	// Infisical via the M2.3 vault fetcher at supervisor startup +
	// passed in here.
	AdminToken string

	// HTTP is the underlying HTTP client. Default 30s timeout; tests
	// inject a mock via httptest.Server.
	HTTP *http.Client

	// Logger is optional; nil falls back to slog.Default.
	Logger *slog.Logger
}

// apiPrefix is MCPJungle's versioned API mount point. Upstream serves
// the management API under /api/v0 (verified against the current
// image: POST /clients → 404, POST /api/v0/clients → 201); only
// /health lives at the root. healthcheck.go therefore does NOT use
// this prefix.
const apiPrefix = "/api/v0"

// NewClient constructs a Client with sensible defaults. baseURL is
// trimmed of any trailing slash so request paths don't double-slash.
func NewClient(baseURL, adminToken string, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		AdminToken: adminToken,
		HTTP:       &http.Client{Timeout: 30 * time.Second},
		Logger:     logger,
	}
}

// URLForCustomer returns the MCPJungle URL for a given customer. M8
// alpha is single-instance: returns BaseURL unconditionally regardless
// of the customer argument. The signature is the structural seam for
// beta-time multi-tenant (Option A from the multi-tenant analysis —
// per-customer MCPJungle instances; the lookup swaps in a customer-id-
// keyed map).
//
// The customerID arg is intentionally unused in M8 alpha; the linter
// flag is acceptable here because the seam is what matters.
func (c *Client) URLForCustomer(_ pgtype.UUID) string {
	return c.BaseURL
}

// CreateMcpClient issues POST /clients. Returns the new client's id on
// 201; ErrServerRegistrationConflict on 409 (caller treats as "client
// already exists" for the reconciler's idempotent path);
// ErrAdminTokenInvalid on 401; ErrUnreachable on connection refused.
func (c *Client) CreateMcpClient(ctx context.Context, params CreateMcpClientParams) (CreateMcpClientResult, error) {
	// params.Name is a CLIENT name (dotted form registers fine upstream);
	// the allow-list entries are SERVER names and need the FR-307
	// boundary translation.
	body, err := json.Marshal(map[string]any{
		"name":         params.Name,
		"allow_list":   upstreamAllowList(params.AllowList),
		"access_token": params.AccessToken,
	})
	if err != nil {
		return CreateMcpClientResult{}, fmt.Errorf("mcpjungle: marshal CreateMcpClient body: %w", err)
	}
	resp, err := c.do(ctx, http.MethodPost, apiPrefix+"/clients", bytes.NewReader(body))
	if err != nil {
		return CreateMcpClientResult{}, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusCreated:
		// Current MCPJungle returns a GORM-shaped body with a numeric
		// "ID"; older builds return a string id. json.Number rejects
		// JSON strings, so decode the raw value and accept either
		// shape (T017 acceptance finding — the json.Number-only decode
		// broke the string-id case the M8 contract pins).
		// encoding/json matches "ID" case-insensitively.
		var out struct {
			ID   json.RawMessage `json:"id"`
			Name string          `json:"name"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return CreateMcpClientResult{}, fmt.Errorf("mcpjungle: parse CreateMcpClient response: %w", err)
		}
		id, err := decodeFlexibleID(out.ID)
		if err != nil {
			return CreateMcpClientResult{}, fmt.Errorf("mcpjungle: parse CreateMcpClient response id: %w", err)
		}
		return CreateMcpClientResult{ID: id, Name: out.Name}, nil
	case http.StatusConflict:
		return CreateMcpClientResult{}, ErrServerRegistrationConflict
	case http.StatusInternalServerError:
		// Current MCPJungle surfaces unique-constraint violations as a
		// 500 with the Postgres "duplicate key" error text rather than
		// a 409. Map it to the conflict error so reconcile stays
		// idempotent across boots.
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if bytes.Contains(b, []byte("duplicate key")) {
			return CreateMcpClientResult{}, ErrServerRegistrationConflict
		}
		return CreateMcpClientResult{}, c.statusErrBody(resp.StatusCode, b, "CreateMcpClient")
	case http.StatusUnauthorized:
		return CreateMcpClientResult{}, ErrAdminTokenInvalid
	default:
		return CreateMcpClientResult{}, c.statusErr(resp, "CreateMcpClient")
	}
}

// decodeFlexibleID renders an id field that upstream serves as either
// a JSON string ("client-x", pre-rename builds + the M8 suite's fakes)
// or a bare number (GORM-shaped current builds) as its string form.
func decodeFlexibleID(raw json.RawMessage) (string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err == nil {
		return n.String(), nil
	}
	return "", fmt.Errorf("id is neither string nor number: %s", string(raw))
}

// DeleteMcpClient issues DELETE /clients/<name>. Returns nil on 204;
// ErrClientNotFound on 404.
func (c *Client) DeleteMcpClient(ctx context.Context, name string) error {
	resp, err := c.do(ctx, http.MethodDelete, apiPrefix+"/clients/"+url.PathEscape(name), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK:
		return nil
	case http.StatusNotFound:
		return ErrClientNotFound
	case http.StatusUnauthorized:
		return ErrAdminTokenInvalid
	default:
		return c.statusErr(resp, "DeleteMcpClient")
	}
}

// UpdateAllowList issues PATCH /clients/<name>/allowlist with the new
// list of MCP server names. Used by the reconciler to keep MCPJungle's
// allow-list in sync with agents.mcp_servers_jsonb after an operator
// approves a skill-change proposal.
func (c *Client) UpdateAllowList(ctx context.Context, name string, allowList []string) error {
	// name is a CLIENT name (dots fine upstream); the allow-list entries
	// are SERVER names and need the FR-307 boundary translation.
	body, err := json.Marshal(map[string]any{"allow_list": upstreamAllowList(allowList)})
	if err != nil {
		return fmt.Errorf("mcpjungle: marshal UpdateAllowList body: %w", err)
	}
	resp, err := c.do(ctx, http.MethodPatch, apiPrefix+"/clients/"+url.PathEscape(name)+"/allowlist", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent:
		return nil
	case http.StatusNotFound:
		return ErrClientNotFound
	case http.StatusUnauthorized:
		return ErrAdminTokenInvalid
	default:
		return c.statusErr(resp, "UpdateAllowList")
	}
}

// UpstreamServerName translates a Garrison server name (FR-307
// "<customer_slug>.<name>", dotted) into the form current MCPJungle
// accepts for SERVER names: ^[a-zA-Z0-9_-]+$ with no consecutive
// underscores — dots are rejected with 400. Client names are NOT
// subject to this regex upstream (dotted McpClient names register
// fine), so only server-name call sites translate.
//
// '.' maps to '-'. A garrison name that itself contains '-' could in
// principle collide post-translation ("garrison.a-b" vs
// "garrison-a.b"); acceptable for the one-customer alpha and flagged
// for the FR-307 doc amendment.
func UpstreamServerName(name string) string {
	return strings.ReplaceAll(name, ".", "-")
}

// upstreamAllowList translates every entry of a Garrison allow-list
// (server names from agents.mcp_servers_jsonb, dotted) for upstream.
func upstreamAllowList(allowList []string) []string {
	if allowList == nil {
		return nil
	}
	out := make([]string, len(allowList))
	for i, n := range allowList {
		out[i] = UpstreamServerName(n)
	}
	return out
}

// RegisterServer issues POST /servers to add a new MCP server to
// MCPJungle's registry. Called by the mcpserverwork worker reactively
// after a dashboard register_mcp_server Server Action commits.
//
// Garrison's transport vocabulary (mcp_servers CHECK constraint) says
// "http"; current MCPJungle upstream rejects that with 400 and accepts
// "streamable_http" instead. Translate at this boundary so the Garrison
// schema and dashboard form stay stable across upstream renames. The
// server name is translated per UpstreamServerName for the same reason.
func (c *Client) RegisterServer(ctx context.Context, spec ServerSpec) (string, error) {
	transport := spec.Transport
	if transport == "http" {
		transport = "streamable_http"
	}
	body, err := json.Marshal(map[string]any{
		"name":         UpstreamServerName(spec.Name),
		"transport":    transport,
		"url":          spec.URL,
		"bearer_token": spec.BearerToken,
	})
	if err != nil {
		return "", fmt.Errorf("mcpjungle: marshal RegisterServer body: %w", err)
	}
	resp, err := c.do(ctx, http.MethodPost, apiPrefix+"/servers", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusCreated:
		var out struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return "", fmt.Errorf("mcpjungle: parse RegisterServer response: %w", err)
		}
		return out.ID, nil
	case http.StatusConflict:
		return "", ErrServerRegistrationConflict
	case http.StatusUnauthorized:
		return "", ErrAdminTokenInvalid
	default:
		return "", c.statusErr(resp, "RegisterServer")
	}
}

// DeregisterServer issues DELETE /servers/<name>. Returns nil on 204;
// ErrServerNotFound on 404.
func (c *Client) DeregisterServer(ctx context.Context, name string) error {
	resp, err := c.do(ctx, http.MethodDelete, apiPrefix+"/servers/"+url.PathEscape(UpstreamServerName(name)), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK:
		return nil
	case http.StatusNotFound:
		return ErrServerNotFound
	case http.StatusUnauthorized:
		return ErrAdminTokenInvalid
	default:
		return c.statusErr(resp, "DeregisterServer")
	}
}

// do is the shared HTTP request builder. Adds the admin bearer token,
// content-type, and maps connection errors to ErrUnreachable.
func (c *Client) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("mcpjungle: build %s %s: %w", method, path, err)
	}
	if c.AdminToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.AdminToken)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		// Connection-level errors (connection refused, timeout, DNS) map
		// to ErrUnreachable so callers can apply the degrade-with-
		// warning posture per FR-308.
		var netErr interface{ Timeout() bool }
		if errors.As(err, &netErr) && netErr.Timeout() {
			return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
		}
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	return resp, nil
}

// statusErr builds a contextual error for unexpected HTTP status codes.
// Includes a snippet of the response body for log forensics.
// statusErrBody mirrors statusErr for callers that already consumed
// the response body (e.g. the duplicate-key sniff in CreateMcpClient).
func (c *Client) statusErrBody(status int, body []byte, op string) error {
	if len(body) > 512 {
		body = body[:512]
	}
	return fmt.Errorf("mcpjungle: %s returned status %d: %s", op, status, strings.TrimSpace(string(body)))
}

func (c *Client) statusErr(resp *http.Response, op string) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("mcpjungle: %s returned status %d: %s", op, resp.StatusCode, strings.TrimSpace(string(body)))
}
