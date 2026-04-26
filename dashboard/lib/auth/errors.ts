// Discriminated error vocabulary for the auth + invite surfaces.
//
// Every kind has a corresponding i18n catalog key under
// `errors.auth.<kind>` (T008 lands the catalog) and a documented
// HTTP status code; the route handlers in T006 + T007 throw an
// AuthError with one of these kinds and the surface code maps it to
// a translated message + appropriate response shape.

export const AuthErrorKind = {
  /** Caller has no session; redirect to /login. (HTTP 401) */
  NoSession: 'no_session',
  /** Invite token does not match any row. (HTTP 404 or 410 depending on context) */
  InviteNotFound: 'invite_not_found',
  /** Invite expires_at is in the past. (HTTP 410) */
  InviteExpired: 'invite_expired',
  /** Invite revoked_at is set. (HTTP 410) */
  InviteRevoked: 'invite_revoked',
  /** Invite redeemed_at already set; the link was used once. (HTTP 410) */
  InviteAlreadyRedeemed: 'invite_already_redeemed',
  /** First-run setup attempted while users table already populated. (HTTP 404) */
  FirstRunLocked: 'first_run_locked',
  /** Sign-up attempted with an email that already exists. (HTTP 409) */
  EmailAlreadyExists: 'email_already_exists',
} as const;

export type AuthErrorKind = (typeof AuthErrorKind)[keyof typeof AuthErrorKind];

export class AuthError extends Error {
  constructor(public kind: AuthErrorKind, message?: string) {
    super(message ?? kind);
    this.name = 'AuthError';
  }
}
