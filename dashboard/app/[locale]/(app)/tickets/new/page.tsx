import { fetchDepartmentRows } from '@/lib/queries/orgOverview';
import { TicketCreateForm } from '@/components/features/ticket-create/TicketCreateForm';

// Operator-driven ticket creation route per FR-025 / FR-049.
// Server Component fetches the department list, then hydrates the
// TicketCreateForm Client Component which calls createTicket.

export const dynamic = 'force-dynamic';

interface RouteParams {
  searchParams: Promise<{ dept?: string }>;
}

export default async function TicketCreatePage({ searchParams }: Readonly<RouteParams>) {
  const [params, deptRows] = await Promise.all([
    searchParams,
    fetchDepartmentRows(),
  ]);
  const departments = deptRows.map((d) => ({ slug: d.slug, name: d.name }));

  return (
    <div className="px-6 py-5 space-y-5 max-w-[800px] mx-auto">
      <header className="space-y-1">
        <h1 className="text-text-1 text-2xl font-semibold tracking-tight">
          Create ticket
        </h1>
        <p className="text-text-3 text-[12px]">
          Operator-authored. Lands in the source column ('todo' by default) of
          the chosen department.
        </p>
      </header>

      {departments.length === 0 ? (
        <div className="rounded border border-warn/40 bg-warn/5 px-4 py-3 text-[12.5px] text-warn">
          No departments configured yet. Create one in the database before
          authoring tickets.
        </div>
      ) : (
        <TicketCreateForm
          departments={departments}
          initialDeptSlug={params.dept}
        />
      )}
    </div>
  );
}
