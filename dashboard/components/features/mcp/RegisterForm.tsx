'use client';

import { useState, useTransition } from 'react';
import { useRouter } from 'next/navigation';
import { registerMcpServer } from '@/lib/actions/mcpServer';

export function RegisterForm({ customerPrefix }: Readonly<{ customerPrefix: string }>) {
  const router = useRouter();
  const [pending, startTransition] = useTransition();
  const [error, setError] = useState<string | null>(null);
  const [transport, setTransport] = useState<string>('http');
  const requiresURL = transport !== 'stdio';

  function onSubmit(formData: FormData) {
    setError(null);
    const name = String(formData.get('name') ?? '').trim();
    const url = String(formData.get('url') ?? '').trim();
    const bearerTokenPath = String(formData.get('bearerTokenPath') ?? '').trim();

    startTransition(async () => {
      const res = await registerMcpServer({ name, transport, url, bearerTokenPath });
      if (!res.ok) {
        setError(res.message);
        return;
      }
      router.push(res.url);
      router.refresh();
    });
  }

  return (
    <form
      action={onSubmit}
      className="space-y-3 border border-border-1 rounded p-4 max-w-xl"
      data-testid="mcp-register-form"
    >
      <h2 className="text-text-1 text-sm font-semibold tracking-tight">Register MCP server</h2>
      <p className="text-text-3 text-xs">
        The supervisor&apos;s reactive worker picks up the pending row, calls MCPJungle&apos;s admin
        API, and flips status to <code>registered</code> or <code>failed</code>. Operators must
        prefix every name with <code className="text-text-2">{customerPrefix}</code>
         per the customer-prefix invariant (FR-307).
      </p>

      <label className="block">
        <span className="text-text-2 text-xs">Name</span>
        <input
          name="name"
          type="text"
          required
          placeholder={`${customerPrefix}linear`}
          className="mt-1 w-full px-2 py-1 bg-surface-2 border border-border-1 rounded text-sm font-mono"
        />
      </label>

      <label className="block">
        <span className="text-text-2 text-xs">Transport</span>
        <select
          name="transport"
          value={transport}
          onChange={(e) => setTransport(e.target.value)}
          className="mt-1 w-full px-2 py-1 bg-surface-2 border border-border-1 rounded text-sm"
        >
          <option value="http">http</option>
          <option value="stdio">stdio</option>
          <option value="sse">sse</option>
        </select>
      </label>

      <label className="block">
        <span className="text-text-2 text-xs">
          URL {requiresURL && <span className="text-warn">*</span>}
        </span>
        <input
          name="url"
          type="text"
          required={requiresURL}
          placeholder="http://upstream-mcp:9000"
          className="mt-1 w-full px-2 py-1 bg-surface-2 border border-border-1 rounded text-sm font-mono"
        />
      </label>

      <label className="block">
        <span className="text-text-2 text-xs">
          Bearer-token vault path <span className="text-text-3">(optional)</span>
        </span>
        <input
          name="bearerTokenPath"
          type="text"
          placeholder="mcpjungle/upstream/linear"
          className="mt-1 w-full px-2 py-1 bg-surface-2 border border-border-1 rounded text-sm font-mono"
        />
      </label>

      {error && (
        <p className="text-warn text-xs" role="alert">
          {error}
        </p>
      )}

      <button
        type="submit"
        disabled={pending}
        className="px-3 py-1 text-sm rounded border border-border-1 bg-surface-2 hover:bg-surface-3 disabled:opacity-50"
      >
        {pending ? 'Registering…' : 'Register'}
      </button>
    </form>
  );
}
