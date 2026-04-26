import { Chip } from '@/components/ui/Chip';
import { Tbl, Th, Td } from '@/components/ui/Tbl';
import type { SecretMetadataRow } from '@/lib/queries/vault';

function rotationTone(status: SecretMetadataRow['rotationStatus']): 'ok' | 'warn' | 'err' | 'neutral' {
  switch (status) {
    case 'fresh':
      return 'ok';
    case 'aging':
      return 'warn';
    case 'overdue':
      return 'err';
    case 'never':
      return 'neutral';
  }
}

export function SecretsList({ rows }: { rows: SecretMetadataRow[] }) {
  return (
    <Tbl>
      <thead>
        <tr>
          <Th>path</Th>
          <Th>roles</Th>
          <Th>last rotated</Th>
          <Th>status</Th>
        </tr>
      </thead>
      <tbody>
        {rows.map((r) => (
          <tr key={`${r.secretPath}:${r.customerId}`} data-testid="secret-row">
            <Td>
              <code className="font-mono text-xs text-text-1">{r.secretPath}</code>
            </Td>
            <Td>
              <div className="flex gap-1 flex-wrap">
                {r.allowedRoleSlugs.map((s) => (
                  <Chip key={s} tone="info">
                    {s}
                  </Chip>
                ))}
              </div>
            </Td>
            <Td>
              <span className="font-mono text-xs text-text-2">
                {r.lastRotatedAt ? new Date(r.lastRotatedAt).toISOString().slice(0, 10) : '—'}
              </span>
            </Td>
            <Td>
              <Chip tone={rotationTone(r.rotationStatus)}>{r.rotationStatus}</Chip>
            </Td>
          </tr>
        ))}
      </tbody>
    </Tbl>
  );
}
