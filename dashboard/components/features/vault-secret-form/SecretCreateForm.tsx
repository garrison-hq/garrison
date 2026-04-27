'use client';

import { useState, useTransition } from 'react';
import { useRouter } from 'next/navigation';
import {
  createSecret,
  type SecretProvenance,
  type RotationProvider,
} from '@/lib/actions/vault';
import { VaultError } from '@/lib/vault/errors';

// SecretCreateForm — operator-facing surface for the createSecret
// server action per FR-052..FR-055 / FR-072 / FR-053 (Rule 4 path
// validation).
//
// Path PREFIX is the part everything except the trailing key name.
// The form composes the full Infisical path as
// `{pathPrefix}/{name}`. Rule 4 requires the prefix start with
// `/<customer_id>/<provenance>` — the operator typically pastes
// the customer-id-prefixed path; the form pre-fills a starter
// template using the operating entity's customer id (resolved
// server-side at page load).

export interface SecretCreateFormProps {
  /** Operating entity's customer_id, resolved server-side and
   *  used to prefill the path-prefix template. */
  customerId: string;
}

const PROVENANCE_VALUES: ReadonlyArray<{ value: SecretProvenance; label: string }> = [
  { value: 'operator_entered', label: 'operator-entered' },
  { value: 'oauth_flow', label: 'oauth-flow' },
  { value: 'environment_bootstrap', label: 'environment-bootstrap' },
  { value: 'customer_delegated', label: 'customer-delegated' },
];

const ROTATION_VALUES: ReadonlyArray<{ value: RotationProvider; label: string; hint: string }> = [
  {
    value: 'manual_paste',
    label: 'manual-paste',
    hint: 'operator pastes a new value at rotation time (default)',
  },
  {
    value: 'infisical_native',
    label: 'infisical-native',
    hint: 'Infisical-supported backends (Postgres, MySQL, AWS IAM, etc.)',
  },
  {
    value: 'not_rotatable',
    label: 'not-rotatable',
    hint: 'rotation UI disabled; the value is treated as static',
  },
];

export function SecretCreateForm({ customerId }: Readonly<SecretCreateFormProps>) {
  const router = useRouter();
  const [name, setName] = useState('');
  const [value, setValue] = useState('');
  const [provenance, setProvenance] = useState<SecretProvenance>('operator_entered');
  const [pathPrefix, setPathPrefix] = useState(`/${customerId}/operator`);
  const [rotationCadenceDays, setRotationCadenceDays] = useState('90');
  const [rotationProvider, setRotationProvider] = useState<RotationProvider>('manual_paste');
  const [error, setError] = useState<string | null>(null);
  const [pending, startTransition] = useTransition();

  function handleProvenanceChange(p: SecretProvenance) {
    setProvenance(p);
    // Realign the prefix template to the new provenance.
    setPathPrefix(`/${customerId}/${prefixSegmentFor(p)}`);
  }

  function handleSubmit() {
    setError(null);
    if (name.length === 0) {
      setError('Name cannot be empty.');
      return;
    }
    if (value.length === 0) {
      setError('Value cannot be empty.');
      return;
    }
    const cadence = Number.parseInt(rotationCadenceDays, 10);
    if (!Number.isFinite(cadence) || cadence < 1) {
      setError('Rotation cadence must be a positive integer (days).');
      return;
    }
    startTransition(async () => {
      try {
        await createSecret({
          name,
          value,
          pathPrefix,
          provenance,
          rotationCadenceDays: cadence,
          rotationProvider,
          ...(provenance === 'customer_delegated' ? { customerId } : {}),
        });
        // Hard navigation. router.push + router.refresh inside a
        // startTransition can leave pending=true indefinitely on
        // navigations whose destination triggers a server-side
        // re-fetch (the /vault list page); the button stays
        // disabled and the URL doesn't change even though the
        // server action returned 200. window.location.assign
        // sidesteps that race entirely.
        window.location.assign('/vault');
      } catch (err) {
        if (err instanceof VaultError) {
          const reason = (err.detail?.reason as string | undefined) ?? err.kind;
          setError(`Create rejected: ${reason}`);
        } else {
          setError(err instanceof Error ? err.message : 'unknown error');
        }
      }
    });
  }

  return (
    <div className="space-y-5">
      <FieldBlock label="name" hint="Trailing key in the secret path. Letters, digits, _ . -">
        <input
          type="text"
          value={name}
          onChange={(e) => setName(e.target.value)}
          autoComplete="off"
          spellCheck={false}
          className="w-full font-mono bg-surface-2 border border-border-1 rounded px-3 py-2 text-[13px] text-text-1 focus:outline-none focus:border-accent"
        />
      </FieldBlock>

      <FieldBlock
        label="value"
        hint="Sent to Infisical; never persisted in dashboard Postgres or logged."
      >
        <textarea
          value={value}
          onChange={(e) => setValue(e.target.value)}
          rows={4}
          autoComplete="off"
          spellCheck={false}
          className="w-full font-mono bg-surface-2 border border-border-1 rounded p-3 text-[12.5px] text-text-1 focus:outline-none focus:border-accent"
        />
      </FieldBlock>

      <FieldBlock label="provenance" hint="Drives the Rule 4 path-prefix segment + the operator-facing classification.">
        <div className="flex gap-2 flex-wrap">
          {PROVENANCE_VALUES.map((p) => (
            <button
              key={p.value}
              type="button"
              onClick={() => handleProvenanceChange(p.value)}
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

      <FieldBlock
        label="path prefix"
        hint="Derived from customer + provenance per threat-model Rule 4. Read-only; pick a different provenance to change it."
      >
        <input
          type="text"
          value={pathPrefix}
          readOnly
          spellCheck={false}
          className="w-full font-mono bg-surface-3 border border-border-1 rounded px-3 py-2 text-[12.5px] text-text-3 cursor-not-allowed"
        />
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
          <p className="text-[11px] text-text-3 mt-1">
            {ROTATION_VALUES.find((r) => r.value === rotationProvider)?.hint}
          </p>
        </FieldBlock>
      </div>

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
          onClick={handleSubmit}
          className="px-3 py-1.5 text-[13px] rounded bg-accent text-white hover:bg-accent/90 disabled:opacity-50 disabled:cursor-not-allowed"
        >
          {pending ? 'Creating…' : 'Create secret'}
        </button>
      </div>
    </div>
  );
}

function prefixSegmentFor(p: SecretProvenance): string {
  switch (p) {
    case 'operator_entered':
      return 'operator';
    case 'oauth_flow':
      return 'oauth';
    case 'environment_bootstrap':
      return 'environment_bootstrap';
    case 'customer_delegated':
      return 'customer_delegated';
  }
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
