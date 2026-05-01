'use client';

// M5.4 — KG recent facts tab. Same shape as RecentPalaceWritesTab but
// renders triples (subject — predicate — object) with optional
// source-ticket deep-links.

import { useCallback, useEffect, useState } from 'react';
import Link from 'next/link';
import {
  getRecentKGFacts,
  type KGTriple,
  type KnowsPaneError,
} from '@/lib/queries/knowsPane';

interface State {
  loaded: KGTriple[];
  isFetching: boolean;
  lastError: KnowsPaneError | null;
}

export function KGRecentFactsTab() {
  const [state, setState] = useState<State>({
    loaded: [],
    isFetching: true,
    lastError: null,
  });

  const fetchOnce = useCallback(async () => {
    setState((s) => ({ ...s, isFetching: true }));
    const result = await getRecentKGFacts();
    setState({
      loaded: result.error ? state.loaded : result.facts,
      isFetching: false,
      lastError: result.error,
    });
  }, [state.loaded]);

  useEffect(() => {
    fetchOnce().catch(() => {});
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return (
    <section className="flex flex-col h-full" data-testid="kg-recent-facts-tab">
      <header className="flex items-center justify-between border-b border-border-1 px-4 py-2">
        <h3 className="text-text-2 text-[13px] font-medium">KG recent facts</h3>
        <button
          type="button"
          className="text-[12px] px-2 py-1 rounded border border-border-1 hover:bg-surface-2 disabled:opacity-50"
          onClick={() => {
            fetchOnce().catch(() => {});
          }}
          disabled={state.isFetching}
          data-testid="kg-refresh"
        >
          {state.isFetching ? 'Refreshing…' : 'Refresh'}
        </button>
      </header>

      <div className="flex-1 overflow-auto p-4 space-y-2">
        {state.lastError ? (
          <div
            role="alert"
            data-testid="kg-error-block"
            data-error-kind={state.lastError}
            className="border border-err/40 bg-err/10 rounded px-3 py-2 text-[12px]"
          >
            <p className="text-err font-medium">MemPalace unreachable</p>
            <p className="text-text-2">
              The supervisor could not reach the palace sidecar. Try Refresh.
            </p>
          </div>
        ) : null}

        {state.loaded.length === 0 && !state.isFetching && !state.lastError ? (
          <p className="text-text-3 text-[13px]">
            No KG facts yet — agents will populate the knowledge graph as they ship work.
          </p>
        ) : (
          <ul
            data-greyed={state.isFetching ? 'true' : 'false'}
            className={state.isFetching ? 'opacity-60' : ''}
          >
            {state.loaded.map((t) => (
              <li
                key={t.id}
                className="border-b border-border-1 last:border-b-0 py-2 text-[12px]"
                data-testid="kg-fact-row"
              >
                <p className="text-text-1">
                  <span>{t.subject}</span>
                  <span className="text-text-3"> — </span>
                  <span>{t.predicate}</span>
                  <span className="text-text-3"> — </span>
                  <span>{t.object}</span>
                </p>
                <p className="text-text-3 mt-0.5">
                  <time dateTime={t.written_at}>{formatTime(t.written_at)}</time>
                  {t.source_ticket_id ? (
                    <>
                      {' · '}
                      <Link
                        href={`/tickets/${t.source_ticket_id}`}
                        className="text-info hover:underline"
                      >
                        ticket
                      </Link>
                    </>
                  ) : null}
                </p>
              </li>
            ))}
          </ul>
        )}
      </div>
    </section>
  );
}

function formatTime(iso: string): string {
  try {
    return new Date(iso).toLocaleString();
  } catch {
    return iso;
  }
}
