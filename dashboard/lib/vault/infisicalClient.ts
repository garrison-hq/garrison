// Module-init wrapper around the @infisical/sdk client
// authenticated as the parked-since-M2.3 garrison-dashboard
// Machine Identity (FR-050 / M2.3 retro Q4).
//
// Design notes:
//
// - Lazy authentication: getDashboardVault() authenticates on
//   first call and memoizes across the dashboard process. The
//   SDK handles automatic auth refresh on 401 internally per
//   Phase 0 research item 5.
//
// - Sensible defaults rather than fail-fast on boot: the
//   dashboard should run for surfaces that don't touch the
//   vault (org overview, agent registry, etc.) even if
//   Infisical credentials aren't provisioned yet. Vault server
//   actions throw VaultError(Unavailable) at call time with a
//   clear pointer to docs/ops-checklist.md M4 if config is
//   missing. isVaultConfigured() lets UI surfaces detect the
//   case and render a setup prompt instead of an action button.
//
// - Per FR-051, the dashboard ML is distinct from the
//   supervisor's read-only ML by env-var name; dashboard env:
//     INFISICAL_DASHBOARD_ML_CLIENT_ID
//     INFISICAL_DASHBOARD_ML_CLIENT_SECRET
//     INFISICAL_DASHBOARD_PROJECT_ID
//     INFISICAL_DASHBOARD_ENVIRONMENT  (defaults to 'prod')
//     INFISICAL_SITE_URL               (defaults to https://app.infisical.com)

import { InfisicalSDK } from '@infisical/sdk';
import { VaultError, VaultErrorKind } from './errors';

// ─── Defaults ─────────────────────────────────────────────────

const DEFAULT_ENVIRONMENT = 'prod';
const DEFAULT_SITE_URL = 'https://app.infisical.com';

// ─── Module state ─────────────────────────────────────────────

let cached: Promise<InfisicalSDK> | null = null;
let testOverride: InfisicalSDK | null = null;

// ─── Config helpers ───────────────────────────────────────────

interface VaultRuntimeConfig {
  clientId: string;
  clientSecret: string;
  projectId: string;
  environment: string;
  siteUrl: string;
}

function readConfig(): { ok: true; config: VaultRuntimeConfig } | { ok: false; missing: string[] } {
  const clientId = process.env.INFISICAL_DASHBOARD_ML_CLIENT_ID;
  const clientSecret = process.env.INFISICAL_DASHBOARD_ML_CLIENT_SECRET;
  const projectId = process.env.INFISICAL_DASHBOARD_PROJECT_ID;
  const environment = process.env.INFISICAL_DASHBOARD_ENVIRONMENT ?? DEFAULT_ENVIRONMENT;
  const siteUrl = process.env.INFISICAL_SITE_URL ?? DEFAULT_SITE_URL;

  const missing: string[] = [];
  if (!clientId) missing.push('INFISICAL_DASHBOARD_ML_CLIENT_ID');
  if (!clientSecret) missing.push('INFISICAL_DASHBOARD_ML_CLIENT_SECRET');
  if (!projectId) missing.push('INFISICAL_DASHBOARD_PROJECT_ID');

  if (missing.length > 0) {
    return { ok: false, missing };
  }
  return {
    ok: true,
    config: {
      clientId: clientId!,
      clientSecret: clientSecret!,
      projectId: projectId!,
      environment,
      siteUrl,
    },
  };
}

/**
 * True if the dashboard's vault credentials are configured. UI
 * surfaces can use this to decide whether to render a "configure
 * vault" prompt instead of action buttons. Cheap (env-var read);
 * safe to call repeatedly.
 */
export function isVaultConfigured(): boolean {
  return readConfig().ok;
}

/**
 * Read the vault project + environment for vault writes. Throws
 * VaultError(Unavailable) with a clear message naming missing
 * env vars if config is incomplete. Sensible defaults applied
 * for environment ('prod') and site URL.
 */
export function getDashboardVaultConfig(): { projectId: string; environment: string } {
  const result = readConfig();
  if (!result.ok) {
    throw new VaultError(VaultErrorKind.Unavailable, {
      reason: 'dashboard vault config incomplete',
      missing: result.missing,
      hint: 'see docs/ops-checklist.md M4 section for provisioning the garrison-dashboard Machine Identity + project/environment',
    });
  }
  return {
    projectId: result.config.projectId,
    environment: result.config.environment,
  };
}

// ─── Authentication ───────────────────────────────────────────

async function authenticate(): Promise<InfisicalSDK> {
  const result = readConfig();
  if (!result.ok) {
    throw new VaultError(VaultErrorKind.Unavailable, {
      reason: 'dashboard vault credentials not configured',
      missing: result.missing,
      hint: 'see docs/ops-checklist.md M4 section',
    });
  }
  const sdk = new InfisicalSDK({ siteUrl: result.config.siteUrl });
  return sdk.auth().universalAuth.login({
    clientId: result.config.clientId,
    clientSecret: result.config.clientSecret,
  });
}

/**
 * Get the dashboard's authenticated Infisical client. Lazy-init
 * on first call; memoized for subsequent calls in the same
 * process. Throws VaultError(Unavailable) if config is missing
 * or auth fails. Cache resets on auth failure so a subsequent
 * call re-attempts (the operator may have fixed the credentials
 * out of band).
 */
export async function getDashboardVault(): Promise<InfisicalSDK> {
  if (testOverride) return testOverride;
  cached ??= authenticate();
  try {
    return await cached;
  } catch (err) {
    cached = null;
    throw err;
  }
}

// ─── Test helpers ─────────────────────────────────────────────

export function setDashboardVaultForTest(stub: InfisicalSDK): void {
  testOverride = stub;
  cached = null;
}

export function clearDashboardVaultForTest(): void {
  testOverride = null;
  cached = null;
}
