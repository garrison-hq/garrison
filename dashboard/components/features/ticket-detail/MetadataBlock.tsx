import { Chip } from '@/components/ui/Chip';
import { columnTone } from '@/lib/format/columnTone';
import { formatShortDateTime, formatIsoFull } from '@/lib/format/relativeTime';
import type { TicketDetailMetadata } from '@/lib/queries/ticketDetail';

// Top block of the ticket-detail surface: title + status pill +
// metadata grid + acceptance criteria.
//
// Layout follows the operator-feedback polish:
//  - Title and pill share a baseline (items-baseline) so the pill
//    sits with the title, not floating above it.
//  - Metadata grid uses a clean key/value list; labels are muted
//    uppercase microcopy, values are normal weight in the column
//    rhythm (font-mono only for the genuinely token-shaped values:
//    id + dept slug + created).
//  - Acceptance criteria gets a quoted-style left-border panel so
//    it reads as a distinct artifact, not free-floating prose.

export function MetadataBlock({
  metadata,
}: Readonly<{ metadata: TicketDetailMetadata }>) {
  return (
    <section className="space-y-4">
      <div className="flex items-baseline justify-between gap-3">
        <h2 className="text-text-1 text-xl font-semibold tracking-tight leading-snug">
          {metadata.objective}
        </h2>
        <Chip tone={columnTone(metadata.columnSlug)}>
          {metadata.columnSlug}
        </Chip>
      </div>
      <dl className="grid grid-cols-2 sm:grid-cols-4 gap-x-5 gap-y-3">
        <Field label="id" value={metadata.id.slice(0, 8)} mono />
        <Field label="department" value={metadata.departmentSlug} mono />
        <Field
          label="created"
          value={formatShortDateTime(metadata.createdAt)}
          title={formatIsoFull(metadata.createdAt)}
          mono
        />
        <Field label="origin" value={metadata.origin} />
      </dl>
      {metadata.acceptanceCriteria ? (
        <div>
          <div className="text-text-3 text-[10.5px] uppercase tracking-[0.08em] font-medium mb-1.5">
            acceptance criteria
          </div>
          <div className="border-l-2 border-border-2 pl-3 py-1 text-text-2 text-[13px] leading-[1.55] whitespace-pre-wrap">
            {metadata.acceptanceCriteria}
          </div>
        </div>
      ) : null}
    </section>
  );
}

function Field({
  label,
  value,
  mono = false,
  title,
}: Readonly<{ label: string; value: string; mono?: boolean; title?: string }>) {
  return (
    <div className="space-y-1">
      <dt className="text-text-3 text-[10.5px] uppercase tracking-[0.08em] font-medium">
        {label}
      </dt>
      <dd
        className={`text-text-1 text-[13px] ${mono ? 'font-mono' : ''}`}
        title={title}
      >
        {value}
      </dd>
    </div>
  );
}
