import { describe, it, expect } from 'vitest';
import { AuthError, AuthErrorKind } from './errors';

describe('lib/auth/errors', () => {
  it('AuthErrorKind enum exposes the expected vocabulary', () => {
    expect(AuthErrorKind).toMatchObject({
      NoSession: 'no_session',
      InviteNotFound: 'invite_not_found',
      InviteExpired: 'invite_expired',
      InviteRevoked: 'invite_revoked',
      InviteAlreadyRedeemed: 'invite_already_redeemed',
      FirstRunLocked: 'first_run_locked',
      EmailAlreadyExists: 'email_already_exists',
    });
  });

  it('AuthError preserves kind + message + name', () => {
    const err = new AuthError(AuthErrorKind.InviteExpired, 'expired link');
    expect(err.kind).toBe('invite_expired');
    expect(err.message).toBe('expired link');
    expect(err.name).toBe('AuthError');
    expect(err).toBeInstanceOf(Error);
  });

  it('AuthError defaults message to the kind value when message is omitted', () => {
    const err = new AuthError(AuthErrorKind.NoSession);
    expect(err.message).toBe('no_session');
  });
});
