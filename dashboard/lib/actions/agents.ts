'use server';

// M4 agent settings server action per plan §"Concrete interfaces >
// editAgent" + spec FR-095..FR-114.
//
// Editable fields: agentMd, model, listensFor, skills (FR-096).
// Per-agent concurrency_cap (FR-103) is NOT editable — concurrency
// caps live on departments per RATIONALE Principle X
// ("Per-department concurrency caps bound parallelism. Per-agent
// caps … are rejected"). Spec FR-103 conflicted with the
// constitution; dropped from M4.
//
// Save flow:
//   1. authenticate (getSession)
//   2. validate input (model enum, listens_for shape, skills slugs)
//   3. fetch the role's agent_role_secrets to determine
//      fetchableValues (each grant points at an Infisical secret;
//      we resolve values via the dashboard ML for the leak-scan).
//   4. run lib/vault/leakScan.ts:scanForLeaks against agentMd
//      with fetchableValues — reject save if any match (FR-088).
//   5. open transaction:
//      a. lib/locks/version.ts:checkAndUpdate keyed on
//         agents.updated_at (FR-101)
//      b. write event_outbox row with field-level diff
//      c. emit pg_notify('agents.changed', roleSlug) for the
//         supervisor cache invalidator (T014)
//      d. emit pg_notify('work.agent.edited', outbox.id) for
//         the activity feed
//   6. return typed result.

import { eq } from 'drizzle-orm';
import { appDb } from '@/lib/db/appClient';
import { agents, agentRoleSecrets } from '@/drizzle/schema.supervisor';
import { getSession } from '@/lib/auth/session';
import { AuthError, AuthErrorKind } from '@/lib/auth/errors';
import { ConflictError, ConflictKind } from '@/lib/locks/conflict';
import { VaultError, VaultErrorKind } from '@/lib/vault/errors';
import {
  writeMutationEventToOutbox,
  type MutationTx,
} from '@/lib/audit/eventOutbox';
import { emitPgNotify } from '@/lib/audit/pgNotify';
import { buildFieldDiff } from '@/lib/audit/diff';
import { checkAndUpdate } from '@/lib/locks/version';
import { scanForLeaks } from '@/lib/vault/leakScan';
import {
  getDashboardVault,
  getDashboardVaultConfig,
  isVaultConfigured,
} from '@/lib/vault/infisicalClient';

// ─── Types ────────────────────────────────────────────────────

export type AgentModel = 'haiku' | 'sonnet' | 'opus';

export interface EditAgentParams {
  /** Primary key — the agents.id uuid. */
  agentId: string;
  /** updated_at snapshot from the load-time read. */
  versionToken: string;
  /** Role slug used as the pg_notify payload for agents.changed
   *  + as the audit-row identifier. Looked up from the agents
   *  row server-side rather than trusted from the client.
   *  Caller passes for context but the action verifies. */
  expectedRoleSlug: string;
  changes: Partial<{
    agentMd: string;
    model: AgentModel;
    listensFor: string[];
    skills: string[];
  }>;
}

export interface AgentSnapshot {
  agentId: string;
  roleSlug: string;
  agentMd: string;
  model: string;
  listensFor: string[];
  skills: string[];
  updatedAt: string;
}

export type EditAgentResult =
  | { accepted: true; newVersionToken: string }
  | { accepted: false; conflict: true; serverState: AgentSnapshot | null };

// ─── Helpers ──────────────────────────────────────────────────

const MODEL_VALUES: AgentModel[] = ['haiku', 'sonnet', 'opus'];
const LISTENS_FOR_PATTERN_RE = /^[a-z0-9._*-]+$/;

async function requireOperatorUserId(): Promise<string> {
  const session = await getSession();
  if (!session) throw new AuthError(AuthErrorKind.NoSession);
  return session.user.id;
}

function validateModel(model: unknown): asserts model is AgentModel {
  if (!MODEL_VALUES.includes(model as AgentModel)) {
    throw new ConflictError(
      ConflictKind.AlreadyExists,
      undefined,
      `model must be one of: ${MODEL_VALUES.join(', ')}`,
    );
  }
}

function validateListensFor(listensFor: unknown): asserts listensFor is string[] {
  if (!Array.isArray(listensFor)) {
    throw new ConflictError(ConflictKind.AlreadyExists, undefined, 'listens_for must be an array of strings');
  }
  for (const entry of listensFor) {
    if (typeof entry !== 'string' || !LISTENS_FOR_PATTERN_RE.test(entry)) {
      throw new ConflictError(
        ConflictKind.AlreadyExists,
        undefined,
        `listens_for entry "${entry}" must match [a-z0-9._*-]+ (channel-pattern shape)`,
      );
    }
  }
}

function validateSkills(skills: unknown): asserts skills is string[] {
  if (!Array.isArray(skills)) {
    throw new ConflictError(ConflictKind.AlreadyExists, undefined, 'skills must be an array of slug strings');
  }
  for (const slug of skills) {
    if (typeof slug !== 'string' || slug.length === 0) {
      throw new ConflictError(
        ConflictKind.AlreadyExists,
        undefined,
        'each skills entry must be a non-empty string',
      );
    }
  }
}

/**
 * Resolve the verbatim secret values fetchable for a role, by
 * joining agent_role_secrets to the dashboard's Infisical client.
 * Used by FR-088 leak-scan parity at agent.md save time.
 *
 * If the dashboard's vault is not configured, returns an empty
 * array — the shape scan still runs against the agent.md, but
 * the verbatim-value pass becomes a no-op. This keeps the dev
 * experience usable without Infisical provisioned.
 */
async function fetchableValuesForRole(roleSlug: string): Promise<string[]> {
  if (!isVaultConfigured()) return [];

  const grants = await appDb
    .select({
      envVarName: agentRoleSecrets.envVarName,
      secretPath: agentRoleSecrets.secretPath,
    })
    .from(agentRoleSecrets)
    .where(eq(agentRoleSecrets.roleSlug, roleSlug));

  if (grants.length === 0) return [];

  const sdk = await getDashboardVault();
  const cfg = getDashboardVaultConfig();
  const values: string[] = [];
  for (const g of grants) {
    try {
      const secretName = g.secretPath.slice(g.secretPath.lastIndexOf('/') + 1);
      const secretPathPrefix = g.secretPath.slice(0, g.secretPath.lastIndexOf('/'));
      // vault-discipline-allow
      const response = await sdk.secrets().getSecret({
        environment: cfg.environment,
        secretName,
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        ...({
          projectId: cfg.projectId,
          secretPath: secretPathPrefix,
        } as any),
      });
      // vault-discipline-allow
      const v =
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        (response as any).secretValue ?? (response as any).value ?? '';
      if (typeof v === 'string' && v.length > 0) {
        values.push(v);
      }
    } catch {
      // Best-effort: if a grant fails to resolve (the secret
      // was deleted out of band, network blip, etc.), skip it.
      // The shape scan still catches obvious patterns; a
      // missing fetchable value just means we can't verify
      // verbatim absence for THAT specific value.
    }
  }
  return values;
}

// ─── editAgent ────────────────────────────────────────────────

function validateChanges(changes: EditAgentParams['changes']): void {
  if (changes.model !== undefined) validateModel(changes.model);
  if (changes.listensFor !== undefined) validateListensFor(changes.listensFor);
  if (changes.skills !== undefined) validateSkills(changes.skills);
  if (changes.agentMd?.length === 0) {
    throw new ConflictError(ConflictKind.AlreadyExists, undefined, 'agent_md must be non-empty');
  }
}

async function loadCurrentAgent(agentId: string, expectedRoleSlug: string) {
  const currentRows = await appDb.select().from(agents).where(eq(agents.id, agentId)).limit(1);
  if (currentRows.length === 0) {
    throw new ConflictError(ConflictKind.AlreadyExists, undefined, 'agent not found');
  }
  const current = currentRows[0];
  if (current.roleSlug !== expectedRoleSlug) {
    throw new ConflictError(
      ConflictKind.AlreadyExists,
      undefined,
      'role slug mismatch — agent has been re-keyed; reload and retry',
    );
  }
  return current;
}

export async function editAgent(params: EditAgentParams): Promise<EditAgentResult> {
  await requireOperatorUserId();

  validateChanges(params.changes);

  const current = await loadCurrentAgent(params.agentId, params.expectedRoleSlug);

  // Run the leak-scan against the proposed agentMd. Per FR-088,
  // saving an agent.md that contains a fetchable secret value
  // verbatim is rejected. Shape patterns also trigger rejection
  // — they're a strong signal of an accidental paste.
  if (params.changes.agentMd !== undefined) {
    await rejectIfLeak(params.changes.agentMd, current.roleSlug);
  }

  // Build the field-level diff (used for both the lock-changes
  // payload and the event_outbox audit row).
  const editableFields: Array<keyof EditAgentParams['changes']> = [
    'agentMd',
    'model',
    'listensFor',
    'skills',
  ];
  const before = {
    agentMd: current.agentMd,
    model: current.model,
    listensFor: Array.isArray(current.listensFor) ? (current.listensFor as string[]) : [],
    skills: Array.isArray(current.skills) ? (current.skills as string[]) : [],
  };
  const after = { ...before, ...params.changes };
  const diff = buildFieldDiff(
    before as Record<string, unknown>,
    after as Record<string, unknown>,
    editableFields as Array<string>,
  );
  if (Object.keys(diff).length === 0) {
    return {
      accepted: true,
      newVersionToken: current.updatedAt,
    };
  }

  // Map the changes to Drizzle update-set values. listensFor and
  // skills are JSONB columns; pass them as JS objects (Drizzle
  // serialises automatically).
  const setMap: Record<string, unknown> = {};
  if (params.changes.agentMd !== undefined) setMap.agentMd = params.changes.agentMd;
  if (params.changes.model !== undefined) setMap.model = params.changes.model;
  if (params.changes.listensFor !== undefined) setMap.listensFor = params.changes.listensFor;
  if (params.changes.skills !== undefined) setMap.skills = params.changes.skills;

  return appDb.transaction(async (tx) => {
    const tx2 = tx as unknown as MutationTx;

    const lockResult = await checkAndUpdate<typeof current>(tx2, {
      table: agents,
      pkColumn: agents.id,
      updatedAtColumn: agents.updatedAt,
      updatedAtFieldName: 'updatedAt',
      idValue: params.agentId,
      expectedVersionToken: params.versionToken,
      changes: setMap,
    });
    if (!lockResult.accepted) {
      const ss = lockResult.serverState;
      return {
        accepted: false as const,
        conflict: true as const,
        serverState: ss
          ? snapshotFrom(ss as Record<string, unknown>)
          : null,
      };
    }

    const outbox = await writeMutationEventToOutbox(tx2, {
      kind: 'agent.edited',
      roleSlug: current.roleSlug,
      diff,
    });

    // T014 supervisor consumer: agents.changed payload is the
    // role slug. Quoted at the receiver side (LISTEN
    // "agents.changed").
    await emitPgNotify(tx2, 'agents.changed', current.roleSlug);
    await emitPgNotify(tx2, 'work.agent.edited', outbox.id);

    return {
      accepted: true as const,
      newVersionToken: lockResult.newVersionToken,
    };
  });
}

function asStr(value: unknown): string {
  return typeof value === 'string' ? value : '';
}

function asStrArray(value: unknown): string[] {
  return Array.isArray(value) ? (value as string[]) : [];
}

/** Run the leak-scan against agentMd; throw VaultError on match. */
async function rejectIfLeak(agentMd: string, roleSlug: string): Promise<void> {
  const fetchableValues = await fetchableValuesForRole(roleSlug);
  const matches = scanForLeaks(agentMd, fetchableValues);
  if (matches.length > 0) {
    const labels = Array.from(new Set(matches.map((m) => m.label)));
    throw new VaultError(VaultErrorKind.ValidationRejected, {
      reason: 'agent_md contains a leak-scan match; refusing to save per Rule 1 / FR-088',
      labels,
    });
  }
}

function snapshotFrom(row: Record<string, unknown>): AgentSnapshot {
  return {
    agentId: asStr(row.id),
    roleSlug: asStr(row.role_slug ?? row.roleSlug),
    agentMd: asStr(row.agent_md ?? row.agentMd),
    model: asStr(row.model),
    listensFor: asStrArray(row.listens_for ?? row.listensFor),
    skills: asStrArray(row.skills),
    updatedAt: asStr(row.updated_at ?? row.updatedAt),
  };
}
