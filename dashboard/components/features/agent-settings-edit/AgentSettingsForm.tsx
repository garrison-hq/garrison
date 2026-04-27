'use client';

import { useState, useTransition } from 'react';
import { useRouter } from 'next/navigation';
import { editAgent, type AgentSnapshot, type AgentModel } from '@/lib/actions/agents';
import { ConflictResolutionModal } from '@/components/ui/ConflictResolutionModal';
import { DiffView } from '@/components/ui/DiffView';
import { ConflictError } from '@/lib/locks/conflict';
import { VaultError } from '@/lib/vault/errors';

// AgentSettingsForm — operator-facing edit surface for an existing
// agent's editable fields per FR-095..FR-114.
//
// Editable fields (FR-096): agentMd, model, listensFor, skills.
// Per-agent concurrency_cap intentionally NOT editable —
// concurrency caps live on departments per RATIONALE Principle X.
//
// Optimistic locking (FR-101) is wired through the editAgent
// server action; on { accepted: false, conflict: true, serverState }
// we surface ConflictResolutionModal so the operator picks
// overwrite / merge-manually / discard.
//
// Leak-scan rejection (FR-088) surfaces as VaultError(
// ValidationRejected); the form renders the matched labels in
// the error toast so the operator knows what to remove.

export interface AgentSettingsFormProps {
  agentId: string;
  roleSlug: string;
  departmentSlug: string;
  initial: {
    agentMd: string;
    model: AgentModel;
    listensFor: string[];
    skills: string[];
    updatedAt: string;
  };
  /** When the operator opens the editor, the route's loading data
   *  fetched a fresh updatedAt. Subsequent saves use the latest
   *  newVersionToken returned by editAgent. */
}

const MODELS: AgentModel[] = ['haiku', 'sonnet', 'opus'];

export function AgentSettingsForm({
  agentId,
  roleSlug,
  departmentSlug,
  initial,
}: Readonly<AgentSettingsFormProps>) {
  const router = useRouter();
  const [agentMd, setAgentMd] = useState(initial.agentMd);
  const [model, setModel] = useState<AgentModel>(initial.model);
  const [listensFor, setListensFor] = useState<string>(initial.listensFor.join('\n'));
  const [skills, setSkills] = useState<string>(initial.skills.join('\n'));
  const [versionToken, setVersionToken] = useState(initial.updatedAt);

  const [error, setError] = useState<string | null>(null);
  const [conflictState, setConflictState] = useState<AgentSnapshot | null>(null);
  const [pending, startTransition] = useTransition();
  const [saved, setSaved] = useState(false);

  function buildChanges() {
    const parsed = {
      agentMd,
      model,
      listensFor: listensFor
        .split('\n')
        .map((s) => s.trim())
        .filter((s) => s.length > 0),
      skills: skills
        .split('\n')
        .map((s) => s.trim())
        .filter((s) => s.length > 0),
    };
    return parsed;
  }

  async function handleSave(versionTokenOverride?: string) {
    setError(null);
    setSaved(false);
    const changes = buildChanges();
    startTransition(async () => {
      try {
        const result = await editAgent({
          agentId,
          versionToken: versionTokenOverride ?? versionToken,
          expectedRoleSlug: roleSlug,
          changes,
        });
        if (result.accepted) {
          setVersionToken(result.newVersionToken);
          setSaved(true);
          router.refresh();
        } else {
          setConflictState(result.serverState);
        }
      } catch (err) {
        if (err instanceof VaultError) {
          const labels = (err.detail?.labels as string[] | undefined)?.join(', ') ?? '';
          setError(
            `Save rejected: agent.md contains a leak-scan match (${labels}). Remove the matched secret-shaped content and try again.`,
          );
        } else if (err instanceof ConflictError) {
          setError(`Save rejected: ${err.message}`);
        } else {
          setError(err instanceof Error ? err.message : 'unknown error');
        }
      }
    });
  }

  function handleOverwrite() {
    if (!conflictState) return;
    setConflictState(null);
    void handleSave(conflictState.updatedAt);
  }

  function handleMergeManually() {
    if (!conflictState) return;
    setAgentMd(conflictState.agentMd);
    setModel(conflictState.model as AgentModel);
    setListensFor(conflictState.listensFor.join('\n'));
    setSkills(conflictState.skills.join('\n'));
    setVersionToken(conflictState.updatedAt);
    setConflictState(null);
  }

  function handleDiscard() {
    setConflictState(null);
    router.push(`/agents`);
  }

  const conflictDiff = conflictState ? (
    <DiffView
      diff={{
        agent_md: { before: conflictState.agentMd, after: agentMd },
        model: { before: conflictState.model, after: model },
        listens_for: {
          before: conflictState.listensFor,
          after: buildChanges().listensFor,
        },
        skills: {
          before: conflictState.skills,
          after: buildChanges().skills,
        },
      }}
    />
  ) : null;

  return (
    <div className="space-y-5">
      <div className="text-[11px] uppercase tracking-wide text-text-3">
        {departmentSlug} / {roleSlug}
      </div>

      <FieldBlock label="agent.md">
        <textarea
          value={agentMd}
          onChange={(e) => setAgentMd(e.target.value)}
          rows={20}
          className="w-full font-mono text-[12.5px] bg-surface-2 border border-border-1 rounded p-3 text-text-1 focus:outline-none focus:border-accent"
          spellCheck={false}
        />
      </FieldBlock>

      <FieldBlock label="model">
        <div className="flex gap-2">
          {MODELS.map((m) => (
            <button
              key={m}
              type="button"
              onClick={() => setModel(m)}
              className={`px-3 py-1.5 text-[13px] rounded border ${
                model === m
                  ? 'bg-accent/10 border-accent/40 text-accent'
                  : 'bg-surface-2 border-border-1 text-text-2 hover:text-text-1'
              }`}
            >
              {m}
            </button>
          ))}
        </div>
      </FieldBlock>

      <FieldBlock
        label="listens_for"
        hint="One channel pattern per line. Format: lowercase + dots + dashes + asterisks."
      >
        <textarea
          value={listensFor}
          onChange={(e) => setListensFor(e.target.value)}
          rows={4}
          className="w-full font-mono text-[12.5px] bg-surface-2 border border-border-1 rounded p-3 text-text-1 focus:outline-none focus:border-accent"
          spellCheck={false}
        />
      </FieldBlock>

      <FieldBlock label="skills" hint="One skill slug per line.">
        <textarea
          value={skills}
          onChange={(e) => setSkills(e.target.value)}
          rows={4}
          className="w-full font-mono text-[12.5px] bg-surface-2 border border-border-1 rounded p-3 text-text-1 focus:outline-none focus:border-accent"
          spellCheck={false}
        />
      </FieldBlock>

      {error && (
        <div className="rounded border border-err/40 bg-err/5 px-4 py-2.5 text-[12.5px] text-err">
          {error}
        </div>
      )}
      {saved && (
        <div className="rounded border border-ok/40 bg-ok/5 px-4 py-2.5 text-[12.5px] text-ok">
          Saved. New config takes effect on the next spawn for this role.
        </div>
      )}

      <div className="flex justify-end gap-2">
        <button
          type="button"
          onClick={() => router.push('/agents')}
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
        open={conflictState !== null}
        title="Another operator changed this agent since you opened the form."
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
