import Link from 'next/link';
import { getTranslations } from 'next-intl/server';

// Three-tab strip shared by /vault, /vault/audit, /vault/matrix.
// Active tab: text-1 + 1px bottom-border accent. Inactive: text-3,
// no underline, hovers up to text-2. Lifts the look-and-feel from
// the operator-supplied design notes — replaces the previous
// underlined-anchor row.

type VaultTab = 'secrets' | 'audit' | 'matrix';

const TABS: { id: VaultTab; href: string; key: 'secretsTab' | 'auditTab' | 'matrixTab' }[] = [
  { id: 'secrets', href: '/vault', key: 'secretsTab' },
  { id: 'audit', href: '/vault/audit', key: 'auditTab' },
  { id: 'matrix', href: '/vault/matrix', key: 'matrixTab' },
];

export async function VaultTabs({ active }: Readonly<{ active: VaultTab }>) {
  const t = await getTranslations('vault');
  return (
    <nav
      aria-label={t('sectionsLabel')}
      className="flex items-center gap-4 border-b border-border-1"
    >
      {TABS.map((tab) => {
        const selected = tab.id === active;
        return (
          <Link
            key={tab.id}
            href={tab.href}
            className={`relative -mb-px px-1 py-2 text-[12px] transition-colors ${
              selected
                ? 'text-text-1 border-b border-accent'
                : 'text-text-3 hover:text-text-2 border-b border-transparent'
            }`}
            aria-current={selected ? 'page' : undefined}
          >
            {t(tab.key)}
          </Link>
        );
      })}
    </nav>
  );
}
