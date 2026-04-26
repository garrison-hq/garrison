import { getTranslations } from 'next-intl/server';
import { ActivityFeed } from '@/components/features/activity-feed/ActivityFeed';
import { FilterChips } from '@/components/features/activity-feed/FilterChips';

export const dynamic = 'force-dynamic';

export default async function ActivityPage() {
  const t = await getTranslations('nav');
  return (
    <div className="p-6 space-y-4">
      <h1 className="text-text-1 text-lg font-semibold">{t('activity')}</h1>
      <FilterChips />
      <ActivityFeed />
    </div>
  );
}
