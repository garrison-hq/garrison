// pgNotify integration test — verifies the load-bearing
// invariant from FR-015 / SC-005 / Phase 0 research item 2:
// pg_notify issued inside a Drizzle (postgres-js) transaction
// becomes visible to LISTEN subscribers ONLY at COMMIT, not at
// SELECT pg_notify time.
//
// The activity feed depends on this property. If the supervisor
// or another LISTEN client could see speculative reads, it
// would render rows that the dashboard then ROLLBACKed —
// breaking audit-row consistency.
//
// Runs against the shared Postgres testcontainer booted by
// vitest's globalSetup.

import { describe, it, expect, beforeAll, afterAll } from 'vitest';
import postgres from 'postgres';
import { drizzle } from 'drizzle-orm/postgres-js';
import { emitPgNotify } from './pgNotify';
import type { MutationTx } from './eventOutbox';

let env: { TEST_SUPERUSER_DSN: string };

beforeAll(async () => {
  const { readFileSync } = await import('node:fs');
  const { resolve } = await import('node:path');
  const path = resolve(import.meta.dirname, '..', '..', 'tests', 'integration', '.harness', 'env.json');
  env = JSON.parse(readFileSync(path, 'utf-8'));
});

describe('lib/audit/pgNotify', () => {
  it('firesAtTransactionCommit_NotBefore: notify is invisible to LISTEN until COMMIT', async () => {
    const channel = `m4_test_${Date.now()}`;
    const writeClient = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    const writeDb = drizzle(writeClient);

    // Dedicated LISTEN connection on a separate client.
    const listenClient = postgres(env.TEST_SUPERUSER_DSN, {
      max: 1,
      onnotice: () => {},
    });

    const received: string[] = [];
    let resolveFirstNotify: () => void = () => {};
    const firstNotifyPromise = new Promise<void>((res) => {
      resolveFirstNotify = res;
    });

    await listenClient.listen(channel, (payload) => {
      received.push(payload);
      resolveFirstNotify();
    });

    let releaseTransaction: () => void = () => {};
    const transactionGate = new Promise<void>((res) => {
      releaseTransaction = res;
    });

    const txPromise = writeDb.transaction(async (tx) => {
      // Inside the transaction: emit the pg_notify.
      await emitPgNotify(tx as unknown as MutationTx, channel, 'payload-1');
      // Hold the transaction open for ~150ms while we assert
      // that LISTEN has NOT seen the notify yet.
      await new Promise<void>((res) => setTimeout(res, 50));
      // Assert mid-transaction:
      expect(received).toEqual([]);
      // Hold further until released.
      await transactionGate;
    });

    // Wait long enough that a faulty implementation (visible
    // before COMMIT) would have surfaced.
    await new Promise<void>((res) => setTimeout(res, 100));
    expect(received).toEqual([]);

    // Release the transaction; await its commit.
    releaseTransaction();
    await txPromise;

    // Now the LISTEN client should see the notification.
    await Promise.race([
      firstNotifyPromise,
      new Promise<never>((_, rej) => setTimeout(() => rej(new Error('LISTEN never received notify post-commit')), 2000)),
    ]);

    expect(received).toEqual(['payload-1']);

    await listenClient.end();
    await writeClient.end();
  });

  it('does NOT deliver if the transaction ROLLBACKs', async () => {
    const channel = `m4_rollback_test_${Date.now()}`;
    const writeClient = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    const writeDb = drizzle(writeClient);

    const listenClient = postgres(env.TEST_SUPERUSER_DSN, {
      max: 1,
      onnotice: () => {},
    });

    const received: string[] = [];
    await listenClient.listen(channel, (payload) => {
      received.push(payload);
    });

    await expect(
      writeDb.transaction(async (tx) => {
        await emitPgNotify(tx as unknown as MutationTx, channel, 'should-not-arrive');
        throw new Error('rollback');
      }),
    ).rejects.toThrow('rollback');

    // Wait long enough that any leak would have surfaced.
    await new Promise<void>((res) => setTimeout(res, 200));
    expect(received).toEqual([]);

    await listenClient.end();
    await writeClient.end();
  });
});
