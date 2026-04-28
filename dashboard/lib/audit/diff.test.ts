import { describe, it, expect } from 'vitest';
import { buildFieldDiff } from './diff';

describe('lib/audit/diff', () => {
  it('builds field diff with before/after for changed primitives', () => {
    const before = { title: 'old', priority: 'medium', description: 'same' };
    const after = { title: 'new', priority: 'high', description: 'same' };
    const diff = buildFieldDiff(before, after, ['title', 'priority', 'description']);
    expect(diff).toEqual({
      title: { before: 'old', after: 'new' },
      priority: { before: 'medium', after: 'high' },
    });
  });

  it('omits unchanged fields entirely (no zero-diff entries)', () => {
    const before = { title: 'same', priority: 'medium' };
    const after = { title: 'same', priority: 'medium' };
    const diff = buildFieldDiff(before, after, ['title', 'priority']);
    expect(diff).toEqual({});
  });

  it('treats arrays via deep equality (same elements, no diff)', () => {
    const before = { skills: ['a', 'b', 'c'] };
    const after = { skills: ['a', 'b', 'c'] };
    const diff = buildFieldDiff(before, after, ['skills']);
    expect(diff).toEqual({});
  });

  it('treats arrays via deep equality (different element produces diff)', () => {
    const before = { skills: ['a', 'b'] };
    const after = { skills: ['a', 'b', 'c'] };
    const diff = buildFieldDiff(before, after, ['skills']);
    expect(diff).toEqual({
      skills: { before: ['a', 'b'], after: ['a', 'b', 'c'] },
    });
  });

  it('treats null and undefined as distinct', () => {
    const before: { assignedAgentRoleSlug: string | null } = { assignedAgentRoleSlug: null };
    const after: { assignedAgentRoleSlug: string | null } = { assignedAgentRoleSlug: 'engineer' };
    const diff = buildFieldDiff(before, after, ['assignedAgentRoleSlug']);
    expect(diff).toEqual({
      assignedAgentRoleSlug: { before: null, after: 'engineer' },
    });
  });

  it('only inspects fields named in the fields argument', () => {
    const before = { title: 'a', secret_field: 'x' };
    const after = { title: 'a', secret_field: 'y' };
    const diff = buildFieldDiff(before, after, ['title']);
    expect(diff).toEqual({});
  });
});
