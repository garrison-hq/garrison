import { NextRequest, NextResponse } from 'next/server';
import createIntlMiddleware from 'next-intl/middleware';
import { routing } from '@/lib/i18n/routing';

// Composed middleware. Order:
//   1. next-intl rewrites the URL to attach the resolved locale
//      to the request internally (with localePrefix: 'as-needed',
//      `/login` resolves to the en catalog without changing the URL).
//   2. The auth gate redirects unauthenticated requests to /login,
//      preserving the original pathname in ?redirect=.
//
// We let next-intl run first so the auth gate sees a locale-aware
// pathname for redirect targets.

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

const intlMiddleware = createIntlMiddleware(routing);

function isPublic(pathname: string): boolean {
  // Strip a leading /<locale>/ segment if next-intl prepended one
  // (e.g. /en/login → /login) so the allow-list compares against the
  // canonical path.
  const stripped = stripLocale(pathname);
  return PUBLIC_PREFIXES.some(
    (prefix) => stripped === prefix || stripped.startsWith(prefix),
  );
}

function stripLocale(pathname: string): string {
  for (const locale of routing.locales) {
    if (pathname === `/${locale}`) return '/';
    if (pathname.startsWith(`/${locale}/`)) return pathname.slice(`/${locale}`.length);
  }
  return pathname;
}

// Static-asset paths bypass both next-intl AND the auth gate. Doing
// this inside the function (rather than the matcher regex) is more
// robust: Next's matcher syntax has subtle interactions with regex
// escapes (\\d, \\.) and lookaheads that have produced false-negatives
// on /_next/static/... in past dev-server builds. Belt-and-braces:
// keep the matcher narrow AND short-circuit static paths here.
const STATIC_PREFIXES = ['/_next/', '/brand/'];
const STATIC_FILES = new Set([
  '/favicon.ico',
  '/favicon.svg',
  '/favicon-dark.svg',
  '/favicon-light.svg',
  '/favicon-16.png',
  '/favicon-32.png',
  '/apple-touch-icon.png',
]);

function isStaticAsset(pathname: string): boolean {
  if (STATIC_FILES.has(pathname)) return true;
  return STATIC_PREFIXES.some((p) => pathname.startsWith(p));
}

export function middleware(req: NextRequest) {
  const { pathname } = req.nextUrl;

  if (isStaticAsset(pathname)) {
    return NextResponse.next();
  }

  // /api/* paths are locale-agnostic — never run them through
  // next-intl's middleware (which would otherwise try to inject a
  // locale prefix and 404 our route handlers).
  if (pathname.startsWith('/api/')) {
    if (isPublic(pathname)) {
      return NextResponse.next();
    }
    const hasSessionCookie = SESSION_COOKIE_NAMES.some(
      (name) => req.cookies.get(name)?.value,
    );
    if (!hasSessionCookie) {
      // For API calls, return a 401 rather than redirecting.
      return NextResponse.json({ error: 'no_session' }, { status: 401 });
    }
    return NextResponse.next();
  }

  // Run intl middleware first. It returns a NextResponse that may
  // include a rewrite/redirect; we propagate it through whatever
  // happens next so cookies + headers it set survive.
  const intlResponse = intlMiddleware(req);

  // Public routes: pass intl's response through unchanged.
  if (isPublic(pathname)) {
    return intlResponse;
  }

  // Auth gate: cookie-only check at the edge. The downstream
  // Server Component validates the token in getSession().
  const hasSessionCookie = SESSION_COOKIE_NAMES.some(
    (name) => req.cookies.get(name)?.value,
  );
  if (!hasSessionCookie) {
    const url = req.nextUrl.clone();
    url.pathname = '/login';
    url.searchParams.set('redirect', pathname);
    return NextResponse.redirect(url);
  }

  return intlResponse;
}

export const config = {
  // Match every path; per-path bypassing happens in
  // middleware() via isStaticAsset(). The matcher excludes only the
  // root-level files Next.js serves internally that should never be
  // observed by user code (no business logic should look at these).
  matcher: ['/((?!_next/static|_next/image).*)'],
};
