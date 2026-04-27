#!/usr/bin/env bun
//
// lint-vault-discipline — TS-side equivalent of the supervisor's
// tools/vaultlog go vet analyzer. Catches obvious leak paths at
// CI time per FR-017.
//
// Rejects:
//   - console.{log,info,warn,error,debug}(<expr containing 'value'>)
//     where the call is inside lib/actions/vault.ts or lib/vault/
//     and the argument expression syntactically references a
//     property name like `value`, `secret`, `Value`, `Secret`,
//     `password`, `apiKey`, `clientSecret` (case-insensitive on
//     the whole identifier — narrow enough to keep noise low).
//   - Any direct write of a secret-shaped string literal to the
//     console / logger (matches scanForLeaks shape patterns).
//
// This is deliberately a grep-style pass rather than an AST
// analyzer — falls back per Phase 0 research item 7. The CI step
// fails if any match is found; the operator either fixes the call
// site or adds an inline `// vault-discipline-allow` comment on
// the same line if the call is a documented exception.

import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, resolve, relative } from 'node:path';

const ROOT = resolve(import.meta.dirname, '..');
const SCAN_DIRS = ['lib/actions', 'lib/vault', 'lib/audit', 'app/api'];

interface Finding {
  file: string;
  line: number;
  excerpt: string;
  rule: string;
}

const SECRET_LIKE_IDENT = /(?:^|[^a-zA-Z])(value|secret|password|apiKey|clientSecret|token)\b/i;
const SHAPE_PATTERNS: RegExp[] = [
  /sk-[A-Za-z0-9]{20,}/,
  /xoxb-[A-Za-z0-9-]{20,}/,
  /AKIA[0-9A-Z]{16}/,
  /-----BEGIN [A-Z ]+-----/,
  /gh[psuor]_[A-Za-z0-9]{30,}/,
];
const LOGGER_CALL = /\b(console\.(?:log|info|warn|error|debug)|logger\.(?:log|info|warn|error|debug|trace))\s*\(/;

const ALLOW_COMMENT = '// vault-discipline-allow';

function* walk(dir: string): IterableIterator<string> {
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry);
    const st = statSync(full);
    if (st.isDirectory()) {
      if (entry === 'node_modules' || entry === '.next') continue;
      yield* walk(full);
    } else if (st.isFile()) {
      if (full.endsWith('.test.ts') || full.endsWith('.test.tsx')) continue;
      if (full.endsWith('.ts') || full.endsWith('.tsx')) yield full;
    }
  }
}

function scanFile(file: string): Finding[] {
  const findings: Finding[] = [];
  const content = readFileSync(file, 'utf-8');
  const lines = content.split('\n');
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    if (line.includes(ALLOW_COMMENT)) continue;

    if (LOGGER_CALL.test(line)) {
      // Logger / console call. Inspect the argument expression
      // (rest-of-line) for a secret-shaped identifier.
      const callIdx = line.search(LOGGER_CALL);
      const restOfCall = line.slice(callIdx);
      if (SECRET_LIKE_IDENT.test(restOfCall)) {
        findings.push({
          file: relative(ROOT, file),
          line: i + 1,
          excerpt: line.trim(),
          rule: 'logger-call-with-secret-shaped-identifier',
        });
        continue;
      }
    }

    for (const re of SHAPE_PATTERNS) {
      if (re.test(line)) {
        findings.push({
          file: relative(ROOT, file),
          line: i + 1,
          excerpt: line.trim(),
          rule: `secret-shaped-literal (${re.source})`,
        });
        break;
      }
    }
  }
  return findings;
}

function main(): number {
  const all: Finding[] = [];
  for (const dir of SCAN_DIRS) {
    const full = join(ROOT, dir);
    try {
      for (const file of walk(full)) all.push(...scanFile(file));
    } catch {
      // Directory may not exist yet (e.g., lib/actions before T007);
      // skip silently.
    }
  }
  if (all.length === 0) {
    console.log('vault-discipline: 0 findings.');
    return 0;
  }
  for (const f of all) {
    console.error(`${f.file}:${f.line}: ${f.rule}`);
    console.error(`    ${f.excerpt}`);
  }
  console.error(`\nvault-discipline: ${all.length} finding(s). See FR-017 + lib/actions/_README.md.`);
  console.error('Add `// vault-discipline-allow` on the same line if the call site is a documented exception.');
  return 1;
}

process.exit(main());
