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
