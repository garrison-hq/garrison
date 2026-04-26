// Smoke test confirming the three DB-client modules compile and
// export the expected shapes, plus exercise the lazy-init factory
// bodies and the missing-DSN error branches. Real query behaviour
// is exercised by per-feature query tests (T010 onwards) and the
// integration suites under tests/integration/.

import { describe, it, expect, beforeAll, vi } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

let env: { DASHBOARD_APP_DSN: string };

beforeAll(() => {
  const path = resolve(import.meta.dirname, '..', '..', 'tests', 'integration', '.harness', 'env.json');
  env = JSON.parse(readFileSync(path, 'utf-8'));
  process.env.DASHBOARD_APP_DSN = env.DASHBOARD_APP_DSN;
  process.env.DASHBOARD_RO_DSN = env.DASHBOARD_APP_DSN;
});

describe('db clients', () => {
  it('TestAllThreeClientsCompileAndExport', async () => {
    const { appDb } = await import('./appClient');
    const { vaultRoDb } = await import('./vaultRoClient');
    const { makeListenClient } = await import('./listenClient');

    expect(appDb).toBeDefined();
    expect(typeof appDb).toBe('object');

    expect(vaultRoDb).toBeDefined();
    expect(typeof vaultRoDb).toBe('object');

    expect(typeof makeListenClient).toBe('function');
  });

  it('appDb proxy lazy-inits and runs a real query', async () => {
    const { appDb } = await import('./appClient');
    const { sql } = await import('drizzle-orm');
    const result = await appDb.execute<{ ok: number }>(sql`SELECT 1 AS ok`);
    expect(result[0]?.ok).toBe(1);
  });

  it('vaultRoDb proxy lazy-inits and runs a real query', async () => {
    const { vaultRoDb } = await import('./vaultRoClient');
    const { sql } = await import('drizzle-orm');
    const result = await vaultRoDb.execute<{ ok: number }>(sql`SELECT 1 AS ok`);
    expect(result[0]?.ok).toBe(1);
  });

  it('makeListenClient returns a usable postgres-js client', async () => {
    const { makeListenClient } = await import('./listenClient');
    const client = makeListenClient();
    try {
      const rows = await client<{ ok: number }[]>`SELECT 1 AS ok`;
      expect(rows[0]?.ok).toBe(1);
    } finally {
      await client.end();
    }
  });

  it('appDb throws a descriptive error when DASHBOARD_APP_DSN is unset', async () => {
    const previous = process.env.DASHBOARD_APP_DSN;
    delete process.env.DASHBOARD_APP_DSN;
    try {
      vi.resetModules();
      const mod = await import('./appClient');
      const { sql } = await import('drizzle-orm');
      expect(() => mod.appDb.execute(sql`SELECT 1`)).toThrow(/DASHBOARD_APP_DSN is unset/);
    } finally {
      process.env.DASHBOARD_APP_DSN = previous;
      vi.resetModules();
    }
  });

  it('vaultRoDb throws a descriptive error when DASHBOARD_RO_DSN is unset', async () => {
    const previous = process.env.DASHBOARD_RO_DSN;
    delete process.env.DASHBOARD_RO_DSN;
    try {
      vi.resetModules();
      const mod = await import('./vaultRoClient');
      const { sql } = await import('drizzle-orm');
      expect(() => mod.vaultRoDb.execute(sql`SELECT 1`)).toThrow(/DASHBOARD_RO_DSN is unset/);
    } finally {
      process.env.DASHBOARD_RO_DSN = previous;
      vi.resetModules();
    }
  });

  it('makeListenClient throws a descriptive error when DASHBOARD_APP_DSN is unset', async () => {
    const previous = process.env.DASHBOARD_APP_DSN;
    delete process.env.DASHBOARD_APP_DSN;
    try {
      vi.resetModules();
      const mod = await import('./listenClient');
      expect(() => mod.makeListenClient()).toThrow(/DASHBOARD_APP_DSN is unset/);
    } finally {
      process.env.DASHBOARD_APP_DSN = previous;
      vi.resetModules();
    }
  });
});
