// Unit tests for the M5.4 knows-pane queries. Mocks `next/headers.cookies()`
// and global `fetch`. Pins URL shape (limit param), happy-path JSON
// decoding, and typed-error propagation.

import { describe, it, expect, vi, beforeEach } from 'vitest';

const cookiesMock = vi.fn();

vi.mock('next/headers', () => ({
  cookies: () => cookiesMock(),
}));

import { getRecentPalaceWrites, getRecentKGFacts } from './knowsPane';

const SUPERVISOR_URL = 'http://garrison-supervisor:8081';

function fakeCookieStore(token: string | null) {
  return {
    get: (name: string) =>
      name === 'better-auth.session_token' && token != null ? { value: token } : undefined,
  };
}

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  });
}

describe('lib/queries/knowsPane', () => {
  beforeEach(() => {
    cookiesMock.mockReset();
    vi.unstubAllGlobals();
    process.env.DASHBOARD_SUPERVISOR_API_URL = SUPERVISOR_URL;
    cookiesMock.mockResolvedValue(fakeCookieStore('valid-token'));
  });

  it('TestGetRecentPalaceWrites_HappyPath', async () => {
    const writes = [
      {
        id: 'd1',
        drawer_name: 'first',
        room_name: 'r',
        wing_name: 'w',
        written_at: '2026-04-30T10:00:00Z',
        body_preview: 'preview-1',
      },
    ];
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(200, { writes })));

    const result = await getRecentPalaceWrites();

    expect(result.error).toBe(null);
    expect(result.writes).toHaveLength(1);
    expect(result.writes[0].id).toBe('d1');
  });

  it('TestGetRecentPalaceWrites_DefaultLimit', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, { writes: [] }));
    vi.stubGlobal('fetch', fetchMock);

    await getRecentPalaceWrites();

    const [url] = fetchMock.mock.calls[0];
    expect(url).toContain('limit=30');
  });

  it('TestGetRecentPalaceWrites_PropagatesUnreachable', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(jsonResponse(503, { error: 'MempalaceUnreachable' })),
    );

    const result = await getRecentPalaceWrites();

    expect(result.error).toBe('MempalaceUnreachable');
    expect(result.writes).toEqual([]);
  });

  it('TestGetRecentKGFacts_HappyPath', async () => {
    const facts = [
      {
        id: 't1',
        subject: 'a',
        predicate: 'b',
        object: 'c',
        written_at: '2026-04-30T10:00:00Z',
      },
    ];
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(200, { facts })));

    const result = await getRecentKGFacts();

    expect(result.error).toBe(null);
    expect(result.facts).toHaveLength(1);
    expect(result.facts[0].id).toBe('t1');
  });

  it('TestGetRecentKGFacts_PropagatesUnreachable', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(jsonResponse(503, { error: 'MempalaceUnreachable' })),
    );

    const result = await getRecentKGFacts();

    expect(result.error).toBe('MempalaceUnreachable');
    expect(result.facts).toEqual([]);
  });
});
