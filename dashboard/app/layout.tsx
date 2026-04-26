// Root layout — passthrough only. The actual <html>/<body> shell
// lives in app/[locale]/layout.tsx so it can wrap children in
// NextIntlClientProvider with the resolved locale + catalog. This
// file exists because Next.js requires app/layout.tsx; per
// next-intl App Router conventions, it just renders {children}.
//
// Global CSS still imports here so it ships in every render
// regardless of which [locale] segment is active.

import './globals.css';

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return children;
}
