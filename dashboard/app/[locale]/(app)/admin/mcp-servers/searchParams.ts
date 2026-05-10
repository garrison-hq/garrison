// /admin/mcp-servers search-param parsers. M8 alpha has no filter
// state (the page groups rows by status server-side); the file is
// declared for parity with other surfaces so adding a status filter
// post-M8.1 is a one-line extension.

export type ParamValue = string | string[] | undefined;

export function parseString(raw: ParamValue): string | undefined {
  return typeof raw === 'string' ? raw : undefined;
}
