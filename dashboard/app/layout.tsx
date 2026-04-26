import type { Metadata } from 'next';
import './globals.css';

export const metadata: Metadata = {
  title: 'Garrison',
  description: 'Operator console for the Garrison agent orchestration system.',
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  // T009 will resolve the operator's theme_preference and rewrite the
  // data-theme attribute server-side. T002 ships dark by default.
  return (
    <html lang="en" data-theme="dark">
      <body>{children}</body>
    </html>
  );
}
