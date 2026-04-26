import { getTranslations } from 'next-intl/server';

// Custom 404 inside the auth shell. Because this lives under
// [locale]/(app)/, Next.js renders it through the (app) layout —
// which provides the sidebar, topbar, and the <main> landmark.
// We render plain content (no extra <main>) so we don't end up
// with two main landmarks.
export default async function NotFoundInApp() {
  const t = await getTranslations('notFound');
  return (
    <div className="flex flex-col items-center justify-center gap-3 p-6 min-h-[60vh]">
      <h1 className="text-text-1 text-2xl font-semibold">{t('title')}</h1>
      <p className="text-text-2 text-sm">{t('description')}</p>
      <a href="/" className="text-text-2 underline hover:text-text-1 text-sm">{t('back')}</a>
    </div>
  );
}
