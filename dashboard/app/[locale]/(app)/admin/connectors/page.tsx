// M10 /admin/connectors — read-only connector-status surface (T015,
// plan §dashboard-surfaces, FR-701 / FR-702). Shows per-connector:
//   - last delivery received (from ingress_deliveries)
//   - accepted delivery count (from ingress_deliveries)
//   - bad-signature rejection count (from GET /ingress/status on the
//     supervisor dashboard-api port — an in-process atomic, NOT a DB row;
//     resets to zero on supervisor restart, per plan R3 / FR-301)
//   - rate-cap breach count (from throttle_events WHERE kind='ingress_rate_cap_exceeded')
//
// No CRUD — this surface is read-only (FR-701: connectors are configured
// at deploy time, not via the dashboard for alpha). Operator dashboard CRUD
// is a candidate follow-up.
//
// force-dynamic — connector state changes on every webhook delivery.
//
// Per AGENTS.md "Tests for Go only, never frontend": ships without vitest
// coverage; the Go-side T013/T014 integration suites pin the row shapes
// these surfaces read.

import { cookies } from 'next/headers';
import { desc, eq, sql, max, count } from 'drizzle-orm';
import { appDb } from '@/lib/db/appClient';
import { ingressDeliveries, throttleEvents } from '@/drizzle/schema.supervisor';
import { SoftPoll } from '@/components/features/org-overview/SoftPoll';
import { EmptyState } from '@/components/ui/EmptyState';
import { formatIsoFull } from '@/lib/format/relativeTime';

export const dynamic = 'force-dynamic';

// ConnectorDBRow is the per-connector summary read from Postgres.
interface ConnectorDBRow {
  connectorId: string;
  lastDeliveryAt: string | null;
  acceptedCount: number;
  ratecapBreachCount: number;
}

// Fetch per-connector last-delivery + accepted-count from ingress_deliveries,
// and rate-cap breach count from throttle_events. Mirrors the GetConnectorStatus
// sqlc query's shape but uses drizzle for the dashboard-side read.
async function fetchConnectorRows(): Promise<ConnectorDBRow[]> {
  // Accepted deliveries aggregated by connector.
  const deliveryRows = await appDb
    .select({
      connectorId: ingressDeliveries.connectorId,
      lastDeliveryAt: max(ingressDeliveries.createdAt),
      acceptedCount: count(ingressDeliveries.id),
    })
    .from(ingressDeliveries)
    .groupBy(ingressDeliveries.connectorId)
    .orderBy(desc(max(ingressDeliveries.createdAt)));

  // Rate-cap breach count per connector from throttle_events.
  const ratecapRows = await appDb
    .select({
      connectorId: sql<string>`(${throttleEvents.payload}->>'connector_id')::text`,
      breachCount: count(throttleEvents.id),
    })
    .from(throttleEvents)
    .where(eq(throttleEvents.kind, 'ingress_rate_cap_exceeded'))
    .groupBy(sql`(${throttleEvents.payload}->>'connector_id')::text`);

  const ratecapMap = new Map<string, number>(
    ratecapRows.map((r) => [r.connectorId, r.breachCount]),
  );

  return deliveryRows.map((r) => ({
    connectorId: r.connectorId,
    lastDeliveryAt: r.lastDeliveryAt ?? null,
    acceptedCount: r.acceptedCount,
    ratecapBreachCount: ratecapMap.get(r.connectorId) ?? 0,
  }));
}

// fetchBadSignatureRejections calls GET /ingress/status on the supervisor
// dashboard-api port (8081) to retrieve the in-process bad-signature
// rejection counter (FR-702, plan R3). Returns 0 on any fetch failure so
// the page renders gracefully when the supervisor is unreachable.
async function fetchBadSignatureRejections(): Promise<number> {
  const base = process.env.DASHBOARD_SUPERVISOR_API_URL;
  if (!base) return 0;

  const cookieStore = await cookies();
  const sessionCookie = cookieStore.get('better-auth.session_token');
  const cookieHeader = sessionCookie
    ? `better-auth.session_token=${sessionCookie.value}`
    : '';

  try {
    const res = await fetch(`${base}/ingress/status`, {
      headers: { Cookie: cookieHeader },
      cache: 'no-store',
    });
    if (!res.ok) return 0;
    const body = (await res.json()) as { bad_signature_rejections?: number };
    return body.bad_signature_rejections ?? 0;
  } catch {
    return 0;
  }
}

export default async function ConnectorsPage() {
  const [connectorRows, badSignatureRejections] = await Promise.all([
    fetchConnectorRows(),
    fetchBadSignatureRejections(),
  ]);

  return (
    <div className="px-6 py-5 space-y-6 max-w-[1400px] mx-auto w-full">
      <SoftPoll intervalMs={60_000} />
      <header className="space-y-1">
        <h1 className="text-text-1 text-2xl font-semibold tracking-tight">Connectors</h1>
        <p className="text-text-3 text-sm">
          Read-only status view for inbound connectors. Connectors are configured at deploy time
          via environment variables and vault secrets. CRUD is a planned follow-up.
        </p>
      </header>

      {/* Bad-signature rejection count is a process-level atomic: it
          applies across all connectors on this supervisor instance and
          resets to zero on supervisor restart (plan R3, FR-301). */}
      <div className="flex items-center gap-3 px-4 py-3 bg-surface-2 border border-border-1 rounded text-sm">
        <span className="text-text-3">Bad-signature rejections (since last supervisor restart)</span>
        <span className="font-mono text-text-1 font-semibold tabular-nums">
          {badSignatureRejections.toLocaleString()}
        </span>
        <span className="text-text-3 text-xs ml-auto">
          Resets to 0 on supervisor restart — not persisted to the database.
        </span>
      </div>

      {connectorRows.length === 0 ? (
        <EmptyState
          description="No deliveries recorded yet"
          caption="When the GitHub connector receives its first webhook delivery, the connector will appear here."
        />
      ) : (
        <table
          className="w-full text-sm border border-border-1 rounded"
          data-testid="connectors-table"
        >
          <thead className="bg-surface-2 text-text-3 text-[11px] uppercase tracking-[0.06em]">
            <tr>
              <th className="text-left px-3 py-2">Connector</th>
              <th className="text-left px-3 py-2">Last delivery</th>
              <th className="text-right px-3 py-2">Accepted</th>
              <th className="text-right px-3 py-2">Rate-cap breaches</th>
            </tr>
          </thead>
          <tbody>
            {connectorRows.map((row) => (
              <tr key={row.connectorId} className="border-t border-border-1 hover:bg-surface-2">
                <td className="px-3 py-2 font-mono text-text-1">{row.connectorId}</td>
                <td className="px-3 py-2 text-text-2 font-mono text-xs">
                  {row.lastDeliveryAt ? formatIsoFull(row.lastDeliveryAt) : 'never'}
                </td>
                <td className="px-3 py-2 text-right font-mono text-text-1 tabular-nums">
                  {row.acceptedCount.toLocaleString()}
                </td>
                <td className="px-3 py-2 text-right font-mono text-text-2 tabular-nums">
                  {row.ratecapBreachCount.toLocaleString()}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
