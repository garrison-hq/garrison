import type { Config } from 'tailwindcss';

// Tailwind v4 reads most configuration from CSS @theme blocks (see
// app/globals.css), but a thin tailwind.config.ts keeps content paths
// explicit and exposes the CSS-variable-driven semantic colors as
// utility classes (`bg-bg`, `text-text-1`, etc.). The names mirror the
// CSS-variable names verbatim so a token rename is one-line in two
// files instead of a sweep through component code.
//
// Token list per spec FR-010 + plan T002:
//   --bg, --surface-{1,2,3}, --border-{1,2}, --text-{1,2,3,4},
//   --accent, --info, --warn, --err, --ok
//
// Both [data-theme="dark"] and [data-theme="light"] blocks define
// these in app/globals.css; component code references the
// CSS-variable names through the utilities below.
const config: Config = {
  content: [
    './app/**/*.{ts,tsx}',
    './components/**/*.{ts,tsx}',
  ],
  theme: {
    extend: {
      colors: {
        bg: 'var(--bg)',
        'surface-1': 'var(--surface-1)',
        'surface-2': 'var(--surface-2)',
        'surface-3': 'var(--surface-3)',
        'border-1': 'var(--border-1)',
        'border-2': 'var(--border-2)',
        'text-1': 'var(--text-1)',
        'text-2': 'var(--text-2)',
        'text-3': 'var(--text-3)',
        'text-4': 'var(--text-4)',
        accent: 'var(--accent)',
        info: 'var(--info)',
        warn: 'var(--warn)',
        err: 'var(--err)',
        ok: 'var(--ok)',
      },
      fontFamily: {
        sans: 'var(--sans)',
        mono: 'var(--mono)',
      },
    },
  },
  plugins: [],
};

export default config;
