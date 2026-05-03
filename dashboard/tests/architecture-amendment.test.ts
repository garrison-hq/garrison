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
    // M5 entry must exist, and each submilestone must have its own
    // entry. Post-M5.4 the doc was restructured from one bundled M5
    // paragraph to one per submilestone, so the assertion checks the
    // file as a whole rather than slicing a single paragraph.
    expect(source).toContain('**M5 — CEO chat');
    for (const submilestone of [
      '**M5.1 — Chat backend',
      '**M5.2 — Chat dashboard surface',
      '**M5.3 — Chat-driven mutations',
      '**M5.4 — "WHAT THE CEO KNOWS"',
    ]) {
      expect(source).toContain(submilestone);
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
    // Post-M5.3 ship: language was edited from plan-form to ship-form.
    // The shipped paragraph keeps the autonomous-execution posture
    // claim; the substring just tracks the canonical wording.
    expect(source).toContain(
      'M5.3 — Chat-driven mutations under autonomous-execution posture'
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
    // Post-M5.4 ship: language was edited from plan-form to ship-form.
    // The shipped paragraph keeps the three-tab knowledge-base shape
    // (Company.md MinIO-backed, recent palace writes, KG recent facts);
    // the substrings track the canonical entry header + each tab.
    expect(source).toContain('**M5.4 — "WHAT THE CEO KNOWS" knowledge-base pane.**');
    expect(source).toContain('Company.md');
    expect(source).toContain('MinIO-backed');
    expect(source).toContain('Recent palace writes');
    expect(source).toContain('KG recent facts');
  });

  it('contains the deployment-topology amendment naming MinIO as the 4th container', () => {
    const path = resolve(import.meta.dirname, '..', '..', 'ARCHITECTURE.md');
    const source = readFileSync(path, 'utf-8');
    expect(source).toContain(
      'four-container Compose stack on `garrison-net` — supervisor + mempalace sidecar + socket-proxy + minio sidecar'
    );
  });
});

describe('ARCHITECTURE.md M6 amendment', () => {
  // Pins the M6 substrings per T019. The M6 paragraph is annotated
  // with shipped status + retro link; the schema section already
  // documents tickets.parent_ticket_id (added pre-M6 as a forward-
  // looking schema scaffold) and gains throttle_events on M6 ship.
  it('M6 paragraph annotated with shipped status', () => {
    const path = resolve(import.meta.dirname, '..', '..', 'ARCHITECTURE.md');
    const source = readFileSync(path, 'utf-8');
    expect(source).toContain('**M6 — ');
    expect(source).toContain('docs/retros/m6.md');
  });

  it('schema section documents parent_ticket_id', () => {
    const path = resolve(import.meta.dirname, '..', '..', 'ARCHITECTURE.md');
    const source = readFileSync(path, 'utf-8');
    expect(source).toContain('parent_ticket_id');
  });

  it('M6 paragraph references throttle_events table', () => {
    const path = resolve(import.meta.dirname, '..', '..', 'ARCHITECTURE.md');
    const source = readFileSync(path, 'utf-8');
    expect(source).toContain('throttle_events');
  });
});

describe('ARCHITECTURE.md M7 amendment', () => {
  // Pins the M7 amendment substrings per T022. The M7 paragraph is
  // annotated with the shipped status + retro link + the threat-model
  // path references the agent-sandbox + hiring threat models the
  // milestone closes against.
  it('M7 paragraph annotated with shipped status', () => {
    const path = resolve(import.meta.dirname, '..', '..', 'ARCHITECTURE.md');
    const source = readFileSync(path, 'utf-8');
    expect(source).toContain('**M7 — ');
    expect(source).toContain('Shipped 2026-05-03');
    expect(source).toContain('docs/retros/m7.md');
  });

  it('M7 paragraph references agent-sandbox-threat-model.md', () => {
    const path = resolve(import.meta.dirname, '..', '..', 'ARCHITECTURE.md');
    const source = readFileSync(path, 'utf-8');
    expect(source).toContain('docs/security/agent-sandbox-threat-model.md');
  });

  it('M7 paragraph references hiring-threat-model.md', () => {
    const path = resolve(import.meta.dirname, '..', '..', 'ARCHITECTURE.md');
    const source = readFileSync(path, 'utf-8');
    expect(source).toContain('docs/security/hiring-threat-model.md');
  });
});
