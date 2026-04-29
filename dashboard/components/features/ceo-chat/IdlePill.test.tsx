// M5.2 — IdlePill renders the active/idle/aborted vocabulary.

import { describe, it, expect } from 'vitest';
import { renderToString } from 'react-dom/server';
import { IdlePill } from './IdlePill';

describe('IdlePill', () => {
  it('TestIdlePillRendersGreenForActive', () => {
    const html = renderToString(<IdlePill status="active" />);
    const visible = html.replace(/<!--\s*-->/g, '');
    expect(visible).toContain('data-status="active"');
    expect(visible).toContain('active');
    // StatusDot tone='ok' surfaces the ok class — kept loose to
    // tolerate StatusDot internal markup changes.
    expect(visible.toLowerCase()).toContain('ok');
  });

  it('TestIdlePillRendersYellowForEnded', () => {
    const html = renderToString(<IdlePill status="ended" />);
    const visible = html.replace(/<!--\s*-->/g, '');
    expect(visible).toContain('data-status="ended"');
    expect(visible).toContain('idle');
    expect(visible.toLowerCase()).toContain('warn');
  });

  it('TestIdlePillRendersRedForAborted', () => {
    const html = renderToString(<IdlePill status="aborted" />);
    const visible = html.replace(/<!--\s*-->/g, '');
    expect(visible).toContain('data-status="aborted"');
    expect(visible).toContain('aborted');
    expect(visible.toLowerCase()).toContain('err');
  });
});
