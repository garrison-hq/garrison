// M5.2 — pin the architecture amendment per FR-310 + plan §1.18.
//
// Reads ARCHITECTURE.md from the repo root and asserts the M5 entry
// enumerates M5.1 / M5.2 / M5.3 / M5.4. Substring-matched, not
// line-pinned, so the test survives line-number drift from future
// edits to other parts of the architecture document.

import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

describe('ARCHITECTURE.md M5 amendment', () => {
  it('TestArchitectureLineEnumeratesM5Submilestones', () => {
    const path = resolve(import.meta.dirname, '..', '..', 'ARCHITECTURE.md');
    const source = readFileSync(path, 'utf-8');
    // Locate the M5 entry by its leading bold marker.
    const m5Idx = source.indexOf('**M5 — CEO chat');
    expect(m5Idx).toBeGreaterThan(-1);
    // Take a window covering the rest of the paragraph (until the
    // next blank-line break).
    const tail = source.slice(m5Idx);
    const paragraphEnd = tail.indexOf('\n\n');
    const paragraph = paragraphEnd === -1 ? tail : tail.slice(0, paragraphEnd);
    for (const submilestone of ['M5.1', 'M5.2', 'M5.3', 'M5.4']) {
      expect(paragraph).toContain(submilestone);
    }
  });
});

describe('ARCHITECTURE.md M5.3 amendment', () => {
  // Pins the M5.3 substrings per FR-500 + FR-501. Substring-match so
  // future edits to other parts of the architecture doc don't break
  // the test from line drift.
  it('contains the M5.3 autonomous-execution posture substring', () => {
    const path = resolve(import.meta.dirname, '..', '..', 'ARCHITECTURE.md');
    const source = readFileSync(path, 'utf-8');
    expect(source).toContain(
      'M5.3 — chat-driven mutations under autonomous-execution posture (no per-call operator approval)'
    );
  });

  it('contains the chat → garrison-mutate diagram substring', () => {
    const path = resolve(import.meta.dirname, '..', '..', 'ARCHITECTURE.md');
    const source = readFileSync(path, 'utf-8');
    expect(source).toContain('Chat ──► garrison-mutate MCP');
  });
});

describe('ARCHITECTURE.md M5.4 amendment', () => {
  // Pins the M5.4 substrings per spec ARCH-1 + ARCH-2. Three amendments:
  // schema-section MinIO reference; M5 build-plan M5.4 sentence;
  // deployment-topology block listing the 4-container Compose stack.
  it('removes the documented company_md column and references MinIO instead', () => {
    const path = resolve(import.meta.dirname, '..', '..', 'ARCHITECTURE.md');
    const source = readFileSync(path, 'utf-8');
    expect(source).toContain(
      's3://garrison-company/<companyId>/company.md in the MinIO sidecar (M5.4)'
    );
    // The previously-documented column must NOT appear in the schema
    // block. (The string still appears elsewhere — in operator config
    // examples — so we narrow the assertion to the M5.4-amended schema
    // sentence.)
    expect(source).not.toContain("company_md TEXT NOT NULL,      -- CEO's always-in-context document");
  });

  it('contains the M5.4 build-plan sentence with knowledge-base pane shape', () => {
    const path = resolve(import.meta.dirname, '..', '..', 'ARCHITECTURE.md');
    const source = readFileSync(path, 'utf-8');
    expect(source).toContain(
      'M5.4 ships the "WHAT THE CEO KNOWS" knowledge-base pane: tabbed surface for Company.md (MinIO-backed, CEO-editable) + recent palace writes + recent KG facts (read-only via supervisor-side proxy to MemPalace).'
    );
  });

  it('contains the deployment-topology amendment naming MinIO as the 4th container', () => {
    const path = resolve(import.meta.dirname, '..', '..', 'ARCHITECTURE.md');
    const source = readFileSync(path, 'utf-8');
    expect(source).toContain(
      'four-container Compose stack on `garrison-net` — supervisor + mempalace sidecar + socket-proxy + minio sidecar'
    );
  });
});
