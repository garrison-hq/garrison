// ─── generated via drizzle-kit pull — do not edit ───
// Run `bun run drizzle:pull` to regenerate.
// Source: goose-managed migrations under ../../migrations/.

import { pgTable, integer, bigint, boolean, timestamp, index, uuid, text, jsonb, foreignKey, numeric, unique, check, smallint, primaryKey, interval } from "drizzle-orm/pg-core"
import { sql } from "drizzle-orm"



export const gooseDbVersion = pgTable("goose_db_version", {
	id: integer().primaryKey().generatedByDefaultAsIdentity({ name: "goose_db_version_id_seq", startWith: 1, increment: 1, minValue: 1, maxValue: 2147483647, cache: 1 }),
	// You can use { mode: "bigint" } if numbers are exceeding js number limitations
	versionId: bigint("version_id", { mode: "number" }).notNull(),
	isApplied: boolean("is_applied").notNull(),
	tstamp: timestamp({ mode: 'string' }).defaultNow().notNull(),
});

export const eventOutbox = pgTable("event_outbox", {
	id: uuid().defaultRandom().primaryKey().notNull(),
	channel: text().notNull(),
	payload: jsonb().notNull(),
	createdAt: timestamp("created_at", { withTimezone: true, mode: 'string' }).defaultNow().notNull(),
	processedAt: timestamp("processed_at", { withTimezone: true, mode: 'string' }),
}, (table) => [
	index("idx_event_outbox_unprocessed").using("btree", table.createdAt.asc().nullsLast().op("timestamptz_ops")).where(sql`(processed_at IS NULL)`),
]);

export const agentInstances = pgTable("agent_instances", {
	id: uuid().defaultRandom().primaryKey().notNull(),
	departmentId: uuid("department_id").notNull(),
	ticketId: uuid("ticket_id").notNull(),
	pid: integer(),
	startedAt: timestamp("started_at", { withTimezone: true, mode: 'string' }).defaultNow().notNull(),
	finishedAt: timestamp("finished_at", { withTimezone: true, mode: 'string' }),
	status: text().notNull(),
	exitReason: text("exit_reason"),
	totalCostUsd: numeric("total_cost_usd", { precision: 10, scale:  6 }),
	wakeUpStatus: text("wake_up_status"),
	roleSlug: text("role_slug").default('engineer').notNull(),
	imageDigest: text("image_digest").default("").notNull(),
	preambleHash: text("preamble_hash").default("").notNull(),
	claudeMdHash: text("claude_md_hash"),
	originatingAuditId: uuid("originating_audit_id"),
}, (table) => [
	index("idx_agent_instances_running").using("btree", table.departmentId.asc().nullsLast().op("uuid_ops")).where(sql`(status = 'running'::text)`),
	foreignKey({
			columns: [table.departmentId],
			foreignColumns: [departments.id],
			name: "agent_instances_department_id_fkey"
		}),
	foreignKey({
			columns: [table.ticketId],
			foreignColumns: [tickets.id],
			name: "agent_instances_ticket_id_fkey"
		}),
	foreignKey({
			columns: [table.originatingAuditId],
			foreignColumns: [chatMutationAudit.id],
			name: "agent_instances_originating_audit_id_fkey"
		}).onDelete("set null"),
]);

export const departments = pgTable("departments", {
	id: uuid().defaultRandom().primaryKey().notNull(),
	slug: text().notNull(),
	name: text().notNull(),
	concurrencyCap: integer("concurrency_cap").default(3).notNull(),
	createdAt: timestamp("created_at", { withTimezone: true, mode: 'string' }).defaultNow().notNull(),
	companyId: uuid("company_id"),
	workspacePath: text("workspace_path"),
	workflow: jsonb().default({}).notNull(),
}, (table) => [
	foreignKey({
			columns: [table.companyId],
			foreignColumns: [companies.id],
			name: "departments_company_id_fkey"
		}),
	unique("departments_slug_key").on(table.slug),
]);

export const tickets = pgTable("tickets", {
	id: uuid().defaultRandom().primaryKey().notNull(),
	departmentId: uuid("department_id").notNull(),
	objective: text().notNull(),
	createdAt: timestamp("created_at", { withTimezone: true, mode: 'string' }).defaultNow().notNull(),
	columnSlug: text("column_slug").default('todo').notNull(),
	acceptanceCriteria: text("acceptance_criteria"),
	metadata: jsonb().default({}).notNull(),
	origin: text().default('sql').notNull(),
	createdViaChatSessionId: uuid("created_via_chat_session_id"),
	parentTicketId: uuid("parent_ticket_id"),
}, (table) => [
	index("idx_tickets_chat_session").using("btree", table.createdViaChatSessionId.asc().nullsLast().op("uuid_ops")).where(sql`(created_via_chat_session_id IS NOT NULL)`),
	index("idx_tickets_parent").using("btree", table.parentTicketId.asc().nullsLast().op("uuid_ops")).where(sql`(parent_ticket_id IS NOT NULL)`),
	foreignKey({
			columns: [table.departmentId],
			foreignColumns: [departments.id],
			name: "tickets_department_id_fkey"
		}),
	foreignKey({
			columns: [table.createdViaChatSessionId],
			foreignColumns: [chatSessions.id],
			name: "tickets_created_via_chat_session_id_fkey"
		}).onDelete("set null"),
	foreignKey({
			columns: [table.parentTicketId],
			foreignColumns: [table.id],
			name: "tickets_parent_ticket_id_fkey"
		}),
]);

export const companies = pgTable("companies", {
	id: uuid().defaultRandom().primaryKey().notNull(),
	name: text().notNull(),
	createdAt: timestamp("created_at", { withTimezone: true, mode: 'string' }).defaultNow().notNull(),
	dailyBudgetUsd: numeric("daily_budget_usd", { precision: 10, scale:  2 }),
	pauseUntil: timestamp("pause_until", { withTimezone: true, mode: 'string' }),
});

export const ticketTransitions = pgTable("ticket_transitions", {
	id: uuid().defaultRandom().primaryKey().notNull(),
	ticketId: uuid("ticket_id").notNull(),
	fromColumn: text("from_column"),
	toColumn: text("to_column").notNull(),
	triggeredByAgentInstanceId: uuid("triggered_by_agent_instance_id"),
	triggeredByUser: boolean("triggered_by_user").default(false).notNull(),
	at: timestamp({ withTimezone: true, mode: 'string' }).defaultNow().notNull(),
	hygieneStatus: text("hygiene_status"),
	suspectedSecretPatternCategory: text("suspected_secret_pattern_category"),
}, (table) => [
	index("idx_ticket_transitions_by_ticket").using("btree", table.ticketId.asc().nullsLast().op("timestamptz_ops"), table.at.asc().nullsLast().op("timestamptz_ops")),
	index("idx_ticket_transitions_pattern_category").using("btree", table.suspectedSecretPatternCategory.asc().nullsLast().op("text_ops")).where(sql`(suspected_secret_pattern_category IS NOT NULL)`),
	foreignKey({
			columns: [table.ticketId],
			foreignColumns: [tickets.id],
			name: "ticket_transitions_ticket_id_fkey"
		}),
	foreignKey({
			columns: [table.triggeredByAgentInstanceId],
			foreignColumns: [agentInstances.id],
			name: "ticket_transitions_triggered_by_agent_instance_id_fkey"
		}),
]);

export const vaultAccessLog = pgTable("vault_access_log", {
	id: uuid().defaultRandom().primaryKey().notNull(),
	agentInstanceId: uuid("agent_instance_id"),
	ticketId: uuid("ticket_id"),
	secretPath: text("secret_path").notNull(),
	customerId: uuid("customer_id").notNull(),
	outcome: text().notNull(),
	timestamp: timestamp({ withTimezone: true, mode: 'string' }).defaultNow().notNull(),
	createdAt: timestamp("created_at", { withTimezone: true, mode: 'string' }).defaultNow().notNull(),
	metadata: jsonb(),
}, (table) => [
	index("idx_vault_access_log_agent_instance").using("btree", table.agentInstanceId.asc().nullsLast().op("uuid_ops")),
	index("idx_vault_access_log_ticket").using("btree", table.ticketId.asc().nullsLast().op("uuid_ops")).where(sql`(ticket_id IS NOT NULL)`),
	foreignKey({
			columns: [table.agentInstanceId],
			foreignColumns: [agentInstances.id],
			name: "vault_access_log_agent_instance_id_fkey"
		}),
	foreignKey({
			columns: [table.ticketId],
			foreignColumns: [tickets.id],
			name: "vault_access_log_ticket_id_fkey"
		}),
]);

export const chatSessions = pgTable("chat_sessions", {
	id: uuid().defaultRandom().primaryKey().notNull(),
	startedByUserId: uuid("started_by_user_id").notNull(),
	startedAt: timestamp("started_at", { withTimezone: true, mode: 'string' }).defaultNow().notNull(),
	endedAt: timestamp("ended_at", { withTimezone: true, mode: 'string' }),
	status: text().default('active').notNull(),
	totalCostUsd: numeric("total_cost_usd", { precision: 20, scale:  10 }).default('0').notNull(),
	claudeSessionLabel: text("claude_session_label"),
	isArchived: boolean("is_archived").default(false).notNull(),
}, (table) => [
	index("idx_chat_sessions_active").using("btree", table.status.asc().nullsLast().op("text_ops")).where(sql`(status = 'active'::text)`),
	index("idx_chat_sessions_user_active_unarchived").using("btree", table.startedByUserId.asc().nullsLast().op("timestamptz_ops"), table.startedAt.desc().nullsFirst().op("uuid_ops")).where(sql`(is_archived = false)`),
	index("idx_chat_sessions_user_started").using("btree", table.startedByUserId.asc().nullsLast().op("uuid_ops"), table.startedAt.desc().nullsFirst().op("uuid_ops")),
	check("chat_sessions_status_check", sql`status = ANY (ARRAY['active'::text, 'ended'::text, 'aborted'::text])`),
]);

export const chatMessages = pgTable("chat_messages", {
	id: uuid().defaultRandom().primaryKey().notNull(),
	sessionId: uuid("session_id").notNull(),
	turnIndex: integer("turn_index").notNull(),
	role: text().notNull(),
	status: text().notNull(),
	content: text(),
	tokensInput: integer("tokens_input"),
	tokensOutput: integer("tokens_output"),
	costUsd: numeric("cost_usd", { precision: 20, scale:  10 }),
	errorKind: text("error_kind"),
	rawEventEnvelope: jsonb("raw_event_envelope"),
	createdAt: timestamp("created_at", { withTimezone: true, mode: 'string' }).defaultNow().notNull(),
	terminatedAt: timestamp("terminated_at", { withTimezone: true, mode: 'string' }),
}, (table) => [
	index("idx_chat_messages_inflight").using("btree", table.sessionId.asc().nullsLast().op("text_ops"), table.status.asc().nullsLast().op("text_ops")).where(sql`(status = ANY (ARRAY['pending'::text, 'streaming'::text]))`),
	foreignKey({
			columns: [table.sessionId],
			foreignColumns: [chatSessions.id],
			name: "chat_messages_session_id_fkey"
		}).onDelete("cascade"),
	unique("chat_messages_session_id_turn_index_key").on(table.sessionId, table.turnIndex),
	check("chat_messages_role_check", sql`role = ANY (ARRAY['operator'::text, 'assistant'::text])`),
	check("chat_messages_status_check", sql`status = ANY (ARRAY['pending'::text, 'streaming'::text, 'completed'::text, 'failed'::text, 'aborted'::text])`),
]);

export const agents = pgTable("agents", {
	id: uuid().defaultRandom().primaryKey().notNull(),
	departmentId: uuid("department_id").notNull(),
	roleSlug: text("role_slug").notNull(),
	agentMd: text("agent_md").notNull(),
	model: text().notNull(),
	skills: jsonb().default([]).notNull(),
	mcpTools: jsonb("mcp_tools").default([]).notNull(),
	listensFor: jsonb("listens_for").notNull(),
	palaceWing: text("palace_wing"),
	status: text().default('active').notNull(),
	createdAt: timestamp("created_at", { withTimezone: true, mode: 'string' }).defaultNow().notNull(),
	mcpConfig: jsonb("mcp_config").default({}).notNull(),
	updatedAt: timestamp("updated_at", { withTimezone: true, mode: 'string' }).defaultNow().notNull(),
	imageDigest: text("image_digest"),
	runtimeCaps: jsonb("runtime_caps"),
	egressGrantJsonb: jsonb("egress_grant_jsonb"),
	mcpServersJsonb: jsonb("mcp_servers_jsonb"),
	lastGrandfatheredAt: timestamp("last_grandfathered_at", { withTimezone: true, mode: 'string' }),
	hostUid: integer("host_uid"),
}, (table) => [
	index("idx_agents_active_by_dept").using("btree", table.departmentId.asc().nullsLast().op("text_ops"), table.roleSlug.asc().nullsLast().op("text_ops")).where(sql`(status = 'active'::text)`),
	index("idx_agents_host_uid").using("btree", table.hostUid.asc().nullsLast().op("int4_ops")).where(sql`(host_uid IS NOT NULL)`),
	foreignKey({
			columns: [table.departmentId],
			foreignColumns: [departments.id],
			name: "agents_department_id_fkey"
		}),
	unique("agents_department_id_role_slug_key").on(table.departmentId, table.roleSlug),
]);

export const throttleEvents = pgTable("throttle_events", {
	id: uuid().defaultRandom().primaryKey().notNull(),
	companyId: uuid("company_id").notNull(),
	kind: text().notNull(),
	firedAt: timestamp("fired_at", { withTimezone: true, mode: 'string' }).defaultNow().notNull(),
	payload: jsonb().default({}).notNull(),
}, (table) => [
	index("idx_throttle_events_company_fired").using("btree", table.companyId.asc().nullsLast().op("timestamptz_ops"), table.firedAt.desc().nullsFirst().op("timestamptz_ops")),
	foreignKey({
			columns: [table.companyId],
			foreignColumns: [companies.id],
			name: "throttle_events_company_id_fkey"
		}),
	check("throttle_events_kind_check", sql`kind = ANY (ARRAY['company_budget_exceeded'::text, 'rate_limit_pause'::text])`),
]);

export const hiringProposals = pgTable("hiring_proposals", {
	id: uuid().defaultRandom().primaryKey().notNull(),
	roleTitle: text("role_title").notNull(),
	departmentSlug: text("department_slug").notNull(),
	justificationMd: text("justification_md").notNull(),
	skillsSummaryMd: text("skills_summary_md"),
	proposedVia: text("proposed_via").notNull(),
	proposedByChatSessionId: uuid("proposed_by_chat_session_id"),
	status: text().default('pending').notNull(),
	createdAt: timestamp("created_at", { withTimezone: true, mode: 'string' }).defaultNow().notNull(),
	targetAgentId: uuid("target_agent_id"),
	proposalType: text("proposal_type").default('new_agent').notNull(),
	skillDiffJsonb: jsonb("skill_diff_jsonb"),
	proposalSnapshotJsonb: jsonb("proposal_snapshot_jsonb"),
	skillDigestAtPropose: text("skill_digest_at_propose"),
	approvedAt: timestamp("approved_at", { withTimezone: true, mode: 'string' }),
	approvedBy: uuid("approved_by"),
	rejectedAt: timestamp("rejected_at", { withTimezone: true, mode: 'string' }),
	rejectedReason: text("rejected_reason"),
}, (table) => [
	index("idx_hp_chat_session").using("btree", table.proposedByChatSessionId.asc().nullsLast().op("uuid_ops")).where(sql`(proposed_by_chat_session_id IS NOT NULL)`),
	index("idx_hp_status_dept").using("btree", table.status.asc().nullsLast().op("timestamptz_ops"), table.departmentSlug.asc().nullsLast().op("text_ops"), table.createdAt.desc().nullsFirst().op("timestamptz_ops")),
	index("idx_hp_target_agent_pending").using("btree", table.targetAgentId.asc().nullsLast().op("text_ops"), table.proposalType.asc().nullsLast().op("text_ops")).where(sql`((status = 'pending'::text) AND (target_agent_id IS NOT NULL))`),
	foreignKey({
			columns: [table.departmentSlug],
			foreignColumns: [departments.slug],
			name: "hiring_proposals_department_slug_fkey"
		}).onDelete("restrict"),
	foreignKey({
			columns: [table.proposedByChatSessionId],
			foreignColumns: [chatSessions.id],
			name: "hiring_proposals_proposed_by_chat_session_id_fkey"
		}).onDelete("set null"),
	foreignKey({
			columns: [table.targetAgentId],
			foreignColumns: [agents.id],
			name: "hiring_proposals_target_agent_id_fkey"
		}),
	check("hiring_proposals_proposed_via_check", sql`proposed_via = ANY (ARRAY['ceo_chat'::text, 'dashboard'::text, 'agent'::text])`),
	check("hiring_proposals_proposal_type_check", sql`proposal_type = ANY (ARRAY['new_agent'::text, 'skill_change'::text, 'version_bump'::text])`),
	check("hiring_proposals_status_check", sql`status = ANY (ARRAY['pending'::text, 'approved'::text, 'rejected'::text, 'superseded'::text, 'install_in_progress'::text, 'installed'::text, 'install_failed'::text])`),
]);

export const agentContainerEvents = pgTable("agent_container_events", {
	id: uuid().defaultRandom().primaryKey().notNull(),
	agentId: uuid("agent_id").notNull(),
	kind: text().notNull(),
	imageDigest: text("image_digest"),
	startedAt: timestamp("started_at", { withTimezone: true, mode: 'string' }),
	stoppedAt: timestamp("stopped_at", { withTimezone: true, mode: 'string' }),
	stopReason: text("stop_reason"),
	cgroupCapsJsonb: jsonb("cgroup_caps_jsonb"),
	retentionClass: text("retention_class"),
	createdAt: timestamp("created_at", { withTimezone: true, mode: 'string' }).defaultNow().notNull(),
}, (table) => [
	index("idx_agent_container_events_agent_created").using("btree", table.agentId.asc().nullsLast().op("timestamptz_ops"), table.createdAt.desc().nullsFirst().op("timestamptz_ops")),
	foreignKey({
			columns: [table.agentId],
			foreignColumns: [agents.id],
			name: "agent_container_events_agent_id_fkey"
		}).onDelete("cascade"),
	check("agent_container_events_kind_check", sql`kind = ANY (ARRAY['created'::text, 'started'::text, 'stopped'::text, 'removed'::text, 'migrated'::text, 'oom_killed'::text, 'crashed'::text, 'image_digest_drift_detected'::text, 'reconciled_on_supervisor_restart'::text])`),
]);

export const agentInstallJournal = pgTable("agent_install_journal", {
	id: uuid().defaultRandom().primaryKey().notNull(),
	proposalId: uuid("proposal_id").notNull(),
	step: text().notNull(),
	outcome: text().notNull(),
	errorKind: text("error_kind"),
	payloadJsonb: jsonb("payload_jsonb").default({}).notNull(),
	createdAt: timestamp("created_at", { withTimezone: true, mode: 'string' }).defaultNow().notNull(),
}, (table) => [
	index("idx_agent_install_journal_proposal_created").using("btree", table.proposalId.asc().nullsLast().op("timestamptz_ops"), table.createdAt.desc().nullsFirst().op("timestamptz_ops")),
	foreignKey({
			columns: [table.proposalId],
			foreignColumns: [hiringProposals.id],
			name: "agent_install_journal_proposal_id_fkey"
		}).onDelete("cascade"),
	check("agent_install_journal_step_check", sql`step = ANY (ARRAY['download'::text, 'verify_digest'::text, 'extract'::text, 'mount'::text, 'container_create'::text, 'container_start'::text])`),
	check("agent_install_journal_outcome_check", sql`outcome = ANY (ARRAY['success'::text, 'failed'::text, 'interrupted'::text])`),
]);

export const chatMutationAudit = pgTable("chat_mutation_audit", {
	id: uuid().defaultRandom().primaryKey().notNull(),
	chatSessionId: uuid("chat_session_id").notNull(),
	chatMessageId: uuid("chat_message_id").notNull(),
	verb: text().notNull(),
	argsJsonb: jsonb("args_jsonb").notNull(),
	outcome: text().notNull(),
	reversibilityClass: smallint("reversibility_class").notNull(),
	affectedResourceId: text("affected_resource_id"),
	affectedResourceType: text("affected_resource_type"),
	createdAt: timestamp("created_at", { withTimezone: true, mode: 'string' }).defaultNow().notNull(),
	retentionClass: text("retention_class"),
}, (table) => [
	index("idx_cma_resource").using("btree", table.affectedResourceType.asc().nullsLast().op("text_ops"), table.affectedResourceId.asc().nullsLast().op("text_ops")),
	index("idx_cma_session").using("btree", table.chatSessionId.asc().nullsLast().op("uuid_ops"), table.createdAt.desc().nullsFirst().op("timestamptz_ops")),
	foreignKey({
			columns: [table.chatSessionId],
			foreignColumns: [chatSessions.id],
			name: "chat_mutation_audit_chat_session_id_fkey"
		}).onDelete("cascade"),
	foreignKey({
			columns: [table.chatMessageId],
			foreignColumns: [chatMessages.id],
			name: "chat_mutation_audit_chat_message_id_fkey"
		}).onDelete("cascade"),
	check("chat_mutation_audit_outcome_check", sql`outcome = ANY (ARRAY['success'::text, 'validation_failed'::text, 'leak_scan_failed'::text, 'ticket_state_changed'::text, 'concurrency_cap_full'::text, 'invalid_transition'::text, 'resource_not_found'::text, 'tool_call_ceiling_reached'::text])`),
	check("chat_mutation_audit_reversibility_class_check", sql`reversibility_class = ANY (ARRAY[1, 2, 3])`),
	check("chat_mutation_audit_affected_resource_type_check", sql`affected_resource_type = ANY (ARRAY['ticket'::text, 'agent_role'::text, 'hiring_proposal'::text])`),
	check("chat_mutation_audit_verb_check", sql`verb = ANY (ARRAY['create_ticket'::text, 'edit_ticket'::text, 'transition_ticket'::text, 'pause_agent'::text, 'resume_agent'::text, 'spawn_agent'::text, 'edit_agent_config'::text, 'propose_hire'::text, 'propose_skill_change'::text, 'bump_skill_version'::text, 'approve_hire'::text, 'reject_hire'::text, 'approve_skill_change'::text, 'reject_skill_change'::text, 'approve_version_bump'::text, 'reject_version_bump'::text, 'update_agent_md'::text, 'grandfathered_at_m7'::text])`),
]);

export const agentRoleSecrets = pgTable("agent_role_secrets", {
	roleSlug: text("role_slug").notNull(),
	secretPath: text("secret_path").notNull(),
	envVarName: text("env_var_name").notNull(),
	customerId: uuid("customer_id").notNull(),
	grantedAt: timestamp("granted_at", { withTimezone: true, mode: 'string' }).defaultNow().notNull(),
	grantedBy: text("granted_by").notNull(),
	createdAt: timestamp("created_at", { withTimezone: true, mode: 'string' }).defaultNow().notNull(),
	updatedAt: timestamp("updated_at", { withTimezone: true, mode: 'string' }).defaultNow().notNull(),
}, (table) => [
	index("idx_agent_role_secrets_secret_path").using("btree", table.secretPath.asc().nullsLast().op("text_ops"), table.customerId.asc().nullsLast().op("text_ops")),
	primaryKey({ columns: [table.roleSlug, table.envVarName, table.customerId], name: "agent_role_secrets_pkey"}),
]);

export const secretMetadata = pgTable("secret_metadata", {
	secretPath: text("secret_path").notNull(),
	customerId: uuid("customer_id").notNull(),
	provenance: text().notNull(),
	rotationCadence: interval("rotation_cadence").default('90 days').notNull(),
	lastRotatedAt: timestamp("last_rotated_at", { withTimezone: true, mode: 'string' }),
	lastAccessedAt: timestamp("last_accessed_at", { withTimezone: true, mode: 'string' }),
	allowedRoleSlugs: text("allowed_role_slugs").array().default([""]).notNull(),
	createdAt: timestamp("created_at", { withTimezone: true, mode: 'string' }).defaultNow().notNull(),
	updatedAt: timestamp("updated_at", { withTimezone: true, mode: 'string' }).defaultNow().notNull(),
	rotationProvider: text("rotation_provider").default('manual_paste').notNull(),
}, (table) => [
	primaryKey({ columns: [table.secretPath, table.customerId], name: "secret_metadata_pkey"}),
	check("secret_metadata_rotation_provider_check", sql`rotation_provider = ANY (ARRAY['infisical_native'::text, 'manual_paste'::text, 'not_rotatable'::text])`),
]);
