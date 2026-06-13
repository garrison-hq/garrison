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

describe('ARCHITECTURE.md M9 amendment', () => {
  // Pins the M9 substrings per T019 (M6 T019 pattern). The M9 entry is
  // annotated with shipped status + retro link, keeps the committed
  // design language (fire-on-recovery-with-collapse, two firing modes,
  // finalize_oneshot), and carries per-thread implementation pointers.
  it('M9 paragraph annotated with shipped status', () => {
    const path = resolve(import.meta.dirname, '..', '..', 'ARCHITECTURE.md');
    const source = readFileSync(path, 'utf-8');
    expect(source).toContain('**M9 — Scheduled / triggered wake-ups (heartbeat).**');
    expect(source).toContain('Shipped 2026-06-11');
    expect(source).toContain('docs/retros/m9.md');
  });

  it('M9 entry keeps the committed design language', () => {
    const path = resolve(import.meta.dirname, '..', '..', 'ARCHITECTURE.md');
    const source = readFileSync(path, 'utf-8');
    expect(source).toContain('fire-on-recovery semantics with collapse');
    expect(source).toContain('**`ticket` mode**');
    expect(source).toContain('**`oneshot` mode**');
    expect(source).toContain('finalize_oneshot');
  });

  it('M9 entry carries per-thread implementation pointers', () => {
    const path = resolve(import.meta.dirname, '..', '..', 'ARCHITECTURE.md');
    const source = readFileSync(path, 'utf-8');
    expect(source).toContain('Shipped implementation pointers, per thread:');
    expect(source).toContain('supervisor/internal/schedule/');
    expect(source).toContain('supervisor/internal/spawn/oneshot.go');
    expect(source).toContain("pg_notify('work.scheduled.oneshot_due')");
    expect(source).toContain('/admin/recurring-jobs');
    expect(source).toContain('20260610000002_m9_scheduled_wakeups.sql');
  });
});

describe('ARCHITECTURE.md M10 amendment', () => {
  // Pins the M10 substrings per T016 (M9 T019 pattern). The M10 entry is
  // annotated with shipped status + retro link, references the
  // ingress-threat-model, names the implementation packages, and carries
  // the migration version.
  it('M10 paragraph annotated with shipped status', () => {
    const path = resolve(import.meta.dirname, '..', '..', 'ARCHITECTURE.md');
    const source = readFileSync(path, 'utf-8');
    expect(source).toContain('**M10 — Ingress connectors');
    expect(source).toContain('Shipped 2026-06-12');
    expect(source).toContain('docs/retros/m10.md');
  });

  it('M10 entry references the ingress threat model', () => {
    const path = resolve(import.meta.dirname, '..', '..', 'ARCHITECTURE.md');
    const source = readFileSync(path, 'utf-8');
    expect(source).toContain('docs/security/ingress-threat-model.md');
  });

  it('M10 entry names the ingress package and migration', () => {
    const path = resolve(import.meta.dirname, '..', '..', 'ARCHITECTURE.md');
    const source = readFileSync(path, 'utf-8');
    expect(source).toContain('supervisor/internal/ingress/');
    expect(source).toContain('20260612000000_m10_ingress_connectors.sql');
  });

  it('M10 entry names the connector-status dashboard surface and dashboardapi endpoint', () => {
    const path = resolve(import.meta.dirname, '..', '..', 'ARCHITECTURE.md');
    const source = readFileSync(path, 'utf-8');
    expect(source).toContain('/admin/connectors');
    expect(source).toContain('GET /ingress/status');
  });

  it('M10 entry carries the provenance storage key names', () => {
    const path = resolve(import.meta.dirname, '..', '..', 'ARCHITECTURE.md');
    const source = readFileSync(path, 'utf-8');
    expect(source).toContain('ingress_connector');
    expect(source).toContain('external_id');
    expect(source).toContain('external_url');
  });
});

describe('ARCHITECTURE.md M11 amendment', () => {
  // Pins the M11 substrings per T013 (M10 T016 pattern). The M11 entry is
  // annotated with shipped status + retro link, references the
  // action-broker-threat-model, names the implementation packages, and
  // carries the migration version.
  it('M11 paragraph annotated with shipped status', () => {
    const path = resolve(import.meta.dirname, '..', '..', 'ARCHITECTURE.md');
    const source = readFileSync(path, 'utf-8');
    expect(source).toContain('**M11 — Action Broker');
    expect(source).toContain('Shipped 2026-06-12');
    expect(source).toContain('docs/retros/m11.md');
  });

  it('M11 entry references the action-broker threat model', () => {
    const path = resolve(import.meta.dirname, '..', '..', 'ARCHITECTURE.md');
    const source = readFileSync(path, 'utf-8');
    expect(source).toContain('docs/security/action-broker-threat-model.md');
  });

  it('M11 entry names the key implementation packages and migration', () => {
    const path = resolve(import.meta.dirname, '..', '..', 'ARCHITECTURE.md');
    const source = readFileSync(path, 'utf-8');
    expect(source).toContain('garrisonmutate/verbs_actions.go');
    expect(source).toContain('supervisor/internal/actionbroker/policy.go');
    expect(source).toContain('supervisor/internal/actionbroker/dispatcher.go');
    expect(source).toContain('supervisor/internal/actionbroker/github.go');
    expect(source).toContain('20260612000001_m11_action_broker.sql');
  });

  it('M11 entry names the Outbox dashboard surface and route', () => {
    const path = resolve(import.meta.dirname, '..', '..', 'ARCHITECTURE.md');
    const source = readFileSync(path, 'utf-8');
    expect(source).toContain('dashboard/app/[locale]/(app)/admin/outbox/');
    expect(source).toContain('/admin/outbox');
  });

  it('M11 entry documents the permanent-Approve floor and four tiers', () => {
    const path = resolve(import.meta.dirname, '..', '..', 'ARCHITECTURE.md');
    const source = readFileSync(path, 'utf-8');
    expect(source).toContain('permanent-Approve floor');
    expect(source).toContain('pending_actions_floor_is_approve');
  });
});
