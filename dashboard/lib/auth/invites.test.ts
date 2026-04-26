// Unit tests for the invite flow. Run against a fresh Postgres
// testcontainer booted in vitest's globalSetup. The shared
// container is reused across test files; each test seeds its own
// inviter via a direct insert and tears down its rows in afterEach.
//
// The 9 tests below match the names in plan.md §"Test strategy >
// unit tests > `lib/auth/invites.test.ts`" verbatim — including the
// concurrent-redemptions race test, which exercises the atomic
// UPDATE ... WHERE redeemed_at IS NULL pattern with two
// Promise.allSettled calls.

import { describe, it, expect, beforeAll, afterEach, vi } from 'vitest';
import postgres from 'postgres';

let env: { DASHBOARD_APP_DSN: string; TEST_SUPERUSER_DSN: string };

beforeAll(async () => {
  // Read the env file the harness produced (vitest globalSetup
  // boots the container; see tests/integration/_harness.ts).
  const { readFileSync } = await import('node:fs');
  const { resolve } = await import('node:path');
  const path = resolve(import.meta.dirname, '..', '..', 'tests', 'integration', '.harness', 'env.json');
  const raw = readFileSync(path, 'utf-8');
  env = JSON.parse(raw);
  process.env.DASHBOARD_APP_DSN = env.DASHBOARD_APP_DSN;
  process.env.DASHBOARD_RO_DSN = env.DASHBOARD_APP_DSN;
  process.env.BETTER_AUTH_SECRET = 'unit_test_secret_long_enough_xxxxxxxxxxxxxxxxxxxx';
});

afterEach(async () => {
  const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
  try {
    await sql`TRUNCATE users, sessions, accounts, verifications, operator_invites RESTART IDENTITY CASCADE`;
  } finally {
    await sql.end();
  }
});

async function seedInviter(): Promise<string> {
  const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
  try {
    const rows = await sql<{ id: string }[]>`
      INSERT INTO users (email, email_verified, name, theme_preference)
      VALUES ('inviter@example.com', true, 'Inviter', 'system')
      RETURNING id
    `;
    return rows[0].id;
  } finally {
    await sql.end();
  }
}

describe('lib/auth/invites', () => {
  it('generateInvite produces a token of expected length and a future expires_at', async () => {
    const { generateInvite, INVITE_TTL_MS } = await import('./invites');
    const inviterId = await seedInviter();
    const before = Date.now();
    const { token, expiresAt } = await generateInvite(inviterId);
    expect(token.length).toBeGreaterThanOrEqual(40); // 32-byte base64url
    expect(expiresAt.getTime()).toBeGreaterThanOrEqual(before + INVITE_TTL_MS - 5_000);
    expect(expiresAt.getTime()).toBeLessThanOrEqual(before + INVITE_TTL_MS + 5_000);
  });

  it('redeemInvite accepts a valid unredeemed unrevoked unexpired token', async () => {
    const { generateInvite, redeemInvite } = await import('./invites');
    const inviterId = await seedInviter();
    const { token } = await generateInvite(inviterId);
    const result = await redeemInvite(token, 'New One', 'goodPassword123', 'new@example.com');
    expect(result.userId).toMatch(/^[0-9a-f-]{36}$/);
    expect(typeof result.sessionToken).toBe('string');
    expect(result.sessionToken.length).toBeGreaterThan(0);
  });

  it('redeemInvite rejects an expired token with InviteExpired', async () => {
    const { redeemInvite } = await import('./invites');
    const { AuthErrorKind } = await import('./errors');
    const inviterId = await seedInviter();
    // Insert directly with expires_at in the past so the
    // generated default isn't used.
    const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    try {
      await sql`
        INSERT INTO operator_invites (token, created_by_user_id, expires_at)
        VALUES ('expired-tok', ${inviterId}, now() - interval '1 hour')
      `;
    } finally {
      await sql.end();
    }
    await expect(
      redeemInvite('expired-tok', 'X', 'goodPassword123', 'x@example.com'),
    ).rejects.toMatchObject({ kind: AuthErrorKind.InviteExpired });
  });

  it('redeemInvite rejects a revoked token with InviteRevoked', async () => {
    const { generateInvite, revokeInvite, redeemInvite } = await import('./invites');
    const { AuthErrorKind } = await import('./errors');
    const inviterId = await seedInviter();
    const { token } = await generateInvite(inviterId);
    const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    try {
      const rows = await sql<{ id: string }[]>`SELECT id FROM operator_invites WHERE token = ${token}`;
      await revokeInvite(rows[0].id);
    } finally {
      await sql.end();
    }
    await expect(
      redeemInvite(token, 'X', 'goodPassword123', 'x@example.com'),
    ).rejects.toMatchObject({ kind: AuthErrorKind.InviteRevoked });
  });

  it('redeemInvite rejects an already-redeemed token with InviteAlreadyRedeemed', async () => {
    const { generateInvite, redeemInvite } = await import('./invites');
    const { AuthErrorKind } = await import('./errors');
    const inviterId = await seedInviter();
    const { token } = await generateInvite(inviterId);
    await redeemInvite(token, 'First', 'goodPassword123', 'first@example.com');
    await expect(
      redeemInvite(token, 'Second', 'goodPassword123', 'second@example.com'),
    ).rejects.toMatchObject({ kind: AuthErrorKind.InviteAlreadyRedeemed });
  });

  it('redeemInvite rejects an unknown token with InviteNotFound', async () => {
    const { redeemInvite } = await import('./invites');
    const { AuthErrorKind } = await import('./errors');
    await expect(
      redeemInvite('unknown-token', 'X', 'goodPassword123', 'x@example.com'),
    ).rejects.toMatchObject({ kind: AuthErrorKind.InviteNotFound });
  });

  it('redeemInvite is atomic — concurrent redemptions of the same token produce exactly one success', async () => {
    const { generateInvite, redeemInvite } = await import('./invites');
    const { AuthError, AuthErrorKind } = await import('./errors');
    const inviterId = await seedInviter();
    const { token } = await generateInvite(inviterId);

    const results = await Promise.allSettled([
      redeemInvite(token, 'A', 'goodPassword123', 'a@example.com'),
      redeemInvite(token, 'B', 'goodPassword123', 'b@example.com'),
    ]);

    const fulfilled = results.filter((r) => r.status === 'fulfilled');
    const rejected = results.filter((r) => r.status === 'rejected');
    expect(fulfilled).toHaveLength(1);
    expect(rejected).toHaveLength(1);
    const err = (rejected[0] as PromiseRejectedResult).reason;
    expect(err).toBeInstanceOf(AuthError);
    expect((err as InstanceType<typeof AuthError>).kind).toBe(
      AuthErrorKind.InviteAlreadyRedeemed,
    );
  });

  it('revokeInvite is idempotent — revoking a revoked invite is a no-op', async () => {
    const { generateInvite, revokeInvite } = await import('./invites');
    const inviterId = await seedInviter();
    await generateInvite(inviterId);

    const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    try {
      const rows = await sql<{ id: string }[]>`SELECT id FROM operator_invites LIMIT 1`;
      const id = rows[0].id;
      await revokeInvite(id);
      const firstRevoke = await sql<{ revoked_at: Date }[]>`
        SELECT revoked_at FROM operator_invites WHERE id = ${id}
      `;
      const firstStamp = firstRevoke[0].revoked_at;
      // Second call should not change revoked_at (the WHERE clause
      // guards against double-update).
      await revokeInvite(id);
      const secondRevoke = await sql<{ revoked_at: Date }[]>`
        SELECT revoked_at FROM operator_invites WHERE id = ${id}
      `;
      expect(secondRevoke[0].revoked_at?.toISOString()).toBe(firstStamp?.toISOString());
    } finally {
      await sql.end();
    }
  });

  it('listPendingInvites excludes revoked, redeemed, and expired invites', async () => {
    const { generateInvite, revokeInvite, redeemInvite, listPendingInvites } = await import('./invites');
    const inviterId = await seedInviter();

    // Pending: should appear.
    const pending = await generateInvite(inviterId);

    // Revoked: should not appear.
    const toRevoke = await generateInvite(inviterId);
    const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    try {
      const rows = await sql<{ id: string }[]>`SELECT id FROM operator_invites WHERE token = ${toRevoke.token}`;
      await revokeInvite(rows[0].id);
    } finally {
      await sql.end();
    }

    // Redeemed: should not appear.
    const toRedeem = await generateInvite(inviterId);
    await redeemInvite(toRedeem.token, 'R', 'goodPassword123', 'r@example.com');

    // Expired: should not appear.
    const sql2 = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    try {
      await sql2`
        INSERT INTO operator_invites (token, created_by_user_id, expires_at)
        VALUES ('expired-list', ${inviterId}, now() - interval '1 hour')
      `;
    } finally {
      await sql2.end();
    }

    const list = await listPendingInvites();
    const tokens = list.map((r) => r.token);
    expect(tokens).toContain(pending.token);
    expect(tokens).not.toContain(toRevoke.token);
    expect(tokens).not.toContain(toRedeem.token);
    expect(tokens).not.toContain('expired-list');
  });
});
