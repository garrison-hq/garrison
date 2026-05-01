'use server';

// M5.4 Server Actions for the Company.md tab. Thin wrappers over the
// supervisor's /api/objstore/company-md proxy:
//
//   getCompanyMD()                    → { content, etag, error: null }
//                                       on 200 hit; { content: '', etag:
//                                       null, error } on non-200.
//   saveCompanyMD(content, ifMatch)   → { content, etag, error: null }
//                                       on 200; typed-error union on
//                                       non-200 (Stale | LeakScanFailed
//                                       | TooLarge | AuthExpired |
//                                       MinIOUnreachable | MinIOAuthFailed).
//
// Cookie-forward pattern (decision 9a in the structural-decision slate):
// the action reads `better-auth.session_token` via `cookies()` and adds
// it to the outgoing fetch's Cookie header. The supervisor's
// dashboardapi auth middleware validates against the dashboard's
// better-auth `sessions` table.

import { cookies } from 'next/headers';

const SESSION_COOKIE = 'better-auth.session_token';

function supervisorURL(path: string): string {
  const base =
    process.env.DASHBOARD_SUPERVISOR_API_URL ?? 'http://garrison-supervisor:8081';
  return `${base}${path}`;
}

async function buildCookieHeader(): Promise<string> {
  const store = await cookies();
  const cookie = store.get(SESSION_COOKIE);
  if (!cookie) return '';
  return `${SESSION_COOKIE}=${cookie.value}`;
}

export type CompanyMDError =
  | 'Stale'
  | 'LeakScanFailed'
  | 'TooLarge'
  | 'AuthExpired'
  | 'MinIOUnreachable'
  | 'MinIOAuthFailed'
  | 'InternalError'
  | 'NetworkError';

export type GetCompanyMDResult =
  | { content: string; etag: string | null; error: null }
  | {
      content: string;
      etag: null;
      error: CompanyMDError;
      message?: string;
    };

export type SaveCompanyMDResult =
  | { content: string; etag: string; error: null }
  | {
      error: CompanyMDError;
      message?: string;
      patternCategory?: string;
    };

interface SupervisorErrorBody {
  error?: string;
  message?: string;
  pattern_category?: string;
}

function mapErrorKind(error: string | undefined, fallback: CompanyMDError): CompanyMDError {
  if (!error) return fallback;
  // The supervisor's writeErrorResponse sends the canonical kinds.
  switch (error) {
    case 'Stale':
    case 'LeakScanFailed':
    case 'TooLarge':
    case 'AuthExpired':
    case 'MinIOUnreachable':
    case 'MinIOAuthFailed':
      return error;
    default:
      return 'InternalError';
  }
}

async function safeJSON<T>(res: Response): Promise<T | null> {
  try {
    return (await res.json()) as T;
  } catch {
    return null;
  }
}

export async function getCompanyMD(): Promise<GetCompanyMDResult> {
  const cookieHeader = await buildCookieHeader();
  let res: Response;
  try {
    res = await fetch(supervisorURL('/api/objstore/company-md'), {
      method: 'GET',
      headers: cookieHeader ? { Cookie: cookieHeader } : {},
      cache: 'no-store',
    });
  } catch {
    return {
      content: '',
      etag: null,
      error: 'NetworkError',
      message: 'Could not reach the supervisor API.',
    };
  }

  if (res.ok) {
    const body = (await safeJSON<{ content: string; etag: string | null }>(res)) ?? {
      content: '',
      etag: null,
    };
    return { content: body.content ?? '', etag: body.etag ?? null, error: null };
  }

  const errBody = (await safeJSON<SupervisorErrorBody>(res)) ?? {};
  if (res.status === 401) {
    return { content: '', etag: null, error: 'AuthExpired', message: errBody.message };
  }
  return {
    content: '',
    etag: null,
    error: mapErrorKind(errBody.error, 'InternalError'),
    message: errBody.message,
  };
}

export async function saveCompanyMD(
  content: string,
  ifMatchEtag: string | null,
): Promise<SaveCompanyMDResult> {
  const cookieHeader = await buildCookieHeader();
  // Empty etag is the FR-624 first-save signal — stringified through
  // If-Match as the empty string. The supervisor pre-checks that the
  // object is missing in that case.
  const ifMatch = ifMatchEtag ?? '';

  let res: Response;
  try {
    res = await fetch(supervisorURL('/api/objstore/company-md'), {
      method: 'PUT',
      headers: {
        ...(cookieHeader ? { Cookie: cookieHeader } : {}),
        'If-Match': ifMatch,
        'Content-Type': 'text/markdown',
      },
      body: content,
      cache: 'no-store',
    });
  } catch {
    return { error: 'NetworkError', message: 'Could not reach the supervisor API.' };
  }

  if (res.ok) {
    const body = (await safeJSON<{ content: string; etag: string }>(res)) ?? {
      content,
      etag: '',
    };
    return { content: body.content ?? content, etag: body.etag ?? '', error: null };
  }

  const errBody = (await safeJSON<SupervisorErrorBody>(res)) ?? {};
  if (res.status === 401) {
    return { error: 'AuthExpired', message: errBody.message };
  }
  return {
    error: mapErrorKind(errBody.error, 'InternalError'),
    message: errBody.message,
    patternCategory: errBody.pattern_category,
  };
}
