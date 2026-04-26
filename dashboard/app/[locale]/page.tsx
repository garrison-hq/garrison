import { getTranslations } from 'next-intl/server';
import { SampleTokenChips } from '@/components/ui/sample';

// T002/T008 placeholder for the org overview surface. T010 replaces
// this with the real KPI strip + per-department rows; T009 wraps it
// in the sidebar/topbar shell. For now, it's a smoke witness that
// the i18n provider, the locale-aware layout, and the token-styled
// chips all render together.

export default async function Home() {
  const t = await getTranslations('nav');
  return (
    <main className="p-8 space-y-6">
      <h1 className="text-text-1 text-2xl font-semibold">{t('orgOverview')}</h1>
      <SampleTokenChips />
    </main>
  );
}
