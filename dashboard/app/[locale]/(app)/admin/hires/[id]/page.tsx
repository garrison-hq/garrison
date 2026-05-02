// M7 /admin/hires/[id] — single-proposal review + approve/reject.
// Renders the proposal's payload + diff (if any) + the immutable
// preamble preview banner per spec FR-307. Operator clicks approve or
// reject; the Server Action commits the data shape change in one tx
// and the page redirects back to the list.

import Link from 'next/link';
import { notFound } from 'next/navigation';
import { Chip } from '@/components/ui/Chip';
import { getProposalById } from '@/lib/queries/hiring';
import {
  approveHireAction,
  approveSkillChangeAction,
  approveVersionBumpAction,
  rejectProposalAction,
} from '@/lib/actions/hiring';

export const dynamic = 'force-dynamic';

function formatTimestamp(value: string | Date | null | undefined): string {
  if (!value) return '—';
  const d = typeof value === 'string' ? new Date(value) : value;
  return d.toISOString().slice(0, 19).replace('T', ' ');
}

function proposalTypeLabel(t: string): string {
  switch (t) {
    case 'new_agent':
      return 'New agent';
    case 'skill_change':
      return 'Skill change';
    case 'version_bump':
      return 'Version bump';
    default:
      return t;
  }
}

interface PageProps {
  params: Promise<{ locale: string; id: string }>;
}

export default async function AdminHireDetailPage({ params }: Readonly<PageProps>) {
  const { id } = await params;
  const proposal = await getProposalById(id);
  if (!proposal) {
    notFound();
  }

  async function approve() {
    'use server';
    if (proposal!.proposalType === 'skill_change') {
      await approveSkillChangeAction(id);
    } else if (proposal!.proposalType === 'version_bump') {
      await approveVersionBumpAction(id);
    } else {
      await approveHireAction(id);
    }
  }

  async function reject(formData: FormData) {
    'use server';
    const reason = String(formData.get('reason') ?? '').trim();
    if (reason.length === 0) {
      throw new Error('reason is required');
    }
    await rejectProposalAction(id, reason);
  }

  const isPending = proposal.status === 'pending';
  const skillDiffText = proposal.skillDiffJsonb
    ? JSON.stringify(proposal.skillDiffJsonb, null, 2)
    : null;
  const snapshotText = proposal.proposalSnapshotJsonb
    ? JSON.stringify(proposal.proposalSnapshotJsonb, null, 2)
    : null;

  return (
    <div className="px-6 py-5 space-y-5 max-w-[1100px] mx-auto">
      <header className="space-y-1">
        <p className="text-text-3 text-[12px] font-mono">
          <Link href="/admin/hires" className="hover:underline">
            ← Back to hires
          </Link>
        </p>
        <h1 className="text-text-1 text-2xl font-semibold tracking-tight">
          {proposalTypeLabel(proposal.proposalType)}: {proposal.roleTitle}
        </h1>
        <p className="text-text-3 text-sm font-mono">{proposal.id}</p>
      </header>

      <section className="rounded border border-border-1 bg-surface-1 p-4 space-y-2">
        <h2 className="text-text-2 text-[11px] uppercase tracking-[0.06em]">Status</h2>
        <Chip tone={isPending ? 'accent' : 'neutral'}>{proposal.status}</Chip>
        <dl className="grid grid-cols-2 gap-x-6 gap-y-2 text-[13px]">
          <dt className="text-text-3">Department</dt>
          <dd className="text-text-1 font-mono">{proposal.departmentSlug}</dd>
          <dt className="text-text-3">Origin</dt>
          <dd className="text-text-1 font-mono">{proposal.proposedVia}</dd>
          <dt className="text-text-3">Created</dt>
          <dd className="text-text-1 font-mono">{formatTimestamp(proposal.createdAt)}</dd>
          {proposal.approvedAt ? (
            <>
              <dt className="text-text-3">Approved</dt>
              <dd className="text-text-1 font-mono">{formatTimestamp(proposal.approvedAt)}</dd>
            </>
          ) : null}
          {proposal.rejectedAt ? (
            <>
              <dt className="text-text-3">Rejected</dt>
              <dd className="text-text-1 font-mono">{formatTimestamp(proposal.rejectedAt)}</dd>
            </>
          ) : null}
          {proposal.rejectedReason ? (
            <>
              <dt className="text-text-3">Reason</dt>
              <dd className="text-text-1">{proposal.rejectedReason}</dd>
            </>
          ) : null}
          {proposal.skillDigestAtPropose ? (
            <>
              <dt className="text-text-3">Digest at propose</dt>
              <dd className="text-text-1 font-mono text-[11px]">{proposal.skillDigestAtPropose}</dd>
            </>
          ) : null}
        </dl>
      </section>

      <section className="rounded border border-accent-1/30 bg-accent-1/5 p-4 space-y-2">
        <h2 className="text-text-2 text-[11px] uppercase tracking-[0.06em]">
          Immutable security preamble
        </h2>
        <p className="text-text-2 text-[13px]">
          Every spawn carries Garrison's immutable security preamble above the agent's
          <code className="font-mono"> agent.md</code>. The preamble cannot be overridden by
          ticket content, palace recall, or skill instructions. Approving this proposal
          activates the agent under that preamble.
        </p>
      </section>

      <section className="rounded border border-border-1 bg-surface-1 p-4 space-y-2">
        <h2 className="text-text-2 text-[11px] uppercase tracking-[0.06em]">Justification</h2>
        <pre className="whitespace-pre-wrap text-[13px] text-text-1">{proposal.justificationMd}</pre>
      </section>

      {skillDiffText ? (
        <section className="rounded border border-border-1 bg-surface-1 p-4 space-y-2">
          <h2 className="text-text-2 text-[11px] uppercase tracking-[0.06em]">Skill diff</h2>
          <pre className="overflow-auto text-[12px] font-mono text-text-1">{skillDiffText}</pre>
        </section>
      ) : null}

      {snapshotText ? (
        <section className="rounded border border-border-1 bg-surface-1 p-4 space-y-2">
          <h2 className="text-text-2 text-[11px] uppercase tracking-[0.06em]">Proposal snapshot</h2>
          <pre className="overflow-auto text-[12px] font-mono text-text-1">{snapshotText}</pre>
        </section>
      ) : null}

      {isPending ? (
        <section className="flex flex-col gap-4 rounded border border-border-1 bg-surface-1 p-4">
          <h2 className="text-text-2 text-[11px] uppercase tracking-[0.06em]">Decision</h2>
          <form action={approve}>
            <button
              type="submit"
              className="rounded bg-accent-1 px-4 py-2 text-[13px] font-medium text-surface-1 hover:opacity-90"
              data-testid="approve-proposal-button"
            >
              Approve
            </button>
          </form>
          <form action={reject} className="space-y-2">
            <label htmlFor="reject-reason" className="block text-text-3 text-[12px]">
              Reject with reason
            </label>
            <textarea
              id="reject-reason"
              name="reason"
              required
              className="w-full rounded border border-border-1 bg-surface-2 p-2 text-[13px] text-text-1"
              rows={3}
              placeholder="why this proposal is being rejected"
            />
            <button
              type="submit"
              className="rounded border border-border-1 px-4 py-2 text-[13px] font-medium text-text-1 hover:bg-surface-2"
              data-testid="reject-proposal-button"
            >
              Reject
            </button>
          </form>
        </section>
      ) : null}
    </div>
  );
}
