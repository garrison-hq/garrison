// Unit tests for the M5.4 Company.md Server Actions. Mocks
// `next/headers.cookies()` and global `fetch`. Goal: pin the call
// shape (URL, method, headers, body) and the typed-error mapping for
// every supervisor error kind.

import { describe, it, expect, vi, beforeEach } from 'vitest';

const cookiesMock = vi.fn();

vi.mock('next/headers', () => ({
  cookies: () => cookiesMock(),
}));

import { getCompanyMD, saveCompanyMD } from './companyMD';

const SUPERVISOR_URL = 'http://garrison-supervisor:8081';

interface FakeStore {
  get: (name: string) => { value: string } | undefined;
}

function fakeCookieStore(token: string | null): FakeStore {
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

describe('lib/actions/companyMD', () => {
  beforeEach(() => {
    cookiesMock.mockReset();
    // Reset fetch each test so we can assert on calls.
    vi.unstubAllGlobals();
    process.env.DASHBOARD_SUPERVISOR_API_URL = SUPERVISOR_URL;
  });

  // -- getCompanyMD --

  it('TestGetCompanyMD_HappyPath', async () => {
    cookiesMock.mockResolvedValue(fakeCookieStore('valid-token'));
    const fetchMock = vi
      .fn()
      .mockResolvedValue(jsonResponse(200, { content: '# Garrison', etag: '"abc"' }));
    vi.stubGlobal('fetch', fetchMock);

    const result = await getCompanyMD();

    expect(result).toEqual({ content: '# Garrison', etag: '"abc"', error: null });
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe(`${SUPERVISOR_URL}/api/objstore/company-md`);
    expect(init.method).toBe('GET');
    expect(init.headers).toEqual({ Cookie: 'better-auth.session_token=valid-token' });
  });

  it('TestGetCompanyMD_PropagatesAuthExpired', async () => {
    cookiesMock.mockResolvedValue(fakeCookieStore('stale-token'));
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(401, { error: 'AuthExpired' })));

    const result = await getCompanyMD();

    expect(result.error).toBe('AuthExpired');
    expect(result.content).toBe('');
    expect(result.etag).toBe(null);
  });

  it('TestGetCompanyMD_PropagatesUnreachable', async () => {
    cookiesMock.mockResolvedValue(fakeCookieStore('valid-token'));
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(jsonResponse(503, { error: 'MinIOUnreachable' })),
    );
    const result = await getCompanyMD();
    expect(result.error).toBe('MinIOUnreachable');
  });

  // -- saveCompanyMD --

  it('TestSaveCompanyMD_HappyPath', async () => {
    cookiesMock.mockResolvedValue(fakeCookieStore('valid-token'));
    const fetchMock = vi
      .fn()
      .mockResolvedValue(jsonResponse(200, { content: '# v2', etag: '"new-etag"' }));
    vi.stubGlobal('fetch', fetchMock);

    const result = await saveCompanyMD('# v2', '"old-etag"');

    expect(result).toEqual({ content: '# v2', etag: '"new-etag"', error: null });
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe(`${SUPERVISOR_URL}/api/objstore/company-md`);
    expect(init.method).toBe('PUT');
    expect(init.body).toBe('# v2');
    expect(init.headers['If-Match']).toBe('"old-etag"');
    expect(init.headers['Content-Type']).toBe('text/markdown');
  });

  it('TestSaveCompanyMD_PropagatesStale', async () => {
    cookiesMock.mockResolvedValue(fakeCookieStore('valid-token'));
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(jsonResponse(412, { error: 'Stale' })),
    );
    const result = await saveCompanyMD('body', '"stale"');
    expect(result.error).toBe('Stale');
  });

  it('TestSaveCompanyMD_PropagatesLeakScan', async () => {
    cookiesMock.mockResolvedValue(fakeCookieStore('valid-token'));
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        jsonResponse(422, { error: 'LeakScanFailed', pattern_category: 'sk-prefix' }),
      ),
    );
    const result = await saveCompanyMD('body with sk-...', '"old"');
    expect(result.error).toBe('LeakScanFailed');
    if (result.error === 'LeakScanFailed') {
      expect(result.patternCategory).toBe('sk-prefix');
    }
  });

  it('TestSaveCompanyMD_PropagatesTooLarge', async () => {
    cookiesMock.mockResolvedValue(fakeCookieStore('valid-token'));
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(jsonResponse(413, { error: 'TooLarge' })),
    );
    const result = await saveCompanyMD('big body', '"old"');
    expect(result.error).toBe('TooLarge');
  });

  it('TestSaveCompanyMD_PropagatesAuthExpired', async () => {
    cookiesMock.mockResolvedValue(fakeCookieStore('valid-token'));
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(jsonResponse(401, { error: 'AuthExpired' })),
    );
    const result = await saveCompanyMD('body', '"old"');
    expect(result.error).toBe('AuthExpired');
  });

  it('TestSaveCompanyMD_PropagatesUnreachable', async () => {
    cookiesMock.mockResolvedValue(fakeCookieStore('valid-token'));
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(jsonResponse(503, { error: 'MinIOUnreachable' })),
    );
    const result = await saveCompanyMD('body', '"old"');
    expect(result.error).toBe('MinIOUnreachable');
  });

  it('TestSaveCompanyMD_ForwardsCookie', async () => {
    cookiesMock.mockResolvedValue(fakeCookieStore('forwarded-token'));
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, { content: 'x', etag: 'e' }));
    vi.stubGlobal('fetch', fetchMock);

    await saveCompanyMD('body', '"old"');

    const [, init] = fetchMock.mock.calls[0];
    expect(init.headers.Cookie).toBe('better-auth.session_token=forwarded-token');
  });
});
