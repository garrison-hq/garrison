// writeVaultMutationLog — write a row to the supervisor-domain
// vault_access_log table for operator-driven vault mutations.
//
// FR-012 / FR-013 / FR-018 / threat model Rule 6: every vault
// access (read OR write) gets one audit row. M2.3 wrote rows for
// supervisor-driven reads with agent_instance_id populated and
// outcome in {granted, denied_*, error_*}. M4 extends this to
// operator-driven writes — rows have agent_instance_id NULL
// (T001b made it nullable), actor identity stored in
// metadata.actor_user_id, and outcome drawn from the M4-extended
// vocabulary (secret_created, secret_edited, secret_deleted,
// grant_added, grant_removed, rotation_initiated,
// rotation_completed, rotation_failed, value_revealed).
//
// The metadata JSONB column carries write-specific context
// (changed fields, target path, rotation step that failed, etc.).
// Per Rule 6, metadata MUST NEVER carry a secret value. This
// helper applies a defensive leak-scan against the metadata
// payload before INSERT — pattern shapes from the M2.3 set —
// and throws if a match is found. The TS-side discipline check
// (FR-017, lib/vault/discipline-check.ts in T006) catches the
// problem at CI time; this runtime check is the defense-in-depth
// floor.

import type { MutationTx } from './eventOutbox';
import { vaultAccessLog } from '@/drizzle/schema.supervisor';

/**
 * Outcome values M4 introduces for write mutations. The TEXT
 * column accepts any string at the DB layer (per the M2.1 / M2.3
 * unconstrained pattern); these constants document the
 * application-level vocabulary.
 */
export const VaultWriteOutcome = {
  SecretCreated: 'secret_created',
  SecretEdited: 'secret_edited',
  SecretDeleted: 'secret_deleted',
  GrantAdded: 'grant_added',
  GrantRemoved: 'grant_removed',
  RotationInitiated: 'rotation_initiated',
  RotationCompleted: 'rotation_completed',
  RotationFailed: 'rotation_failed',
  ValueRevealed: 'value_revealed',
} as const;
export type VaultWriteOutcome = typeof VaultWriteOutcome[keyof typeof VaultWriteOutcome];

export interface WriteVaultMutationLogParams {
  outcome: string;
  secretPath: string;
  customerId: string;
  /** Operator identity (better-auth user uuid) — written into
   *  metadata.actor_user_id rather than a top-level column. */
  actorUserId: string;
  /** Optional ticket context for vault mutations triggered while
   *  viewing a specific ticket. Most M4 vault mutations have no
   *  ticket context; the column is nullable for both reads and
   *  writes. */
  ticketId?: string;
  /** Write-specific context: changed fields, rotation step that
   *  failed, target path on rename, etc. Per Rule 6 / FR-017,
   *  MUST NOT carry secret values. */
  metadata?: Record<string, unknown>;
}

/**
 * Patterns shaped like secrets — defensive leak-scan against the
 * metadata payload to prevent accidental violations of Rule 6.
 * Mirrors the M2.3 supervisor scanner's prefix set; the TS-side
 * leak-scan discipline (lib/vault/leakScan.ts in T006) is the
 * primary boundary, this is the runtime backstop.
 */
const SECRET_SHAPED_PATTERNS: RegExp[] = [
  /\bsk-[A-Za-z0-9_-]{20,}\b/,
  /\bxoxb-[A-Za-z0-9-]+\b/,
  /\bAKIA[0-9A-Z]{16}\b/,
  /-----BEGIN [A-Z ]+-----/,
  /\bgh[psuor]_[A-Za-z0-9]{20,}\b/,
];

function metadataLooksLikeSecret(value: unknown): { matched: true; pattern: string } | { matched: false } {
  // Walk the JSON tree; reject any string node matching a
  // secret-shape pattern. Numbers / booleans / nulls are safe.
  const queue: unknown[] = [value];
  while (queue.length > 0) {
    const node = queue.shift();
    if (typeof node === 'string') {
      for (const pat of SECRET_SHAPED_PATTERNS) {
        if (pat.test(node)) {
          return { matched: true, pattern: pat.source };
        }
      }
    } else if (Array.isArray(node)) {
      queue.push(...node);
    } else if (node && typeof node === 'object') {
      queue.push(...Object.values(node as Record<string, unknown>));
    }
  }
  return { matched: false };
}

export class VaultAuditLeakError extends Error {
  constructor(public pattern: string) {
    super(`vault_access_log.metadata payload contains a secret-shaped value (pattern: ${pattern}); refusing to write per Rule 6`);
  }
}

/**
 * Write a vault mutation audit row inside the passed Drizzle
 * transaction. Throws VaultAuditLeakError if the metadata
 * payload would carry a secret-shaped value (Rule 6 backstop).
 */
export async function writeVaultMutationLog(
  tx: MutationTx,
  params: WriteVaultMutationLogParams,
): Promise<void> {
  const metadata: Record<string, unknown> = {
    actor_user_id: params.actorUserId,
    ...params.metadata,
  };
  const leak = metadataLooksLikeSecret(metadata);
  if (leak.matched) {
    throw new VaultAuditLeakError(leak.pattern);
  }
  await tx.insert(vaultAccessLog).values({
    agentInstanceId: null,
    ticketId: params.ticketId ?? null,
    secretPath: params.secretPath,
    customerId: params.customerId,
    outcome: params.outcome,
    metadata,
  });
}
