// M5.2 — chat error_kind → display copy table (plan §1.15).
//
// CHAT_ERROR_DISPLAY is keyed on the M5.1 chat ErrorKind vocabulary
// from supervisor/internal/chat/errorkind.go. Adding a new kind in any
// future milestone requires both a Go-side const and a corresponding
// entry here — the M5.2 plan §1.15 calls this out explicitly.
//
// The table covers every value of the M5.1 ErrorKind enum:
//   token_not_found, token_expired, vault_unavailable,
//   container_crashed, docker_proxy_unreachable, rate_limit_exhausted,
//   claude_runtime_error, turn_timeout, session_cost_cap_reached,
//   session_ended, session_not_found, supervisor_shutdown,
//   supervisor_restart
// Plus the dynamic mcp_<server>_<status> family — covered by the
// MCP_PREFIX prefix matcher.

export interface ChatErrorDisplay {
  headline: string;
  body: string;
  deepLinkHref?: string;
  deepLinkLabel?: string;
}

export const CHAT_ERROR_DISPLAY: Record<string, ChatErrorDisplay> = {
  token_not_found: {
    headline: 'Claude token missing',
    body: 'The Claude OAuth token is not in the vault. Add it to keep chatting.',
    deepLinkHref: '/vault',
    deepLinkLabel: 'Open vault',
  },
  token_expired: {
    headline: 'Claude token expired',
    body: 'The Claude OAuth token expired. Rotate it and your next message will spawn a fresh container.',
    deepLinkHref: '/vault/edit/operator/CLAUDE_CODE_OAUTH_TOKEN',
    deepLinkLabel: 'Rotate token',
  },
  vault_unavailable: {
    headline: 'Vault unreachable',
    body: "Couldn't reach the vault. Try again in a moment; if the problem persists, check supervisor logs.",
  },
  claude_runtime_error: {
    headline: 'Claude runtime error',
    body: 'The Claude container surfaced an error mid-turn. Send another message to retry.',
  },
  rate_limit_exhausted: {
    headline: 'Rate limit reached',
    body: 'The chat container hit a rate limit. Wait a moment and try again.',
  },
  container_crashed: {
    headline: 'Chat container crashed',
    body: 'The chat container exited unexpectedly mid-turn. Send another message to retry.',
  },
  docker_proxy_unreachable: {
    headline: 'Docker proxy unreachable',
    body: 'The supervisor could not reach the docker proxy. Check the proxy container.',
  },
  turn_timeout: {
    headline: 'Turn timed out',
    body: 'The chat container exceeded the per-turn timeout. Try a more focused question.',
  },
  session_cost_cap_reached: {
    headline: 'Cost cap reached',
    body: 'This thread hit its per-session cost limit. Start a new thread to keep talking.',
  },
  session_ended: {
    headline: 'Thread ended',
    body: 'This thread is closed. Start a new one to send another message.',
  },
  session_not_found: {
    headline: 'Thread not found',
    body: "Couldn't find this thread. It may have been deleted. Open a new one.",
  },
  supervisor_shutdown: {
    headline: 'Supervisor shut down',
    body: 'The supervisor shut down mid-turn. Send another message in a new thread.',
  },
  supervisor_restart: {
    headline: 'Supervisor restarted',
    body: 'The supervisor restarted before this turn finished. Send another message in a new thread.',
  },
};

// MCP errors arrive as mcp_<server>_<status> (e.g. mcp_postgres_failed).
// Resolve to a generic per-server display.
export function resolveChatErrorDisplay(errorKind: string): ChatErrorDisplay {
  if (errorKind in CHAT_ERROR_DISPLAY) {
    return CHAT_ERROR_DISPLAY[errorKind];
  }
  if (errorKind.startsWith('mcp_')) {
    const server = errorKind.slice(4).split('_')[0] || 'mcp';
    return {
      headline: `${capitalize(server)} MCP unhealthy`,
      body: `The chat container couldn't reach the ${server} MCP server. Check supervisor logs.`,
    };
  }
  return {
    headline: 'Turn failed',
    body: `Unrecognised error: ${errorKind}. Try again or open a new thread.`,
  };
}

function capitalize(s: string): string {
  if (s.length === 0) return s;
  return s.charAt(0).toUpperCase() + s.slice(1);
}
