// Top-level fallback for 404s that escape the [locale] segment
// (mistyped locale prefixes, etc.). The root layout still wraps this,
// so <html lang> + <body> are present; we just need a localizable shell
// with a real <main> and <h1> for screen readers.
export default function RootNotFound() {
  return (
    <main className="min-h-screen flex flex-col items-center justify-center gap-3 p-6">
      <h1 className="text-text-1 text-2xl font-semibold">Page not found</h1>
      <p className="text-text-2 text-sm">The page you tried to reach doesn&apos;t exist or has moved.</p>
      <a href="/" className="text-text-2 underline hover:text-text-1 text-sm">Back to org overview</a>
    </main>
  );
}
