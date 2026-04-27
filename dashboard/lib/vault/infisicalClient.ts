// Module-init singleton wrapper around the @infisical/sdk client
// authenticated as the parked-since-M2.3 garrison-dashboard
// Machine Identity (FR-050 / M2.3 retro Q4).
//
// Design notes:
//
// - The SDK's Universal Auth login is async and returns a NEW
//   authenticated SDK instance. We lazy-init on first call and
//   memoize across the dashboard process. The SDK handles
//   automatic auth refresh on 401 internally (per Phase 0
//   research item 5).
//
// - All vault server actions (T007 onwards) call
//   `getDashboardVault()` to obtain the authenticated client.
//   Test code can pass an alternate via `setDashboardVaultForTest`.
//
// - Per FR-051, the runtime distinguishes the dashboard's ML
//   from the supervisor's read-only ML by name; the env vars
//   used here are scoped specifically to the dashboard:
//     INFISICAL_DASHBOARD_ML_CLIENT_ID
//     INFISICAL_DASHBOARD_ML_CLIENT_SECRET
//     INFISICAL_SITE_URL
//
// - In dev / unit-test runs without these env vars set, the
//   getter throws on first access — fail-fast rather than
//   silently fall back to a misconfigured client.

import { InfisicalSDK } from '@infisical/sdk';

let cached: Promise<InfisicalSDK> | null = null;
let testOverride: InfisicalSDK | null = null;

function readEnv(): { clientId: string; clientSecret: string; siteUrl?: string } {
  const clientId = process.env.INFISICAL_DASHBOARD_ML_CLIENT_ID;
  const clientSecret = process.env.INFISICAL_DASHBOARD_ML_CLIENT_SECRET;
  if (!clientId || !clientSecret) {
    throw new Error(
      'INFISICAL_DASHBOARD_ML_CLIENT_ID and INFISICAL_DASHBOARD_ML_CLIENT_SECRET must be set ' +
        'to use the dashboard\'s vault writer (FR-050). See docs/ops-checklist.md M4 section ' +
        'for provisioning.',
    );
  }
  return {
    clientId,
    clientSecret,
    siteUrl: process.env.INFISICAL_SITE_URL,
  };
}

/**
 * Read the Infisical workspace + environment the dashboard
 * targets for vault writes. Both come from env vars set per
 * docs/ops-checklist.md M4 section. Throws if not set —
 * fail-fast rather than send writes to an unintended workspace.
 */
export function getDashboardVaultConfig(): { projectId: string; environment: string } {
  const projectId = process.env.INFISICAL_DASHBOARD_PROJECT_ID;
  const environment = process.env.INFISICAL_DASHBOARD_ENVIRONMENT;
  if (!projectId || !environment) {
    throw new Error(
      'INFISICAL_DASHBOARD_PROJECT_ID and INFISICAL_DASHBOARD_ENVIRONMENT must be set ' +
        'so the dashboard knows which Infisical project + environment to target ' +
        '(FR-051). See docs/ops-checklist.md M4 section.',
    );
  }
  return { projectId, environment };
}

async function authenticate(): Promise<InfisicalSDK> {
  const env = readEnv();
  const sdk = new InfisicalSDK({ siteUrl: env.siteUrl });
  return sdk.auth().universalAuth.login({
    clientId: env.clientId,
    clientSecret: env.clientSecret,
  });
}

/**
 * Get the dashboard's authenticated Infisical client. Lazy-init
 * on first call; memoized for subsequent calls in the same process.
 *
 * Throws if the required env vars are not set, or if Universal
 * Auth fails. Callers wrap the returned errors as VaultError
 * variants per FR-075.
 */
export async function getDashboardVault(): Promise<InfisicalSDK> {
  if (testOverride) return testOverride;
  if (!cached) cached = authenticate();
  try {
    return await cached;
  } catch (err) {
    // Reset the cache on auth failure so a subsequent call
    // re-attempts (the operator may have fixed the credentials
    // out of band).
    cached = null;
    throw err;
  }
}

/**
 * Test helper: replace the singleton with a stub. Callers must
 * `clearDashboardVaultForTest()` in afterEach to avoid leaking
 * test state across files.
 */
export function setDashboardVaultForTest(stub: InfisicalSDK): void {
  testOverride = stub;
  cached = null;
}

export function clearDashboardVaultForTest(): void {
  testOverride = null;
  cached = null;
}
