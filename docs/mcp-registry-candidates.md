# MCP server registry/proxy candidates — target-state evaluation

**Status**: target-state concern, deferred to M8-ish (when Garrison agents need to call third-party MCP servers for Hey Anton-style product work).
**Surfaced**: 2026-04-24, during the post-UUID-fix matrix run.
**Relationship to SkillHub**: similar pattern (private registry for agent components) but substantively different concerns. Kept as a separate doc, not bundled under a "registries" heading. See §"Why not paired with SkillHub" below.

## Why this isn't near-term

Current Garrison uses three in-tree MCP servers (`pgmcp`, `finalize`, `mempalace`) plus whatever `claude` pulls in by default. No third-party MCP servers are in use. No remote MCP servers are in use. Through M2.3 (vault), M3 (dashboard), M5 (CEO chat), and M7 (hiring), this doesn't change materially.

Around M8 — when Garrison starts integrating Hey Anton product surfaces (Twilio for phone, Gmail/Microsoft for email, Vapi for voice, etc.) — agents will start needing third-party MCP servers. At that point, "where do these MCP servers come from" and "how do agents reach them" become live architectural questions.

## What the 2026-04-24 research found

The MCP ecosystem has matured substantially since M2.2.2 shipped:

### Official MCP Registry (modelcontextprotocol.io/registry)

- Launched September 2025, still in preview
- Metadata-only catalog; actual packages live on npm/PyPI/Docker Hub
- **Explicitly does not support private servers**
- **Explicitly not designed for self-hosting**
- Provides an OpenAPI spec that private registries can implement to benefit from host-application support
- Term of art: "subregistries" are third-party implementations of the registry spec

**Relevance**: not a candidate for Garrison directly. Is the spec to conform to if building a private subregistry.

### MCPJungle (github.com/mcpjungle/MCPJungle) — CHOSEN FOR M8

- Self-hosted MCP gateway combining registry + runtime proxy
- **License**: MPL-2.0 (file-level copyleft on MCPJungle's own source; doesn't bind Garrison's separate Go code that talks to MCPJungle via HTTP)
- Go-based (matches Garrison's stack posture)
- SQLite or Postgres (Postgres matches Garrison)
- Docker-deployable (matches Coolify)
- Enterprise mode with ACLs: per-client access policies controlling which MCP clients can reach which registered servers
- Stateful or stateless session modes (stateful reduces latency for stdio-based MCP servers that have startup overhead)
- CLI for administration, HTTP gateway for clients

**Fit assessment for target-state Garrison**:
- For: stack match (Go + Postgres + Docker), combines the two concerns (registry + proxy) that Garrison's architecture will need together, per-agent ACL primitive (`McpClient.AllowList`) maps cleanly to "this agent can only reach these MCP servers"
- Mixed: the ACL story is **per-agent native, per-tenant via Garrison-side naming convention** (no native tenant primitive in the model). Workable at single-tenant alpha; multi-tenant beta uses one MCPJungle instance per customer (option A) so per-instance namespaces give hard isolation
- Against: per-client `AllowList` is currently a JSON-array-on-row primitive; source carries `// will be removed in favor of a separate table for ACLs` comment on `internal/model/mcp_client.go`. Watch-item — the upstream relational-ACL refactor may eventually land a tenant primitive, at which point Garrison's naming-convention enforcement becomes redundant

**Maturity check (2026-05-03, post-M7 ship)**:
- 995 stars, last commit same day as the check
- 68 open issues, active maintenance signal positive
- Per-agent + naming-convention-per-tenant ACL surface verified by source-read spike (`docs/research/m8-mcpjungle-spike.md`)

**Disposition**: chosen for M8 integration. Maturity gate fired positively at M7 kickoff per the cadence below; M8 plan re-confirms at plan time. Per-customer MCPJungle instance (option A) is the committed beta path for multi-tenant.

### MCPProxy (pricing.mcpproxy.app)

- Runtime proxy, not a registry
- JSONL audit log export (SIEM-friendly)
- Request-ID correlation across proxy and upstreams
- Grafana-friendly metrics
- Self-hosted, commercial pricing tiers being gauged

**Fit assessment**: if Garrison only needed the proxy half (not the registry half), this is well-designed. Given Garrison wants both (per the operator's 2026-04-24 framing), MCPJungle covers more of the surface.

**Disposition**: backup candidate if MCPJungle has matured poorly by M8.

### TrueFoundry

- Enterprise SaaS with proxy + registry + OAuth token management + managed observability
- Sub-3ms latency claimed
- Commercial, data-residency options

**Fit assessment**: over-engineered for solo-founder deployment, same pattern as SkillHub's iflytek alternative. Enterprise-oriented, assumes VPC deployment and platform team.

**Disposition**: not a fit at Garrison's scale. Re-evaluate only if Garrison becomes a multi-operator product with governance requirements.

### Kong AI Gateway (AI MCP Proxy plugin, Gateway 3.12+)

- Kong API Gateway extended with MCP support
- Enterprise-only plugin, requires Kong AI Gateway license
- Strong fit if Kong is already deployed

**Fit assessment**: Garrison doesn't run Kong. Adopting Kong just for MCP is substantial overhead.

**Disposition**: not relevant.

### Microsoft MCP Gateway

- Kubernetes-native, Azure-oriented
- Open source, no managed tier
- Azure Entra ID integration

**Fit assessment**: stack mismatch (Kubernetes vs Coolify, Azure vs Hetzner).

**Disposition**: not relevant.

### Build-our-own

Could extend `internal/pgmcp`'s patterns to a more general in-tree proxy + lightweight registry. Garrison already has the in-tree MCP server pattern and the Postgres-for-state pattern; a registry/proxy would be additive.

**Fit assessment**:
- For: perfect stack fit (by definition), full control, no dependency on external project lifecycle
- Against: real engineering work for a problem that external projects already solve; adds scope to M8

**Disposition**: fallback if no external candidate has matured acceptably by M8.

## Why not paired with SkillHub

SkillHub (committed target-state) and an MCP server registry (this doc) look like the same decision pattern — "private registry for things agents use" — but they differ enough to keep separate:

| Concern | SkillHub | MCP server registry |
|---|---|---|
| What it distributes | `SKILL.md` + supporting files (markdown + text) | MCP server packages (binaries, containers, npm packages) |
| When it's consumed | Install-time, into agent workspace | Runtime, via MCP protocol |
| Failure mode | Missing skill → agent has fewer capabilities | Missing MCP server → agent tool calls fail at runtime |
| Security surface | Static file distribution | Runtime protocol with tool-call authorization |
| Protocol standardization | None — skills are a convention | MCP has an official spec + official registry spec |
| Analog in older ecosystems | Puppet modules, ansible galaxy | Service mesh + package registry (Istio + Maven) |

These differences mean the decision criteria for picking a solution are different. SkillHub-class evaluation emphasizes simplicity and governance for markdown files; MCP-registry-class evaluation has to consider runtime proxy behavior, ACL enforcement, session management, and protocol conformance.

Keeping them separate avoids a "pick one registry to do both jobs" mistake.

## Acceptance criteria for M8 decision

When M8-era work starts and a private MCP registry becomes load-bearing, the decision between candidates should be made using these criteria:

1. **Stack match**: Go or language-agnostic (HTTP/JSON-RPC), Postgres-backed, Docker-deployable, Coolify-friendly
2. **Combined registry + proxy**: both install-time cataloguing and runtime routing, since Garrison's architecture will need both
3. **ACL surface**: ability to scope which agents/tenants can reach which servers (important for multi-tenant operation once Hey Anton has real customers)
4. **Observability**: audit log of tool calls with request IDs, compatible with whatever observability stack exists by M8
5. **MCP spec conformance**: implements the subregistry API spec or is drop-in compatible
6. **Maturity**: active maintenance within the past 3 months, not a single-contributor abandoned project

## Re-evaluation cadence

**M7 kickoff re-check fired 2026-05-03** (post-M7 ship). Source-read
spike confirmed per-agent ACL primitive, the absence of a native
tenant primitive, MPL-2.0 license, active-maintenance signals. See
`docs/research/m8-mcpjungle-spike.md`. Disposition moves from
leading-candidate to chosen.

Next re-check: at M8 plan time (the candidates doc is binding input
to the M8 plan; plan confirms the same signals haven't degraded).

## Related files

- `docs/research/m8-mcpjungle-spike.md` — 2026-05-03 source-read spike confirming MCPJungle's ACL primitives + license + maturity signals
- `specs/_context/m8-context.md` — M8 context doc citing this file as binding
- `docs/architecture-target-state.md` (if/when written) — should reference this doc as "MCP registry is a target-state concern, see this file"
- `docs/skill-registry-candidates.md` — parallel doc for SkillHub evaluation, kept separate per this doc's rationale
- `docs/issues/agent-workspace-sandboxing.md` — closed by M7's per-agent container shipping; retained as historical precedent