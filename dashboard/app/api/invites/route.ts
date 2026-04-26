// /api/invites
//   POST  generate a new invite for the calling operator
//   GET   list pending invites (for the admin/invites surface)
//
// Both gated by an authenticated session. The middleware (T006)
// allow-lists /api/auth/** but NOT /api/invites/**, so an
// unauthenticated request lands on the redirect-to-login path
// before reaching this handler. We re-check the session
// server-side as a defense-in-depth measure.

import { NextResponse } from 'next/server';
import { getSession } from '@/lib/auth/session';
import { generateInvite, listPendingInvites } from '@/lib/auth/invites';

export const dynamic = 'force-dynamic';

export async function GET() {
  const session = await getSession();
  if (!session) {
    return NextResponse.json({ error: 'no_session' }, { status: 401 });
  }
  const invites = await listPendingInvites();
  return NextResponse.json({ invites });
}

export async function POST() {
  const session = await getSession();
  if (!session) {
    return NextResponse.json({ error: 'no_session' }, { status: 401 });
  }
  const { token, expiresAt } = await generateInvite(session.user.id);
  return NextResponse.json({ token, expiresAt }, { status: 201 });
}
