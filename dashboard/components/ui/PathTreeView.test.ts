import { describe, it, expect } from 'vitest';
import { buildTreeFromPaths } from './PathTreeView';

describe('components/ui/PathTreeView', () => {
  it('builds a tree from a single path', () => {
    const tree = buildTreeFromPaths(['/cust/operator/stripe_key']);
    expect(tree.children).toHaveLength(1);
    expect(tree.children[0].label).toBe('cust');
    expect(tree.children[0].children).toHaveLength(1);
    expect(tree.children[0].children[0].label).toBe('operator');
    expect(tree.children[0].children[0].children).toHaveLength(1);
    expect(tree.children[0].children[0].children[0].label).toBe('stripe_key');
    expect(tree.children[0].children[0].children[0].isLeaf).toBe(true);
  });

  it('shares prefixes across paths', () => {
    const tree = buildTreeFromPaths([
      '/cust/operator/stripe_key',
      '/cust/operator/db_url',
      '/cust/oauth/google_token',
    ]);
    expect(tree.children).toHaveLength(1);
    const cust = tree.children[0];
    expect(cust.label).toBe('cust');
    expect(cust.children).toHaveLength(2);
    const operator = cust.children.find((c) => c.label === 'operator');
    expect(operator?.children.map((c) => c.label).sort()).toEqual(['db_url', 'stripe_key']);
  });

  it('marks intermediate prefixes as non-leaf when also a real path is present', () => {
    const tree = buildTreeFromPaths(['/cust/operator', '/cust/operator/sub']);
    const cust = tree.children[0];
    const operator = cust.children[0];
    expect(operator.isLeaf).toBe(true);
    expect(operator.children[0].isLeaf).toBe(true);
    expect(operator.children[0].label).toBe('sub');
  });

  it('handles empty input', () => {
    const tree = buildTreeFromPaths([]);
    expect(tree.children).toEqual([]);
  });
});
