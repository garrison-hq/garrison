'use server';

// M7 hiring Server Actions per spec FR-104..FR-110a + plan §6.
// Approve / reject for new-agent hires, skill changes, version bumps;
// the install pipeline kickoff happens supervisor-side via the
// transition_to_install_in_progress hook (the supervisor's restart-
// recovery path picks up install_in_progress proposals).
//
// Per the F3 lean (decision #1) update_agent_md is Server-Action-only;
// it lands in this file alongside the approve/reject actions.
//
// These actions write through Drizzle directly (matching M5.4's pattern
// for chat-driven mutations the dashboard handles). The Go-side
// internal/garrisonmutate/approve.go is the canonical library for the
// same shape; both code paths converge on the same hiring_proposals +
// chat_mutation_audit + agents row writes.

import { eq, and, ne, sql } from 'drizzle-orm';
import { appDb } from '@/lib/db/appClient';
import {
  hiringProposals,
  agents,
  departments,
  chatMutationAudit,
} from '@/drizzle/schema.supervisor';
import { getSession } from '@/lib/auth/session';
import { AuthError, AuthErrorKind } from '@/lib/auth/errors';

export type ApproveHireResult = {
  agentId: string;
  auditId: string;
};

export type ApproveSkillResult = {
  agentId: string;
  auditId: string;
  supersededCount: number;
};

export class HiringActionError extends Error {
  constructor(
    message: string,
    readonly kind:
      | 'not_found'
      | 'not_pending'
      | 'wrong_type'
      | 'missing_target_agent'
      | 'reason_required',
  ) {
    super(message);
    this.name = 'HiringActionError';
  }
}

async function assertOperator(): Promise<{ operatorId: string }> {
  const session = await getSession();
  if (!session) {
    throw new AuthError(
      AuthErrorKind.NoSession,
      'must be authenticated to approve / reject hiring proposals',
    );
  }
  return { operatorId: session.user.id };
}

function defaultListensFor(deptSlug: string): string[] {
  return [`work.ticket.created.${deptSlug}.todo`];
}

function defaultAgentMD(roleTitle: string, justificationMD: string, skillsSummary: string | null): string {
  const skills = skillsSummary ? `\n\n## Skills\n\n${skillsSummary}` : '';
  return `# ${roleTitle}\n\n${justificationMD}${skills}\n`;
}

const DEFAULT_MODEL = 'claude-haiku-4-5-20251001';

// approveHireAction lifts a new-agent proposal into a live agents row.
// Single tx: read proposal, INSERT agents, mark proposal approved +
// install_in_progress, write the audit snapshot. The supervisor's
// install pipeline picks up install_in_progress rows on its next tick.
export async function approveHireAction(proposalId: string): Promise<ApproveHireResult> {
  const { operatorId } = await assertOperator();

  return appDb.transaction(async (tx) => {
    const proposal = await readPendingProposal(tx, proposalId, 'new_agent');
    const dept = await tx
      .select()
      .from(departments)
      .where(eq(departments.slug, proposal.departmentSlug))
      .limit(1);
    if (dept.length === 0) {
      throw new HiringActionError(
        `department ${proposal.departmentSlug} not found`,
        'not_found',
      );
    }

    const inserted = await tx
      .insert(agents)
      .values({
        departmentId: dept[0].id,
        roleSlug: proposal.roleTitle,
        agentMd: defaultAgentMD(
          proposal.roleTitle,
          proposal.justificationMd,
          proposal.skillsSummaryMd ?? null,
        ),
        model: DEFAULT_MODEL,
        skills: [],
        listensFor: defaultListensFor(proposal.departmentSlug),
        status: 'active',
        imageDigest: '',
        mcpServersJsonb: [],
      })
      .returning({ id: agents.id });
    const agentId = inserted[0].id;

    await tx
      .update(hiringProposals)
      .set({ status: 'approved', approvedAt: sql`NOW()`, approvedBy: operatorId })
      .where(and(eq(hiringProposals.id, proposalId), eq(hiringProposals.status, 'pending')));

    await tx
      .update(hiringProposals)
      .set({ status: 'install_in_progress' })
      .where(and(eq(hiringProposals.id, proposalId), eq(hiringProposals.status, 'approved')));

    const auditRow = await tx
      .insert(chatMutationAudit)
      .values({
        chatSessionId: null,
        chatMessageId: null,
        verb: 'approve_hire',
        argsJsonb: {
          proposal_id: proposalId,
          agent_id: agentId,
          role_title: proposal.roleTitle,
          department_slug: proposal.departmentSlug,
          proposal_type: 'new_agent',
          superseded_count: 0,
        },
        outcome: 'success',
        reversibilityClass: 3,
        affectedResourceId: agentId,
        affectedResourceType: 'agent_role',
      })
      .returning({ id: chatMutationAudit.id });

    return { agentId, auditId: auditRow[0].id };
  });
}

// approveSkillChangeAction supersedes sibling pending skill_change
// proposals for the same target_agent_id (FR-110a) inside the same tx
// as the approve.
export async function approveSkillChangeAction(proposalId: string): Promise<ApproveSkillResult> {
  return approveSkillFlow(proposalId, 'skill_change', 'approve_skill_change');
}

export async function approveVersionBumpAction(proposalId: string): Promise<ApproveSkillResult> {
  return approveSkillFlow(proposalId, 'version_bump', 'approve_version_bump');
}

async function approveSkillFlow(
  proposalId: string,
  expectedType: 'skill_change' | 'version_bump',
  verb: 'approve_skill_change' | 'approve_version_bump',
): Promise<ApproveSkillResult> {
  const { operatorId } = await assertOperator();
  return appDb.transaction(async (tx) => {
    const proposal = await readPendingProposal(tx, proposalId, expectedType);
    if (!proposal.targetAgentId) {
      throw new HiringActionError(
        'proposal is missing target_agent_id',
        'missing_target_agent',
      );
    }

    await tx
      .update(hiringProposals)
      .set({ status: 'approved', approvedAt: sql`NOW()`, approvedBy: operatorId })
      .where(and(eq(hiringProposals.id, proposalId), eq(hiringProposals.status, 'pending')));

    await tx
      .update(hiringProposals)
      .set({ status: 'install_in_progress' })
      .where(and(eq(hiringProposals.id, proposalId), eq(hiringProposals.status, 'approved')));

    const supersededRows = await tx
      .update(hiringProposals)
      .set({
        status: 'superseded',
        rejectedAt: sql`NOW()`,
        rejectedReason: sql`${`superseded_by:${proposalId}`}`,
      })
      .where(
        and(
          eq(hiringProposals.status, 'pending'),
          eq(hiringProposals.targetAgentId, proposal.targetAgentId),
          eq(hiringProposals.proposalType, expectedType),
          ne(hiringProposals.id, proposalId),
        ),
      )
      .returning({ id: hiringProposals.id });

    const auditRow = await tx
      .insert(chatMutationAudit)
      .values({
        chatSessionId: null,
        chatMessageId: null,
        verb,
        argsJsonb: {
          proposal_id: proposalId,
          agent_id: proposal.targetAgentId,
          proposal_type: expectedType,
          superseded_count: supersededRows.length,
        },
        outcome: 'success',
        reversibilityClass: 3,
        affectedResourceId: proposal.targetAgentId,
        affectedResourceType: 'agent_role',
      })
      .returning({ id: chatMutationAudit.id });

    return {
      agentId: proposal.targetAgentId,
      auditId: auditRow[0].id,
      supersededCount: supersededRows.length,
    };
  });
}

export async function rejectProposalAction(
  proposalId: string,
  reason: string,
): Promise<{ auditId: string }> {
  const { operatorId } = await assertOperator();
  if (reason.trim().length === 0) {
    throw new HiringActionError('reason is required to reject a proposal', 'reason_required');
  }
  return appDb.transaction(async (tx) => {
    const proposal = await readPendingProposal(tx, proposalId, undefined);

    await tx
      .update(hiringProposals)
      .set({ status: 'rejected', rejectedAt: sql`NOW()`, rejectedReason: reason })
      .where(and(eq(hiringProposals.id, proposalId), eq(hiringProposals.status, 'pending')));

    const verb = rejectVerbForProposalType(proposal.proposalType);
    const auditRow = await tx
      .insert(chatMutationAudit)
      .values({
        chatSessionId: null,
        chatMessageId: null,
        verb,
        argsJsonb: {
          proposal_id: proposalId,
          reason,
          operator_id: operatorId,
          proposal_type: proposal.proposalType,
          target_agent_id: proposal.targetAgentId ?? null,
        },
        outcome: 'success',
        reversibilityClass: 3,
        affectedResourceId: proposalId,
        affectedResourceType: 'hiring_proposal',
      })
      .returning({ id: chatMutationAudit.id });

    return { auditId: auditRow[0].id };
  });
}

export async function updateAgentMDAction(
  agentId: string,
  newMD: string,
): Promise<{ auditId: string }> {
  const { operatorId } = await assertOperator();
  return appDb.transaction(async (tx) => {
    const prior = await tx
      .select({ agentMd: agents.agentMd })
      .from(agents)
      .where(eq(agents.id, agentId))
      .limit(1);
    if (prior.length === 0) {
      throw new HiringActionError(`agent ${agentId} not found`, 'not_found');
    }
    await tx.update(agents).set({ agentMd: newMD }).where(eq(agents.id, agentId));

    const auditRow = await tx
      .insert(chatMutationAudit)
      .values({
        chatSessionId: null,
        chatMessageId: null,
        verb: 'update_agent_md',
        argsJsonb: {
          agent_id: agentId,
          operator_id: operatorId,
          prior_agent_md: prior[0].agentMd,
          new_agent_md: newMD,
        },
        outcome: 'success',
        reversibilityClass: 3,
        affectedResourceId: agentId,
        affectedResourceType: 'agent_role',
      })
      .returning({ id: chatMutationAudit.id });

    return { auditId: auditRow[0].id };
  });
}

function rejectVerbForProposalType(t: string): 'reject_hire' | 'reject_skill_change' | 'reject_version_bump' {
  if (t === 'skill_change') return 'reject_skill_change';
  if (t === 'version_bump') return 'reject_version_bump';
  return 'reject_hire';
}

type Tx = Parameters<Parameters<typeof appDb.transaction>[0]>[0];

async function readPendingProposal(
  tx: Tx,
  proposalId: string,
  expectedType: 'new_agent' | 'skill_change' | 'version_bump' | undefined,
) {
  const rows = await tx
    .select()
    .from(hiringProposals)
    .where(eq(hiringProposals.id, proposalId))
    .limit(1);
  if (rows.length === 0) {
    throw new HiringActionError(`proposal ${proposalId} not found`, 'not_found');
  }
  const row = rows[0];
  if (row.status !== 'pending') {
    throw new HiringActionError(
      `proposal is in status ${row.status}; only pending proposals can transition`,
      'not_pending',
    );
  }
  if (expectedType !== undefined && row.proposalType !== expectedType) {
    throw new HiringActionError(
      `proposal type ${row.proposalType} does not match approve verb (${expectedType})`,
      'wrong_type',
    );
  }
  return row;
}
