// Smoke test confirming the three DB-client modules compile and
// export the expected shapes. Real query behaviour is exercised by
// per-feature query tests (T010 onwards) and the integration
// suites under tests/integration/.
//
// Each client throws on missing DSN at import time, so the test
// sets harmless DSNs before importing.

import { describe, it, expect, beforeAll } from 'vitest';

beforeAll(() => {
  process.env.DASHBOARD_APP_DSN ??= 'postgres://test:test@localhost:5432/testdb?sslmode=disable';
  process.env.DASHBOARD_RO_DSN ??= 'postgres://test:test@localhost:5432/testdb?sslmode=disable';
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
    // makeListenClient() opens a real socket; we only assert the
    // factory shape here to keep this a pure compile-time smoke.
  });
});
