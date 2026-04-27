'use client';

import { useRouter } from 'next/navigation';
import { PathTreeView } from '@/components/ui/PathTreeView';

// PathTreeNavigator — Client wrapper around the PathTreeView
// primitive that wires leaf clicks to the secret edit page.
// Used by /vault/tree.

export function PathTreeNavigator({
  paths,
}: Readonly<{
  paths: string[];
}>) {
  const router = useRouter();
  return (
    <PathTreeView
      paths={paths}
      onLeafClick={(secretPath) => {
        // Mirror SecretRowActions's editHref encoding so the
        // route resolves correctly. Next.js 16 requires the
        // catch-all to be the last segment.
        const editHref =
          '/vault/edit' +
          secretPath
            .split('/')
            .map((s) => (s ? '/' + encodeURIComponent(s) : ''))
            .join('');
        router.push(editHref);
      }}
    />
  );
}
