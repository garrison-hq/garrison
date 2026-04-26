import { SampleTokenChips } from '@/components/ui/sample';

// T002 placeholder. The real org overview surface lands at
// app/[locale]/(app)/page.tsx in T010 once the auth shell + i18n
// routing are wired (T006–T009). This file exists so `bun run dev`
// boots a server that renders something against the token system,
// proving the Tailwind v4 + CSS-variable wiring is live.

export default function Home() {
  return (
    <main className="p-8 space-y-6">
      <h1 className="text-text-1 text-2xl font-semibold">Garrison</h1>
      <p className="text-text-2">
        Dashboard scaffold (T002). Auth, surfaces, and i18n land in later tasks.
      </p>
      <SampleTokenChips />
    </main>
  );
}
