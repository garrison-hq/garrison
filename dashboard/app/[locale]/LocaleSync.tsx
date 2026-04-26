'use client';

import { useEffect } from 'react';

// Tiny client island that syncs the <html lang> attribute to the
// active locale on every navigation. The root layout owns the
// initial value via the static lang="en"; this island flips it
// when the operator switches locales mid-session.

export function LocaleSync({ locale }: { locale: string }) {
  useEffect(() => {
    document.documentElement.lang = locale;
  }, [locale]);
  return null;
}
