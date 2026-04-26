// Detail block revealed when an operator expands a sandbox-escape
// transition row. Shows the artifact-claimed-vs-on-disk path pair
// so the operator can triage the failure mode without going to
// `psql`. Per spec FR-055.

export function SandboxEscapeDetail({
  claimedPath,
  onDiskPath,
}: Readonly<{
  claimedPath: string;
  onDiskPath: string;
}>) {
  return (
    <div className="grid grid-cols-1 sm:grid-cols-2 gap-3 text-xs" data-testid="sandbox-escape-detail">
      <div>
        <div className="text-text-3 uppercase tracking-wider">claimed</div>
        <code className="text-text-1 font-mono break-all">{claimedPath}</code>
      </div>
      <div>
        <div className="text-text-3 uppercase tracking-wider">on disk</div>
        <code className="text-text-1 font-mono break-all">{onDiskPath}</code>
      </div>
    </div>
  );
}
