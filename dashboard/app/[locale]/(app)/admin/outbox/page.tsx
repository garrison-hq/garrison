// M11 /admin/outbox — operator Outbox approval queue surface.
//
// Force-dynamic Server Component; pull-only, no SSE (Q-C / FR-028).
// Follows the M7 hiring-queue pattern: re-fetches on navigation /
// Server-Action completion (router.refresh() from the client controls).
//
// Three sections:
//   1. Approve-tier pending actions — the operator's approval gate
//      (FR-025 / US1 #3). Each row has Approve + Reject buttons.
//   2. Human-only prepared actions — the agent has prepared the
//      payload; the operator performs it by hand and marks it done
//      (FR-027 / US5 #2).
//   3. Notify-tier post-hoc feed — executed notify-tier actions
//      surfaced after the fact (FR-028 / US4 #2). Read-only.
//
// Per AGENTS.md "Tests for Go only, never frontend": ships without
// vitest coverage; the Go-side T011 integration suite pins row shapes.

import { Chip } from '@/components/ui/Chip';
import { EmptyState } from '@/components/ui/EmptyState';
import {
  listPendingApproveActions,
  listHumanOnlyPreparedActions,
  listNotifyPostHocFeedItems,
  getAgentSummariesForActions,
  getTicketSummariesForActions,
  renderGitHubTarget,
  type PendingActionRow,
} from '@/lib/queries/outbox';
import { OutboxControls } from './OutboxControls';

export const dynamic = 'force-dynamic';

// ---------------------------------------------------------------------------
// Helper formatters
// ---------------------------------------------------------------------------

function formatTimestamp(value: string): string {
  const d = new Date(value);
  return d.toISOString().slice(0, 19).replace('T', ' ');
}

function tierTone(tier: string): 'accent' | 'warn' | 'info' | 'neutral' {
  switch (tier) {
    case 'approve':
      return 'accent';
    case 'notify':
      return 'info';
    case 'human_only':
      return 'warn';
    case 'auto':
    default:
      return 'neutral';
  }
}

function statusTone(status: string): 'accent' | 'ok' | 'warn' | 'err' | 'neutral' {
  switch (status) {
    case 'pending':
      return 'accent';
    case 'approved':
    case 'executed':
    case 'done':
      return 'ok';
    case 'rejected':
    case 'failed':
      return 'err';
    default:
      return 'neutral';
  }
}

function actionTypeLabel(actionType: string): string {
  if (actionType === 'github_issue_comment') return 'GitHub issue comment';
  return actionType;
}

function renderTarget(row: PendingActionRow): string {
  if (row.actionType === 'github_issue_comment') {
    return renderGitHubTarget(row.target);
  }
  try {
    return JSON.stringify(row.target);
  } catch {
    return String(row.target);
  }
}

// ---------------------------------------------------------------------------
// Page component
// ---------------------------------------------------------------------------

export default async function OutboxPage() {
  const [approveRows, humanOnlyRows, notifyRows] = await Promise.all([
    listPendingApproveActions(),
    listHumanOnlyPreparedActions(),
    listNotifyPostHocFeedItems(),
  ]);

  // Collect all agent instance IDs and ticket IDs for joined lookups.
  const allRows = [...approveRows, ...humanOnlyRows, ...notifyRows];
  const agentIds = [...new Set(allRows.map((r) => r.agentInstanceId))];
  const ticketIds = [
    ...new Set(allRows.map((r) => r.ticketId).filter((id): id is string => id !== null)),
  ];

  const [agentMap, ticketMap] = await Promise.all([
    getAgentSummariesForActions(agentIds),
    getTicketSummariesForActions(ticketIds),
  ]);

  function agentLabel(agentInstanceId: string): string {
    const summary = agentMap.get(agentInstanceId);
    if (summary?.roleSlug) return summary.roleSlug;
    return agentInstanceId.slice(0, 8) + '…';
  }

  function ticketLabel(ticketId: string | null): string | null {
    if (!ticketId) return null;
    const summary = ticketMap.get(ticketId);
    if (!summary) return ticketId.slice(0, 8) + '…';
    const obj = summary.objective.length > 40
      ? summary.objective.slice(0, 40) + '…'
      : summary.objective;
    return obj;
  }

  const totalPending = approveRows.length + humanOnlyRows.length;

  return (
    <div className="px-6 py-5 space-y-8 max-w-[1400px] mx-auto w-full">
      <header className="space-y-1">
        <div className="flex items-center gap-3">
          <h1 className="text-text-1 text-2xl font-semibold tracking-tight">Outbox</h1>
          {totalPending > 0 ? (
            <span className="text-[11px] font-mono px-2 py-0.5 rounded bg-accent/10 text-accent border border-accent/30">
              {totalPending} pending
            </span>
          ) : null}
        </div>
        <p className="text-text-3 text-sm">
          Operator approval queue for agent-requested external actions. Approve-tier actions require
          your approval before dispatch. Human-only actions are prepared by agents for you to
          perform by hand.
        </p>
      </header>

      {/* ─── Section 1: Approve-tier pending actions ─── */}
      <section className="space-y-3">
        <h2 className="text-text-2 text-[11px] uppercase tracking-[0.06em]">
          Awaiting approval
          {approveRows.length > 0 ? (
            <span className="ml-2 font-mono text-accent">{approveRows.length}</span>
          ) : null}
        </h2>

        {approveRows.length === 0 ? (
          <EmptyState
            description="No actions awaiting approval"
            caption="When an agent calls request_external_action for an approve-tier action type, it appears here pending your decision."
          />
        ) : (
          <div className="space-y-4">
            {approveRows.map((row) => (
              <ActionCard
                key={row.id}
                row={row}
                mode="approve"
                agentLabel={agentLabel(row.agentInstanceId)}
                ticketLabel={ticketLabel(row.ticketId)}
              />
            ))}
          </div>
        )}
      </section>

      {/* ─── Section 2: Human-only prepared actions ─── */}
      <section className="space-y-3">
        <h2 className="text-text-2 text-[11px] uppercase tracking-[0.06em]">
          Human-only — perform by hand
          {humanOnlyRows.length > 0 ? (
            <span className="ml-2 font-mono text-warn">{humanOnlyRows.length}</span>
          ) : null}
        </h2>

        {humanOnlyRows.length === 0 ? (
          <EmptyState
            description="No human-only actions prepared"
            caption="When an agent prepares a human_only-tier action, it appears here for you to perform manually and mark as done."
          />
        ) : (
          <div className="space-y-4">
            {humanOnlyRows.map((row) => (
              <ActionCard
                key={row.id}
                row={row}
                mode="human_only"
                agentLabel={agentLabel(row.agentInstanceId)}
                ticketLabel={ticketLabel(row.ticketId)}
              />
            ))}
          </div>
        )}
      </section>

      {/* ─── Section 3: Notify-tier post-hoc feed ─── */}
      {notifyRows.length > 0 ? (
        <section className="space-y-3">
          <h2 className="text-text-2 text-[11px] uppercase tracking-[0.06em]">
            Notify — executed (post-hoc feed)
          </h2>
          <table
            className="w-full text-sm border border-border-1 rounded"
            data-testid="outbox-notify-table"
          >
            <thead className="bg-surface-2 text-text-3 text-[11px] uppercase tracking-[0.06em]">
              <tr>
                <th className="text-left px-3 py-2">Action type</th>
                <th className="text-left px-3 py-2">Target</th>
                <th className="text-left px-3 py-2">Agent</th>
                <th className="text-left px-3 py-2">Tier / reason</th>
                <th className="text-left px-3 py-2">Status</th>
                <th className="text-left px-3 py-2">Dispatched</th>
              </tr>
            </thead>
            <tbody>
              {notifyRows.map((row) => (
                <tr key={row.id} className="border-t border-border-1">
                  <td className="px-3 py-2 text-text-2">{actionTypeLabel(row.actionType)}</td>
                  <td className="px-3 py-2 font-mono text-text-1 text-[12px]">
                    {renderTarget(row)}
                  </td>
                  <td className="px-3 py-2 text-text-3 font-mono text-[12px]">
                    {agentLabel(row.agentInstanceId)}
                  </td>
                  <td className="px-3 py-2">
                    <Chip tone={tierTone(row.tier)}>{row.tier}</Chip>
                    <span className="ml-2 text-text-3 text-[11px]">{row.tierReason}</span>
                  </td>
                  <td className="px-3 py-2">
                    <Chip tone={statusTone(row.status)}>{row.status}</Chip>
                  </td>
                  <td className="px-3 py-2 text-text-3 font-mono text-[12px]">
                    {row.dispatchedAt ? formatTimestamp(row.dispatchedAt) : '—'}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </section>
      ) : null}
    </div>
  );
}

// ---------------------------------------------------------------------------
// ActionCard — one pending action. mode='approve' renders the approve +
// reject controls (FR-025/US1#3); mode='human_only' renders the
// mark-as-done form over the agent's prepared payload (FR-027/US5#2).
// ---------------------------------------------------------------------------

function ActionCard({
  row,
  mode,
  agentLabel,
  ticketLabel,
}: Readonly<{
  row: PendingActionRow;
  mode: 'approve' | 'human_only';
  agentLabel: string;
  ticketLabel: string | null;
}>) {
  const isApprove = mode === 'approve';
  return (
    <div
      className="border border-border-1 rounded bg-surface-1 p-4 space-y-3"
      data-testid={isApprove ? 'outbox-approve-card' : 'outbox-human-only-card'}
    >
      {/* Header row */}
      <div className="flex items-start justify-between gap-4">
        <div className="space-y-1">
          <div className="flex items-center gap-2 flex-wrap">
            <span className="text-text-1 text-sm font-medium">
              {actionTypeLabel(row.actionType)}
            </span>
            <Chip tone={tierTone(row.tier)}>{row.tier}</Chip>
            <Chip tone={statusTone(row.status)}>{row.status}</Chip>
          </div>
          <p className="text-text-3 text-[11px] font-mono">{row.tierReason}</p>
        </div>
        <span className="text-text-3 text-[11px] font-mono shrink-0">
          {formatTimestamp(row.createdAt)}
        </span>
      </div>

      {/* Target */}
      <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-[13px]">
        <dt className="text-text-3">Target</dt>
        <dd className="text-text-1 font-mono text-[12px]">{renderTarget(row)}</dd>

        <dt className="text-text-3">Agent</dt>
        <dd className="text-text-3 font-mono text-[12px]">{agentLabel}</dd>

        {ticketLabel ? (
          <>
            <dt className="text-text-3">Ticket</dt>
            <dd className="text-text-3 text-[12px]">{ticketLabel}</dd>
          </>
        ) : null}
      </dl>

      {/* Rendered payload (for human_only: the agent's prepared action text) */}
      <section className="space-y-1">
        <h3 className="text-text-3 text-[11px] uppercase tracking-[0.06em]">
          {isApprove ? 'Payload' : 'Prepared payload'}
        </h3>
        <pre
          className="whitespace-pre-wrap text-[13px] text-text-1 bg-surface-2 rounded p-3 max-h-48 overflow-auto"
          data-testid={isApprove ? 'outbox-approve-payload' : 'outbox-human-only-payload'}
        >
          {row.renderedPayload}
        </pre>
      </section>

      {/* Tier-appropriate controls */}
      <OutboxControls
        id={row.id}
        mode={mode}
      />
    </div>
  );
}
