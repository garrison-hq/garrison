// M7 hiring Server Action types + error class. Pulled out of
// lib/actions/hiring.ts so the 'use server' module can re-export only
// async functions (Next.js 16's 'use server' transport requires it).
// Client Components and other modules import from here directly.

export type ApproveHireResult = {
  agentId: string;
  auditId: string;
};

export type ApproveSkillResult = {
  agentId: string;
  auditId: string;
  supersededCount: number;
};

export type HiringActionErrorKind =
  | 'not_found'
  | 'not_pending'
  | 'wrong_type'
  | 'missing_target_agent'
  | 'reason_required';

export class HiringActionError extends Error {
  constructor(
    message: string,
    readonly kind: HiringActionErrorKind,
  ) {
    super(message);
    this.name = 'HiringActionError';
  }
}
