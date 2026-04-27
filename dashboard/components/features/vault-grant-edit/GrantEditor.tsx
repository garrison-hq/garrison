'use client';

import { useState, useTransition } from 'react';
import { useRouter } from 'next/navigation';
import { addGrant, removeGrant } from '@/lib/actions/vault';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { VaultError } from '@/lib/vault/errors';

// GrantEditor — operator-facing surface for addGrant + removeGrant
// per FR-061 / FR-062 / FR-063 / FR-082 / FR-083.
//
// Sits below the role-secret matrix on /vault/matrix. Two surfaces:
//
//  - Add grant form: role slug + env var name + secret path. The
//    server action validates env-var shape ([A-Z][A-Z0-9_]*) and
//    rejects with VaultError(ValidationRejected). Duplicate
//    grants throw VaultError(GrantConflict) per the M2.3 PK on
//    (role_slug, env_var_name, customer_id). Missing
//    secret_metadata row throws SecretNotFound.
//  - Existing grants list: each row carries a × button. Click →
//    typed-name confirm (the role+env-var pair) → removeGrant.
//    Single-grant operations only per FR-083.

export interface GrantEditorProps {
  /** All current grants, fetched server-side via fetchAllGrants. */
  grants: ReadonlyArray<{
    roleSlug: string;
    envVarName: string;
    secretPath: string;
    grantedAt: Date;
  }>;
  /** Distinct role slugs from agent_role_secrets + the agents
   *  table; used to populate the role dropdown. */
  roleSlugs: ReadonlyArray<string>;
  /** All known secret paths from secret_metadata; populates the
   *  secret_path dropdown. */
  secretPaths: ReadonlyArray<string>;
}

export function GrantEditor({
  grants: initialGrants,
  roleSlugs,
  secretPaths,
}: GrantEditorProps) {
  const router = useRouter();
  const [grants, setGrants] = useState(initialGrants);
  const [roleSlug, setRoleSlug] = useState(roleSlugs[0] ?? '');
  const [envVarName, setEnvVarName] = useState('');
  const [secretPath, setSecretPath] = useState(secretPaths[0] ?? '');
  const [error, setError] = useState<string | null>(null);
  const [pending, startTransition] = useTransition();
  const [confirmRemoveKey, setConfirmRemoveKey] = useState<string | null>(null);

  function handleAdd() {
    setError(null);
    if (!roleSlug || !envVarName || !secretPath) {
      setError('Pick a role, enter an env var name, and pick a secret path.');
      return;
    }
    if (!/^[A-Z][A-Z0-9_]*$/.test(envVarName)) {
      setError('env var name must match [A-Z][A-Z0-9_]*');
      return;
    }
    startTransition(async () => {
      try {
        await addGrant({ roleSlug, envVarName, secretPath });
        // Optimistic append; router.refresh pulls canonical
        // state with granted_at populated.
        setGrants((prev) => [
          ...prev,
          { roleSlug, envVarName, secretPath, grantedAt: new Date() },
        ]);
        setEnvVarName('');
        router.refresh();
      } catch (err) {
        if (err instanceof VaultError) {
          setError(`Add rejected: ${err.kind}`);
        } else {
          setError(err instanceof Error ? err.message : 'unknown error');
        }
      }
    });
  }

  function handleRemove(g: { roleSlug: string; envVarName: string; secretPath: string }) {
    setError(null);
    setConfirmRemoveKey(null);
    startTransition(async () => {
      try {
        const result = await removeGrant({
          roleSlug: g.roleSlug,
          envVarName: g.envVarName,
          secretPath: g.secretPath,
        });
        if (result.removed) {
          setGrants((prev) =>
            prev.filter(
              (x) =>
                !(
                  x.roleSlug === g.roleSlug &&
                  x.envVarName === g.envVarName &&
                  x.secretPath === g.secretPath
                ),
            ),
          );
          router.refresh();
        } else {
          setError('Grant not found (may have been removed already).');
        }
      } catch (err) {
        if (err instanceof VaultError) {
          setError(`Remove rejected: ${err.kind}`);
        } else {
          setError(err instanceof Error ? err.message : 'unknown error');
        }
      }
    });
  }

  return (
    <section className="bg-surface-1 border border-border-1 rounded">
      <header className="px-4 py-2.5 border-b border-border-1">
        <span className="text-text-3 text-[10.5px] uppercase tracking-[0.08em] font-medium">
          edit grants
        </span>
      </header>

      <div className="p-4 space-y-4">
        <div className="grid grid-cols-1 md:grid-cols-3 gap-3">
          <select
            value={roleSlug}
            onChange={(e) => setRoleSlug(e.target.value)}
            className="bg-surface-2 border border-border-1 rounded px-3 py-2 text-[13px] text-text-1 focus:outline-none focus:border-accent"
          >
            {roleSlugs.length === 0 ? (
              <option value="">(no roles)</option>
            ) : (
              roleSlugs.map((r) => (
                <option key={r} value={r}>
                  {r}
                </option>
              ))
            )}
          </select>
          <input
            type="text"
            placeholder="ENV_VAR_NAME"
            value={envVarName}
            onChange={(e) => setEnvVarName(e.target.value.toUpperCase())}
            className="font-mono bg-surface-2 border border-border-1 rounded px-3 py-2 text-[13px] text-text-1 focus:outline-none focus:border-accent"
          />
          <select
            value={secretPath}
            onChange={(e) => setSecretPath(e.target.value)}
            className="font-mono bg-surface-2 border border-border-1 rounded px-3 py-2 text-[12.5px] text-text-1 focus:outline-none focus:border-accent"
          >
            {secretPaths.length === 0 ? (
              <option value="">(no secrets)</option>
            ) : (
              secretPaths.map((p) => (
                <option key={p} value={p}>
                  {p}
                </option>
              ))
            )}
          </select>
        </div>

        <div className="flex items-center gap-3">
          <button
            type="button"
            onClick={handleAdd}
            disabled={pending || roleSlugs.length === 0 || secretPaths.length === 0}
            className="px-3 py-1.5 text-[13px] rounded bg-accent text-white hover:bg-accent/90 disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {pending ? 'Working…' : 'Add grant'}
          </button>
          {error && (
            <span className="text-[12px] text-err">{error}</span>
          )}
        </div>

        {grants.length === 0 ? (
          <p className="text-[12.5px] text-text-3 italic">No grants yet.</p>
        ) : (
          <table className="w-full text-[12.5px] mt-2">
            <thead className="text-[10.5px] uppercase tracking-wide text-text-3">
              <tr className="border-b border-border-1">
                <th className="text-left px-3 py-2 font-normal">role</th>
                <th className="text-left px-3 py-2 font-normal">env var</th>
                <th className="text-left px-3 py-2 font-normal">secret path</th>
                <th className="text-right px-3 py-2 font-normal"></th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border-1">
              {grants.map((g) => {
                const key = `${g.roleSlug}|${g.envVarName}|${g.secretPath}`;
                return (
                  <tr key={key}>
                    <td className="px-3 py-2 text-text-1 font-mono">{g.roleSlug}</td>
                    <td className="px-3 py-2 text-text-2 font-mono">{g.envVarName}</td>
                    <td className="px-3 py-2 text-text-3 font-mono truncate max-w-[400px]" title={g.secretPath}>
                      {g.secretPath}
                    </td>
                    <td className="px-3 py-2 text-right">
                      <button
                        type="button"
                        onClick={() => setConfirmRemoveKey(key)}
                        className="text-[12px] text-err hover:underline"
                      >
                        Remove
                      </button>
                      <ConfirmDialog
                        open={confirmRemoveKey === key}
                        tier="single-click"
                        intent="destructive"
                        title="Remove grant"
                        body={
                          <span>
                            Remove the <code className="font-mono">{g.envVarName}</code>{' '}
                            grant for role <code className="font-mono">{g.roleSlug}</code>?
                            Running instances of the role keep the env var until they exit;
                            future spawns will not see it.
                          </span>
                        }
                        confirmLabel="Remove"
                        onConfirm={() => handleRemove(g)}
                        onCancel={() => setConfirmRemoveKey(null)}
                      />
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </div>
    </section>
  );
}
