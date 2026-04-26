// Theme resolution. The operator's stored preference is one of
// 'dark' | 'light' | 'system'. When 'system', we fall back to
// whatever the browser's prefers-color-scheme advertises (passed
// in by the caller — Server Components don't see it directly, so
// the layout reads the ?system_theme cookie or defaults to 'dark').
//
// FR-010a: "System-preference detection applies only when the
// operator has not yet made an explicit choice." This function is
// the single resolution point; UI code never inspects the raw
// preference.

export type ThemePreference = 'dark' | 'light' | 'system';
export type ResolvedTheme = 'dark' | 'light';

export function resolveTheme(
  operatorPref: ThemePreference,
  systemPref: ResolvedTheme,
): ResolvedTheme {
  if (operatorPref === 'system') return systemPref;
  return operatorPref;
}
