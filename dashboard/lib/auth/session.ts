// Session reader for Server Components and route handlers.
//
// `getSession()` reads the request via Next's `headers()` helper and
// asks better-auth to resolve the session. Returns
// `{ user, session }` for authenticated requests or `null`
// otherwise. Treat null as "redirect to /login" (the middleware in
// T006 enforces that for protected routes).
//
// Server-Component layouts call this once per render to pick the
// theme + render the topbar's operator-email control. The route
// handlers under app/api/ call it for auth-gating writes (invite
// generate, theme write).

import { headers } from 'next/headers';
import { auth } from './index';

export async function getSession() {
  const requestHeaders = await headers();
  return auth.api.getSession({ headers: requestHeaders });
}

export type Session = NonNullable<Awaited<ReturnType<typeof getSession>>>;
