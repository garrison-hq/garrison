// Post-test server-coverage converter.
//
// Walks the NODE_V8_COVERAGE dump dir, for each profile entry that
// matches a dashboard server chunk runs v8-to-istanbul against the
// chunk + its sibling .map file, then merges all istanbul-format
// outputs and writes a single lcov.info.
//
// Why not monocart? Monocart's source-map resolver can't map the
// standalone-bundle's chunks back to source files — the relative
// paths in the .map ('../../../../lib/...') only resolve correctly
// from the ORIGINAL .next/server/ emit dir, not from the
// standalone copy. v8-to-istanbul handles that path canonicalisation
// directly when given the source-file path.

import { readdirSync, readFileSync, writeFileSync, mkdirSync, existsSync } from 'node:fs';
import { resolve } from 'node:path';
import v8toIstanbul from 'v8-to-istanbul';
import libCoverage from 'istanbul-lib-coverage';

const DASHBOARD_DIR = resolve(import.meta.dirname, '..', '..');
const SERVER_COVERAGE_DIR = resolve(DASHBOARD_DIR, 'coverage', 'integration-server');
const REPORT_DIR = resolve(DASHBOARD_DIR, 'coverage', 'integration-server-report');

interface V8Entry {
  url?: string;
  scriptId?: string;
  functions?: unknown[];
}

async function main(): Promise<void> {
  if (!existsSync(SERVER_COVERAGE_DIR)) {
    console.log('[server-cov] no profile dir; skipping');
    return;
  }
  const profiles = readdirSync(SERVER_COVERAGE_DIR).filter((f) => f.endsWith('.json'));
  if (profiles.length === 0) {
    console.log('[server-cov] no profiles; skipping');
    return;
  }

  const map = libCoverage.createCoverageMap({});

  for (const f of profiles) {
    let payload: { result?: V8Entry[] };
    try {
      payload = JSON.parse(readFileSync(resolve(SERVER_COVERAGE_DIR, f), 'utf-8'));
    } catch {
      continue;
    }
    if (!Array.isArray(payload.result)) continue;

    for (const entry of payload.result) {
      const url = entry.url ?? '';
      if (!url.startsWith('file://')) continue;
      // Only consider dashboard chunks. Sidestep node_modules + the
      // SIGTERM-handler data: URL.
      if (!url.includes('/dashboard/.next/standalone/')) continue;
      if (url.includes('/node_modules/')) continue;

      // Convert file:// URL → filesystem path. Use the ORIGINAL
      // .next/server/ path (not the .next/standalone/.next/server/
      // copy) so the .map file's relative paths resolve correctly.
      const standalonePath = url.replace(/^file:\/\//, '');
      const originalPath = standalonePath.replace(
        '/dashboard/.next/standalone/.next/server/',
        '/dashboard/.next/server/',
      );
      if (!existsSync(originalPath)) {
        continue;
      }

      try {
        const converter = v8toIstanbul(originalPath, 0, undefined, (sourcePath: string) => {
          // Filter out node internals + node_modules even if they
          // sneak in via inlined sources of a dashboard chunk's
          // sourcemap.
          if (sourcePath.includes('/node_modules/')) return true;
          if (sourcePath.includes('next/dist/')) return true;
          if (sourcePath.includes('[turbopack]/')) return true;
          return false;
        });
        await converter.load();
        converter.applyCoverage(entry.functions ?? ([] as never[]));
        const istanbul = converter.toIstanbul();
        // Drop entries pointing at compiled chunks (those are
        // post-source-map fallbacks); keep only entries whose path
        // looks like a source file.
        const filtered: Record<string, unknown> = {};
        for (const [key, val] of Object.entries(istanbul)) {
          if (
            key.includes('/dashboard/lib/') ||
            key.includes('/dashboard/app/') ||
            key.includes('/dashboard/components/')
          ) {
            filtered[key] = val;
          }
        }
        if (Object.keys(filtered).length > 0) {
          map.merge(filtered);
        }
      } catch {
        // v8-to-istanbul rejects rare entries (anonymous scripts,
        // extra-toplevel functions Next emits). Skip them — the
        // bulk of source coverage is in the page/component chunks
        // which all have well-formed sourcemaps.
      }
    }
  }

  const total = map.files().length;
  console.log('[server-cov] coverage map files:', total);
  if (total === 0) {
    return;
  }

  // Write LCOV manually — istanbul-reports' lcovonly reporter
  // double-prefixes paths and percent-encodes brackets which Sonar
  // can't match against repo files. The format is simple enough
  // that handcrafting it is more reliable.
  const repoRoot = resolve(DASHBOARD_DIR, '..');
  if (!existsSync(REPORT_DIR)) {
    mkdirSync(REPORT_DIR, { recursive: true });
  }

  const lines: string[] = [];
  for (const file of map.files()) {
    let rel = file.startsWith(repoRoot + '/') ? file.slice(repoRoot.length + 1) : file;
    // V8 / source-map URLs percent-encode bracket and paren chars
    // in App Router segment paths ('[locale]' → '%5Blocale%5D',
    // '(app)' → '%28app%29'). Decode so Sonar can match against
    // the actual repo files.
    try {
      rel = decodeURIComponent(rel);
    } catch {
      // leave as-is on invalid percent sequences
    }
    // Skip synthetic paths Next inserts for hot-reload proxies on
    // Client Components — they have no real source file to match.
    if (rel.endsWith('/__nextjs-internal-proxy.mjs')) continue;
    const fc = map.fileCoverageFor(file);
    const lineHits = fc.getLineCoverage(); // { [line]: hits }
    const fnHits = fc.f; // { [fnId]: hits }
    const fnMap = fc.fnMap; // { [fnId]: { name, line } }
    const branchHits = fc.b; // { [bId]: number[] }
    const branchMap = fc.branchMap; // { [bId]: { line, type, locations } }

    lines.push('TN:');
    lines.push(`SF:${rel}`);

    // Functions
    for (const [id, fn] of Object.entries(fnMap)) {
      lines.push(`FN:${(fn as { line: number }).line},${(fn as { name: string }).name || `(anonymous_${id})`}`);
    }
    let fnFound = 0;
    let fnHit = 0;
    for (const [id, hits] of Object.entries(fnHits)) {
      const name = (fnMap[id] as { name?: string } | undefined)?.name || `(anonymous_${id})`;
      lines.push(`FNDA:${hits},${name}`);
      fnFound++;
      if ((hits as number) > 0) fnHit++;
    }
    lines.push(`FNF:${fnFound}`);
    lines.push(`FNH:${fnHit}`);

    // Branches
    let brFound = 0;
    let brHit = 0;
    for (const [id, hitsArr] of Object.entries(branchHits)) {
      const branch = branchMap[id] as { line: number } | undefined;
      if (!branch) continue;
      (hitsArr as number[]).forEach((h, idx) => {
        lines.push(`BRDA:${branch.line},0,${idx},${h}`);
        brFound++;
        if (h > 0) brHit++;
      });
    }
    lines.push(`BRF:${brFound}`);
    lines.push(`BRH:${brHit}`);

    // Lines
    let lineFound = 0;
    let lineHit = 0;
    for (const [line, hits] of Object.entries(lineHits)) {
      lines.push(`DA:${line},${hits}`);
      lineFound++;
      if ((hits as number) > 0) lineHit++;
    }
    lines.push(`LF:${lineFound}`);
    lines.push(`LH:${lineHit}`);
    lines.push('end_of_record');
  }

  const lcovPath = resolve(REPORT_DIR, 'lcov.info');
  writeFileSync(lcovPath, lines.join('\n') + '\n');
  console.log('[server-cov] wrote', lcovPath, '(', map.files().length, 'files)');
}

main().catch((err) => {
  console.error('[server-cov] fatal:', err);
  process.exit(0); // never fail the test run on a coverage-step error
});
