'use client';

// M5.4 — Recent palace writes tab. Read-only list of MemPalace
// drawer entries with refresh-on-demand. Greys the prior list at 60%
// opacity while a refresh is in-flight; on resolution, swaps to new
// data and full opacity. Errors render above the prior list.

import { useCallback, useEffect, useState } from 'react';
import {
  getRecentPalaceWrites,
  type DrawerEntry,
  type KnowsPaneError,
} from '@/lib/queries/knowsPane';

interface State {
  loaded: DrawerEntry[];
  isFetching: boolean;
  lastError: KnowsPaneError | null;
}

export function RecentPalaceWritesTab() {
  const [state, setState] = useState<State>({
    loaded: [],
    isFetching: true,
    lastError: null,
  });

  const fetchOnce = useCallback(async () => {
    setState((s) => ({ ...s, isFetching: true }));
    const result = await getRecentPalaceWrites();
    setState({
      loaded: result.error ? state.loaded : result.writes,
      isFetching: false,
      lastError: result.error,
    });
  }, [state.loaded]);

  useEffect(() => {
    fetchOnce().catch(() => {});
    // Run-on-mount only; subsequent runs come from the Refresh button.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return (
    <section
      className="flex flex-col h-full"
      data-testid="recent-palace-writes-tab"
    >
      <header className="flex items-center justify-between border-b border-border-1 px-4 py-2">
        <h3 className="text-text-2 text-[13px] font-medium">Recent palace writes</h3>
        <button
          type="button"
          className="text-[12px] px-2 py-1 rounded border border-border-1 hover:bg-surface-2 disabled:opacity-50"
          onClick={() => {
            fetchOnce().catch(() => {});
          }}
          disabled={state.isFetching}
          data-testid="palace-refresh"
        >
          {state.isFetching ? 'Refreshing…' : 'Refresh'}
        </button>
      </header>

      <div className="flex-1 overflow-auto p-4 space-y-2">
        {state.lastError ? (
          <div
            role="alert"
            data-testid="palace-error-block"
            data-error-kind={state.lastError}
            className="border border-err/40 bg-err/10 rounded px-3 py-2 text-[12px]"
          >
            {state.lastError === 'AuthExpired' ? (
              <>
                <p className="text-err font-medium">Your session expired</p>
                <p className="text-text-2">
                  Sign in again to load recent palace writes.
                </p>
              </>
            ) : state.lastError === 'NetworkError' ? (
              <>
                <p className="text-err font-medium">Network error</p>
                <p className="text-text-2">
                  Could not reach the supervisor. Try Refresh.
                </p>
              </>
            ) : (
              <>
                <p className="text-err font-medium">MemPalace unreachable</p>
                <p className="text-text-2">
                  The supervisor could not reach the palace sidecar. Try Refresh.
                </p>
              </>
            )}
          </div>
        ) : null}

        {state.loaded.length === 0 && !state.isFetching && !state.lastError ? (
          <p className="text-text-3 text-[13px]">
            No palace writes yet — agents will record their work here.
          </p>
        ) : (
          <ul
            data-greyed={state.isFetching ? 'true' : 'false'}
            className={state.isFetching ? 'opacity-60' : ''}
          >
            {state.loaded.map((d) => (
              <li
                key={d.id}
                className="border-b border-border-1 last:border-b-0 py-2 text-[12px]"
                data-testid="palace-write-row"
              >
                <p className="text-text-3">
                  <time dateTime={d.written_at}>{formatTime(d.written_at)}</time>
                  {' · '}
                  <span>{d.wing_name}</span>
                  {' / '}
                  <span>{d.room_name}</span>
                  {d.source_agent_role_slug ? (
                    <>
                      {' · '}
                      <span>{d.source_agent_role_slug}</span>
                    </>
                  ) : null}
                </p>
                <p className="text-text-1 mt-0.5">{d.body_preview}</p>
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
