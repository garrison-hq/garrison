import { stopHarness } from './_harness';
import { CoverageReport } from 'monocart-coverage-reports';
import { COVERAGE_ROOT, RAW_DATA_DIR } from './_coverage';
import { existsSync } from 'node:fs';

export default async function globalTeardown() {
  await stopHarness();
  // Generate lcov + html from the V8 entries spec workers dumped
  // under coverage/integration/.raw/.cache/ (monocart's 'raw'
  // reporter writes its files into outputDir/.cache/). The fresh
  // CoverageReport here reads from that path → applies the
  // source-path filter → emits coverage/integration/lcov.info
  // (consumed by Sonar via sonar.javascript.lcov.reportPaths) and
  // a browseable HTML report at coverage/integration/index.html.
  if (!existsSync(RAW_DATA_DIR)) {
    // No spec captured coverage (likely all skipped or harness
    // failed). Skip silently — no lcov to emit.
    return;
  }
  // inputDir tells monocart where the spec workers' 'raw' reporter
  // dumped their coverage-*.json + source-*.json files; generate()
  // reads them, applies the sourceFilter, and emits lcov + v8.
  const report = new CoverageReport({
    name: 'Garrison dashboard — Playwright integration coverage',
    outputDir: COVERAGE_ROOT,
    inputDir: RAW_DATA_DIR,
    reports: ['lcovonly', 'v8'],
    sourceFilter: (sourcePath: string) => {
      if (sourcePath.includes('/node_modules/')) return false;
      if (sourcePath.includes('webpack://')) return false;
      if (sourcePath.includes('next/dist/')) return false;
      // [turbopack]/browser/runtime/... is the Turbopack browser
      // runtime (shipped as part of every Next chunk); not our
      // source code.
      if (sourcePath.startsWith('[turbopack]/')) return false;
      return true;
    },
    // Monocart emits SF: paths prefixed with '[project]/' — its
    // convention for "the dashboard root". Sonar reads
    // sonar.javascript.lcov.reportPaths from repo root, so rewrite
    // to 'dashboard/<rest>' so Sonar can match the lcov entries
    // against the actual repo source files.
    sourcePath: (filePath: string) => {
      if (filePath.startsWith('[project]/')) {
        return 'dashboard/' + filePath.slice('[project]/'.length);
      }
      return filePath;
    },
  });
  await report.generate();
}
