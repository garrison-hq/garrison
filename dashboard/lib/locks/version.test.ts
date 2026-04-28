// version.test.ts — optimistic-locking helper integration test.
// Runs against the harness Postgres container; uses secret_metadata
// (which has the updated_at column M2.3 introduced) as the version-
// token target. Real M4 callers will pass the same table-and-column
// references for secret_metadata (T007 onwards) and — once a future
// migration adds agents.updated_at — for the agents table (T013).

import { describe, it, expect, beforeAll, afterEach } from 'vitest';
import postgres from 'postgres';
import { drizzle } from 'drizzle-orm/postgres-js';
import { secretMetadata } from '@/drizzle/schema.supervisor';
import { checkAndUpdate } from './version';
import type { MutationTx } from '@/lib/audit/eventOutbox';

let env: { TEST_SUPERUSER_DSN: string };

beforeAll(async () => {
  const { readFileSync } = await import('node:fs');
  const { resolve } = await import('node:path');
  const path = resolve(import.meta.dirname, '..', '..', 'tests', 'integration', '.harness', 'env.json');
  env = JSON.parse(readFileSync(path, 'utf-8'));
});

afterEach(async () => {
  const client = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
  try {
    await client`DELETE FROM secret_metadata WHERE secret_path LIKE '/m4-locktest-%'`;
  } finally {
    await client.end();
  }
});

const TEST_CUSTOMER_ID = '00000000-0000-0000-0000-000000004001';

async function seedSecret(client: postgres.Sql, suffix: string): Promise<{ secretPath: string; updatedAt: string }> {
  const path = `/m4-locktest-${suffix}/operator/test_key`;
  const rows = await client<{ secret_path: string; updated_at: string }[]>`
    INSERT INTO secret_metadata (
      secret_path, customer_id, provenance, rotation_cadence, rotation_provider,
      created_at, updated_at, allowed_role_slugs
    )
    VALUES (
      ${path},
      ${TEST_CUSTOMER_ID},
      'operator_entered',
      INTERVAL '90 days',
      'manual_paste',
      now(),
      now(),
      ARRAY[]::text[]
    )
    RETURNING secret_path, updated_at
  `;
  return { secretPath: rows[0].secret_path, updatedAt: rows[0].updated_at };
}

describe('lib/locks/version', () => {
  it('checkAndUpdate returns accepted on matching versionToken', async () => {
    const client = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    const db = drizzle(client);
    try {
      const { secretPath, updatedAt } = await seedSecret(client, 'a');

      const result = await db.transaction(async (tx) => {
        return checkAndUpdate(tx as unknown as MutationTx, {
            table: secretMetadata,
            pkColumn: secretMetadata.secretPath,
            updatedAtColumn: secretMetadata.updatedAt,
            updatedAtFieldName: 'updatedAt',
            idValue: secretPath,
            expectedVersionToken: updatedAt,
            changes: { provenance: 'oauth_flow' },
          });
      });
      expect(result.accepted).toBe(true);
      if (result.accepted) {
        expect(result.newVersionToken).not.toBe(updatedAt);
      }
    } finally {
      await client.end();
    }
  });

  it('checkAndUpdate returns serverState on mismatched versionToken', async () => {
    const client = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    const db = drizzle(client);
    try {
      const { secretPath } = await seedSecret(client, 'b');
      const stale = '2020-01-01T00:00:00.000Z';

      const result = await db.transaction(async (tx) => {
        return checkAndUpdate(tx as unknown as MutationTx, {
            table: secretMetadata,
            pkColumn: secretMetadata.secretPath,
            updatedAtColumn: secretMetadata.updatedAt,
            updatedAtFieldName: 'updatedAt',
            idValue: secretPath,
            expectedVersionToken: stale,
            changes: { provenance: 'oauth_flow' },
          });
      });
      expect(result.accepted).toBe(false);
      if (!result.accepted) {
        expect(result.serverState).not.toBeNull();
      }
    } finally {
      await client.end();
    }
  });

  it('rejects changes that include an updated_at field (helper bumps it automatically)', async () => {
    const client = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    const db = drizzle(client);
    try {
      const { secretPath, updatedAt } = await seedSecret(client, 'c');
      await expect(
        db.transaction(async (tx) => {
          await checkAndUpdate(tx as unknown as MutationTx, {
            table: secretMetadata,
            pkColumn: secretMetadata.secretPath,
            updatedAtColumn: secretMetadata.updatedAt,
            updatedAtFieldName: 'updatedAt',
            idValue: secretPath,
            expectedVersionToken: updatedAt,
            changes: { updatedAt: new Date().toISOString() },
          });
        }),
      ).rejects.toThrow(/updatedAt/);
    } finally {
      await client.end();
    }
  });

  it('two operators with the same stale token: only the first wins, the second is rejected', async () => {
    // Simulates the operator-side conflict scenario: operator A
    // and operator B both load the same row at the same time
    // (both get version token X). A submits first; B submits
    // second. A's UPDATE matches WHERE updated_at = X and
    // succeeds; B's UPDATE — because A's commit advanced
    // updated_at to Y — matches zero rows and returns the
    // serverState carrying A's changes.
    //
    // (A truly-concurrent variant of this is exercised in T018's
    // Playwright two-browser-context conflict test against a
    // built dashboard. The unit-level sequential simulation
    // proves the same correctness property at the helper layer.)
    const client = postgres(env.TEST_SUPERUSER_DSN, { max: 5 });
    const db = drizzle(client);
    try {
      const { secretPath, updatedAt } = await seedSecret(client, 'd');

      const op = (provenance: string) =>
        db.transaction(async (tx) => {
          return checkAndUpdate(tx as unknown as MutationTx, {
            table: secretMetadata,
            pkColumn: secretMetadata.secretPath,
            updatedAtColumn: secretMetadata.updatedAt,
            updatedAtFieldName: 'updatedAt',
            idValue: secretPath,
            expectedVersionToken: updatedAt,
            changes: { provenance },
          });
        });

      // Sequential — operator A wins.
      const r1 = await op('oauth_flow');
      const r2 = await op('environment_bootstrap');

      expect(r1.accepted).toBe(true);
      expect(r2.accepted).toBe(false);
      if (!r2.accepted) {
        expect(r2.serverState).not.toBeNull();
      }
    } finally {
      await client.end();
    }
  });
});
