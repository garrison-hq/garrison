'use client';

import { useTransition } from 'react';
import { useRouter } from 'next/navigation';

// Tiny client component that POSTs to better-auth's sign-out
// endpoint and redirects to /login. Lives next to Topbar (the
// only consumer) but in its own file so the Topbar Server
// Component doesn't need 'use client'.

export function LogOutButton({ label }: Readonly<{ label: string }>) {
  const router = useRouter();
  const [pending, start] = useTransition();
  return (
    <button
      type="button"
      disabled={pending}
      onClick={() =>
        start(async () => {
          await fetch('/api/auth/sign-out', { method: 'POST', credentials: 'include' });
          router.push('/login');
          router.refresh();
        })
      }
      className="text-text-2 hover:text-text-1 hover:bg-surface-3 border border-border-1 hover:border-border-2 rounded text-[11px] font-medium px-2.5 py-1 transition-colors disabled:opacity-60"
    >
      {label}
    </button>
  );
}
