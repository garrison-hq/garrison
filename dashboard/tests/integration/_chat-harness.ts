// M5.2 — chat stack harness (T013).
//
// Extends the existing _harness.ts with a chat-stack boot:
//   testcontainer Postgres + Infisical (already bootstrapped by the
//   M3/M4 harness) + garrison-mockclaude:m5 chat image (built once via
//   supervisor/Dockerfile.mockclaude.chat) + supervisor process +
//   standalone dashboard bundle.
//
// The chat-stack boot is GATED on the presence of the
// garrison-mockclaude:m5 docker image AND the supervisor binary.
// When either is missing the harness throws a marker error so the
// Playwright test bodies can `test.skip()` cleanly in CI environments
// without the chat infrastructure (matches the M5.1 pattern of
// gating chat-specific integration tests on infra availability).
//
// setSupervisorEnv lets sub-scenarios override env vars on a per-test
// basis — e.g. T015 sub-scenario `i` sets
// GARRISON_CHAT_SESSION_IDLE_TIMEOUT=10s for the SC-208 idle-flip
// assertion without affecting the surrounding tests.

import { ChildProcess, spawn, spawnSync } from 'node:child_process';
import { existsSync } from 'node:fs';
import { resolve } from 'node:path';
import type { HarnessEnv } from './_harness';

const REPO_ROOT = resolve(import.meta.dirname, '..', '..', '..');
const SUPERVISOR_DIR = resolve(REPO_ROOT, 'supervisor');
const SUPERVISOR_BIN = resolve(SUPERVISOR_DIR, 'bin', 'supervisor');

export class ChatHarnessNotAvailableError extends Error {
  constructor(reason: string) {
    super(`chat harness skipped: ${reason}`);
    this.name = 'ChatHarnessNotAvailableError';
  }
}

export interface ChatHarnessEnv extends HarnessEnv {
  /** Per-test overrides applied via setSupervisorEnv. */
  supervisorEnv: Record<string, string>;
}

let chatSupervisorProc: ChildProcess | null = null;

export function chatStackAvailable(): { ok: boolean; reason?: string } {
  if (!existsSync(SUPERVISOR_BIN)) {
    return { ok: false, reason: `supervisor binary missing at ${SUPERVISOR_BIN}` };
  }
  // Probe for the mockclaude image. `docker image inspect` exits 0 if
  // the image exists; non-zero otherwise. We don't auto-build here —
  // that's a CI infra step (per FR-300 the mockclaude image is built
  // once and cached).
  const probe = spawnSync('docker', ['image', 'inspect', 'garrison-mockclaude:m5'], {
    stdio: 'pipe',
  });
  if (probe.status !== 0) {
    return { ok: false, reason: 'garrison-mockclaude:m5 image not present (run scripts/build-mockclaude.sh)' };
  }
  return { ok: true };
}

export function ensureChatStackAvailable(): void {
  const probe = chatStackAvailable();
  if (!probe.ok) {
    throw new ChatHarnessNotAvailableError(probe.reason ?? 'unknown');
  }
}

export function setSupervisorEnv(
  base: ChatHarnessEnv,
  overrides: Record<string, string>,
): ChatHarnessEnv {
  return { ...base, supervisorEnv: { ...base.supervisorEnv, ...overrides } };
}

export interface BootSupervisorOptions {
  baseEnv: HarnessEnv;
  supervisorEnv?: Record<string, string>;
}

/**
 * Boot the supervisor binary against the existing testcontainer
 * Postgres + Infisical. Returns the spawned ChildProcess so the
 * caller can stop it via teardown(). Throws
 * ChatHarnessNotAvailableError when the binary or mockclaude image
 * is unavailable.
 */
export async function bootChatSupervisor(opts: BootSupervisorOptions): Promise<ChildProcess> {
  ensureChatStackAvailable();
  const env: NodeJS.ProcessEnv = {
    ...process.env,
    ...opts.baseEnv,
    GARRISON_CHAT_CONTAINER_IMAGE: 'garrison-mockclaude:m5',
    ...(opts.supervisorEnv ?? {}),
  };
  const proc = spawn(SUPERVISOR_BIN, [], {
    cwd: SUPERVISOR_DIR,
    env,
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  proc.stdout?.on('data', (chunk) => {
    if (process.env.GARRISON_TEST_VERBOSE) {
      process.stdout.write(`[supervisor] ${String(chunk)}`);
    }
  });
  proc.stderr?.on('data', (chunk) => {
    if (process.env.GARRISON_TEST_VERBOSE) {
      process.stderr.write(`[supervisor] ${String(chunk)}`);
    }
  });
  // Give the supervisor time to LISTEN on chat.message.sent before
  // returning. 1s is conservative; the M5.1 happy-path test takes ~250ms
  // to reach the LISTEN-bound state.
  await new Promise((r) => setTimeout(r, 1000));
  chatSupervisorProc = proc;
  return proc;
}

export async function stopChatSupervisor(): Promise<void> {
  const p = chatSupervisorProc;
  if (!p) return;
  chatSupervisorProc = null;
  p.kill('SIGTERM');
  // 5s grace before SIGKILL per AGENTS.md concurrency rule 7.
  await new Promise<void>((resolve) => {
    const timer = setTimeout(() => {
      try {
        p.kill('SIGKILL');
      } catch {
        // ignore
      }
      resolve();
    }, 5000);
    p.once('exit', () => {
      clearTimeout(timer);
      resolve();
    });
  });
}
