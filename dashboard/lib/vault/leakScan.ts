// leakScan — TS port of supervisor/internal/vault/scanner.go.
//
// FR-088 / FR-097 / Rule 1: agent.md saves at the dashboard MUST
// reject content containing a fetchable secret value verbatim.
// The supervisor's spawn-time leak-scan is the primary
// enforcement boundary; this TS-side check catches the violation
// at save time so the bad config does not persist until the
// next spawn.
//
// Two scan modes:
//
//  - SHAPE scan: regex-match the same 10 patterns the supervisor
//                scanner uses (sk_prefix, xoxb_prefix, aws_akia,
//                pem_header, github_pat/app/user/server/refresh,
//                bearer_shape). Best-effort.
//
//  - VERBATIM scan: match against the literal values of every
//                   secret currently fetchable for the agent's
//                   role (joining agent_role_secrets to Infisical
//                   secret values). Required by Rule 1 — a
//                   secret that doesn't match any known shape
//                   pattern still must not appear verbatim in
//                   agent.md.
//
// Both modes return all matches; the caller (T013's editAgent
// server action) rejects the save if either set is non-empty.

export type LeakLabel =
  | 'sk_prefix'
  | 'xoxb_prefix'
  | 'aws_akia'
  | 'pem_header'
  | 'github_pat'
  | 'github_app'
  | 'github_user'
  | 'github_server'
  | 'github_refresh'
  | 'bearer_shape'
  | 'verbatim';

export interface LeakMatch {
  label: LeakLabel;
  /** Byte offset of the match start within the scanned content. */
  offset: number;
  /** Length of the matched substring. */
  length: number;
}

interface ShapePattern {
  label: LeakLabel;
  re: RegExp;
}

// Patterns mirror supervisor/internal/vault/scanner.go:patterns.
// The supervisor uses Go regexp; JS RegExp is compatible for
// these expressions. The `g` flag enables findAll-style scanning.
const SHAPE_PATTERNS: ShapePattern[] = [
  { label: 'sk_prefix', re: /sk-[A-Za-z0-9]{20,}/g },
  { label: 'xoxb_prefix', re: /xoxb-[A-Za-z0-9-]{20,}/g },
  { label: 'aws_akia', re: /AKIA[0-9A-Z]{16}/g },
  { label: 'pem_header', re: /-----BEGIN [A-Z ]+-----/g },
  { label: 'github_pat', re: /ghp_[A-Za-z0-9]{30,}/g },
  { label: 'github_app', re: /gho_[A-Za-z0-9]{30,}/g },
  { label: 'github_user', re: /ghu_[A-Za-z0-9]{30,}/g },
  { label: 'github_server', re: /ghs_[A-Za-z0-9]{30,}/g },
  { label: 'github_refresh', re: /ghr_[A-Za-z0-9]{30,}/g },
  { label: 'bearer_shape', re: /authorization:\s*bearer\s+[A-Za-z0-9._~+/-]+=*/gi },
];

/**
 * Two-pass scan against `content` (typically agent.md body).
 *
 * Pass 1: shape patterns (10 supervisor labels).
 * Pass 2: verbatim match against each fetchable value the agent
 *         role can receive at spawn time.
 *
 * Returns all matches. Empty array = clean.
 */
export function scanForLeaks(
  content: string,
  fetchableValues: ReadonlyArray<string> = [],
): LeakMatch[] {
  const matches: LeakMatch[] = [];

  // Pass 1: shape patterns.
  for (const { label, re } of SHAPE_PATTERNS) {
    const re2 = new RegExp(re.source, re.flags);
    let match: RegExpExecArray | null;
    while ((match = re2.exec(content)) !== null) {
      matches.push({ label, offset: match.index, length: match[0].length });
      if (re2.lastIndex === match.index) re2.lastIndex++; // avoid zero-width loop
    }
  }

  // Pass 2: verbatim values.
  for (const value of fetchableValues) {
    if (value.length < 8) continue; // skip degenerate values that would over-match
    let from = 0;
    while (true) {
      const idx = content.indexOf(value, from);
      if (idx < 0) break;
      matches.push({ label: 'verbatim', offset: idx, length: value.length });
      from = idx + value.length;
    }
  }

  return matches;
}

/**
 * True if content has any leak match (shape or verbatim).
 * Convenience wrapper.
 */
export function hasLeak(content: string, fetchableValues: ReadonlyArray<string> = []): boolean {
  return scanForLeaks(content, fetchableValues).length > 0;
}
