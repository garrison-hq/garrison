import { stopHarness } from './_harness';
import { CoverageReport } from 'monocart-coverage-reports';
import { COVERAGE_ROOT, RAW_DATA_DIR } from './_coverage';
import { existsSync, readdirSync, readFileSync } from 'node:fs';
import { resolve } from 'node:path';

// Path A — server-side V8 coverage. NODE_V8_COVERAGE drops
// per-process coverage profiles to this dir on clean exit.
const SERVER_COVERAGE_DIR = resolve(COVERAGE_ROOT, '..', 'integration-server');
// Server-side lcov lands at a sibling dir to the browser lcov
// — Sonar reads both via comma-separated reportPaths.
const SERVER_REPORT_DIR = resolve(COVERAGE_ROOT, '..', 'integration-server-report');

function isDashboardServerScript(url: string): boolean {
  if (!url.startsWith('file://')) return false;
  if (!url.includes('/dashboard/.next/standalone/')) return false;
  if (url.includes('/node_modules/')) return false;
  return true;
}

export default async function globalTeardown() {
  await stopHarness();

  // Browser-side coverage report (Path B). Independent
  // CoverageReport instance so its outputDir cleanup doesn't
  // disturb the server-side cache.
  if (existsSync(RAW_DATA_DIR)) {
    const browserReport = new CoverageReport({
      name: 'Garrison dashboard — Playwright integration browser coverage',
      outputDir: COVERAGE_ROOT,
      inputDir: RAW_DATA_DIR,
      reports: ['lcovonly', 'v8'],
      sourceFilter: (sourcePath: string) => {
        if (sourcePath.includes('/node_modules/')) return false;
        if (sourcePath.includes('webpack://')) return false;
        if (sourcePath.includes('next/dist/')) return false;
        if (sourcePath.startsWith('[turbopack]/')) return false;
        return true;
      },
      sourcePath: (filePath: string) => {
        if (filePath.startsWith('[project]/')) {
          return 'dashboard/' + filePath.slice('[project]/'.length);
        }
        return filePath;
      },
    });
    await browserReport.generate();
  }

  // Server-side coverage report (Path A). Independent instance,
  // independent outputDir → no interaction with the browser side
  // and no shared cache to clean. Reads NODE_V8_COVERAGE profiles
  // directly via report.add(). The harness symlinks .next/server
  // .map files into .next/standalone/.next/server so monocart's
  // source-map resolver can map compiled positions back to
  // dashboard/lib/**, dashboard/app/**, dashboard/components/**.
  if (existsSync(SERVER_COVERAGE_DIR)) {
    const serverReport = new CoverageReport({
      name: 'Garrison dashboard — Playwright integration server coverage',
      outputDir: SERVER_REPORT_DIR,
      // Don't clean outputDir on generate — clean: true wipes
      // the .cache subdir mid-generate, dropping the entries we
      // just added.
      clean: false,
      reports: ['lcovonly', 'v8'],
      // baseDir: where monocart resolves source-map relative
      // paths from. The maps emitted by Next live at
      // .next/server/chunks/.../*.js.map and reference sources
      // like '../../../../lib/actions/vault.ts' which only
      // resolve correctly from that directory. Setting
      // baseDir=dashboard makes monocart's path
      // canonicalisation produce '/lib/actions/vault.ts' for
      // the absolute path, which sourcePath then rewrites to
      // 'dashboard/lib/actions/vault.ts'.
      baseDir: resolve(import.meta.dirname, '..', '..'),
      sourcePath: (filePath: string) => {
        if (filePath.startsWith('[project]/')) {
          return 'dashboard/' + filePath.slice('[project]/'.length);
        }
        // Absolute paths produced via baseDir resolution land as
        // /lib/foo.ts → rewrite to dashboard/lib/foo.ts.
        if (filePath.startsWith('/')) {
          return 'dashboard' + filePath;
        }
        return filePath;
      },
    });
    const profileFiles = readdirSync(SERVER_COVERAGE_DIR).filter((f) =>
      f.endsWith('.json'),
    );
    let totalAdded = 0;
    for (const f of profileFiles) {
      try {
        const profile = JSON.parse(
          readFileSync(resolve(SERVER_COVERAGE_DIR, f), 'utf-8'),
        ) as { result?: Array<{ url?: string }> };
        if (!Array.isArray(profile.result)) continue;
        const filtered = profile.result
          .filter((entry) => isDashboardServerScript(entry.url ?? ''))
          // Rewrite URL: file:///dashboard/.next/standalone/.next/server/...
          // → file:///dashboard/.next/server/... so monocart's
          // source-map resolver applies the maps' relative paths
          // ('../../../../lib/...') from the ORIGINAL emit location
          // (where they correctly point at dashboard source). The
          // standalone copy preserves the chunks but the relative
          // paths bake in the ORIGINAL build dir, so resolving
          // them from the standalone dir lands one level off.
          .map((entry) => ({
            ...entry,
            url: (entry.url as string).replace(
              '/dashboard/.next/standalone/.next/server/',
              '/dashboard/.next/server/',
            ),
          }));
        if (filtered.length > 0) {
          await serverReport.add(filtered);
          totalAdded += filtered.length;
        }
      } catch (e) {
        console.log('[server-cov] skip', f, e instanceof Error ? e.message : e);
      }
    }
    console.log('[server-cov] added', totalAdded, 'V8 entries from', profileFiles.length, 'profiles');
    try {
      const result = await serverReport.generate();
      console.log('[server-cov] generated; lines:', result?.summary?.lines ?? 'n/a');
    } catch (e) {
      console.log('[server-cov] generate error:', e);
    }
  }
}
