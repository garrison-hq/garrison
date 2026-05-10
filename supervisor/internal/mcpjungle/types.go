// Package mcpjungle is Garrison's HTTP client for MCPJungle's admin
// API, plus a per-agent reconciler that ensures every active agent has
// an MCPJungle McpClient + a vault grant for its bearer token.
//
// MCPJungle is Garrison's chosen MCP-server registry/proxy at M8 (see
// docs/mcp-registry-candidates.md "MCPJungle — CHOSEN FOR M8" and
// docs/research/m8-mcpjungle-spike.md for the maturity-check details).
// Garrison runs MCPJungle as a sidecar container; agents connect to
// MCPJungle via a per-agent bearer token managed in Infisical.
//
// All admin API calls authenticate with the MCPJungle admin token
// (vault path mcpjungle/admin by default; configured via
// GARRISON_MCPJUNGLE_ADMIN_TOKEN_PATH). The token is operator-only —
// agents NEVER receive the admin token; their per-agent bearer tokens
// are scoped via MCPJungle's enterprise-mode McpClient.AllowList
// (matches the per-customer naming convention encoded in
// FR-304/FR-307).
package mcpjungle

import "errors"

// Error sentinels surfaced by the MCPJungle HTTP client. Callers use
// errors.Is to map these to typed verb-level Result.ErrorKind values.
var (
	ErrServerNotFound             = errors.New("mcpjungle: MCP server not found")
	ErrClientNotFound             = errors.New("mcpjungle: MCP client not found")
	ErrAdminTokenInvalid          = errors.New("mcpjungle: admin token rejected (401)")
	ErrUnreachable                = errors.New("mcpjungle: server unreachable")
	ErrServerRegistrationConflict = errors.New("mcpjungle: server with that name already registered (409)")
)

// CreateMcpClientParams is the request shape for POST /clients.
// Caller is responsible for the customer-prefix naming convention
// (<customer_slug>.<role-slug>.<agent-uuid-short>) per FR-304.
type CreateMcpClientParams struct {
	Name        string   // MCPJungle client name — operator-controlled per FR-304
	AllowList   []string // registered MCP server names the client may reach
	AccessToken string   // operator-supplied bearer token; clients send it as Authorization: Bearer <token>
}

// CreateMcpClientResult is the response shape for POST /clients.
type CreateMcpClientResult struct {
	ID   string // MCPJungle's internal client id
	Name string
}

// ServerSpec is the request shape for POST /servers (register an MCP
// server with MCPJungle). Garrison's mcpserverwork worker calls this
// when the operator submits a registration via the dashboard.
type ServerSpec struct {
	Name        string // <customer_slug>.<server-name>
	Transport   string // "http" | "stdio" | "sse"
	URL         string // for http/sse transports
	BearerToken string // optional upstream bearer if the registered MCP server needs auth
}
