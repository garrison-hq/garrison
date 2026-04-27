// ─── generated via drizzle-kit pull — do not edit ───
// Run `bun run drizzle:pull` to regenerate.
// Source: goose-managed migrations under ../../migrations/.

import { pgTable, integer, bigint, boolean, timestamp, index, foreignKey, uuid, text, numeric, jsonb, unique, primaryKey, check, interval } from "drizzle-orm/pg-core"
import { sql } from "drizzle-orm"



export const gooseDbVersion = pgTable("goose_db_version", {
	id: integer().primaryKey().generatedByDefaultAsIdentity({ name: "goose_db_version_id_seq", startWith: 1, increment: 1, minValue: 1, maxValue: 2147483647, cache: 1 }),
	// You can use { mode: "bigint" } if numbers are exceeding js number limitations
	versionId: bigint("version_id", { mode: "number" }).notNull(),
	isApplied: boolean("is_applied").notNull(),
	tstamp: timestamp({ mode: 'string' }).defaultNow().notNull(),
});

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
]);

export const eventOutbox = pgTable("event_outbox", {
	id: uuid().defaultRandom().primaryKey().notNull(),
	channel: text().notNull(),
	payload: jsonb().notNull(),
	createdAt: timestamp("created_at", { withTimezone: true, mode: 'string' }).defaultNow().notNull(),
	processedAt: timestamp("processed_at", { withTimezone: true, mode: 'string' }),
}, (table) => [
	index("idx_event_outbox_unprocessed").using("btree", table.createdAt.asc().nullsLast().op("timestamptz_ops")).where(sql`(processed_at IS NULL)`),
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

export const companies = pgTable("companies", {
	id: uuid().defaultRandom().primaryKey().notNull(),
	name: text().notNull(),
	createdAt: timestamp("created_at", { withTimezone: true, mode: 'string' }).defaultNow().notNull(),
});

export const tickets = pgTable("tickets", {
	id: uuid().defaultRandom().primaryKey().notNull(),
	departmentId: uuid("department_id").notNull(),
	objective: text().notNull(),
	createdAt: timestamp("created_at", { withTimezone: true, mode: 'string' }).defaultNow().notNull(),
	columnSlug: text("column_slug").default('todo').notNull(),
	acceptanceCriteria: text("acceptance_criteria"),
	metadata: jsonb().default({}).notNull(),
	origin: text().default('sql').notNull(),
}, (table) => [
	foreignKey({
			columns: [table.departmentId],
			foreignColumns: [departments.id],
			name: "tickets_department_id_fkey"
		}),
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
}, (table) => [
	index("idx_agents_active_by_dept").using("btree", table.departmentId.asc().nullsLast().op("text_ops"), table.roleSlug.asc().nullsLast().op("text_ops")).where(sql`(status = 'active'::text)`),
	foreignKey({
			columns: [table.departmentId],
			foreignColumns: [departments.id],
			name: "agents_department_id_fkey"
		}),
	unique("agents_department_id_role_slug_key").on(table.departmentId, table.roleSlug),
]);

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
	agentInstanceId: uuid("agent_instance_id").notNull(),
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
