// Browser-side V8 coverage capture for Playwright integration tests.
//
// Wraps the standard `test` export with two fixtures:
//
//   context — the default fixture-provided context. The fixture
//             registers a 'page' handler that starts coverage on
//             every newly-opened page; after the test body
//             completes, it flushes still-open pages.
//   browser — overrides browser.newContext so test-created
//             contexts get the same instrumentation. Also wraps
//             the returned context's close() so coverage is
//             flushed BEFORE pages are closed (Playwright closes
//             pages first → context, and `page.coverage.stop`
//             rejects on a closed page).
//
// Both paths spool entries to the same on-disk raw cache via
// monocart's 'raw' reporter — global teardown (global-teardown.ts)
// reads the cache and emits coverage/integration/lcov.info.
//
// Cross-process state: Playwright spec workers run in separate
// processes from globalTeardown. The 'raw' reporter persists each
// .add() call as JSON files so the teardown's fresh CoverageReport
// instance can load them via dataDir.
//
// Browser-only: page.coverage works in Chromium and only captures
// JS executed in the browser context (Client Components, hydration
// chunks, page-level event handlers). Server Components + Server
// Actions execute in the dashboard's Node process and are NOT
// covered here.

import { test as base, type BrowserContext, type Page } from '@playwright/test';
import { CoverageReport } from 'monocart-coverage-reports';
import { resolve } from 'node:path';

export const COVERAGE_ROOT = resolve(import.meta.dirname, '..', '..', 'coverage', 'integration');
// The 'raw' reporter writes its files into outputDir/.cache/ (monocart's
// internal layout: source-*.json + coverage-*.json). We expose both
// paths so global-teardown.ts knows where to read from.
export const RAW_DIR = resolve(COVERAGE_ROOT, '.raw');
export const RAW_DATA_DIR = resolve(RAW_DIR, '.cache');

const WORKER_REPORT = new CoverageReport({
  name: 'Garrison dashboard — Playwright integration coverage (raw)',
  outputDir: RAW_DIR,
  reports: [['raw', { outputDir: RAW_DIR }]],
  entryFilter: (entry: { url: string }) => {
    if (entry.url.startsWith('http://localhost:3010/_next/static/')) return true;
    return false;
  },
});

const COVERED_PAGES = new WeakSet<Page>();

async function startCoverageOn(page: Page): Promise<void> {
  if (!page.coverage) return;
  if (COVERED_PAGES.has(page)) return;
  COVERED_PAGES.add(page);
  try {
    await page.coverage.startJSCoverage({ resetOnNavigation: false });
  } catch {
    // Already started, page closed, etc.
  }
}

async function flushCoverageFrom(page: Page): Promise<void> {
  if (!page.coverage) return;
  if (page.isClosed()) return;
  if (!COVERED_PAGES.has(page)) return;
  COVERED_PAGES.delete(page);
  try {
    const entries = await page.coverage.stopJSCoverage();
    if (entries.length > 0) {
      await WORKER_REPORT.add(entries);
    }
  } catch {
    // Page or context torn down mid-flush — V8 has discarded it.
  }
}

function instrumentContext(context: BrowserContext): void {
  context.on('page', (page) => {
    void startCoverageOn(page);
  });
  for (const page of context.pages()) {
    void startCoverageOn(page);
  }
}

async function flushContextPages(context: BrowserContext): Promise<void> {
  for (const page of context.pages()) {
    await flushCoverageFrom(page);
  }
}

export const test = base.extend({
  context: async ({ context }, use) => {
    instrumentContext(context);
    await use(context);
    await flushContextPages(context);
  },
  browser: async ({ browser }, use) => {
    const original = browser.newContext.bind(browser);
    browser.newContext = (async (...args: Parameters<typeof original>) => {
      const ctx = await original(...args);
      instrumentContext(ctx);
      // Wrap close so coverage flushes BEFORE pages close.
      const originalClose = ctx.close.bind(ctx);
      ctx.close = (async (...closeArgs: Parameters<typeof originalClose>) => {
        await flushContextPages(ctx);
        return originalClose(...closeArgs);
      }) as typeof originalClose;
      return ctx;
    }) as typeof original;
    await use(browser);
  },
});

export { expect } from '@playwright/test';

export type {
  BrowserContext,
  Page,
  Locator,
  Browser,
  Cookie,
  Request,
  Response,
  Route,
} from '@playwright/test';
