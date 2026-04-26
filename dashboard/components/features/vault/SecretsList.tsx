import { getTranslations } from 'next-intl/server';
import { Chip } from '@/components/ui/Chip';
import { Tbl, Th, Td } from '@/components/ui/Tbl';
import { relativeTime, formatIsoFull } from '@/lib/format/relativeTime';
import type { SecretMetadataRow } from '@/lib/queries/vault';

function rotationTone(
  status: SecretMetadataRow['rotationStatus'],
): 'ok' | 'warn' | 'err' | 'neutral' {
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

export async function SecretsList({
  rows,
}: Readonly<{ rows: SecretMetadataRow[] }>) {
  const t = await getTranslations('vault.headers');
  return (
    <Tbl>
      <thead>
        <tr>
          <Th>{t('path')}</Th>
          <Th>{t('roles')}</Th>
          <Th>{t('lastRotated')}</Th>
          <Th>{t('status')}</Th>
        </tr>
      </thead>
      <tbody>
        {rows.map((r) => (
          <tr key={`${r.secretPath}:${r.customerId}`} data-testid="secret-row">
            <Td>
              <code className="font-mono text-[12px] text-text-1">
                {r.secretPath}
              </code>
            </Td>
            <Td>
              <div className="flex gap-1.5 flex-wrap">
                {r.allowedRoleSlugs.length === 0 ? (
                  <span className="text-text-3 text-xs">—</span>
                ) : (
                  r.allowedRoleSlugs.map((s) => (
                    <Chip key={s} tone="neutral">
                      {s}
                    </Chip>
                  ))
                )}
              </div>
            </Td>
            <Td>
              {r.lastRotatedAt ? (
                <span
                  className="font-mono font-tabular text-[12px] text-text-2"
                  title={formatIsoFull(r.lastRotatedAt)}
                >
                  {relativeTime(r.lastRotatedAt)}
                </span>
              ) : (
                <span className="text-text-3 text-xs">—</span>
              )}
            </Td>
            <Td>
              <Chip tone={rotationTone(r.rotationStatus)}>
                {r.rotationStatus}
              </Chip>
            </Td>
          </tr>
        ))}
      </tbody>
    </Tbl>
  );
}
