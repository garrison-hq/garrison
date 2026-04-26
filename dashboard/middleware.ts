import { NextRequest, NextResponse } from 'next/server';

// Auth gate. Every route except the explicit allow-list redirects to
// /login when the request has no better-auth session cookie.
//
// Allow-list (no auth needed):
//   /login                — login form
//   /setup                — first-run wizard (the page itself
//                            self-404s when users table is non-empty)
//   /invite/[token]       — invite-redemption form (T007)
//   /api/auth/**          — better-auth's built-in handlers
//   /api/invites/redeem   — public POST consumed by /invite/[token]
//                            (T007 wires this; the middleware exception
//                            ships now so T007 can hit it without
//                            another middleware edit)
//   /api/sse/activity     — does its own session check + 401 (T015)
//   /_next/**, /public/** — Next.js static assets
//
// We do NOT call better-auth's getSession() here. Edge-runtime
// middleware shouldn't perform DB reads on every request; instead,
// we look for the better-auth session cookie. If the cookie is
// missing, redirect; if present, the downstream Server Component
// validates it via getSession() and surfaces NoSession on
// expired/forged tokens. Better-auth's cookie name is
// `better-auth.session_token` (the default).

const PUBLIC_PREFIXES = [
  '/login',
  '/setup',
  '/invite/',
  '/api/auth/',
  '/api/invites/redeem',
  '/api/sse/activity',
  '/_next/',
  '/favicon',
];

const SESSION_COOKIE_NAMES = [
  'better-auth.session_token',
  '__Secure-better-auth.session_token',
];

export function middleware(req: NextRequest) {
  const { pathname } = req.nextUrl;

  // Public routes pass through unchanged.
  if (PUBLIC_PREFIXES.some((prefix) => pathname === prefix || pathname.startsWith(prefix))) {
    return NextResponse.next();
  }

  // Look for the better-auth session cookie. The actual token
  // validation happens server-side in getSession(); we only gate
  // access at the network edge.
  const hasSessionCookie = SESSION_COOKIE_NAMES.some(
    (name) => req.cookies.get(name)?.value,
  );
  if (!hasSessionCookie) {
    const url = req.nextUrl.clone();
    url.pathname = '/login';
    url.searchParams.set('redirect', pathname);
    return NextResponse.redirect(url);
  }

  return NextResponse.next();
}

export const config = {
  // Run on every request except the explicit static-asset paths
  // Next.js handles internally. The matcher is broad on purpose;
  // PUBLIC_PREFIXES inside the function does the fine-grained
  // allow-listing.
  matcher: ['/((?!_next/static|_next/image|favicon.ico).*)'],
};
