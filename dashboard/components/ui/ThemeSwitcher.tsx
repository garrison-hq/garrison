'use client';

import { useState } from 'react';
import type { ThemePreference } from '@/lib/theme/resolve';

// Tri-state theme switcher — dark / light / system. Persists to the
// operator's better-auth user record via /api/theme; updates the
// document.documentElement.dataset.theme optimistically so the
// surface re-renders immediately without a full page reload.

const PREFERENCES: ThemePreference[] = ['dark', 'light', 'system'];

export function ThemeSwitcher({ initial }: { initial: ThemePreference }) {
  const [preference, setPreference] = useState<ThemePreference>(initial);
  const [pending, setPending] = useState(false);

  async function setPref(next: ThemePreference) {
    if (next === preference || pending) return;
    setPending(true);
    setPreference(next);
    // Optimistically apply the theme to the live DOM so the user
    // sees the switch immediately. The server-side persistence call
    // races but is monotone — if it fails we rollback the local
    // state.
    if (next !== 'system') {
      document.documentElement.dataset.theme = next;
    } else {
      const sys = window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
      document.documentElement.dataset.theme = sys;
    }
    try {
      const res = await fetch('/api/theme', {
        method: 'PUT',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ theme_preference: next }),
      });
      if (!res.ok) throw new Error(`theme update failed (HTTP ${res.status})`);
    } catch {
      setPreference(preference);
    } finally {
      setPending(false);
    }
  }

  return (
    <div
      role="radiogroup"
      aria-label="Theme"
      className="inline-flex items-center bg-surface-2 border border-border-1 rounded text-xs"
    >
      {PREFERENCES.map((pref) => {
        const selected = pref === preference;
        return (
          <button
            key={pref}
            type="button"
            role="radio"
            aria-checked={selected}
            disabled={pending}
            onClick={() => setPref(pref)}
            data-testid={`theme-${pref}`}
            className={`px-2 py-1 font-mono ${
              selected ? 'bg-surface-3 text-text-1' : 'text-text-3 hover:text-text-2'
            }`}
          >
            {pref}
          </button>
        );
      })}
    </div>
  );
}
