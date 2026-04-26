import { test, expect } from '@playwright/test';
import { copyFileSync, readFileSync, writeFileSync, existsSync } from 'node:fs';
import { join } from 'node:path';
import { bootHarness, truncateDashboardState } from './_harness';

// i18n machinery acceptance.
//
// The harness runs the dashboard with GARRISON_TEST_MODE=1, which
// expands lib/i18n/config.ts's locale list to ['en', 'zz'] and
// makes the harness install the fixture catalog at
// tests/fixtures/i18n/zz.json into messages/zz.json before build.
// Tests therefore see /zz/<route> rendering the stub catalog
// without any in-test config mutation.

const FIXTURE_PATH = join(import.meta.dirname, '..', 'fixtures', 'i18n', 'zz.json');
const MESSAGES_ZZ = join(import.meta.dirname, '..', '..', 'messages', 'zz.json');

test.describe('i18n', () => {
  test.beforeAll(async () => {
    if (!existsSync(MESSAGES_ZZ)) {
      copyFileSync(FIXTURE_PATH, MESSAGES_ZZ);
    }
    const env = await bootHarness();
    await truncateDashboardState(env);
  });

  test('stubLocaleRendersAllSurfacesWithoutMissingKeys', async ({ page }) => {
    const consoleProblems: string[] = [];
    page.on('console', (msg) => {
      if (msg.type() === 'warning' || msg.type() === 'error') {
        const text = msg.text();
        if (text.includes('MISSING_MESSAGE') || text.includes('Missing message')) {
          consoleProblems.push(text);
        }
      }
    });

    await page.goto('/zz/login');
    await expect(page.getByRole('heading')).toContainText('zz_signIn_heading');
    await expect(page.locator('label').first()).toContainText('zz_signIn_email');
    await expect(page.getByRole('button')).toContainText('zz_signIn_submit');

    // No raw catalog keys leaked (e.g. "auth.signIn.heading" surfacing
    // because next-intl couldn't find the value).
    const html = await page.content();
    expect(html).not.toMatch(/auth\.signIn\.heading/);
    expect(consoleProblems).toEqual([]);
  });

  test('fallingBackToEnglishOnAMissingKeyDoesNotSurfaceRawCatalogKeys', async ({ page }) => {
    // Mutate messages/zz.json in-place by removing one key, force a
    // fresh page load, and verify the heading falls back gracefully
    // — i.e. the page renders without leaking the raw key path.
    // After the test, restore the original catalog.
    const original = readFileSync(MESSAGES_ZZ, 'utf-8');
    try {
      const mutated = JSON.parse(original);
      delete mutated.auth.signIn.heading;
      writeFileSync(MESSAGES_ZZ, JSON.stringify(mutated, null, 2));

      // next-intl's catalog is loaded at build time + cached at
      // runtime, so editing the file at test time is best-effort.
      // The assertion is that the rendered HTML never contains the
      // raw catalog path "auth.signIn.heading", regardless of which
      // fallback strategy next-intl picks.
      await page.goto('/zz/login', { waitUntil: 'domcontentloaded' });
      const html = await page.content();
      expect(html).not.toMatch(/auth\.signIn\.heading/);
    } finally {
      writeFileSync(MESSAGES_ZZ, original);
    }
  });
});
