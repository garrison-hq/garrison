import { stopHarness } from './_harness';
import { CoverageReport } from 'monocart-coverage-reports';
import { COVERAGE_ROOT, RAW_DATA_DIR } from './_coverage';
import { existsSync } from 'node:fs';

// Browser-side V8 coverage (Path B). Path A server-side coverage
// is wired via NODE_V8_COVERAGE in the harness — the dashboard
// dumps profiles to coverage/integration-server/ on clean exit.
// A separate post-test step (next iteration) converts those
// profiles to lcov; for now the server profiles land on disk
// for offline analysis but don't feed Sonar yet.

export default async function globalTeardown() {
  await stopHarness();
  if (!existsSync(RAW_DATA_DIR)) {
    return;
  }
  const report = new CoverageReport({
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
  await report.generate();
}
