import { Chip } from '@/components/ui/Chip';
import type { TicketDetailMetadata } from '@/lib/queries/ticketDetail';

export function MetadataBlock({ metadata }: Readonly<{ metadata: TicketDetailMetadata }>) {
  return (
    <section className="bg-surface-1 border border-border-1 rounded p-4 space-y-2">
      <div className="flex items-center justify-between">
        <h2 className="text-text-1 text-base font-semibold">{metadata.objective}</h2>
        <Chip tone="neutral">{metadata.columnSlug}</Chip>
      </div>
      <dl className="grid grid-cols-2 sm:grid-cols-4 gap-3 text-xs">
        <div>
          <dt className="text-text-3 uppercase tracking-wider">id</dt>
          <dd className="text-text-1 font-mono">{metadata.id.slice(0, 8)}</dd>
        </div>
        <div>
          <dt className="text-text-3 uppercase tracking-wider">department</dt>
          <dd className="text-text-1 font-mono">{metadata.departmentSlug}</dd>
        </div>
        <div>
          <dt className="text-text-3 uppercase tracking-wider">created</dt>
          <dd className="text-text-2 font-mono">
            {new Date(metadata.createdAt).toISOString().slice(0, 16)}Z
          </dd>
        </div>
        <div>
          <dt className="text-text-3 uppercase tracking-wider">origin</dt>
          <dd className="text-text-2 font-mono">{metadata.origin}</dd>
        </div>
      </dl>
      {metadata.acceptanceCriteria ? (
        <div className="pt-2 border-t border-border-1">
          <dt className="text-text-3 text-xs uppercase tracking-wider">acceptance criteria</dt>
          <dd className="text-text-2 text-sm whitespace-pre-wrap">{metadata.acceptanceCriteria}</dd>
        </div>
      ) : null}
    </section>
  );
}
