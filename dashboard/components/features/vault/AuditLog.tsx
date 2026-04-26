import { Chip } from '@/components/ui/Chip';
import { Tbl, Th, Td } from '@/components/ui/Tbl';
import type { VaultAuditRow } from '@/lib/queries/vault';

function outcomeTone(outcome: string): 'ok' | 'err' | 'warn' | 'neutral' {
  if (outcome === 'granted') return 'ok';
  if (outcome === 'denied' || outcome === 'fail_closed') return 'err';
  if (outcome === 'error') return 'warn';
  return 'neutral';
}

export function AuditLog({ rows }: { rows: VaultAuditRow[] }) {
  return (
    <Tbl>
      <thead>
        <tr>
          <Th>timestamp</Th>
          <Th>role</Th>
          <Th>secret path</Th>
          <Th>outcome</Th>
          <Th>ticket</Th>
        </tr>
      </thead>
      <tbody>
        {rows.map((r) => (
          <tr key={r.id} data-testid="audit-row">
            <Td>
              <span className="font-mono text-xs text-text-2">
                {new Date(r.timestamp).toISOString().slice(0, 16)}Z
              </span>
            </Td>
            <Td>
              <Chip tone="info">{r.roleSlug}</Chip>
            </Td>
            <Td>
              <code className="font-mono text-xs text-text-1">{r.secretPath}</code>
            </Td>
            <Td>
              <Chip tone={outcomeTone(r.outcome)}>{r.outcome}</Chip>
            </Td>
            <Td>
              {r.ticketId ? (
                <a
                  href={`/tickets/${r.ticketId}`}
                  className="font-mono text-xs text-info hover:underline"
                >
                  {r.ticketId.slice(0, 8)}
                </a>
              ) : (
                <span className="text-text-3">—</span>
              )}
            </Td>
          </tr>
        ))}
      </tbody>
    </Tbl>
  );
}
