import './globals.css';
import { getSession } from '@/lib/auth/session';
import { resolveTheme, type ThemePreference, type ResolvedTheme } from '@/lib/theme/resolve';

// Root layout. Owns the <html>/<body> shell + the data-theme
// attribute (FR-010 + FR-010a). Reads the operator's saved theme
// preference from the session (when present) and resolves it
// against a default 'dark' system fallback.
//
// Why here rather than [locale]/layout.tsx: Next.js renders the
// root layout's html as authoritative; nesting another <html> in
// [locale]/layout.tsx produced flat invalid HTML where the
// data-theme attribute didn't reach the rendered <html>. The
// [locale] layout still owns the lang attribute via a small
// runtime sync (set in app/[locale]/layout.tsx).

function isThemePref(v: unknown): v is ThemePreference {
  return v === 'dark' || v === 'light' || v === 'system';
}

export default async function RootLayout({ children }: { children: React.ReactNode }) {
  const session = await getSession();
  const stored = (session?.user as { themePreference?: unknown } | undefined)?.themePreference;
  const operatorPref: ThemePreference = isThemePref(stored) ? stored : 'system';
  const systemFallback: ResolvedTheme = 'dark';
  const dataTheme = resolveTheme(operatorPref, systemFallback);

  return (
    <html lang="en" data-theme={dataTheme}>
      <body>{children}</body>
    </html>
  );
}
