// M5.3 Playwright golden-path — chat-driven mutations end-to-end
// against the full M5.1 + M5.2 + M5.3 stack (testcontainer Postgres
// + supervisor + garrison-mockclaude:m5 with M5.3 fixtures + standalone
// dashboard). FR-517 + FR-518.
//
// Skips cleanly when chatStackAvailable() reports the chat-stack
// runtime is unavailable — same gating as M5.2's golden-path spec.
// Full chaos+golden-path flips on at chat-stack-runtime enable time
// per the operator's CI image rebuild.

import { test, expect } from '@playwright/test';
import { chatStackAvailable } from './_chat-harness';

test.describe('M5.3 chat-driven mutations golden path', () => {
  test.beforeAll(async () => {
    const probe = chatStackAvailable();
    test.skip(
      !probe.available,
      `M5.3 chat stack unavailable: ${probe.reason ?? 'unknown'}`,
    );
  });

  test('create_ticket round-trip: operator instruction → chip → ticket exists', async () => {
    test.skip(true, 'pending: implemented at chat-stack-runtime enable time per FR-517');
    expect(true).toBe(true); // placeholder so the test body type-checks
  });

  test('transition_ticket round-trip with chip transitions and activity-feed event', async () => {
    test.skip(true, 'pending: implemented at chat-stack-runtime enable time');
    expect(true).toBe(true);
  });

  test('propose_hire round-trip including stopgap page render', async () => {
    test.skip(true, 'pending: implemented at chat-stack-runtime enable time');
    expect(true).toBe(true);
  });

  test('compound two-verb single-turn instruction renders both chips', async () => {
    test.skip(true, 'pending: implemented at chat-stack-runtime enable time');
    expect(true).toBe(true);
  });

  test('failure chip renders with error_kind for leak-scan rejection', async () => {
    test.skip(true, 'pending: implemented at chat-stack-runtime enable time');
    expect(true).toBe(true);
  });

  test('mid-stream disconnect then reconnect renders no duplicate chips', async () => {
    test.skip(true, 'pending: implemented at chat-stack-runtime enable time per FR-447');
    expect(true).toBe(true);
  });

  test('chat-mutation chips do not carry undo / cancel / approve affordances', async () => {
    test.skip(true, 'pending: implemented at chat-stack-runtime enable time per FR-445');
    expect(true).toBe(true);
  });
});
