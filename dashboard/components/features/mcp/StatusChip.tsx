import { Chip } from '@/components/ui/Chip';
import type { McpServerStatus } from '@/lib/queries/mcpServers';

const toneByStatus: Record<McpServerStatus, 'neutral' | 'accent' | 'ok' | 'warn'> = {
  pending: 'accent',
  registered: 'ok',
  failed: 'warn',
  deregistered: 'neutral',
};

const labelByStatus: Record<McpServerStatus, string> = {
  pending: 'Pending',
  registered: 'Registered',
  failed: 'Failed',
  deregistered: 'Deregistered',
};

export function StatusChip({ status }: Readonly<{ status: McpServerStatus }>) {
  return <Chip tone={toneByStatus[status]}>{labelByStatus[status]}</Chip>;
}
