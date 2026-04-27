// buildFieldDiff — extracts field-level before/after pairs from
// two snapshots. Used by inline-edit and agent-edit server
// actions to construct the audit row's `diff` payload (FR-014).
//
// Only the fields named in `fields` are inspected; unchanged
// fields are omitted from the result. If no fields changed, an
// empty record is returned (callers SHOULD skip writing the
// audit row in that case — FR-022 forbids audit rows for failed
// or no-op mutations, and a no-op edit qualifies).

/**
 * Build a field-level diff between `before` and `after` for the
 * named `fields`. Returns only the fields whose value changed
 * (compared via `Object.is` on primitives, deep equality on
 * arrays / plain objects via JSON serialization).
 *
 * Callers should treat the empty result as "no audit row needed."
 */
export function buildFieldDiff<T extends Record<string, unknown>>(
  before: T,
  after: T,
  fields: ReadonlyArray<keyof T>,
): Record<string, { before: unknown; after: unknown }> {
  const out: Record<string, { before: unknown; after: unknown }> = {};
  for (const field of fields) {
    const beforeValue = before[field];
    const afterValue = after[field];
    if (!isDeepEqual(beforeValue, afterValue)) {
      out[field as string] = { before: beforeValue, after: afterValue };
    }
  }
  return out;
}

function isDeepEqual(a: unknown, b: unknown): boolean {
  if (Object.is(a, b)) return true;
  if (a === null || b === null) return false;
  if (typeof a !== typeof b) return false;
  if (typeof a !== 'object') return false;
  // Arrays + plain objects: JSON-serialize for a structural
  // comparison. Acceptable for the size of objects we diff
  // (ticket fields, agent.md content); not load-bearing for
  // performance.
  try {
    return JSON.stringify(a) === JSON.stringify(b);
  } catch {
    return false;
  }
}
