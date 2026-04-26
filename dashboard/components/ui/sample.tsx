// Smoke component verifying the token wiring lit up correctly:
// each chip resolves its color via a CSS-variable-driven Tailwind
// utility class. T009 replaces this file's exports with the real UI
// primitive set (Chip, StatusDot, Kbd, Tbl, ...); for T002 it exists
// only so `bun run dev` renders something that proves the tokens
// flow from globals.css through tailwind.config.ts into the DOM.

export function SampleTokenChips() {
  const tokens = [
    { label: 'accent', cls: 'bg-accent text-bg' },
    { label: 'info', cls: 'bg-info text-bg' },
    { label: 'warn', cls: 'bg-warn text-bg' },
    { label: 'err', cls: 'bg-err text-bg' },
    { label: 'ok', cls: 'bg-ok text-bg' },
  ];
  return (
    <div className="flex gap-2 flex-wrap">
      {tokens.map((t) => (
        <span
          key={t.label}
          className={`px-2 py-1 rounded text-xs font-mono ${t.cls}`}
        >
          {t.label}
        </span>
      ))}
    </div>
  );
}
