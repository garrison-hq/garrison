import { getTranslations } from 'next-intl/server';
import { ActivityFeed } from '@/components/features/activity-feed/ActivityFeed';
import { FilterChips } from '@/components/features/activity-feed/FilterChips';

export const dynamic = 'force-dynamic';

export default async function ActivityPage() {
  const t = await getTranslations('nav');
  const meta = await getTranslations('activityMeta');
  return (
    <div className="px-6 py-5 space-y-5 max-w-[1600px] mx-auto h-full flex flex-col min-h-0">
      <header className="space-y-1">
        <h1 className="text-text-1 text-2xl font-semibold tracking-tight">
          {t('activity')}
        </h1>
        <p className="text-text-3 text-xs">
          {meta('lastHourLabel')} · {' '}
          <span className="font-mono">live</span>
        </p>
      </header>
      <FilterChips />
      <div className="flex-1 min-h-0">
        <ActivityFeed />
      </div>
    </div>
  );
}
