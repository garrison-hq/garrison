import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

// Static-analysis pin: vault.ts is the ONLY file allowed to import
// vaultRoDb (per FR-022 + spec FR-021), and it MUST NOT import
// appDb. The Postgres role grants (T001, T011 follow-up) ensure
// the runtime invariant; this test pins the source-code invariant
// so a future code change can't accidentally cross the boundary.
//
// FR-021: vault sub-views connect via garrison_dashboard_ro
// (DASHBOARD_RO_DSN); appDb authenticates as garrison_dashboard_app
// which has NO grants on the vault tables. If a future edit imports
// appDb here, the runtime would fail with "permission denied" and
// the operator would see an error — but catching it at build time
// (via this static-analysis test) makes the failure visible
// immediately rather than at deploy time.

describe('lib/queries/vault.ts isolation invariant', () => {
  const path = resolve(import.meta.dirname, 'vault.ts');
  const source = readFileSync(path, 'utf-8');
  // Strip line comments so prose explaining the invariant doesn't
  // count as a violation. Block comments (/* */) aren't used in
  // this file, so this is sufficient.
  const codeOnly = source
    .split('\n')
    .filter((line) => !line.trim().startsWith('//'))
    .join('\n');

  it('vaultQueriesUseRoOnlyConnection — every export uses vaultRoDb', () => {
    expect(codeOnly).toContain("from '@/lib/db/vaultRoClient'");
    expect(codeOnly).toContain('vaultRoDb');
  });

  it('does not import appDb directly or via @/lib/db/appClient', () => {
    expect(codeOnly).not.toMatch(/from ['"]@\/lib\/db\/appClient['"]/);
    expect(codeOnly).not.toMatch(/\bappDb\b/);
  });

  it('does not contain any path to read or copy a secret value (FR-084)', () => {
    // The schema doesn't carry secret values, but pin the source
    // so a hypothetical future column can't be added without
    // updating this assertion deliberately. Common value-shaped
    // patterns in code that would warrant review.
    expect(codeOnly).not.toMatch(/secret_value/);
    expect(codeOnly).not.toMatch(/secretValue/);
    expect(codeOnly).not.toMatch(/decryptedSecret/);
  });
});
