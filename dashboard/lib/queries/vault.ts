// Vault read views (FR-080 → FR-084).
//
// === SECURITY-CRITICAL FILE ===
//
// Every export in this file MUST connect to Postgres through
// vaultRoDb (DASHBOARD_RO_DSN → garrison_dashboard_ro). The
// dashboard's primary appDb authenticates as garrison_dashboard_app
// — a separate role with NO grants on the vault tables. The
// invariant is enforced at the Postgres layer (the role denies
// SELECT on agent_role_secrets / vault_access_log /
// secret_metadata) AND at the application layer by static
// analysis: the vault.test.ts pin verifies this file imports only
// vaultRoDb, never appDb.
//
// Per spec FR-084, no surface anywhere in the dashboard exposes a
// path to read or copy a secret value. The schema doesn't carry
// values — only paths, role-grant relations, rotation metadata,
// and audit-log outcomes. Any future column that surfaces a value
// MUST be filtered out here BEFORE the data reaches a component.

import { sql } from 'drizzle-orm';
import { vaultRoDb } from '@/lib/db/vaultRoClient';

export interface SecretMetadataRow {
  secretPath: string;
  customerId: string;
  provenance: string;
  rotationCadence: string;
  lastRotatedAt: Date | null;
  /** Status string derived from lastRotatedAt + rotationCadence. */
  rotationStatus: 'fresh' | 'aging' | 'overdue' | 'never';
  allowedRoleSlugs: string[];
}

export interface VaultAuditFilter {
  roleSlug?: string;
  ticketId?: string;
  since?: Date;
  until?: Date;
  cursor?: string;
  limit?: number;
}

export interface VaultAuditRow {
  id: string;
  agentInstanceId: string;
  ticketId: string | null;
  secretPath: string;
  outcome: string;
  timestamp: Date;
  roleSlug: string;
}

export interface MatrixCell {
  roleSlug: string;
  secretPath: string;
}

function classifyRotation(
  lastRotatedAt: Date | null,
  cadence: string,
): SecretMetadataRow['rotationStatus'] {
  if (!lastRotatedAt) return 'never';
  // Cadence comes in as ISO interval text (e.g. "90 days"). The
  // regex is bounded with character classes that don't overlap
  // (\d vs space) so it can't backtrack super-linearly; the
  // fallback `?? '90'` defaults to the migration-baked
  // rotation_cadence DEFAULT.
  const cadenceMatch = /^(\d+) /.exec(cadence);
  const cadenceDays = Number(cadenceMatch?.[1] ?? '90');
  const ageDays = (Date.now() - new Date(lastRotatedAt).getTime()) / (1000 * 60 * 60 * 24);
  if (ageDays > cadenceDays) return 'overdue';
  if (ageDays > cadenceDays * 0.8) return 'aging';
  return 'fresh';
}

export async function fetchSecretsList(): Promise<SecretMetadataRow[]> {
  const rows = await vaultRoDb.execute<{
    secret_path: string;
    customer_id: string;
    provenance: string;
    rotation_cadence: string;
    last_rotated_at: Date | null;
    allowed_role_slugs: string[];
  }>(sql`
    SELECT secret_path, customer_id, provenance, rotation_cadence::text AS rotation_cadence,
           last_rotated_at, allowed_role_slugs
      FROM secret_metadata
     ORDER BY secret_path ASC
  `);
  return rows.map((r) => ({
    secretPath: r.secret_path,
    customerId: r.customer_id,
    provenance: r.provenance,
    rotationCadence: r.rotation_cadence,
    lastRotatedAt: r.last_rotated_at,
    rotationStatus: classifyRotation(r.last_rotated_at, r.rotation_cadence),
    allowedRoleSlugs: r.allowed_role_slugs ?? [],
  }));
}

export async function fetchAuditLog(filter: VaultAuditFilter = {}): Promise<{
  rows: VaultAuditRow[];
  hasMore: boolean;
}> {
  const limit = Math.max(1, Math.min(200, filter.limit ?? 50));
  const rows = await vaultRoDb.execute<{
    id: string;
    agent_instance_id: string;
    ticket_id: string | null;
    secret_path: string;
    outcome: string;
    timestamp: Date;
    role_slug: string;
  }>(sql`
    SELECT vl.id, vl.agent_instance_id, vl.ticket_id, vl.secret_path, vl.outcome,
           vl.timestamp, ai.role_slug
      FROM vault_access_log vl
      LEFT JOIN agent_instances ai ON ai.id = vl.agent_instance_id
     WHERE 1=1
       ${filter.roleSlug ? sql`AND ai.role_slug = ${filter.roleSlug}` : sql``}
       ${filter.ticketId ? sql`AND vl.ticket_id = ${filter.ticketId}` : sql``}
       ${filter.since ? sql`AND vl.timestamp >= ${filter.since}` : sql``}
       ${filter.until ? sql`AND vl.timestamp <= ${filter.until}` : sql``}
       ${filter.cursor ? sql`AND vl.id < ${filter.cursor}` : sql``}
     ORDER BY vl.timestamp DESC, vl.id DESC
     LIMIT ${limit + 1}
  `);
  const hasMore = rows.length > limit;
  return {
    rows: rows.slice(0, limit).map((r) => ({
      id: r.id,
      agentInstanceId: r.agent_instance_id,
      ticketId: r.ticket_id,
      secretPath: r.secret_path,
      outcome: r.outcome,
      timestamp: r.timestamp,
      roleSlug: r.role_slug,
    })),
    hasMore,
  };
}

export async function fetchRoleSecretMatrix(): Promise<{
  roles: string[];
  secrets: string[];
  cells: MatrixCell[];
}> {
  const grants = await vaultRoDb.execute<{
    role_slug: string;
    secret_path: string;
  }>(sql`
    SELECT DISTINCT role_slug, secret_path
      FROM agent_role_secrets
  `);
  const rolesSet = new Set<string>();
  const secretsSet = new Set<string>();
  const cells: MatrixCell[] = grants.map((g) => {
    rolesSet.add(g.role_slug);
    secretsSet.add(g.secret_path);
    return { roleSlug: g.role_slug, secretPath: g.secret_path };
  });
  const cmp = (a: string, b: string) => a.localeCompare(b);
  return {
    roles: Array.from(rolesSet).sort(cmp),
    secrets: Array.from(secretsSet).sort(cmp),
    cells,
  };
}
