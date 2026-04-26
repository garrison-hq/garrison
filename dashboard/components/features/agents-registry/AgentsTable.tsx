import { Chip } from '@/components/ui/Chip';
import { Tbl, Th, Td } from '@/components/ui/Tbl';
import type { AgentRow } from '@/lib/queries/agents';

function formatRelative(d: Date | null): string {
  if (!d) return '—';
  const ms = Date.now() - new Date(d).getTime();
  const mins = Math.round(ms / (1000 * 60));
  if (mins < 60) return `${mins}m ago`;
  const hours = Math.round(mins / 60);
  if (hours < 48) return `${hours}h ago`;
  const days = Math.round(hours / 24);
  return `${days}d ago`;
}

function formatListensFor(value: unknown): string {
  if (Array.isArray(value)) return value.join(', ');
  if (typeof value === 'string') return value;
  return JSON.stringify(value);
}

export function AgentsTable({ rows }: { rows: AgentRow[] }) {
  return (
    <Tbl>
      <thead>
        <tr>
          <Th>department</Th>
          <Th>role</Th>
          <Th>model</Th>
          <Th>cap</Th>
          <Th>listens for</Th>
          <Th>last spawn</Th>
          <Th>spawns / 7d</Th>
        </tr>
      </thead>
      <tbody>
        {rows.map((r) => {
          const listens = formatListensFor(r.listensFor);
          return (
            <tr key={r.id} data-testid="agent-row">
              <Td>
                <span className="font-mono text-text-1 text-xs">{r.departmentSlug}</span>
              </Td>
              <Td>
                <Chip tone="info">{r.roleSlug}</Chip>
              </Td>
              <Td>
                <span className="font-mono text-text-2 text-xs">{r.model}</span>
              </Td>
              <Td>
                <span className="font-mono text-text-2">{r.concurrencyCap}</span>
              </Td>
              <Td>
                <code
                  className="font-mono text-text-2 text-[11px] truncate max-w-[200px] block"
                  title={listens}
                  data-testid="listens-for"
                >
                  {listens}
                </code>
              </Td>
              <Td>
                <span className="font-mono text-text-2 text-xs">
                  {formatRelative(r.lastSpawnedAt)}
                </span>
              </Td>
              <Td>
                <span className="font-mono text-text-1">
                  {r.spawnsThisWeek === 0 ? '—' : r.spawnsThisWeek}
                </span>
              </Td>
            </tr>
          );
        })}
      </tbody>
    </Tbl>
  );
}
