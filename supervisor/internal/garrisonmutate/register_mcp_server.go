package garrisonmutate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5/pgconn"
)

// RegisterMcpServerArgs is the JSON shape the dashboard's Server
// Action posts. Drizzle on the dashboard side validates fields and
// commits the mcp_servers row; this supervisor-side verb provides a
// shared validation surface for tests + future chat-driven re-entry
// paths.
//
// Name must carry the customer-prefix invariant from FR-307:
// <active customer_slug>.<server-name>. The validator rejects any
// shape that breaks this.
type RegisterMcpServerArgs struct {
	CustomerSlug    string `json:"customer_slug"`
	Name            string `json:"name"`
	Transport       string `json:"transport"`
	URL             string `json:"url,omitempty"`
	BearerTokenPath string `json:"bearer_token_path,omitempty"`
}

// validTransports enumerates the mcp_servers.transport CHECK values.
// Mirrored in code so the verb's typed rejection happens before the
// INSERT round-trip.
var validTransports = map[string]bool{"http": true, "stdio": true, "sse": true}

// realRegisterMcpServerHandler implements
// garrison-mutate.register_mcp_server (Server-Action surface only).
//
// FR-306 single-row invariant: no audit row is written here. The
// reactive worker (internal/mcpserverwork) writes the canonical
// `chat_mutation_audit` row when MCPJungle's API call completes,
// anchored on the final outcome ('success' or 'failed').
func realRegisterMcpServerHandler(ctx context.Context, deps Deps, raw json.RawMessage) (Result, error) {
	args, vRes := parseRegisterMcpServerArgs(raw)
	if vRes != nil {
		return *vRes, nil
	}
	q := store.New(deps.Pool)
	urlPtr := stringPtrOrNil(args.URL)
	bearerPtr := stringPtrOrNil(args.BearerTokenPath)
	row, err := q.InsertMcpServer(ctx, store.InsertMcpServerParams{
		CustomerSlug:    args.CustomerSlug,
		Name:            args.Name,
		Transport:       args.Transport,
		Url:             urlPtr,
		BearerTokenPath: bearerPtr,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return validationFailure(fmt.Sprintf(
				"register_mcp_server: server %q already registered for customer %q",
				args.Name, args.CustomerSlug,
			)), nil
		}
		if isFKViolation(err) {
			return resourceNotFound(
				"register_mcp_server: customer_slug %q is not registered",
				args.CustomerSlug,
			), nil
		}
		return Result{}, fmt.Errorf("register_mcp_server: insert: %w", err)
	}
	resourceID := uuidString(row.ID)
	return Result{
		Success:             true,
		AffectedResourceID:  resourceID,
		AffectedResourceURL: "/admin/mcp-servers/" + resourceID,
		Message: fmt.Sprintf(
			"Queued MCP server %q for registration with MCPJungle. The worker will flip the status to 'registered' or 'failed' once the API call completes.",
			args.Name,
		),
	}, nil
}

// parseRegisterMcpServerArgs validates the input shape + the
// customer-prefix invariant. Returns a typed validation Result on
// any rejection so the dashboard renders a friendly error.
func parseRegisterMcpServerArgs(raw json.RawMessage) (RegisterMcpServerArgs, *Result) {
	var args RegisterMcpServerArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		r := validationFailure("register_mcp_server: parse args: " + err.Error())
		return RegisterMcpServerArgs{}, &r
	}
	args.CustomerSlug = strings.TrimSpace(args.CustomerSlug)
	args.Name = strings.TrimSpace(args.Name)
	args.Transport = strings.TrimSpace(args.Transport)
	args.URL = strings.TrimSpace(args.URL)
	args.BearerTokenPath = strings.TrimSpace(args.BearerTokenPath)
	if args.CustomerSlug == "" {
		r := validationFailure("register_mcp_server: customer_slug is required")
		return args, &r
	}
	if args.Name == "" {
		r := validationFailure("register_mcp_server: name is required")
		return args, &r
	}
	if !validTransports[args.Transport] {
		r := validationFailure("register_mcp_server: transport must be one of http, stdio, sse")
		return args, &r
	}
	// FR-307 customer-prefix invariant: <customer_slug>.<server-name>.
	prefix := args.CustomerSlug + "."
	if !strings.HasPrefix(args.Name, prefix) || len(args.Name) <= len(prefix) {
		r := validationFailure(fmt.Sprintf(
			"register_mcp_server: name must start with %q (customer-prefix invariant)", prefix,
		))
		return args, &r
	}
	if args.Transport != "stdio" && args.URL == "" {
		r := validationFailure("register_mcp_server: url is required for http/sse transports")
		return args, &r
	}
	return args, nil
}

// isUniqueViolation reports whether err is Postgres's
// unique_violation (23505) — used to map the (customer_slug, name)
// UNIQUE collision to a friendly validation_failed.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}

// isFKViolation reports whether err is Postgres's
// foreign_key_violation (23503) — used to map the customer_slug FK
// miss to a resource_not_found result.
func isFKViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23503"
	}
	return false
}

func init() {
	handleRegisterMcpServer = realRegisterMcpServerHandler
}
