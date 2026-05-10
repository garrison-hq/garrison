// M8 audit-row read-side. Per FR-502, /activity gains an
// agent_instance_id filter that surfaces every audit row anchored to
// a specific agent's run. Chat-anchored rows continue to render via
// the M5.3 SSE path; this query is server-rendered alongside the
// live feed when the operator passes ?agent_instance_id=<uuid>.
//
// Per AGENTS.md "Tests for Go only, never frontend": ships without
// vitest coverage; the Go-side T019 integration test covers shape.

import { eq, desc } from 'drizzle-orm';
import { appDb } from '@/lib/db/appClient';
import {
  chatMutationAudit,
  agentInstances,
  agents,
  tickets,
} from '@/drizzle/schema.supervisor';

export interface AgentAnchoredAuditRow {
  id: string;
  agentInstanceId: string;
  roleSlug: string;
  ticketId: string | null;
  ticketObjective: string | null;
  verb: string;
  outcome: string;
  reversibilityClass: number;
  affectedResourceId: string | null;
  affectedResourceType: string | null;
  argsJsonb: unknown;
  createdAt: string;
}

/** listAuditByAgentInstance — surfaces every audit row anchored to a
 *  specific agent_instance_id, joined with the originating ticket and
 *  the agent's role for the linked-list rendering FR-502 calls for. */
export async function listAuditByAgentInstance(
  agentInstanceId: string,
  limit = 200,
): Promise<AgentAnchoredAuditRow[]> {
  const rows = await appDb
    .select({
      id: chatMutationAudit.id,
      agentInstanceId: chatMutationAudit.agentInstanceId,
      roleSlug: agentInstances.roleSlug,
      ticketId: agentInstances.ticketId,
      ticketObjective: tickets.objective,
      verb: chatMutationAudit.verb,
      outcome: chatMutationAudit.outcome,
      reversibilityClass: chatMutationAudit.reversibilityClass,
      affectedResourceId: chatMutationAudit.affectedResourceId,
      affectedResourceType: chatMutationAudit.affectedResourceType,
      argsJsonb: chatMutationAudit.argsJsonb,
      createdAt: chatMutationAudit.createdAt,
    })
    .from(chatMutationAudit)
    .leftJoin(agentInstances, eq(agentInstances.id, chatMutationAudit.agentInstanceId))
    .leftJoin(tickets, eq(tickets.id, agentInstances.ticketId))
    .where(eq(chatMutationAudit.agentInstanceId, agentInstanceId))
    .orderBy(desc(chatMutationAudit.createdAt))
    .limit(limit);
  // The leftJoins make some fields nullable in TS but the schema
  // guarantees agentInstanceId is non-null for the filtered set.
  return rows
    .filter((r): r is typeof r & { agentInstanceId: string } => r.agentInstanceId !== null)
    .map((r) => ({
      id: r.id,
      agentInstanceId: r.agentInstanceId,
      roleSlug: r.roleSlug ?? '',
      ticketId: r.ticketId,
      ticketObjective: r.ticketObjective,
      verb: r.verb,
      outcome: r.outcome,
      reversibilityClass: r.reversibilityClass,
      affectedResourceId: r.affectedResourceId,
      affectedResourceType: r.affectedResourceType,
      argsJsonb: r.argsJsonb,
      createdAt: r.createdAt as string,
    }));
}
