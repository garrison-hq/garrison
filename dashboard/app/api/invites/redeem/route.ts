// POST /api/invites/redeem
//
// Public route (allow-listed in middleware) consumed by the invite
// redemption form at /invite/[token]. Accepts the token + the
// invitee's name + email + password; on success returns a 200 with
// the new session info. The Set-Cookie response header issued by
// better-auth is forwarded to the browser so the redirect lands on
// the org overview as the new operator.

import { NextResponse } from 'next/server';
import { redeemInvite } from '@/lib/auth/invites';
import { AuthError, AuthErrorKind } from '@/lib/auth/errors';

export const dynamic = 'force-dynamic';

interface Body {
  token?: string;
  email?: string;
  name?: string;
  password?: string;
}

function statusForKind(kind: AuthErrorKind): number {
  switch (kind) {
    case AuthErrorKind.InviteNotFound:
      return 404;
    case AuthErrorKind.InviteExpired:
    case AuthErrorKind.InviteRevoked:
    case AuthErrorKind.InviteAlreadyRedeemed:
      return 410;
    case AuthErrorKind.EmailAlreadyExists:
      return 409;
    default:
      return 400;
  }
}

export async function POST(req: Request) {
  let body: Body;
  try {
    body = (await req.json()) as Body;
  } catch {
    return NextResponse.json({ error: 'invalid_body' }, { status: 400 });
  }
  const { token, email, name, password } = body;
  if (!token || !email || !name || !password || password.length < 8) {
    return NextResponse.json({ error: 'invalid_body' }, { status: 400 });
  }
  try {
    const { userId, sessionToken, cookies } = await redeemInvite(
      token,
      name,
      password,
      email,
    );
    const res = NextResponse.json({ userId, sessionToken }, { status: 200 });
    // Forward the Set-Cookie headers better-auth issued during
    // sign-up so the browser is logged in immediately.
    cookies.forEach((value, key) => {
      if (key.toLowerCase() === 'set-cookie') {
        res.headers.append('set-cookie', value);
      }
    });
    return res;
  } catch (err) {
    if (err instanceof AuthError) {
      return NextResponse.json({ error: err.kind }, { status: statusForKind(err.kind) });
    }
    throw err;
  }
}
