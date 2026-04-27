'use client';

import { useState, useTransition } from 'react';
import { useRouter } from 'next/navigation';
import {
  editSecret,
  type SecretProvenance,
  type RotationProvider,
  type SecretSnapshot,
} from '@/lib/actions/vault';
import { ConflictResolutionModal } from '@/components/ui/ConflictResolutionModal';
import { DiffView } from '@/components/ui/DiffView';
import { VaultError } from '@/lib/vault/errors';

// SecretEditForm — operator-facing edit surface for editSecret per
// FR-056 / FR-057 / FR-084 / FR-094.
//
// Optimistic locking via secret_metadata.updated_at: server action
// returns { accepted: false, conflict: true, serverState } on
// stale versionToken; we open ConflictResolutionModal so the
// operator picks overwrite / merge-manually / discard.
//
// Rule 6 / Rule 1: when editing the value, redact both sides of
// the diff via DiffView's redactValues prop — confirmation shows
// "value will change" without revealing either side. Other field
// diffs render normally.

const PROVENANCE_VALUES: ReadonlyArray<{ value: SecretProvenance; label: string }> = [
  { value: 'operator_entered', label: 'operator-entered' },
  { value: 'oauth_flow', label: 'oauth-flow' },
  { value: 'environment_bootstrap', label: 'environment-bootstrap' },
  { value: 'customer_delegated', label: 'customer-delegated' },
];

const ROTATION_VALUES: ReadonlyArray<{ value: RotationProvider; label: string }> = [
  { value: 'manual_paste', label: 'manual-paste' },
  { value: 'infisical_native', label: 'infisical-native' },
  { value: 'not_rotatable', label: 'not-rotatable' },
];

function cadenceDaysFromInterval(interval: string): number {
  const match = /^(\d+)/.exec(interval);
  return match ? Number.parseInt(match[1], 10) : 90;
}

export interface SecretEditFormProps {
  initial: {
    secretPath: string;
    provenance: SecretProvenance;
    rotationCadence: string;
    rotationProvider: RotationProvider;
    updatedAt: string;
  };
}

export function SecretEditForm({ initial }: Readonly<SecretEditFormProps>) {
  const router = useRouter();
  const [provenance, setProvenance] = useState<SecretProvenance>(initial.provenance);
  const [rotationCadenceDays, setRotationCadenceDays] = useState<string>(
    String(cadenceDaysFromInterval(initial.rotationCadence)),
  );
  const [rotationProvider, setRotationProvider] = useState<RotationProvider>(
    initial.rotationProvider,
  );
  const [newValue, setNewValue] = useState('');
  const [versionToken, setVersionToken] = useState(initial.updatedAt);
  const [error, setError] = useState<string | null>(null);
  const [conflict, setConflict] = useState<SecretSnapshot | null>(null);
  const [pending, startTransition] = useTransition();

  function buildChanges() {
    const out: {
      provenance?: SecretProvenance;
      rotationCadenceDays?: number;
      rotationProvider?: RotationProvider;
      value?: string;
    } = {};
    if (provenance !== initial.provenance) out.provenance = provenance;
    const cadence = Number.parseInt(rotationCadenceDays, 10);
    if (Number.isFinite(cadence) && cadence !== cadenceDaysFromInterval(initial.rotationCadence)) {
      out.rotationCadenceDays = cadence;
    }
    if (rotationProvider !== initial.rotationProvider) out.rotationProvider = rotationProvider;
    if (newValue.length > 0) out.value = newValue;
    return out;
  }

  async function handleSave(versionTokenOverride?: string) {
    setError(null);
    const changes = buildChanges();
    if (Object.keys(changes).length === 0) {
      setError('No changes to save.');
      return;
    }
    startTransition(async () => {
      try {
        const result = await editSecret({
          secretPath: initial.secretPath,
          versionToken: versionTokenOverride ?? versionToken,
          changes,
        });
        if (result.accepted) {
          // Hard navigation. router.push + router.refresh inside
          // a startTransition can leave pending=true indefinitely
          // when the destination triggers a server-side re-fetch
          // (the /vault list page); the Save button stays
          // disabled and the URL doesn't change even though the
          // server action returned 200. window.location.assign
          // sidesteps that race entirely.
          window.location.assign('/vault');
        } else {
          setConflict(result.serverState);
        }
      } catch (err) {
        if (err instanceof VaultError) {
          const reason = (err.detail?.reason as string | undefined) ?? err.kind;
          setError(`Edit rejected: ${reason}`);
        } else {
          setError(err instanceof Error ? err.message : 'unknown error');
        }
      }
    });
  }

  function handleOverwrite() {
    if (!conflict) return;
    setConflict(null);
    void handleSave(conflict.updatedAt);
  }

  function handleMergeManually() {
    if (!conflict) return;
    setProvenance(conflict.provenance as SecretProvenance);
    setRotationProvider(conflict.rotationProvider as RotationProvider);
    setVersionToken(conflict.updatedAt);
    setConflict(null);
  }

  function handleDiscard() {
    setConflict(null);
    router.push('/vault');
  }

  // Construct a diff for the conflict modal. Value is redacted
  // (Rule 6); other fields render normally.
  const conflictDiff = conflict ? (
    <DiffView
      diff={{
        provenance: { before: conflict.provenance, after: provenance },
        rotation_provider: {
          before: conflict.rotationProvider,
          after: rotationProvider,
        },
        ...(newValue.length > 0
          ? { value: { before: '(unchanged)', after: '(new operator-supplied value)' } }
          : {}),
      }}
      redactFields={new Set(['value'])}
    />
  ) : null;

  return (
    <div className="space-y-5">
      <div className="text-[11px] uppercase tracking-wide text-text-3 font-mono">
        {initial.secretPath}
      </div>

      <FieldBlock label="provenance">
        <div className="flex gap-2 flex-wrap">
          {PROVENANCE_VALUES.map((p) => (
            <button
              key={p.value}
              type="button"
              onClick={() => setProvenance(p.value)}
              className={`px-3 py-1.5 text-[13px] rounded border ${
                provenance === p.value
                  ? 'bg-accent/10 border-accent/40 text-accent'
                  : 'bg-surface-2 border-border-1 text-text-2 hover:text-text-1'
              }`}
            >
              {p.label}
            </button>
          ))}
        </div>
      </FieldBlock>

      <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
        <FieldBlock label="rotation cadence (days)">
          <input
            type="number"
            min={1}
            max={3650}
            value={rotationCadenceDays}
            onChange={(e) => setRotationCadenceDays(e.target.value)}
            className="w-full bg-surface-2 border border-border-1 rounded px-3 py-2 text-[13px] text-text-1 font-mono focus:outline-none focus:border-accent"
          />
        </FieldBlock>
        <FieldBlock label="rotation provider">
          <select
            value={rotationProvider}
            onChange={(e) => setRotationProvider(e.target.value as RotationProvider)}
            className="w-full bg-surface-2 border border-border-1 rounded px-3 py-2 text-[13px] text-text-1 focus:outline-none focus:border-accent"
          >
            {ROTATION_VALUES.map((r) => (
              <option key={r.value} value={r.value}>
                {r.label}
              </option>
            ))}
          </select>
        </FieldBlock>
      </div>

      <FieldBlock
        label="new value (optional)"
        hint="Leave blank to keep the existing value. The new value is sent to Infisical only."
      >
        <textarea
          value={newValue}
          onChange={(e) => setNewValue(e.target.value)}
          rows={4}
          autoComplete="off"
          spellCheck={false}
          className="w-full font-mono bg-surface-2 border border-border-1 rounded p-3 text-[12.5px] text-text-1 focus:outline-none focus:border-accent"
        />
      </FieldBlock>

      {error && (
        <div className="rounded border border-err/40 bg-err/5 px-4 py-2.5 text-[12.5px] text-err">
          {error}
        </div>
      )}

      <div className="flex justify-end gap-2">
        <button
          type="button"
          onClick={() => router.push('/vault')}
          className="px-3 py-1.5 text-[13px] text-text-2 hover:text-text-1 border border-border-1 rounded bg-surface-2 hover:bg-surface-3"
        >
          Cancel
        </button>
        <button
          type="button"
          disabled={pending}
          onClick={() => void handleSave()}
          className="px-3 py-1.5 text-[13px] rounded bg-accent text-white hover:bg-accent/90 disabled:opacity-50 disabled:cursor-not-allowed"
        >
          {pending ? 'Saving…' : 'Save changes'}
        </button>
      </div>

      <ConflictResolutionModal
        open={conflict !== null}
        title="Another operator changed this secret since you opened the form."
        diff={conflictDiff}
        onOverwrite={handleOverwrite}
        onMergeManually={handleMergeManually}
        onDiscard={handleDiscard}
      />
    </div>
  );
}

function FieldBlock({
  label,
  hint,
  children,
}: Readonly<{ label: string; hint?: string; children: React.ReactNode }>) {
  return (
    <div className="space-y-1.5">
      <label className="block text-[10.5px] uppercase tracking-wide text-text-3 font-mono">
        {label}
      </label>
      {children}
      {hint && <p className="text-[11px] text-text-3">{hint}</p>}
    </div>
  );
}
