// Unit tests for the session reader. Stubs Next's `headers()` helper
// and better-auth's session API so the tests run without a live
// Postgres or HTTP request — the goal is to pin the call shape, not
// to exercise better-auth's internals (those are covered by the
// integration suite in T006/T007).

import { describe, it, expect, vi, beforeEach } from 'vitest';

const headersMock = vi.fn();
const getSessionMock = vi.fn();

vi.mock('next/headers', () => ({
  headers: () => headersMock(),
}));

vi.mock('./index', () => ({
  auth: {
    api: {
      getSession: (args: { headers: Headers }) => getSessionMock(args),
    },
  },
}));

import { getSession } from './session';

describe('lib/auth/session', () => {
  beforeEach(() => {
    headersMock.mockReset();
    getSessionMock.mockReset();
  });

  it('sessionReaderReturnsNullForUnauthenticatedRequests', async () => {
    headersMock.mockResolvedValue(new Headers());
    getSessionMock.mockResolvedValue(null);

    const result = await getSession();

    expect(result).toBeNull();
    expect(getSessionMock).toHaveBeenCalledTimes(1);
    expect(getSessionMock.mock.calls[0][0]).toHaveProperty('headers');
  });

  it('sessionReaderReturnsTheSessionForAuthenticatedRequests', async () => {
    const fakeHeaders = new Headers({ cookie: 'better-auth.session_token=abc' });
    const fakeSession = {
      user: {
        id: 'op-1',
        email: 'op@example.com',
        name: 'Op One',
        emailVerified: true,
        image: null,
        createdAt: new Date('2026-04-26T00:00:00Z'),
        updatedAt: new Date('2026-04-26T00:00:00Z'),
        theme_preference: 'dark',
      },
      session: {
        id: 'sess-1',
        userId: 'op-1',
        token: 'sess-token',
        expiresAt: new Date('2026-05-26T00:00:00Z'),
        createdAt: new Date('2026-04-26T00:00:00Z'),
        ipAddress: null,
        userAgent: null,
      },
    };

    headersMock.mockResolvedValue(fakeHeaders);
    getSessionMock.mockResolvedValue(fakeSession);

    const result = await getSession();

    expect(result).toEqual(fakeSession);
    expect(getSessionMock).toHaveBeenCalledTimes(1);
    expect(getSessionMock.mock.calls[0][0].headers).toBe(fakeHeaders);
  });
});
