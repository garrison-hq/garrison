'use server';

// M4 vault server actions per plan §"Concrete interfaces > Server
// action signatures > lib/actions/vault.ts". Each action follows
// the canonical 5-step transactional flow documented in
// lib/actions/_README.md:
//
//   1. authenticate (getSession)
//   2. authorize (any-logged-in-operator)
//   3. validate input (Rule 4 path conventions for create per
//      FR-053)
//   4. open transaction:
//      a. optimistic lock check (edit only)
//      b. write to Infisical via dashboardVault
//      c. write secret_metadata row
//      d. write vault_access_log audit row
//      e. emit pg_notify on work.vault.<kind> (FR-076)
//   5. return typed result or throw VaultError / ConflictError
//
// All vault writes route through the parked-since-M2.3
// garrison-dashboard Machine Identity (FR-050). Secret VALUES
// flow through the dashboard process at write time only — never
// persisted to dashboard Postgres tables, never logged through
// console / slog, never sent to the client outside an explicit
// reveal action (FR-018).
//
// T007 ships create + edit + delete. T008–T010 extend this
// module with grant editing / rotation / path tree / reveal
// server actions.

import { eq, sql } from 'drizzle-orm';
import { appDb } from '@/lib/db/appClient';
import { secretMetadata, companies, agentRoleSecrets } from '@/drizzle/schema.supervisor';
import { getSession } from '@/lib/auth/session';
import { AuthError, AuthErrorKind } from '@/lib/auth/errors';
import { VaultError, VaultErrorKind } from '@/lib/vault/errors';
import { ConflictError, ConflictKind } from '@/lib/locks/conflict';
import { getDashboardVault, getDashboardVaultConfig } from '@/lib/vault/infisicalClient';
import {
  writeVaultMutationLog,
  VaultWriteOutcome,
} from '@/lib/audit/vaultAccessLog';
import { emitPgNotify } from '@/lib/audit/pgNotify';
import { checkAndUpdate } from '@/lib/locks/version';
import type { MutationTx } from '@/lib/audit/eventOutbox';

// ─── Types ────────────────────────────────────────────────────

export type SecretProvenance =
  | 'operator_entered'
  | 'oauth_flow'
  | 'environment_bootstrap'
  | 'customer_delegated';

export type RotationProvider = 'infisical_native' | 'manual_paste' | 'not_rotatable';

export interface CreateSecretParams {
  /** Display name for the secret. Stored as the Infisical secret
   *  key (the trailing path segment); the secret path is
   *  composed as `${pathPrefix}/${name}`. */
  name: string;
  /** The secret value. Sent to Infisical; never persisted in
   *  dashboard Postgres or logged. */
  value: string;
  /** Path PREFIX (everything except the trailing key name).
   *  Validated against Rule 4 conventions: must start with
   *  /<customer_id>/<provenance>. */
  pathPrefix: string;
  provenance: SecretProvenance;
  rotationCadenceDays: number;
  rotationProvider: RotationProvider;
  /** Required when provenance='customer_delegated'; FR-089. For
   *  other provenances, the resolver picks the operating
   *  entity's customer_id (single-tenant default). */
  customerId?: string;
}

export interface EditSecretParams {
  secretPath: string;
  /** updated_at value snapshot from the load-time read. */
  versionToken: string;
  /** Subset of editable fields. Value is optional — when
   *  present, the new value is written to Infisical. Other
   *  fields are metadata only (no Infisical round-trip). */
  changes: Partial<{
    value: string;
    provenance: SecretProvenance;
    rotationCadenceDays: number;
    rotationProvider: RotationProvider;
  }>;
}

export interface DeleteSecretParams {
  secretPath: string;
  /** Operator must type the secret path verbatim to confirm. */
  confirmationName: string;
}

export interface SecretSnapshot {
  secretPath: string;
  customerId: string;
  provenance: string;
  rotationProvider: string;
  updatedAt: string;
}

// ─── Helpers ──────────────────────────────────────────────────

async function requireOperatorUserId(): Promise<string> {
  const session = await getSession();
  if (!session) {
    throw new AuthError(AuthErrorKind.NoSession);
  }
  return session.user.id;
}

async function resolveCustomerId(explicit?: string): Promise<string> {
  if (explicit) return explicit;
  // Single-tenant default: the operating entity's company id.
  // Mirrors the supervisor's M2.3 ListGrantsForRole pattern of
  // picking the first row.
  const rows = await appDb.select().from(companies).limit(1);
  if (rows.length === 0) {
    throw new VaultError(VaultErrorKind.ValidationRejected, {
      reason: 'no companies row exists; bootstrap required',
    });
  }
  return rows[0].id;
}

const PATH_PREFIX_RE = /^\/[0-9a-f-]{36}\/(operator|oauth|environment_bootstrap|customer_delegated)(\/.*)?$/;

function validatePathPrefix(pathPrefix: string): void {
  if (!PATH_PREFIX_RE.test(pathPrefix)) {
    throw new VaultError(VaultErrorKind.ValidationRejected, {
      field: 'pathPrefix',
      reason: 'path prefix must match /<customer_id>/<provenance>[/<remainder>] per Rule 4',
    });
  }
}

const SECRET_NAME_RE = /^[A-Za-z0-9_.-]+$/;

function validateSecretName(name: string): void {
  if (!SECRET_NAME_RE.test(name)) {
    throw new VaultError(VaultErrorKind.ValidationRejected, {
      field: 'name',
      reason: 'secret name must contain only [A-Za-z0-9_.-]',
    });
  }
}

function cadenceIntervalFromDays(days: number): string {
  if (!Number.isFinite(days) || days < 1 || days > 3650) {
    throw new VaultError(VaultErrorKind.ValidationRejected, {
      field: 'rotationCadenceDays',
      reason: 'rotation cadence must be a positive integer in days, ≤ 10 years',
    });
  }
  return `${Math.floor(days)} days`;
}

// ─── createSecret ─────────────────────────────────────────────

export async function createSecret(
  params: CreateSecretParams,
): Promise<{ secretPath: string }> {
  const actorUserId = await requireOperatorUserId();

  validateSecretName(params.name);
  validatePathPrefix(params.pathPrefix);
  if (params.provenance === 'customer_delegated' && !params.customerId) {
    throw new VaultError(VaultErrorKind.ValidationRejected, {
      field: 'customerId',
      reason: 'customer_id is required when provenance=customer_delegated (FR-089)',
    });
  }

  const customerId = await resolveCustomerId(params.customerId);
  const cadence = cadenceIntervalFromDays(params.rotationCadenceDays);
  const fullPath = `${params.pathPrefix.replace(/\/$/, '')}/${params.name}`;

  // Step 1: write to Infisical FIRST (per FR-054). On Infisical
  // success, write the local Postgres rows in a transaction with
  // the audit row + pg_notify. If the local writes fail, the
  // hygiene-critical desync alert path (FR-080) surfaces.
  const sdk = await getDashboardVault();
  const cfg = getDashboardVaultConfig();
  try {
    await sdk.secrets().createSecret(params.name, {
      projectId: cfg.projectId,
      environment: cfg.environment,
      secretValue: params.value,
      // vault-discipline-allow
      secretPath: params.pathPrefix,
    });
  } catch (err) {
    throw mapInfisicalError(err, 'create');
  }

  // Step 2: write secret_metadata + audit row in one transaction
  // and emit pg_notify at COMMIT.
  await appDb.transaction(async (tx) => {
    const tx2 = tx as unknown as MutationTx;
    await tx.insert(secretMetadata).values({
      secretPath: fullPath,
      customerId,
      provenance: params.provenance,
      rotationCadence: cadence,
      rotationProvider: params.rotationProvider,
      allowedRoleSlugs: [],
    });
    await writeVaultMutationLog(tx2, {
      outcome: VaultWriteOutcome.SecretCreated,
      secretPath: fullPath,
      customerId,
      actorUserId,
      metadata: {
        provenance: params.provenance,
        rotation_provider: params.rotationProvider,
      },
    });
    await emitPgNotify(tx2, 'work.vault.secret_created', fullPath);
  });

  return { secretPath: fullPath };
}

// ─── editSecret ───────────────────────────────────────────────

export type EditSecretResult =
  | { accepted: true }
  | { accepted: false; conflict: true; serverState: SecretSnapshot | null };

export async function editSecret(params: EditSecretParams): Promise<EditSecretResult> {
  const actorUserId = await requireOperatorUserId();

  const cadenceUpdate =
    params.changes.rotationCadenceDays !== undefined
      ? { rotationCadence: cadenceIntervalFromDays(params.changes.rotationCadenceDays) }
      : {};
  const provenanceUpdate =
    params.changes.provenance !== undefined ? { provenance: params.changes.provenance } : {};
  const rotationProviderUpdate =
    params.changes.rotationProvider !== undefined
      ? { rotationProvider: params.changes.rotationProvider }
      : {};

  const localChanges = {
    ...provenanceUpdate,
    ...cadenceUpdate,
    ...rotationProviderUpdate,
  };

  // If a value change is requested, write it to Infisical. We
  // do this BEFORE the local optimistic-lock check so the
  // Infisical-side state matches the post-commit local state
  // (if the local check fails, the Infisical value still
  // becomes the new value — the desync surfaces via the
  // mismatched updated_at).
  if (params.changes.value !== undefined) {
    const sdk = await getDashboardVault();
    const cfg = getDashboardVaultConfig();
    try {
      await sdk.secrets().updateSecret(extractSecretName(params.secretPath), {
        projectId: cfg.projectId,
        environment: cfg.environment,
        secretValue: params.changes.value,
        // vault-discipline-allow
        secretPath: extractPathPrefix(params.secretPath),
      });
    } catch (err) {
      throw mapInfisicalError(err, 'edit');
    }
  }

  // No local field changes? Just write the audit row.
  // (Edit-with-only-value still records the audit; the
  // metadata.changed_fields will name 'value' — Rule 6 means
  // we don't include the value itself.)
  const result = await appDb.transaction(async (tx) => {
    const tx2 = tx as unknown as MutationTx;

    const customerIdRow = await tx
      .select({ customerId: secretMetadata.customerId })
      .from(secretMetadata)
      .where(eq(secretMetadata.secretPath, params.secretPath))
      .limit(1);
    if (customerIdRow.length === 0) {
      throw new VaultError(VaultErrorKind.SecretNotFound, { secretPath: params.secretPath });
    }
    const customerId = customerIdRow[0].customerId;

    if (Object.keys(localChanges).length > 0) {
      const lockResult = await checkAndUpdate<{ secret_path: string; updated_at: string }>(
        tx2,
        secretMetadata,
        secretMetadata.secretPath,
        secretMetadata.updatedAt,
        'updatedAt',
        params.secretPath,
        params.versionToken,
        localChanges,
      );
      if (!lockResult.accepted) {
        return {
          accepted: false as const,
          conflict: true as const,
          serverState: lockResult.serverState
            ? snapshotFrom(lockResult.serverState as Record<string, unknown>)
            : null,
        };
      }
    }

    const changedFields = Object.keys(params.changes);
    await writeVaultMutationLog(tx2, {
      outcome: VaultWriteOutcome.SecretEdited,
      secretPath: params.secretPath,
      customerId,
      actorUserId,
      metadata: { changed_fields: changedFields },
    });
    await emitPgNotify(tx2, 'work.vault.secret_edited', params.secretPath);

    return { accepted: true as const };
  });

  return result;
}

// ─── deleteSecret ─────────────────────────────────────────────

export async function deleteSecret(params: DeleteSecretParams): Promise<void> {
  const actorUserId = await requireOperatorUserId();

  if (params.confirmationName !== params.secretPath) {
    throw new ConflictError(ConflictKind.AlreadyExists, undefined, 'typed-name confirmation does not match secret path');
  }

  // Surface affected roles before delete (FR-058 caller-side
  // guard; the server action enforces no roles can be
  // grant-mapped before deletion). The matrix view at
  // /vault/matrix is the operator-facing surface for this; the
  // server action returns a typed error if the operator
  // attempts to delete a secret with grants.
  const grants = await appDb
    .select({ roleSlug: agentRoleSecrets.roleSlug })
    .from(agentRoleSecrets)
    .where(eq(agentRoleSecrets.secretPath, params.secretPath));
  if (grants.length > 0) {
    throw new VaultError(VaultErrorKind.SecretInUseCannotDelete, {
      secretPath: params.secretPath,
      grantCount: grants.length,
      roleSlugs: grants.map((g) => g.roleSlug),
    });
  }

  // Delete from Infisical first.
  const sdk = await getDashboardVault();
  const cfg = getDashboardVaultConfig();
  try {
    await sdk.secrets().deleteSecret(extractSecretName(params.secretPath), {
      projectId: cfg.projectId,
      environment: cfg.environment,
      // vault-discipline-allow
      secretPath: extractPathPrefix(params.secretPath),
    });
  } catch (err) {
    throw mapInfisicalError(err, 'delete');
  }

  // Delete local rows + write audit + pg_notify.
  await appDb.transaction(async (tx) => {
    const tx2 = tx as unknown as MutationTx;

    const customerIdRow = await tx
      .select({ customerId: secretMetadata.customerId })
      .from(secretMetadata)
      .where(eq(secretMetadata.secretPath, params.secretPath))
      .limit(1);
    const customerId =
      customerIdRow.length > 0 ? customerIdRow[0].customerId : (await resolveCustomerId());

    await tx
      .delete(secretMetadata)
      .where(eq(secretMetadata.secretPath, params.secretPath));

    await writeVaultMutationLog(tx2, {
      outcome: VaultWriteOutcome.SecretDeleted,
      secretPath: params.secretPath,
      customerId,
      actorUserId,
    });
    await emitPgNotify(tx2, 'work.vault.secret_deleted', params.secretPath);
  });
}

// ─── Helpers ──────────────────────────────────────────────────

function extractSecretName(fullPath: string): string {
  const idx = fullPath.lastIndexOf('/');
  return idx >= 0 ? fullPath.slice(idx + 1) : fullPath;
}

function extractPathPrefix(fullPath: string): string {
  const idx = fullPath.lastIndexOf('/');
  return idx > 0 ? fullPath.slice(0, idx) : '/';
}

function snapshotFrom(row: Record<string, unknown>): SecretSnapshot {
  return {
    secretPath: String(row.secret_path ?? row.secretPath ?? ''),
    customerId: String(row.customer_id ?? row.customerId ?? ''),
    provenance: String(row.provenance ?? ''),
    rotationProvider: String(row.rotation_provider ?? row.rotationProvider ?? ''),
    updatedAt: String(row.updated_at ?? row.updatedAt ?? ''),
  };
}

// ─── addGrant / removeGrant (T008) ────────────────────────────

export interface AddGrantParams {
  roleSlug: string;
  envVarName: string;
  secretPath: string;
  /** Optional explicit customer_id; defaults to the operator's
   *  single-tenant company. */
  customerId?: string;
}

const ENV_VAR_NAME_RE = /^[A-Z][A-Z0-9_]*$/;

export async function addGrant(params: AddGrantParams): Promise<{ added: true }> {
  const actorUserId = await requireOperatorUserId();

  if (!ENV_VAR_NAME_RE.test(params.envVarName)) {
    throw new VaultError(VaultErrorKind.ValidationRejected, {
      field: 'envVarName',
      reason: 'env var name must match [A-Z][A-Z0-9_]*',
    });
  }
  if (!params.roleSlug) {
    throw new VaultError(VaultErrorKind.ValidationRejected, {
      field: 'roleSlug',
      reason: 'role slug must be a non-empty string',
    });
  }

  const customerId = await resolveCustomerId(params.customerId);

  await appDb.transaction(async (tx) => {
    const tx2 = tx as unknown as MutationTx;

    // Validate the secret exists in secret_metadata first.
    // Insert that doesn't reference an existing secret_path is
    // allowed by the M2.3 schema (no FK), but we want the
    // operator-facing UI to reject it.
    const secretRow = await tx
      .select({ secretPath: secretMetadata.secretPath })
      .from(secretMetadata)
      .where(eq(secretMetadata.secretPath, params.secretPath))
      .limit(1);
    if (secretRow.length === 0) {
      throw new VaultError(VaultErrorKind.SecretNotFound, { secretPath: params.secretPath });
    }

    try {
      await tx.insert(agentRoleSecrets).values({
        roleSlug: params.roleSlug,
        secretPath: params.secretPath,
        envVarName: params.envVarName,
        customerId,
        grantedBy: actorUserId,
      });
    } catch (err) {
      // PK is (role_slug, env_var_name, customer_id) per M2.3
      // schema — duplicate insert violates the PK constraint.
      const msg = err instanceof Error ? err.message : String(err);
      if (/duplicate key|unique|primary key/i.test(msg)) {
        throw new VaultError(VaultErrorKind.GrantConflict, {
          roleSlug: params.roleSlug,
          envVarName: params.envVarName,
        });
      }
      throw err;
    }

    // The rebuild_secret_metadata_role_slugs trigger fires
    // automatically inside this transaction (M2.3 invariant).

    await writeVaultMutationLog(tx2, {
      outcome: VaultWriteOutcome.GrantAdded,
      secretPath: params.secretPath,
      customerId,
      actorUserId,
      metadata: {
        role_slug: params.roleSlug,
        env_var_name: params.envVarName,
      },
    });
    await emitPgNotify(tx2, 'work.vault.grant_added', params.secretPath);
  });

  return { added: true };
}

export interface RemoveGrantParams {
  roleSlug: string;
  envVarName: string;
  secretPath: string;
  customerId?: string;
}

export async function removeGrant(params: RemoveGrantParams): Promise<{ removed: boolean }> {
  const actorUserId = await requireOperatorUserId();
  const customerId = await resolveCustomerId(params.customerId);

  let removed = false;
  await appDb.transaction(async (tx) => {
    const tx2 = tx as unknown as MutationTx;

    const result = await tx.execute(sql`
      DELETE FROM agent_role_secrets
       WHERE role_slug = ${params.roleSlug}
         AND env_var_name = ${params.envVarName}
         AND customer_id = ${customerId}
         AND secret_path = ${params.secretPath}
       RETURNING role_slug
    `);
    removed = result.length > 0;

    if (!removed) {
      // No row to remove — surface as not-found (FR-058 caller
      // surface; operators see "grant doesn't exist" rather
      // than a silent no-op).
      return;
    }

    await writeVaultMutationLog(tx2, {
      outcome: VaultWriteOutcome.GrantRemoved,
      secretPath: params.secretPath,
      customerId,
      actorUserId,
      metadata: {
        role_slug: params.roleSlug,
        env_var_name: params.envVarName,
      },
    });
    await emitPgNotify(tx2, 'work.vault.grant_removed', params.secretPath);
  });

  return { removed };
}

// ─── Internal helpers ─────────────────────────────────────────

function mapInfisicalError(err: unknown, op: 'create' | 'edit' | 'delete'): VaultError {
  const msg = err instanceof Error ? err.message : String(err);
  if (/already exists/i.test(msg)) {
    return new VaultError(VaultErrorKind.PathAlreadyExists, { op, source: msg });
  }
  if (/rate limit|429/i.test(msg)) {
    return new VaultError(VaultErrorKind.RateLimited, { op, source: msg });
  }
  if (/unauthor|401/.test(msg)) {
    return new VaultError(VaultErrorKind.AuthExpired, { op, source: msg });
  }
  if (/forbidden|403/.test(msg)) {
    return new VaultError(VaultErrorKind.PermissionDenied, { op, source: msg });
  }
  if (/not found|404/i.test(msg)) {
    return new VaultError(VaultErrorKind.SecretNotFound, { op, source: msg });
  }
  return new VaultError(VaultErrorKind.Unavailable, { op, source: msg });
}
