import type { ReactNode } from 'react';

// Empty-state vocabulary used everywhere data tables / feeds can
// be empty (FR-102). Icon + translated one-line description +
// caption explaining what would populate the surface.

export function EmptyState({
  icon,
  description,
  caption,
}: Readonly<{
  icon?: ReactNode;
  description: ReactNode;
  caption?: ReactNode;
}>) {
  return (
    <div className="flex flex-col items-center justify-center text-center gap-2 py-10 text-text-3">
      {icon ? <div className="text-text-4 text-2xl">{icon}</div> : null}
      <p className="text-sm">{description}</p>
      {caption ? <p className="text-xs text-text-4">{caption}</p> : null}
    </div>
  );
}
