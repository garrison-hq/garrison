// M5.2 — Composer static rendering pins.
//
// Interactive scenarios (Cmd+Enter submit, plain Enter newline,
// auto-focus, paste truncation) require user-event interaction +
// jsdom; those are exercised by the Playwright sub-scenarios in
// T013–T015 against a real browser. The SSR pins here cover the
// disabled-state matrix (ended / streaming / cost-cap), the
// always-rendered ⌘↵ shortcut hint, and the ARIA keyshortcuts
// attribute per FR-333.

import { describe, it, expect, vi } from 'vitest';
import { renderToString } from 'react-dom/server';

vi.mock('next/navigation', () => ({
  useRouter: () => ({ push: () => {}, refresh: () => {}, replace: () => {} }),
  usePathname: () => '/chat',
  useSearchParams: () => new URLSearchParams(),
}));

import { Composer } from './Composer';

function visible(html: string): string {
  return html.replace(/<!--\s*-->/g, '');
}

describe('Composer rendering matrix', () => {
  // `disabled=""` is the boolean-attribute SSR form; `disabled:opacity`
  // is the Tailwind variant in the className. Match the boolean form
  // explicitly so the className doesn't false-positive.
  const TEXTAREA_DISABLED = /<textarea[^>]*\sdisabled=""/;

  it('TestComposerDisablesOnEndedSession', () => {
    const html = renderToString(<Composer sessionId="s-1" status="ended" />);
    const v = visible(html);
    expect(v).toMatch(TEXTAREA_DISABLED);
    expect(v).toContain('This thread is ended');
  });

  it('TestComposerStaysEnabledOnAbortedSession', () => {
    // Per FR-215 'aborted' is NOT a disabled state.
    const html = renderToString(<Composer sessionId="s-2" status="aborted" />);
    const v = visible(html);
    expect(v).not.toMatch(TEXTAREA_DISABLED);
  });

  it('TestComposerDisablesDuringStreaming', () => {
    const html = renderToString(<Composer sessionId="s-3" status="active" isStreaming />);
    const v = visible(html);
    expect(v).toMatch(TEXTAREA_DISABLED);
    expect(v).toContain('CEO is responding');
  });

  it('disabled-cost-cap caption appears when latestErrorKind=session_cost_cap_reached', () => {
    const html = renderToString(
      <Composer sessionId="s-4" status="active" latestErrorKind="session_cost_cap_reached" />,
    );
    expect(visible(html)).toContain('cost cap');
  });

  it('renders the ⌘↵ keyboard hint and aria-keyshortcuts attribute (FR-333)', () => {
    const html = renderToString(<Composer sessionId="s-5" status="active" />);
    const v = visible(html);
    expect(v).toContain('⌘↵');
    expect(v).toContain('aria-keyshortcuts="Meta+Enter"');
  });

  it('TestComposerSendButtonDisabledForEmpty', () => {
    // Empty draft: send button disabled at render time too (we
    // re-evaluate on each keystroke client-side). React SSR may emit
    // the `disabled` boolean attribute before any other attribute on
    // the button, so look for the data-testid + disabled in either
    // order via a substring scan.
    const html = renderToString(<Composer sessionId="s-6" status="active" />);
    const v = visible(html);
    const sendButtonMatch = /<button[^>]*data-testid="chat-composer-send"[^>]*>/.exec(v);
    const sendButtonAlt = /<button[^>]*disabled=""[^>]*data-testid="chat-composer-send"/.exec(v);
    if (sendButtonMatch) {
      expect(sendButtonMatch[0]).toMatch(/\sdisabled=""/);
    } else {
      expect(sendButtonAlt).not.toBeNull();
    }
  });

  it('renders the palace-live chip in the footer', () => {
    const html = renderToString(<Composer sessionId="s-7" status="active" palaceAgeMs={120_000} />);
    expect(visible(html)).toContain('palace-live-chip');
  });
});
