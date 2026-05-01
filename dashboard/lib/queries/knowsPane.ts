// M5.4 read-side queries for the knows-pane. Mirror of
// lib/actions/companyMD.ts shape but for the read-only mempalace
// proxies (recent-writes + recent-kg). Server-only; runs inside Server
// Components / Server Actions, never on the client.

import { cookies } from 'next/headers';

const SESSION_COOKIE = 'better-auth.session_token';
const DEFAULT_LIMIT = 30;

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

export interface DrawerEntry {
  id: string;
  drawer_name: string;
  room_name: string;
  wing_name: string;
  source_agent_role_slug?: string;
  written_at: string;
  body_preview: string;
}

export interface KGTriple {
  id: string;
  subject: string;
  predicate: string;
  object: string;
  source_ticket_id?: string;
  source_agent_role_slug?: string;
  written_at: string;
}

export type KnowsPaneError = 'AuthExpired' | 'MempalaceUnreachable' | 'NetworkError';

export type GetRecentPalaceWritesResult =
  | { writes: DrawerEntry[]; error: null }
  | { writes: DrawerEntry[]; error: KnowsPaneError };

export type GetRecentKGFactsResult =
  | { facts: KGTriple[]; error: null }
  | { facts: KGTriple[]; error: KnowsPaneError };

interface SupervisorErrorBody {
  error?: string;
  message?: string;
}

function mapErr(error: string | undefined): KnowsPaneError {
  if (error === 'AuthExpired') return 'AuthExpired';
  return 'MempalaceUnreachable';
}

async function fetchSupervisor(path: string, limit: number): Promise<Response | null> {
  const cookieHeader = await buildCookieHeader();
  const params = new URLSearchParams({ limit: String(limit) });
  try {
    return await fetch(`${supervisorURL(path)}?${params.toString()}`, {
      method: 'GET',
      headers: cookieHeader ? { Cookie: cookieHeader } : {},
      cache: 'no-store',
    });
  } catch {
    return null;
  }
}

export async function getRecentPalaceWrites(
  limit: number = DEFAULT_LIMIT,
): Promise<GetRecentPalaceWritesResult> {
  const res = await fetchSupervisor('/api/mempalace/recent-writes', limit);
  if (res === null) {
    return { writes: [], error: 'NetworkError' };
  }

  if (res.ok) {
    try {
      const body = (await res.json()) as { writes?: DrawerEntry[] };
      return { writes: body.writes ?? [], error: null };
    } catch {
      return { writes: [], error: 'MempalaceUnreachable' };
    }
  }

  let errBody: SupervisorErrorBody = {};
  try {
    errBody = (await res.json()) as SupervisorErrorBody;
  } catch {
    /* ignore */
  }
  return { writes: [], error: mapErr(errBody.error) };
}

export async function getRecentKGFacts(
  limit: number = DEFAULT_LIMIT,
): Promise<GetRecentKGFactsResult> {
  const res = await fetchSupervisor('/api/mempalace/recent-kg', limit);
  if (res === null) {
    return { facts: [], error: 'NetworkError' };
  }

  if (res.ok) {
    try {
      const body = (await res.json()) as { facts?: KGTriple[] };
      return { facts: body.facts ?? [], error: null };
    } catch {
      return { facts: [], error: 'MempalaceUnreachable' };
    }
  }

  let errBody: SupervisorErrorBody = {};
  try {
    errBody = (await res.json()) as SupervisorErrorBody;
  } catch {
    /* ignore */
  }
  return { facts: [], error: mapErr(errBody.error) };
}
