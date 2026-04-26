import Image from 'next/image';
import Link from 'next/link';
import { getTranslations } from 'next-intl/server';
import { StatusDot } from '@/components/ui/StatusDot';

// Static sidebar navigation — same vocabulary as the mocks'
// shell.jsx + the navigation labels in messages/en.json.
//
// Department links are hardcoded to engineering + qa-engineer per
// AGENTS.md §Scope discipline ("Multi-department wire-up beyond
// engineering + qa-engineer is post-M3"). T010+ surfaces query
// the agents/departments tables at request time.

export async function Sidebar() {
  const t = await getTranslations('nav');
  return (
    <aside className="w-60 bg-surface-1 border-r border-border-1 flex-shrink-0 flex flex-col py-3">
      <Link href="/" className="px-4 pb-3 block">
        <Image
          src="/brand/lockup/garrison-lockup-dark.svg"
          alt="Garrison"
          width={160}
          height={28}
          className="theme-asset-dark h-7 w-auto"
          priority
        />
        <Image
          src="/brand/lockup/garrison-lockup-light.svg"
          alt="Garrison"
          width={160}
          height={28}
          className="theme-asset-light h-7 w-auto"
          priority
        />
      </Link>

      <nav className="flex-1 flex flex-col gap-0.5 px-2">
        <NavLink href="/" label={t('orgOverview')} />
        <NavGroup label={t('departments')}>
          <NavSubLink href="/departments/engineering" label="engineering" />
          <NavSubLink href="/departments/qa-engineer" label="qa-engineer" />
        </NavGroup>
        <NavLink href="/activity" label={t('activity')} />
        <NavLink href="/hygiene" label={t('hygiene')} />
        <NavLink href="/vault" label={t('vault')} />
        <NavLink href="/agents" label={t('agents')} />
        <NavGroup label={t('admin')}>
          <NavSubLink href="/admin/invites" label="invites" />
        </NavGroup>
      </nav>
    </aside>
  );
}

function NavLink({ href, label }: Readonly<{ href: string; label: string }>) {
  return (
    <Link
      href={href}
      className="flex items-center gap-2 px-3 py-1.5 rounded text-text-2 hover:bg-surface-2 hover:text-text-1 text-sm"
    >
      <StatusDot tone="neutral" />
      <span>{label}</span>
    </Link>
  );
}

function NavGroup({ label, children }: Readonly<{ label: string; children: React.ReactNode }>) {
  return (
    <div className="mt-3 mb-1">
      <div className="px-3 py-1 text-[10px] uppercase tracking-wider text-text-3 font-medium">
        {label}
      </div>
      <div className="flex flex-col gap-0.5">{children}</div>
    </div>
  );
}

function NavSubLink({ href, label }: Readonly<{ href: string; label: string }>) {
  return (
    <Link
      href={href}
      className="flex items-center gap-2 pl-9 pr-3 py-1 rounded text-text-2 hover:bg-surface-2 hover:text-text-1 text-xs font-mono"
    >
      {label}
    </Link>
  );
}
