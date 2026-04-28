// Client-safe re-export of the pattern-category constants. Imported
// from PatternCategoryFilter (a Client Component); kept separate
// from lib/queries/hygiene.ts because that module imports
// lib/db/appClient (server-only — pulls postgres-js, fs, net, etc.)
// and Next.js's bundler refuses to ship those into the client.
//
// The query layer continues to re-export from here so server-side
// callers keep their existing import sites working.

/** The 10 supervisor pattern labels per
 *  supervisor/internal/vault/scanner.go + 'unknown' for pre-M4
 *  rows (FR-118). The hygiene UI renders one category per row;
 *  this list drives the filter chip. */
export const PATTERN_CATEGORIES = [
  'sk_prefix',
  'xoxb_prefix',
  'aws_akia',
  'pem_header',
  'github_pat',
  'github_app',
  'github_user',
  'github_server',
  'github_refresh',
  'bearer_shape',
  'unknown',
] as const;
export type PatternCategory = (typeof PATTERN_CATEGORIES)[number];
