'use client';

import { useTransition } from 'react';
import { useLocale } from 'next-intl';
import { useRouter, usePathname } from 'next/navigation';
import { routing } from '@/lib/i18n/routing';

// Locale switcher placeholder. M3 ships only English so the switcher
// renders as read-only when there's a single locale; T008 set up the
// machinery so adding a second locale is a translation-file edit
// only. When a future locale enters routing.locales, this primitive
// shows it as a clickable chip and pushes the locale-prefixed
// pathname.

export function LocaleSwitcher() {
  const active = useLocale();
  const router = useRouter();
  const pathname = usePathname();
  const [pending, startTransition] = useTransition();

  function setLocale(next: string) {
    if (next === active) return;
    // Strip an existing locale prefix from the pathname (so
    // /en/login becomes /login, then we re-prefix with the next
    // locale via push('/en/login') etc).
    let stripped = pathname;
    for (const locale of routing.locales) {
      if (stripped === `/${locale}`) {
        stripped = '/';
        break;
      }
      if (stripped.startsWith(`/${locale}/`)) {
        stripped = stripped.slice(`/${locale}`.length);
        break;
      }
    }
    const target = next === routing.defaultLocale ? stripped : `/${next}${stripped}`;
    startTransition(() => router.push(target));
  }

  return (
    <div
      role="radiogroup"
      aria-label="Locale"
      className="inline-flex items-center bg-surface-2 border border-border-1 rounded p-0.5 text-[11px]"
    >
      {routing.locales.map((locale) => {
        const selected = locale === active;
        return (
          <button
            key={locale}
            type="button"
            role="radio"
            aria-checked={selected}
            disabled={pending || selected}
            onClick={() => setLocale(locale)}
            className={`px-2 py-0.5 rounded font-mono transition-colors ${
              selected
                ? 'bg-surface-3 text-text-1 shadow-sm'
                : 'text-text-3 hover:text-text-2'
            }`}
          >
            {locale}
          </button>
        );
      })}
    </div>
  );
}
