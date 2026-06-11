import Link from 'next/link';
import { Chip } from '@/components/ui/Chip';
import { EmptyState } from '@/components/ui/EmptyState';
import { relativeTime, formatIsoFull } from '@/lib/format/relativeTime';
import type { ScheduledTaskRunRow, ScheduledRunOutcome } from '@/lib/queries/scheduledTasks';

// M9 — run history for one scheduled task (T015, plan §8). Server
// component: getTaskRunHistory feeds it newest-first run rows joined
// to agent_instances.status, so oneshot rows can show terminal
// instance state + the structured-outcome summary committed by
// WriteFinalizeOneshot.
//
// Outcome vocabulary is the scheduled_task_runs CHECK
// (fired | skipped_overlap | gate_deferred | failed); chips render
// the operator-facing labels from the task contract.
//
// Per AGENTS.md "Tests for Go only, never frontend": no vitest; the
// Go-side T016/T017 integration suites pin the row shapes read here.

const outcomeLabel: Record<ScheduledRunOutcome, string> = {
  fired: 'fired',
  skipped_overlap: 'skipped overlap',
  gate_deferred: 'gate deferred',
  failed: 'failed',
};

const outcomeTone: Record<ScheduledRunOutcome, 'ok' | 'neutral' | 'warn' | 'err'> = {
  fired: 'ok',
  skipped_overlap: 'neutral',
  gate_deferred: 'warn',
  failed: 'err',
};

export function OutcomeChip({ outcome }: Readonly<{ outcome: ScheduledRunOutcome }>) {
  return <Chip tone={outcomeTone[outcome]}>{outcomeLabel[outcome]}</Chip>;
}

// structured_outcome is the finalize_oneshot payload + verification
// sub-object (supervisor-written JSONB). Read defensively — the
// column is NULL until the oneshot commits, and the dashboard never
// trusts JSONB shape beyond what it renders.
interface StructuredSummary {
  outcome: string | null;
  thinDiary: boolean;
  missingKGFacts: boolean;
}

function summarizeStructuredOutcome(structured: unknown): StructuredSummary | null {
  if (!structured || typeof structured !== 'object') return null;
  const obj = structured as { outcome?: unknown; verification?: unknown };
  const outcome = typeof obj.outcome === 'string' ? obj.outcome : null;
  const verification =
    obj.verification && typeof obj.verification === 'object'
      ? (obj.verification as { thin_diary?: unknown; missing_kg_facts?: unknown })
      : null;
  return {
    outcome,
    thinDiary: verification?.thin_diary === true,
    missingKGFacts: verification?.missing_kg_facts === true,
  };
}

function RunTimestamp({ value }: Readonly<{ value: string }>) {
  return (
    <span
      className="font-mono font-tabular text-text-2 text-xs"
      title={formatIsoFull(value)}
      suppressHydrationWarning
    >
      {relativeTime(value)}
    </span>
  );
}

export function RunHistoryTable({ runs }: Readonly<{ runs: ScheduledTaskRunRow[] }>) {
  if (runs.length === 0) {
    return (
      <EmptyState
        description="No runs yet"
        caption="The supervisor's tick loop records a run for every claimed slot — fired, skipped, deferred, or failed."
      />
    );
  }

  return (
    <table
      className="w-full text-sm border border-border-1 rounded"
      data-testid="run-history-table"
    >
      <thead className="bg-surface-2 text-text-3 text-[11px] uppercase tracking-[0.06em]">
        <tr>
          <th className="text-left px-3 py-2">Slot</th>
          <th className="text-left px-3 py-2">Fired</th>
          <th className="text-left px-3 py-2">Outcome</th>
          <th className="text-left px-3 py-2">Result</th>
        </tr>
      </thead>
      <tbody>
        {runs.map((run) => {
          const summary = summarizeStructuredOutcome(run.structuredOutcome);
          return (
            <tr key={run.id} className="border-t border-border-1 align-top">
              <td className="px-3 py-2 whitespace-nowrap">
                <span className="font-mono font-tabular text-text-2 text-xs">
                  {formatIsoFull(run.slotAt)}
                </span>
              </td>
              <td className="px-3 py-2 whitespace-nowrap">
                <RunTimestamp value={run.firedAt} />
              </td>
              <td className="px-3 py-2 whitespace-nowrap">
                <OutcomeChip outcome={run.outcome} />
              </td>
              <td className="px-3 py-2">
                <div className="space-y-1">
                  {run.ticketId ? (
                    <div>
                      <Link
                        href={`/tickets/${run.ticketId}`}
                        className="font-mono text-xs text-text-2 hover:text-text-1 hover:underline"
                      >
                        ticket {run.ticketId.slice(0, 8)}
                      </Link>
                    </div>
                  ) : null}
                  {run.agentInstanceId ? (
                    <div className="flex items-center gap-2">
                      <span className="font-mono text-xs text-text-3">
                        instance {run.agentInstanceId.slice(0, 8)}
                      </span>
                      {run.instanceStatus ? (
                        <Chip tone={run.instanceStatus === 'running' ? 'info' : 'neutral'}>
                          {run.instanceStatus}
                          {run.instanceExitReason ? `: ${run.instanceExitReason}` : ''}
                        </Chip>
                      ) : null}
                    </div>
                  ) : null}
                  {summary?.outcome ? (
                    <p className="text-text-2 text-xs leading-relaxed line-clamp-3">
                      {summary.outcome}
                    </p>
                  ) : null}
                  {summary?.thinDiary ? <Chip tone="warn">thin diary</Chip> : null}
                  {summary?.missingKGFacts ? <Chip tone="warn">missing KG facts</Chip> : null}
                  {run.detail ? <p className="text-text-3 text-xs">{run.detail}</p> : null}
                  {!run.ticketId && !run.agentInstanceId && !summary && !run.detail ? (
                    <span className="text-text-4 text-xs">—</span>
                  ) : null}
                </div>
              </td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}
