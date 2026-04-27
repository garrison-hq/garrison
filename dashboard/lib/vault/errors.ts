// Typed vault error vocabulary. M2.3 enumerated the supervisor-side
// failure modes (`vault_unavailable`, `vault_auth_expired`,
// `vault_permission_denied`, `vault_rate_limited`,
// `vault_secret_not_found`); M4 adds operator-write-specific
// failures.
//
// Each kind has a corresponding errors.vault.<kind> i18n catalog
// key in messages/en.json; the operator-facing UI uses
// VaultErrorBanner (T010) to render the localized copy + a
// suggested next action (and a link to the relevant ops-checklist
// section where applicable).

export const VaultErrorKind = {
  // M2.3 supervisor-side read failures (re-enumerated here for
  // dashboard-side error UI consistency).
  Unavailable: 'vault_unavailable',
  AuthExpired: 'vault_auth_expired',
  PermissionDenied: 'vault_permission_denied',
  RateLimited: 'vault_rate_limited',
  SecretNotFound: 'vault_secret_not_found',
  // M4 write-side failures.
  PathAlreadyExists: 'vault_path_already_exists',
  ValidationRejected: 'vault_validation_rejected',
  RotationUnsupported: 'vault_rotation_unsupported',
  GrantConflict: 'vault_grant_conflict',
  SecretInUseCannotDelete: 'vault_secret_in_use_cannot_delete',
} as const;
export type VaultErrorKind = (typeof VaultErrorKind)[keyof typeof VaultErrorKind];

export class VaultError extends Error {
  constructor(
    public kind: VaultErrorKind,
    public detail?: Record<string, unknown>,
    message?: string,
  ) {
    super(message ?? kind);
    this.name = 'VaultError';
  }
}
