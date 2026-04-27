'use client';

import { useMemo, useState, type ReactNode } from 'react';

// PathTreeView — hierarchical visualization of vault paths
// organized by prefix segments. Operator-facing affordances
// (create / rename / move) live in the parent (T010 wires
// PathTreeOps); this component is the rendering primitive.
//
// The tree is built from a flat list of path strings via the
// helper `buildTreeFromPaths` below. Each node exposes the
// path it represents (or a prefix) and any associated metadata
// the parent passes through `nodeMeta` for per-row chips/icons.

export interface PathNodeMeta {
  /** Render extra chips/icons on the right side of the node row
   *  (e.g., rotation status, allowed roles count). */
  rightSlot?: ReactNode;
  /** Render an action menu for this node (e.g., rename / move
   *  / delete). The parent owns the action wiring. */
  actions?: ReactNode;
}

export interface PathTreeViewProps {
  /** Flat list of vault paths (e.g., `/cust/operator/stripe_key`). */
  paths: string[];
  /** Optional per-path metadata. Keyed by full path. */
  nodeMeta?: Record<string, PathNodeMeta>;
  /** Default expanded segments — if omitted, all nodes start
   *  collapsed except the root. */
  defaultExpanded?: ReadonlySet<string>;
  /** Optional substring filter — only paths containing this
   *  substring (case-insensitive) render. The filter applies
   *  to the full path string before tree composition. */
  filter?: string;
  /** Click handler invoked when the operator clicks a leaf
   *  (a node representing an actual secret path). Parent
   *  typically navigates to the secret edit form. */
  onLeafClick?: (path: string) => void;
}

interface TreeNode {
  /** Full prefix this node represents. */
  prefix: string;
  /** Last segment of the prefix (the rendered label). */
  label: string;
  /** Whether this node corresponds to a real path (leaf). */
  isLeaf: boolean;
  children: TreeNode[];
}

/** Build a tree from a flat list of paths. Internal nodes are
 *  created for each segment prefix; leaves represent actual
 *  paths from the input. */
export function buildTreeFromPaths(paths: string[]): TreeNode {
  const root: TreeNode = { prefix: '', label: '/', isLeaf: false, children: [] };
  const sorted = [...paths].sort();
  for (const path of sorted) {
    const segments = path.split('/').filter((s) => s !== '');
    let cursor = root;
    let prefixSoFar = '';
    for (let i = 0; i < segments.length; i++) {
      const segment = segments[i];
      prefixSoFar = `${prefixSoFar}/${segment}`;
      const isLast = i === segments.length - 1;
      let child = cursor.children.find((c) => c.label === segment);
      if (!child) {
        child = { prefix: prefixSoFar, label: segment, isLeaf: false, children: [] };
        cursor.children.push(child);
      }
      if (isLast) {
        child.isLeaf = true;
      }
      cursor = child;
    }
  }
  return root;
}

export function PathTreeView({
  paths,
  nodeMeta,
  defaultExpanded,
  filter,
  onLeafClick,
}: PathTreeViewProps) {
  const filtered = useMemo(() => {
    if (!filter || filter.trim() === '') return paths;
    const needle = filter.trim().toLowerCase();
    return paths.filter((p) => p.toLowerCase().includes(needle));
  }, [paths, filter]);

  const tree = useMemo(() => buildTreeFromPaths(filtered), [filtered]);

  const [expanded, setExpanded] = useState<Set<string>>(() => {
    if (defaultExpanded) return new Set(defaultExpanded);
    // Default: root + first level open.
    const out = new Set<string>(['']);
    for (const child of tree.children) out.add(child.prefix);
    return out;
  });

  const toggle = (prefix: string) => {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(prefix)) next.delete(prefix);
      else next.add(prefix);
      return next;
    });
  };

  if (filtered.length === 0) {
    return <p className="text-[12px] text-text-3 italic px-3 py-4">no paths match the filter.</p>;
  }

  return (
    <div className="text-[13px] text-text-1 font-mono">
      {tree.children.map((node) => (
        <PathTreeNode
          key={node.prefix}
          node={node}
          depth={0}
          expanded={expanded}
          onToggle={toggle}
          nodeMeta={nodeMeta}
          onLeafClick={onLeafClick}
        />
      ))}
    </div>
  );
}

interface PathTreeNodeProps {
  node: TreeNode;
  depth: number;
  expanded: Set<string>;
  onToggle: (prefix: string) => void;
  nodeMeta?: Record<string, PathNodeMeta>;
  onLeafClick?: (path: string) => void;
}

function PathTreeNode({
  node,
  depth,
  expanded,
  onToggle,
  nodeMeta,
  onLeafClick,
}: PathTreeNodeProps) {
  const isOpen = expanded.has(node.prefix);
  const hasChildren = node.children.length > 0;
  const meta = nodeMeta?.[node.prefix];
  const indent = depth * 16;

  return (
    <div>
      <div
        className="flex items-center gap-2 py-1 px-2 hover:bg-surface-2 rounded cursor-pointer"
        style={{ paddingLeft: 8 + indent }}
        onClick={() => {
          if (hasChildren) onToggle(node.prefix);
          else if (node.isLeaf) onLeafClick?.(node.prefix);
        }}
      >
        <span className="text-text-3 w-3 text-center">
          {hasChildren ? (isOpen ? '▾' : '▸') : '·'}
        </span>
        <span className={node.isLeaf ? 'text-text-1' : 'text-text-2'}>{node.label}</span>
        {meta?.rightSlot && <span className="ml-auto">{meta.rightSlot}</span>}
        {meta?.actions && <span className="ml-2">{meta.actions}</span>}
      </div>
      {hasChildren && isOpen && (
        <div>
          {node.children.map((child) => (
            <PathTreeNode
              key={child.prefix}
              node={child}
              depth={depth + 1}
              expanded={expanded}
              onToggle={onToggle}
              nodeMeta={nodeMeta}
              onLeafClick={onLeafClick}
            />
          ))}
        </div>
      )}
    </div>
  );
}
