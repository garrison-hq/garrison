import Link from 'next/link';
import { getTranslations } from 'next-intl/server';

// Three-tab strip shared by /vault, /vault/audit, /vault/matrix.
// Active tab: text-1 + 1px bottom-border accent. Inactive: text-3,
// no underline, hovers up to text-2. Lifts the look-and-feel from
// the operator-supplied design notes — replaces the previous
// underlined-anchor row.

type VaultTab = 'secrets' | 'audit' | 'matrix' | 'rotation';

const TABS: { id: VaultTab; href: string; label: string }[] = [
  { id: 'secrets', href: '/vault', label: 'secrets' },
  { id: 'audit', href: '/vault/audit', label: 'audit' },
  { id: 'matrix', href: '/vault/matrix', label: 'matrix' },
  { id: 'rotation', href: '/vault/rotation', label: 'rotation' },
];

export async function VaultTabs({ active }: Readonly<{ active: VaultTab }>) {
  const t = await getTranslations('vault');
  // M3 catalog covers secretsTab/auditTab/matrixTab; M4 adds
  // rotation as a fourth tab. Until messages/en.json gains the
  // rotationTab key the tab uses an inline literal label.
  const labelFor = (tab: typeof TABS[number]): string => {
    if (tab.id === 'rotation') return 'rotation';
    if (tab.id === 'secrets') return t('secretsTab');
    if (tab.id === 'audit') return t('auditTab');
    return t('matrixTab');
  };
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
            {labelFor(tab)}
          </Link>
        );
      })}
    </nav>
  );
}
