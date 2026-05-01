'use client';

// M5.4 Company.md tab. Owns the read/edit state machine, the
// Save/Cancel affordance, and the inline error blocks for every
// supervisor-side typed error. The CodeMirror leaf lives in
// CompanyMDEditor.tsx; this component wires Server Actions
// (lib/actions/companyMD.ts) to the editor.

import { useCallback, useEffect, useState } from 'react';
import Link from 'next/link';
import {
  getCompanyMD,
  saveCompanyMD,
  type CompanyMDError,
} from '@/lib/actions/companyMD';
import { CompanyMDEditor } from './CompanyMDEditor';

type Mode = 'view' | 'edit';

interface CompanyMDError_State {
  error: CompanyMDError;
  message?: string;
  patternCategory?: string;
}

const MAX_BYTES = 64 * 1024;

export function CompanyMDTab() {
  const [mode, setMode] = useState<Mode>('view');
  const [loaded, setLoaded] = useState<string>('');
  const [etag, setEtag] = useState<string | null>(null);
  const [buffer, setBuffer] = useState<string>('');
  const [loadError, setLoadError] = useState<CompanyMDError_State | null>(null);
  const [saveError, setSaveError] = useState<CompanyMDError_State | null>(null);
  const [isSaving, setIsSaving] = useState(false);
  const [savedHint, setSavedHint] = useState(false);
  const [confirmingCancel, setConfirmingCancel] = useState(false);

  const reload = useCallback(async () => {
    const result = await getCompanyMD();
    if (result.error) {
      setLoadError({ error: result.error, message: result.message });
      setLoaded('');
      setEtag(null);
    } else {
      setLoadError(null);
      setLoaded(result.content);
      setEtag(result.etag);
    }
  }, []);

  useEffect(() => {
    void reload();
  }, [reload]);

  function startEdit() {
    setBuffer(loaded);
    setSaveError(null);
    setMode('edit');
  }

  async function commitSave() {
    setIsSaving(true);
    setSaveError(null);
    const result = await saveCompanyMD(buffer, etag);
    setIsSaving(false);
    if (result.error) {
      setSaveError({
        error: result.error,
        message: result.message,
        patternCategory: result.patternCategory,
      });
      return;
    }
    setLoaded(result.content);
    setEtag(result.etag);
    setMode('view');
    setSavedHint(true);
    setTimeout(() => setSavedHint(false), 3000);
  }

  function tryCancel() {
    if (buffer === loaded) {
      setMode('view');
      setSaveError(null);
      return;
    }
    setConfirmingCancel(true);
  }

  function discardChanges() {
    setBuffer(loaded);
    setMode('view');
    setSaveError(null);
    setConfirmingCancel(false);
  }

  async function refreshAndDiscard() {
    await reload();
    setMode('view');
    setBuffer('');
    setSaveError(null);
  }

  const isEmpty = loaded === '' && etag === null && !loadError;

  return (
    <section
      className="flex flex-col h-full"
      data-testid="company-md-tab"
      data-mode={mode}
    >
      <header className="flex items-center justify-between border-b border-border-1 px-4 py-2">
        <h3 className="text-text-2 text-[13px] font-medium">Company.md</h3>
        {mode === 'view' ? (
          <button
            type="button"
            className="text-[12px] px-2 py-1 rounded border border-border-1 hover:bg-surface-2"
            onClick={startEdit}
          >
            Edit
          </button>
        ) : (
          <div className="flex gap-2">
            <button
              type="button"
              className="text-[12px] px-2 py-1 rounded border border-border-1 hover:bg-surface-2"
              onClick={tryCancel}
              disabled={isSaving}
            >
              Cancel
            </button>
            <button
              type="button"
              className="text-[12px] px-2 py-1 rounded bg-info text-white hover:bg-info/90 disabled:opacity-50"
              onClick={() => void commitSave()}
              disabled={isSaving}
            >
              {isSaving ? 'Saving…' : 'Save'}
            </button>
          </div>
        )}
      </header>

      <div className="flex-1 overflow-auto p-4 space-y-3">
        {loadError ? (
          <ErrorBlock state={loadError} kind="load" />
        ) : null}

        {savedHint ? (
          <p className="text-[12px] text-success" role="status">
            Saved.
          </p>
        ) : null}

        {saveError ? (
          <ErrorBlock
            state={saveError}
            kind="save"
            onRefreshAndDiscard={() => void refreshAndDiscard()}
          />
        ) : null}

        {isEmpty && mode === 'view' ? (
          <p className="text-text-3 text-[13px]">
            No Company.md yet — click Edit to create one.
          </p>
        ) : (
          <CompanyMDEditor
            value={mode === 'view' ? loaded : buffer}
            onChange={setBuffer}
            readOnly={mode === 'view'}
          />
        )}

        {confirmingCancel ? (
          <div
            role="dialog"
            aria-label="Discard unsaved changes?"
            className="border border-border-1 rounded p-3 bg-surface-2"
          >
            <p className="text-[13px] text-text-1">Discard your unsaved changes?</p>
            <div className="mt-2 flex gap-2">
              <button
                type="button"
                className="text-[12px] px-2 py-1 rounded border border-border-1 hover:bg-surface-3"
                onClick={() => setConfirmingCancel(false)}
              >
                Keep editing
              </button>
              <button
                type="button"
                className="text-[12px] px-2 py-1 rounded bg-err text-white hover:bg-err/90"
                onClick={discardChanges}
              >
                Discard
              </button>
            </div>
          </div>
        ) : null}
      </div>
    </section>
  );
}

interface ErrorBlockProps {
  state: CompanyMDError_State;
  kind: 'load' | 'save';
  onRefreshAndDiscard?: () => void;
}

function ErrorBlock({ state, kind, onRefreshAndDiscard }: ErrorBlockProps) {
  const headline = headlineForError(state.error);
  const body = bodyForError(state, kind);
  return (
    <div
      role="alert"
      data-testid={`company-md-error-${state.error.toLowerCase()}`}
      data-error-kind={state.error}
      className="border border-err/40 bg-err/10 rounded px-3 py-2 text-[12px] space-y-1"
    >
      <p className="text-err font-medium">{headline}</p>
      <p className="text-text-2">{body}</p>
      {state.error === 'Stale' && onRefreshAndDiscard ? (
        <button
          type="button"
          className="text-info hover:underline mt-1"
          onClick={onRefreshAndDiscard}
        >
          Refresh and discard my changes
        </button>
      ) : null}
      {state.error === 'AuthExpired' ? (
        <Link href="/login?next=/chat" className="text-info hover:underline">
          Sign in again
        </Link>
      ) : null}
    </div>
  );
}

function headlineForError(error: CompanyMDError): string {
  switch (error) {
    case 'Stale':
      return 'Document changed elsewhere';
    case 'LeakScanFailed':
      return 'Save rejected: secret-shape match';
    case 'TooLarge':
      return 'Document too large';
    case 'AuthExpired':
      return 'Your session expired';
    case 'MinIOUnreachable':
      return 'Storage unreachable';
    case 'MinIOAuthFailed':
      return 'Storage authentication failed';
    case 'NetworkError':
      return 'Network error';
    case 'InternalError':
      return 'Something went wrong';
  }
}

function bodyForError(state: CompanyMDError_State, kind: 'load' | 'save'): string {
  const verb = kind === 'load' ? 'load' : 'save';
  switch (state.error) {
    case 'Stale':
      return 'This document was changed elsewhere; refresh to load the latest version.';
    case 'LeakScanFailed':
      return state.patternCategory
        ? `A value matching ${state.patternCategory} was detected. Remove it before saving.`
        : 'A secret-shape match was detected. Remove it before saving.';
    case 'TooLarge':
      return `Company.md is capped at ${Math.round(MAX_BYTES / 1024)} KB.`;
    case 'AuthExpired':
      return 'Your session expired — sign in again to save your changes.';
    case 'MinIOUnreachable':
      return `Could not reach object storage to ${verb} Company.md. Try again in a moment.`;
    case 'MinIOAuthFailed':
      return 'Could not authenticate against object storage. Operator action may be required.';
    case 'NetworkError':
      return 'Could not reach the supervisor. Check your connection and try again.';
    case 'InternalError':
      return state.message || 'An unexpected error occurred.';
  }
}
