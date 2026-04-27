import { describe, it, expect, beforeEach } from 'vitest';
import { isVaultConfigured, getDashboardVaultConfig } from './infisicalClient';
import { VaultError, VaultErrorKind } from './errors';

describe('lib/vault/infisicalClient', () => {
  const ENV_KEYS = [
    'INFISICAL_DASHBOARD_ML_CLIENT_ID',
    'INFISICAL_DASHBOARD_ML_CLIENT_SECRET',
    'INFISICAL_DASHBOARD_PROJECT_ID',
    'INFISICAL_DASHBOARD_ENVIRONMENT',
    'INFISICAL_SITE_URL',
  ];
  const saved: Record<string, string | undefined> = {};

  beforeEach(() => {
    for (const k of ENV_KEYS) {
      saved[k] = process.env[k];
      delete process.env[k];
    }
    return () => {
      for (const k of ENV_KEYS) {
        if (saved[k] === undefined) delete process.env[k];
        else process.env[k] = saved[k];
      }
    };
  });

  it('isVaultConfigured returns false when credentials are missing', () => {
    expect(isVaultConfigured()).toBe(false);
  });

  it('isVaultConfigured returns true when all required env vars are set', () => {
    process.env.INFISICAL_DASHBOARD_ML_CLIENT_ID = 'cid';
    process.env.INFISICAL_DASHBOARD_ML_CLIENT_SECRET = 'csecret';
    process.env.INFISICAL_DASHBOARD_PROJECT_ID = 'pid';
    expect(isVaultConfigured()).toBe(true);
  });

  it('getDashboardVaultConfig defaults environment to "prod"', () => {
    process.env.INFISICAL_DASHBOARD_ML_CLIENT_ID = 'cid';
    process.env.INFISICAL_DASHBOARD_ML_CLIENT_SECRET = 'csecret';
    process.env.INFISICAL_DASHBOARD_PROJECT_ID = 'pid';
    expect(getDashboardVaultConfig()).toEqual({
      projectId: 'pid',
      environment: 'prod',
    });
  });

  it('getDashboardVaultConfig honors an explicit environment override', () => {
    process.env.INFISICAL_DASHBOARD_ML_CLIENT_ID = 'cid';
    process.env.INFISICAL_DASHBOARD_ML_CLIENT_SECRET = 'csecret';
    process.env.INFISICAL_DASHBOARD_PROJECT_ID = 'pid';
    process.env.INFISICAL_DASHBOARD_ENVIRONMENT = 'staging';
    expect(getDashboardVaultConfig().environment).toBe('staging');
  });

  it('getDashboardVaultConfig throws VaultError(Unavailable) listing missing vars when incomplete', () => {
    try {
      getDashboardVaultConfig();
      throw new Error('expected to throw');
    } catch (err) {
      expect(err).toBeInstanceOf(VaultError);
      const ve = err as VaultError;
      expect(ve.kind).toBe(VaultErrorKind.Unavailable);
      expect(ve.detail?.missing).toEqual(
        expect.arrayContaining([
          'INFISICAL_DASHBOARD_ML_CLIENT_ID',
          'INFISICAL_DASHBOARD_ML_CLIENT_SECRET',
          'INFISICAL_DASHBOARD_PROJECT_ID',
        ]),
      );
    }
  });
});
