import { notFound } from 'next/navigation';
import { fetchAgentForEdit } from '@/lib/queries/agents';
import { AgentSettingsForm } from '@/components/features/agent-settings-edit/AgentSettingsForm';
import { RunningInstancesBanner } from '@/components/features/agent-settings-edit/RunningInstancesBanner';
import type { AgentModel } from '@/lib/actions/agents';

// Agent settings editor route. Server Component reads the current
// agent state (lib/queries/agents.ts:fetchAgentForEdit) including
// the optimistic-lock version token (updated_at), then hydrates
// the AgentSettingsForm Client Component which calls editAgent.

export const dynamic = 'force-dynamic';

interface RouteParams {
  params: Promise<{ deptSlug: string; roleSlug: string; locale: string }>;
}

export default async function AgentEditPage({ params }: Readonly<RouteParams>) {
  const { deptSlug, roleSlug } = await params;
  const snapshot = await fetchAgentForEdit(deptSlug, roleSlug);
  if (!snapshot) {
    notFound();
  }

  return (
    <div className="px-6 py-5 space-y-5 max-w-[900px] mx-auto">
      <header className="space-y-1">
        <h1 className="text-text-1 text-2xl font-semibold tracking-tight">
          Edit agent
        </h1>
        <p className="text-text-3 text-[12px]">
          {snapshot.departmentName} · <span className="font-mono">{snapshot.roleSlug}</span>
        </p>
      </header>

      <RunningInstancesBanner count={snapshot.liveInstances} />

      <AgentSettingsForm
        agentId={snapshot.id}
        roleSlug={snapshot.roleSlug}
        departmentSlug={snapshot.departmentSlug}
        initial={{
          agentMd: snapshot.agentMd,
          model: snapshot.model as AgentModel,
          listensFor: snapshot.listensFor,
          skills: snapshot.skills,
          updatedAt: snapshot.updatedAt,
        }}
      />
    </div>
  );
}
