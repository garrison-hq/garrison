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
      className="text-text-2 text-xs px-2 py-1 hover:text-text-1 disabled:opacity-60"
    >
      {label}
    </button>
  );
}
