// PUT /api/theme — persist the calling operator's theme preference
// (FR-010a). Auth-gated; 401 on unauthenticated. The body must
// match the CHECK constraint enum on users.theme_preference
// ('dark' | 'light' | 'system') — anything else is a 400.

import { NextResponse } from 'next/server';
import { eq } from 'drizzle-orm';
import { getSession } from '@/lib/auth/session';
import { appDb } from '@/lib/db/appClient';
import { users } from '@/drizzle/schema.dashboard';

export const dynamic = 'force-dynamic';

const VALID = new Set(['dark', 'light', 'system']);

export async function PUT(req: Request) {
  const session = await getSession();
  if (!session) {
    return NextResponse.json({ error: 'no_session' }, { status: 401 });
  }
  let body: { theme_preference?: unknown };
  try {
    body = (await req.json()) as { theme_preference?: unknown };
  } catch {
    return NextResponse.json({ error: 'invalid_body' }, { status: 400 });
  }
  const pref = body.theme_preference;
  if (typeof pref !== 'string' || !VALID.has(pref)) {
    return NextResponse.json({ error: 'invalid_theme_preference' }, { status: 400 });
  }
  await appDb
    .update(users)
    .set({ themePreference: pref, updatedAt: new Date() })
    .where(eq(users.id, session.user.id));
  return NextResponse.json({ ok: true });
}
