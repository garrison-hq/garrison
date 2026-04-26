// Catch-all better-auth route handler. Forwards every GET/POST under
// /api/auth/** to the better-auth instance's HTTP handler, which
// dispatches sign-in, sign-up, sign-out, get-session, and the rest
// of better-auth's built-in surface.
//
// The middleware in T006 carves /api/auth/** out of the auth-gate
// allow-list so unauthenticated callers can still hit /api/auth/sign-in
// and /api/auth/sign-up.

import { auth } from '@/lib/auth';

export const GET = auth.handler;
export const POST = auth.handler;
