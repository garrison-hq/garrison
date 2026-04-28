// RunningInstancesBanner — surfaced above the agent settings form
// when N > 0 instances are still running with the prior config.
// FR-099: copy is "N instances currently running with prior config —
// change takes effect on next spawn for this role."

export function RunningInstancesBanner({ count }: Readonly<{ count: number }>) {
  if (count <= 0) return null;
  return (
    <div className="rounded border border-info/40 bg-info/5 px-4 py-2.5 text-[12.5px] text-info">
      <span className="font-mono tabular-nums">{count}</span>
      {' '}
      instance{count === 1 ? '' : 's'} currently running with prior config — change takes effect on next spawn for this role.
    </div>
  );
}
