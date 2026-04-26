// POST /api/invites/[id]/revoke
//
// Authenticated-operator-only revocation surface. Idempotent —
// revoking an already-revoked invite returns 200 either way. Also
// returns 200 for unknown IDs to avoid leaking which IDs exist via
// timing or status code.

import { NextResponse } from 'next/server';
import { getSession } from '@/lib/auth/session';
import { revokeInvite } from '@/lib/auth/invites';

export const dynamic = 'force-dynamic';

export async function POST(
  _req: Request,
  ctx: { params: Promise<{ id: string }> },
) {
  const session = await getSession();
  if (!session) {
    return NextResponse.json({ error: 'no_session' }, { status: 401 });
  }
  const { id } = await ctx.params;
  await revokeInvite(id);
  return NextResponse.json({ ok: true });
}
