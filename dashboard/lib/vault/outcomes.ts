// VaultOutcome type — application-level enumeration of the
// vault_access_log.outcome string vocabulary.
//
// The DB column is TEXT (no Postgres ENUM) per the M2.1 / M2.3
// unconstrained pattern. The supervisor (M2.3) writes the read-side
// outcomes (`granted`, `denied_*`, `error_*`); M4 dashboard mutation
// server actions (T007 onwards) write the write-side outcomes
// enumerated here.
//
// outcomes.test.ts asserts this constant matches the M2.3 + T001
// + T001b migration intent — adding a new outcome is an explicit
// code change here.

export const VaultReadOutcome = {
  Granted: 'granted',
  DeniedNoGrant: 'denied_no_grant',
  DeniedInfisical: 'denied_infisical',
  ErrorFetching: 'error_fetching',
  ErrorInjecting: 'error_injecting',
} as const;
export type VaultReadOutcome = (typeof VaultReadOutcome)[keyof typeof VaultReadOutcome];

// M4 write-side outcomes from FR-012 — re-exported from
// lib/audit/vaultAccessLog.ts for callers that want the union
// type without depending on the writer module.
export {
  VaultWriteOutcome,
  type VaultWriteOutcome as VaultWriteOutcomeT,
} from '@/lib/audit/vaultAccessLog';

import { VaultWriteOutcome } from '@/lib/audit/vaultAccessLog';

export type VaultOutcome = VaultReadOutcome | (typeof VaultWriteOutcome)[keyof typeof VaultWriteOutcome];

export const ALL_VAULT_OUTCOMES: readonly string[] = [
  ...Object.values(VaultReadOutcome),
  ...Object.values(VaultWriteOutcome),
] as const;

export function isWriteOutcome(outcome: string): boolean {
  return Object.values(VaultWriteOutcome).includes(outcome as never);
}

export function isReadOutcome(outcome: string): boolean {
  return Object.values(VaultReadOutcome).includes(outcome as never);
}
