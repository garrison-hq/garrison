# Repository layout

Linked from AGENTS.md so the file tree doesn't sit in every-session context. When in doubt about where a file belongs, look at what this tree implies and follow the pattern. If the pattern doesn't cover your case, ask.

```
garrison/
├── AGENTS.md                     ← rules; loaded every session via CLAUDE.md
├── ARCHITECTURE.md
├── RATIONALE.md
├── README.md
├── CONTRIBUTING.md
├── CODE_OF_CONDUCT.md
├── SECURITY.md
├── CHANGELOG.md
├── LICENSE                       ← AGPL-3.0-only
├── LICENSE-DOCS                  ← CC-BY-4.0
├── .agents/                      ← Garrison-flavored slash commands (.claude → .agents)
│   └── commands/
│       ├── garrison-specify.md
│       ├── garrison-plan.md
│       ├── garrison-tasks.md
│       └── garrison-implement.md
├── specs/
│   ├── _context/                 ← filenames mix dots/hyphens (historical — do not normalise mid-milestone)
│   │   ├── m1-context.md
│   │   ├── m2.1-context.md
│   │   ├── m2.2-context.md
│   │   ├── m2-2-1-context.md
│   │   ├── m2.2.2-context.md
│   │   ├── m2.3-context.md
│   │   └── m{N}-context.md       ← active milestone's context file
│   ├── m1-event-bus/             ← M1 (un-numbered, predates the 00N scheme)
│   ├── 003-m2-1-claude-invocation/
│   ├── 004-m2-2-mempalace/
│   ├── 005-m2-2-1-finalize-ticket/
│   ├── 006-m2-2-2-compliance-calibration/
│   ├── 007-m2-3-infisical-vault/
│   ├── 008-m3-dashboard/
│   ├── 009-m4-dashboard-mutations/
│   ├── 010-m5-1-ceo-chat-backend/
│   ├── 011-m5-2-ceo-chat-frontend/
│   ├── 012-m5-3-chat-driven-mutations/
│   └── 013-m5-4-knows-pane/
├── supervisor/                   ← Go binary
│   ├── cmd/supervisor/           ← main + `mcp postgres` + `mcp finalize` + `mcp garrison-mutate` subcommands
│   ├── internal/
│   │   ├── claudeproto/          ← M2.1: stream-json event types + Router
│   │   ├── mcpconfig/            ← M2.1 + M2.3: per-invocation MCP config + Rule 3 pre-check
│   │   ├── pgmcp/                ← M2.1: in-tree Postgres MCP server (CallToolResult-shape after 59fc977)
│   │   ├── agents/               ← M2.1: startup-once cache + M4: agents.changed listener
│   │   ├── spawn/                ← M2.1 + M2.2 + M2.2.1 + M2.3: subprocess pipeline, finalize write, vault orchestration
│   │   ├── mempalace/            ← M2.2: bootstrap + wake-up + Client + DockerExec seam
│   │   ├── hygiene/              ← M2.2 + M2.2.1: Evaluator + listener + sweep
│   │   ├── finalize/             ← M2.2.1 + M2.2.2: finalize_ticket MCP server + richer-error infra
│   │   ├── vault/                ← M2.3: SecretValue + Client + ScanAndRedact + audit row
│   │   ├── chat/                 ← M5.1 + M5.2 + M5.3: chat policy + transport + listener + tool-use surface
│   │   ├── garrisonmutate/       ← M5.3: 8 chat-driven mutation verbs (in-tree MCP server)
│   │   ├── leakscan/             ← M5.4: shared 10-pattern set extracted from finalize
│   │   ├── objstore/             ← M5.4: MinIO wrapper + leak-scan + size-cap + ETag-aware GET/PUT
│   │   ├── dashboardapi/         ← M5.4: HTTP server on port 8081 (Company.md + mempalace proxy)
│   │   ├── config/, store/, events/, pgdb/, recovery/, health/, concurrency/, testdb/
│   ├── tools/
│   │   └── vaultlog/             ← M2.3: custom go vet analyzer rejecting SecretValue logging
│   ├── go.mod
│   └── Dockerfile
├── dashboard/                    ← Next.js 16 app (M3 + M4 + M5.1 + M5.2 + M5.4 shipped)
│   ├── lib/actions/companyMD.ts  ← M5.4: Server Actions for Company.md GET/PUT
│   ├── lib/queries/knowsPane.ts  ← M5.4: recent palace writes + KG facts queries
│   └── components/features/ceo-chat/{KnowsPane,CompanyMDTab,CompanyMDEditor,RecentPalaceWritesTab,KGRecentFactsTab}.tsx ← M5.4
├── migrations/                   ← SQL, consumed by sqlc (Go) and Drizzle (TS)
│   └── seed/                     ← engineer.md, qa-engineer.md (embedded into migrations via +embed-agent-md)
├── docs/
│   ├── agents/                   ← agent-context reference docs linked from AGENTS.md
│   │   ├── milestone-context.md
│   │   └── repository-layout.md  ← this file
│   ├── architecture.md           ← pointer file
│   ├── architecture-reconciliation-2026-04-24.md  ← frozen decision-provenance snapshot
│   ├── getting-started.md
│   ├── mcp-registry-candidates.md   ← M8 input: MCPJungle commitment
│   ├── skill-registry-candidates.md ← M7 input: SkillHub commitment
│   ├── ops-checklist.md          ← post-migrate and post-deploy steps
│   ├── README.md
│   ├── research/
│   │   ├── m2-spike.md
│   │   ├── m5-spike.md
│   │   └── m5-4-spike-minio.md   ← M5.4 binding input (MinIO behaviour)
│   ├── security/
│   │   ├── vault-threat-model.md
│   │   └── chat-threat-model.md  ← M5.3 + M5.4 amendments
│   ├── forensics/
│   │   └── pgmcp-three-bug-chain.md  ← post-M2.2.2 root-cause investigation
│   ├── issues/
│   │   ├── agent-workspace-sandboxing.md  ← Docker-per-agent fix planned post-M3
│   │   └── cost-telemetry-blind-spot.md   ← supervisor signal-handling fix
│   └── retros/
│       ├── m1.md
│       ├── m1-retro-addendum.md
│       ├── m2-1.md
│       ├── m2-2.md
│       ├── m2-2-1.md
│       ├── m2-2-2.md
│       ├── m2-2-x-compliance-retro.md     ← arc synthesis
│       ├── m2-3.md
│       ├── m3.md
│       ├── m4.md
│       ├── m5-1.md
│       ├── m5-2.md
│       ├── m5-3.md
│       └── m5-4.md
├── experiment-results/           ← exploratory matrices (e.g. matrix-post-uuid-fix.md), not production
├── examples/                     ← toy company YAML, sample agent.md files
└── .specify/                     ← spec-kit scaffolding
    ├── memory/constitution.md
    ├── scripts/
    └── templates/
```
