import { describe, it, expect } from 'vitest';
import { writeVaultMutationLog, VaultAuditLeakError, VaultWriteOutcome } from './vaultAccessLog';

// Unit-level test of the leak-scan invariant. The integration
// tests (T017's mutation-audit-rule6.spec.ts) cover the full
// transactional path against a Postgres testcontainer; these
// unit tests pin the shape-scan behavior so a failure in the
// helper surfaces locally.

function fakeTx(insertCalls: Array<{ values: Record<string, unknown> }>): unknown {
  return {
    insert(_table: unknown) {
      return {
        values(values: Record<string, unknown>) {
          insertCalls.push({ values });
          return Promise.resolve();
        },
      };
    },
    execute(_query: unknown) {
      return Promise.resolve();
    },
  };
}

describe('lib/audit/vaultAccessLog', () => {
  it('writeVaultMutationLog persists the row when metadata is clean', async () => {
    const calls: Array<{ values: Record<string, unknown> }> = [];
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    await writeVaultMutationLog(fakeTx(calls) as any, {
      outcome: VaultWriteOutcome.SecretCreated,
      secretPath: '/cust/operator/stripe_key',
      customerId: '00000000-0000-0000-0000-000000000001',
      actorUserId: '00000000-0000-0000-0000-000000000abc',
      metadata: { changed_fields: ['name', 'value'] },
    });
    expect(calls).toHaveLength(1);
    const inserted = calls[0].values;
    expect(inserted).toMatchObject({
      agentInstanceId: null,
      ticketId: null,
      secretPath: '/cust/operator/stripe_key',
      outcome: 'secret_created',
    });
    const meta = inserted.metadata as Record<string, unknown>;
    expect(meta.actor_user_id).toBe('00000000-0000-0000-0000-000000000abc');
    expect(meta.changed_fields).toEqual(['name', 'value']);
  });

  it('rejects metadata containing an sk-prefix string (Rule 6 backstop)', async () => {
    const calls: Array<{ values: Record<string, unknown> }> = [];
    await expect(
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      writeVaultMutationLog(fakeTx(calls) as any, {
        outcome: VaultWriteOutcome.SecretEdited,
        secretPath: '/cust/operator/x',
        customerId: '00000000-0000-0000-0000-000000000001',
        actorUserId: '00000000-0000-0000-0000-000000000abc',
        metadata: { accidental: 'sk-test_aaaaaaaaaaaaaaaaaaaaaaaa' },
      }),
    ).rejects.toBeInstanceOf(VaultAuditLeakError);
    expect(calls).toHaveLength(0);
  });

  it('rejects metadata containing an AWS AKIA prefix', async () => {
    const calls: Array<{ values: Record<string, unknown> }> = [];
    await expect(
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      writeVaultMutationLog(fakeTx(calls) as any, {
        outcome: VaultWriteOutcome.SecretEdited,
        secretPath: '/cust/operator/aws',
        customerId: '00000000-0000-0000-0000-000000000001',
        actorUserId: '00000000-0000-0000-0000-000000000abc',
        metadata: { mistake: 'AKIAIOSFODNN7EXAMPLE' },
      }),
    ).rejects.toBeInstanceOf(VaultAuditLeakError);
    expect(calls).toHaveLength(0);
  });

  it('rejects metadata containing a PEM header anywhere in the JSON tree', async () => {
    const calls: Array<{ values: Record<string, unknown> }> = [];
    await expect(
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      writeVaultMutationLog(fakeTx(calls) as any, {
        outcome: VaultWriteOutcome.RotationCompleted,
        secretPath: '/cust/operator/cert',
        customerId: '00000000-0000-0000-0000-000000000001',
        actorUserId: '00000000-0000-0000-0000-000000000abc',
        metadata: {
          nested: { deep: { value: '-----BEGIN PRIVATE KEY-----' } },
        },
      }),
    ).rejects.toBeInstanceOf(VaultAuditLeakError);
    expect(calls).toHaveLength(0);
  });

  it('persists when metadata is empty (no scanning false positives)', async () => {
    const calls: Array<{ values: Record<string, unknown> }> = [];
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    await writeVaultMutationLog(fakeTx(calls) as any, {
      outcome: VaultWriteOutcome.ValueRevealed,
      secretPath: '/cust/operator/api',
      customerId: '00000000-0000-0000-0000-000000000001',
      actorUserId: '00000000-0000-0000-0000-000000000abc',
    });
    expect(calls).toHaveLength(1);
  });
});
