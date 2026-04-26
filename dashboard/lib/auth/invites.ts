// Operator invite flow.
//
// Implementation choice: hand-rolled SQL+API path (rather than the
// hypothetical first-class better-auth invite plugin from plan
// §"Phase 0 research item 1"). better-auth 1.6.9 does not ship an
// invite plugin in its core; the organisation plugin's invite flow
// is a different surface (multi-org membership) and would carry
// concepts M3 doesn't have. The hand-rolled path here uses
// better-auth ONLY for user/session creation and owns the invite
// lifecycle end-to-end.
//
// Atomicity: `redeemInvite` claims the invite via a single
// UPDATE … WHERE redeemed_at IS NULL AND revoked_at IS NULL AND
// expires_at > now() RETURNING id. Postgres MVCC guarantees exactly
// one of N concurrent redemptions sees the row in the redeemable
// state; the others get 0 rows back and exit with the appropriate
// AuthError after a follow-up classification SELECT.
// better-auth user creation runs after the claim; if it fails, the
// claim is rolled back (redeemed_at set back to NULL) so the invite
// can be retried.

import { randomBytes } from 'node:crypto';
import { eq, and, isNull, gt, sql } from 'drizzle-orm';
import { appDb } from '@/lib/db/appClient';
import { operatorInvites, users } from '@/drizzle/schema.dashboard';
import { auth } from '@/lib/auth';
import { AuthError, AuthErrorKind } from './errors';

/** 72 hours in milliseconds. The default invite TTL. */
export const INVITE_TTL_MS = 72 * 60 * 60 * 1000;

export type InviteRow = typeof operatorInvites.$inferSelect;

/**
 * Generate a one-time invite token bound to the calling operator.
 * The token is 32 bytes from `crypto.randomBytes` encoded as
 * base64url (URL-safe + no padding) — long enough to make
 * brute-force redemption infeasible against the bounded
 * operator-pool size.
 */
export async function generateInvite(
  creatorUserId: string,
): Promise<{ token: string; expiresAt: Date }> {
  const token = randomBytes(32).toString('base64url');
  const expiresAt = new Date(Date.now() + INVITE_TTL_MS);
  await appDb.insert(operatorInvites).values({
    token,
    createdByUserId: creatorUserId,
    expiresAt,
  });
  return { token, expiresAt };
}

/**
 * List unredeemed, unrevoked, unexpired invites in descending
 * created_at order. Powers the admin/invites surface.
 */
export async function listPendingInvites(): Promise<InviteRow[]> {
  return appDb
    .select()
    .from(operatorInvites)
    .where(
      and(
        isNull(operatorInvites.revokedAt),
        isNull(operatorInvites.redeemedAt),
        gt(operatorInvites.expiresAt, new Date()),
      ),
    )
    .orderBy(sql`${operatorInvites.createdAt} desc`);
}

/**
 * Mark an invite as revoked. Idempotent — revoking an already-
 * revoked invite is a no-op. Returns silently in both cases. Does
 * NOT revoke an already-redeemed invite (the redemption stands).
 */
export async function revokeInvite(inviteId: string): Promise<void> {
  await appDb
    .update(operatorInvites)
    .set({ revokedAt: new Date() })
    .where(
      and(
        eq(operatorInvites.id, inviteId),
        isNull(operatorInvites.revokedAt),
        isNull(operatorInvites.redeemedAt),
      ),
    );
}

/**
 * Atomically redeem a token, create the operator account, and log
 * the new operator in. Concurrent redemption attempts of the same
 * token serialize via Postgres MVCC: only one UPDATE finds the
 * invite in the (unredeemed, unrevoked, unexpired) state.
 *
 * Throws an AuthError with one of:
 *   InviteNotFound — token doesn't match any row
 *   InviteRevoked — revoked_at is set
 *   InviteAlreadyRedeemed — redeemed_at is set
 *   InviteExpired — expires_at is past
 *   EmailAlreadyExists — better-auth rejected the email
 */
export async function redeemInvite(
  token: string,
  name: string,
  password: string,
  email: string,
): Promise<{ userId: string; sessionToken: string; cookies: Headers }> {
  // Step 1: claim the invite atomically.
  const claimed = await appDb
    .update(operatorInvites)
    .set({ redeemedAt: new Date() })
    .where(
      and(
        eq(operatorInvites.token, token),
        isNull(operatorInvites.revokedAt),
        isNull(operatorInvites.redeemedAt),
        gt(operatorInvites.expiresAt, new Date()),
      ),
    )
    .returning({ id: operatorInvites.id });

  if (claimed.length === 0) {
    // Classify the failure with a follow-up SELECT. The diagnostic
    // SELECT is racy (the row could be modified between the failed
    // UPDATE and the SELECT), but the result is monotone — once a
    // row is revoked / redeemed / expired, it stays in that state.
    const lookup = await appDb
      .select()
      .from(operatorInvites)
      .where(eq(operatorInvites.token, token))
      .limit(1);
    if (lookup.length === 0) {
      throw new AuthError(AuthErrorKind.InviteNotFound);
    }
    const row = lookup[0];
    if (row.revokedAt) throw new AuthError(AuthErrorKind.InviteRevoked);
    if (row.redeemedAt) throw new AuthError(AuthErrorKind.InviteAlreadyRedeemed);
    if (row.expiresAt < new Date()) throw new AuthError(AuthErrorKind.InviteExpired);
    // Row exists but none of the failure predicates match — rare
    // race window where the diagnostic SELECT raced past a
    // concurrent rollback. Surface as AlreadyRedeemed: the user
    // can retry the original token if they re-fetch state.
    throw new AuthError(AuthErrorKind.InviteAlreadyRedeemed);
  }

  const inviteId = claimed[0].id;

  // Step 2: create the user via better-auth. better-auth manages
  // its own connection; on failure we roll back the claim so the
  // invite can be retried (typed errors only — the link itself is
  // still good).
  let userId: string;
  let sessionToken: string;
  let cookies: Headers;
  try {
    const result = await auth.api.signUpEmail({
      body: { email, name, password },
      returnHeaders: true,
    });
    userId = result.response.user.id;
    if (!result.response.token) {
      throw new Error('better-auth signUpEmail returned no session token');
    }
    sessionToken = result.response.token;
    cookies = result.headers ?? new Headers();
  } catch (err) {
    await appDb
      .update(operatorInvites)
      .set({ redeemedAt: null })
      .where(eq(operatorInvites.id, inviteId));
    if (err instanceof Error && /exist/i.test(err.message)) {
      throw new AuthError(AuthErrorKind.EmailAlreadyExists, err.message);
    }
    throw err;
  }

  // Step 3: stamp the redeemed_by_user_id now that we know the new
  // user's id. The redemption is "complete" only after this step;
  // a crash between step 2 and step 3 leaves an invite in
  // (redeemed_at IS NOT NULL, redeemed_by_user_id IS NULL) — the
  // listing query excludes it (redeemed_at is set) so the slot is
  // not reusable, and the new user still exists with their session.
  await appDb
    .update(operatorInvites)
    .set({ redeemedByUserId: userId })
    .where(eq(operatorInvites.id, inviteId));

  return { userId, sessionToken, cookies };
}

// Re-exports purely for test plumbing — not part of the public API.
export const __testing = { users };
