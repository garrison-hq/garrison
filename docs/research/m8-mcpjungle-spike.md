# M8 spike — MCPJungle ACL primitives

**Spike question**: does MCPJungle support per-agent AND per-tenant ACL
scoping, or does it only support per-client?

**Spike date**: 2026-05-03 (post-M7 ship; the candidates-doc maturity
re-check gate at M7 kickoff is now due).

**Spike scope**: documentation + source-code reading only. No deployment;
no live policy testing. Time-boxed at ~30 minutes. The findings below
are sufficient to commit the M8 ACL design without a live test.

**Output**: this file. The spike does NOT amend
`docs/mcp-registry-candidates.md` or commit Garrison to MCPJungle —
that's the M8 plan's job. The spike establishes facts about what
MCPJungle's ACL surface actually offers so the plan can decide what
to commit.

---

## Environment

- MCPJungle repository: https://github.com/mcpjungle/MCPJungle
- Default branch read at: `main`, last pushed `2026-05-03T17:52:58Z`
- Stars: 995. Open issues: 68. Active maintenance signal positive.
- License: **MPL-2.0** (Mozilla Public License 2.0). File-level copyleft
  on modified MCPJungle source itself; linking from Garrison's Go code
  via HTTP is fine (no viral effect on Garrison's MIT/internal code).
  This contradicts an earlier off-the-cuff "Apache-2.0" framing in the
  M8 context conversation; correcting here.
- Stack: Go server, Postgres backing store (also supports SQLite for
  dev). Docker-deployable via official `docker-compose.yaml` (development)
  and `docker-compose.prod.yaml` (enterprise). Both shipped in the
  repo root.
- Server modes: `development` (no ACL; all clients reach all servers)
  vs `enterprise` (ACL enforced). Garrison must run in `enterprise`
  mode for any production use.

---

## Findings

### F1 — Per-agent ACL: ✅ confirmed-supported, primitive name `McpClient`

`internal/model/mcp_client.go` defines:

```go
type McpClient struct {
    gorm.Model
    Name        string `gorm:"uniqueIndex;not null"`
    Description string
    AccessToken string `gorm:"unique; not null"`
    AllowList   datatypes.JSON `gorm:"type:jsonb; not null"`
}

func (c *McpClient) CheckHasServerAccess(serverName string) bool {
    // unmarshals AllowList; returns true if serverName ∈ AllowList
    // OR AllowList contains the AllowAll wildcard.
}
```

Every authenticated client (e.g. an agent's claude container) sends
`Authorization: Bearer <token>`. The proxy resolves the token to a
McpClient row; the AllowList JSON array determines which registered
MCP servers are reachable. Wildcards via `types.AllowAllMcpServers`
are supported but should NOT be used in Garrison enterprise deploy.

**Garrison mapping**: each Garrison agent gets exactly one McpClient
created in MCPJungle at agent activation time (post-approve in T010
+ migrate7 in T014), with the AllowList scoped to the MCP servers
the operator approved during the hire (`agents.mcp_servers_jsonb`
already populated per M7 schema). This maps cleanly.

### F2 — Per-tenant ACL: ❌ no native primitive

Domain models (`internal/model/`):

- `mcp_client.go` — per-client allow-list (per F1)
- `user.go` — human users (admin / regular) with access tokens; no
  resource-scoping
- `mcp_server.go` — registered MCP servers; flat namespace, unique by
  Name across the whole MCPJungle instance
- `tool_group.go` — groups of tools (subset of what an MCP client can
  *see*); orthogonal to clients
- `server_config.go` — the MCPJungle server's own config

There is **no Tenant, Organization, Namespace, or Workspace primitive
in the model.** The `mcp_servers` table is a flat list per MCPJungle
instance. The `mcp_clients` table is a flat list per MCPJungle
instance. No FK from either to a tenant row.

The `mcp_client.go` source carries this comment on `AllowList`:

> // storing the list of server names as a JSON array is a convenient
> // way for now. In the future, this will be removed in favor of a
> // separate table for ACLs.

So the per-client ACL surface itself is acknowledged as immature
(JSON-array-on-row rather than a relational ACL table). A tenant
primitive would be a substantial future addition, not a near-term
patch.

### F3 — Tool Groups slice the *tool surface*, not the client surface

`tool_group.go`:

```go
type ToolGroup struct {
    Name           string
    IncludedTools  datatypes.JSON
    IncludedServers datatypes.JSON
    ExcludedTools  datatypes.JSON
}
```

ToolGroups are operator-defined slices of "expose only this subset of
tools to certain clients." Combined with McpClient.AllowList they let
you say "client X can see servers A+B and within them only tools t1
+t2." Useful for fine-grained scoping but does NOT add a tenant axis.

### F4 — Auth surface

- Enterprise mode requires `mcpjungle init-server` to create the admin
  user; admin token stored at `~/.mcpjungle.conf`.
- Each McpClient has a single bearer token. Tokens can be auto-
  generated or operator-supplied via `--access-token` flag or via a
  JSON config file's `access_token_ref.{file,env}` indirection.
- Token rotation: re-create the McpClient with a fresh token. No
  documented in-place rotation primitive surfaced in the README; would
  need to verify the API + DB shape if rotation becomes a Garrison
  requirement.

### F5 — Stack fit confirmation

- Go server: matches Garrison's stack posture.
- Postgres: matches Garrison.
- Docker: deployable via `docker-compose.prod.yaml`. Single sidecar
  on `garrison-net`, same shape as the M2.2 mempalace + M5.4 minio
  sidecars.
- Stateful sessions supported — reduces stdio MCP server cold-start
  cost. Relevant for any MCP servers Garrison adopts that have
  meaningful startup overhead.

---

## Surprises

### S1 — License is MPL-2.0, not Apache-2.0

Spec drafts had been informally treating MCPJungle as "Apache-style
self-hosted." It's MPL-2.0. Practically equivalent for Garrison's use
(self-host, no source modifications planned), but the distinction
matters if Garrison ever forks or extends MCPJungle. MPL's file-level
copyleft means: changes to MCPJungle's own files would have to be
published under MPL; Garrison's separate Go code that *uses* MCPJungle
via HTTP stays under Garrison's chosen license.

### S2 — Per-client ACL is JSON-array-on-row, with a documented intent
to refactor

The acknowledged "this will be removed in favor of a separate table
for ACLs" comment means even the F1 primitive is in flux. Garrison
should track the upstream ACL refactor as a watch-item; an upgrade
path that handles a relational-ACL migration may matter at M9+ when
multi-tenant lands.

### S3 — Server names are unique across the whole instance

If Garrison's near-term has only a single `mcpjungle` deployment, that
flat namespace is fine. Once Hey Anton has multiple customers each
with their own custom MCP server set, name collisions become a real
risk (e.g. two customers both registering an MCP server named
`postgres-readonly`). Workarounds: name-prefix convention enforced by
Garrison's hire flow, or run one MCPJungle instance per customer (cost
likely prohibitive at scale).

---

## Open questions for the M8 plan

The spike answered the F1 + F2 question definitively. These open
questions are the M8 plan's territory:

- **Q1**: how does Garrison name McpClients to encode tenancy by
  convention? Proposed: `<customer-id>.<role-slug>.<agent-uuid>`. Plan
  decides the exact shape; spec frames the requirement.
- **Q2**: does Garrison wait for MCPJungle's relational-ACL refactor
  (S2 watch-item) before adopting the registry, or adopt now and
  migrate? Proposed: adopt now per F1's working primitive; the
  refactor will be backwards-compatible with existing AllowLists per
  the comment's "removed in favor of" framing (assumes Garrison's
  data is migratable).
- **Q3**: does M8 ship a Garrison-side wrapper that enforces tenant-
  name conventions on McpClient creation (e.g. reject API calls that
  create clients without a customer-id prefix)? Proposed: yes —
  enforce in `internal/garrisonmutate.ApproveHire` since that's where
  per-agent McpClient creation lands. M8 plan binds.
- **Q4**: token rotation — does Garrison need it for M8? If yes,
  needs an upstream check (whether MCPJungle has a documented
  rotation API or whether the workaround is "create new client + delete
  old"). Spike did not investigate; M8 plan decides whether to scope
  this in.

---

## Implications for M8

1. **MCPJungle is fit for M8 as designed** — per-agent ACL is the
   primitive M8 needs in the alpha (single-tenant) phase. The
   committed `docs/mcp-registry-candidates.md` "MCPJungle leading
   candidate" disposition holds; the spike confirms it.

2. **Per-tenant scoping is not blocker for M8 alpha**, but it's a
   planning constraint for the future. M8 should commit to a McpClient
   naming convention that encodes tenancy from day one (Q1 + Q3
   above). Doing it later would mean a rename pass across every
   already-deployed McpClient.

3. **`docs/mcp-registry-candidates.md` should be amended** with this
   finding before the M8 plan cites it as binding. Specifically:
   - Drop the off-the-cuff "Apache-licensed" framing (S1).
   - Soften the "ACL surface" line (the doc says the ACL story "is
     the right shape for 'this tenant's agents can only reach these
     MCP servers'") to reflect that per-tenant requires a Garrison-
     side naming convention; MCPJungle has per-client, not per-tenant
     primitives.
   - Add the watch-item from S2 about the upstream relational-ACL
     refactor.

4. **Stack-fit confirmation** survives the spike: Go + Postgres +
   Docker + enterprise-mode-by-default for production. Maturity
   re-check signal positive (995 stars, last commit same day as the
   spike).

5. **Forward-looking** — Garrison should consider, at M9+ multi-tenant
   territory, whether to:
   - Wait for the upstream relational-ACL refactor;
   - Run one MCPJungle instance per customer;
   - Wrap MCPJungle with a Garrison-side tenant enforcer that
     pre-validates every McpClient creation against tenant policy.

   The M8 ship doesn't pick between these; it just records the trade
   so M9 doesn't rediscover.

---

## Spike status

This spike is closed. Findings are sufficient to:

- Commit MCPJungle as the M8 MCP-registry choice (subject to one
  more pass at M8 plan time confirming the same stars/issues/last-
  commit signals haven't degraded).
- Carry the per-tenant naming convention as an explicit M8 plan
  decision rather than an open assumption.

No live-deployment work is required before the M8 spec lands; that's
the plan + tasks territory. Re-open this spike only if S1/S2/S3 turn
out to be wrong on a closer read by a future implementer.

---

## Cross-references

- `docs/mcp-registry-candidates.md` — the binding candidates doc; due
  for the amendments listed in §"Implications for M8" #3.
- `docs/architecture-reconciliation-2026-04-24.md` — frozen snapshot
  of the original 2026-04-24 architecture-session decision; kept
  unchanged. Future architecture passes get their own dated file.
- `RATIONALE.md` §13 — spike-first rule for external-tool-behaviour
  milestones. M8 has both an external-registry component (this) and
  the agent-spawned-tickets component (no spike needed; in-tree).
