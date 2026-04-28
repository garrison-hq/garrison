// Typed conflict-error vocabulary for M4 mutations.
//
// Distinct from VaultError (lib/vault/errors.ts) because
// conflicts are operator-recoverable via the
// ConflictResolutionModal — the operator picks overwrite /
// merge / discard. VaultError variants are typically not
// recoverable in-flight (the operator retries or escalates).

export const ConflictKind = {
  /** Optimistic-lock check failed — another writer changed the
   *  row since the operator opened the form. */
  StaleVersion: 'stale_version',
  /** Create with a key that already exists (path collision,
   *  duplicate grant, etc.). */
  AlreadyExists: 'already_exists',
  /** Delete blocked by FK or grant reference. */
  InUse: 'in_use',
  /** Multi-step mutation step failed (rotation, multi-secret
   *  path move). */
  RotationStepFailed: 'rotation_step_failed',
  /** Path validation rejected because the target collides with
   *  an existing path. */
  PathCollision: 'path_collision',
} as const;
export type ConflictKind = (typeof ConflictKind)[keyof typeof ConflictKind];

export class ConflictError extends Error {
  constructor(public kind: ConflictKind, public serverState?: unknown, message?: string) {
    super(message ?? `conflict: ${kind}`);
    this.name = 'ConflictError';
  }
}
