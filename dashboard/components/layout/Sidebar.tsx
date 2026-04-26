import Image from 'next/image';
import Link from 'next/link';
import { getTranslations } from 'next-intl/server';
import { StatusDot } from '@/components/ui/StatusDot';
import { getSession } from '@/lib/auth/session';
import { fetchSidebarStats } from '@/lib/queries/sidebarStats';
import {
  AdminIcon,
  AgentIcon,
  ActivityIcon,
  DeptIcon,
  HomeIcon,
  HygieneIcon,
  VaultIcon,
  GearIcon,
} from '@/components/ui/icons';

// Sidebar — same vocabulary as .workspace/m3-mocks/garrison-reference/
// shell.jsx. Three sections:
//   1. Brand lockup at the top (theme-aware: dark/light variants
//      ship as two <img> tags, CSS hides the wrong one).
//   2. Nav with thin-line icons. Department links are hardcoded to
//      engineering + qa-engineer per AGENTS.md §Scope discipline.
//   3. Live-agents status box + operator footer at the bottom.
//
// `fetchSidebarStats` is a 1-row query — adding it to every layout
// render is fine; the M3 read-only data plane is built around
// short, stateless queries per surface.

export async function Sidebar() {
  const t = await getTranslations('nav');
  const session = await getSession();
  const stats = await fetchSidebarStats();
  const email = session?.user.email ?? '';
  const initials = email
    .split('@')[0]
    .split(/[._-]/)
    .map((s) => s[0]?.toUpperCase() ?? '')
    .slice(0, 2)
    .join('') || 'OP';

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
        <NavLink href="/" label={t('orgOverview')} icon={<HomeIcon />} />
        <NavGroup label={t('departments')} icon={<DeptIcon />}>
          <NavSubLink href="/departments/engineering" label="engineering" />
          <NavSubLink href="/departments/qa-engineer" label="qa-engineer" />
        </NavGroup>
        <NavLink href="/activity" label={t('activity')} icon={<ActivityIcon />} />
        <NavLink href="/hygiene" label={t('hygiene')} icon={<HygieneIcon />} />
        <NavLink href="/vault" label={t('vault')} icon={<VaultIcon />} />
        <NavLink href="/agents" label={t('agents')} icon={<AgentIcon />} />
        <NavGroup label={t('admin')} icon={<AdminIcon />}>
          <NavSubLink href="/admin/invites" label="invites" />
        </NavGroup>
      </nav>

      <div className="px-3 pt-3">
        <div className="border border-border-1 rounded bg-surface-2 px-2.5 py-2 text-[11px] text-text-2 leading-snug">
          <div className="flex items-center gap-1.5 text-text-1">
            <StatusDot tone="ok" pulse />
            <span className="font-medium">{stats.liveAgents} agents live</span>
          </div>
          <div className="text-text-3 font-mono text-[10px] mt-0.5">
            {stats.liveAgents} / {stats.totalCapacity} capacity
          </div>
        </div>
      </div>

      <div className="mx-3 mt-3 flex items-center gap-2 border-t border-border-1 pt-3">
        <div className="w-7 h-7 rounded bg-surface-3 text-text-1 grid place-items-center font-mono font-semibold text-[10px]">
          {initials}
        </div>
        <div className="flex-1 min-w-0">
          <div className="text-text-1 text-xs truncate" title={email}>
            {email || '—'}
          </div>
          <div className="text-text-3 text-[10px] font-mono">operator</div>
        </div>
        <span className="text-text-3 hover:text-text-1 cursor-default" title="Settings (M4+)">
          <GearIcon />
        </span>
      </div>
    </aside>
  );
}

function NavLink({
  href,
  label,
  icon,
}: Readonly<{ href: string; label: string; icon: React.ReactNode }>) {
  return (
    <Link
      href={href}
      className="flex items-center gap-2 px-3 py-1.5 rounded text-text-2 hover:bg-surface-2 hover:text-text-1 text-sm"
    >
      <span className="text-text-3 group-hover:text-text-2 shrink-0">{icon}</span>
      <span>{label}</span>
    </Link>
  );
}

function NavGroup({
  label,
  icon,
  children,
}: Readonly<{ label: string; icon: React.ReactNode; children: React.ReactNode }>) {
  return (
    <div className="mt-3 mb-1">
      <div className="px-3 py-1 flex items-center gap-2 text-[10px] uppercase tracking-wider text-text-3 font-medium">
        <span className="shrink-0">{icon}</span>
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
      <StatusDot tone="ok" />
      {label}
    </Link>
  );
}
