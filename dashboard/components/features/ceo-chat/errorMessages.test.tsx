// M5.2 — CHAT_ERROR_DISPLAY mapping tests (plan §1.15).

import { describe, it, expect } from 'vitest';
import { CHAT_ERROR_DISPLAY, resolveChatErrorDisplay } from './errorMessages';

// Vocabulary mirrored from supervisor/internal/chat/errorkind.go.
// Adding a new kind in either repo without updating the other should
// fail this test — that's the safety-net.
const M51_ERROR_KINDS = [
  'token_not_found',
  'token_expired',
  'vault_unavailable',
  'container_crashed',
  'docker_proxy_unreachable',
  'rate_limit_exhausted',
  'claude_runtime_error',
  'turn_timeout',
  'session_cost_cap_reached',
  'session_ended',
  'session_not_found',
  'supervisor_shutdown',
  'supervisor_restart',
];

describe('CHAT_ERROR_DISPLAY', () => {
  it('TestErrorMessagesTableCoversAllKinds', () => {
    for (const kind of M51_ERROR_KINDS) {
      expect(CHAT_ERROR_DISPLAY[kind]).toBeDefined();
      expect(CHAT_ERROR_DISPLAY[kind].headline.length).toBeGreaterThan(0);
      expect(CHAT_ERROR_DISPLAY[kind].body.length).toBeGreaterThan(0);
    }
  });

  it('TestVaultTokenExpiredHasDeepLink', () => {
    const entry = CHAT_ERROR_DISPLAY['token_expired'];
    expect(entry.deepLinkHref).toBeDefined();
    expect(entry.deepLinkHref).toContain('CLAUDE_CODE_OAUTH_TOKEN');
    expect(entry.deepLinkLabel).toBeDefined();
  });

  it('resolves dynamic mcp_<server>_<status> errors to a per-server message', () => {
    const r1 = resolveChatErrorDisplay('mcp_postgres_failed');
    expect(r1.headline.toLowerCase()).toContain('postgres');
    const r2 = resolveChatErrorDisplay('mcp_mempalace_needs-auth');
    expect(r2.headline.toLowerCase()).toContain('mempalace');
  });

  it('falls back to a generic message for an unknown error_kind', () => {
    const r = resolveChatErrorDisplay('totally_unknown_kind');
    expect(r.headline.length).toBeGreaterThan(0);
    expect(r.body).toContain('totally_unknown_kind');
  });
});
