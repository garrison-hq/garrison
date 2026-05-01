// AssistantMarkdown — renders the CEO's assistant message text as
// constrained Markdown. Pre-M5.4 the message body rendered as plain
// text via {content}, so numbered/bulleted lists in claude's replies
// flowed inline (e.g. "1. … 2. …" on a single paragraph). Live on the
// running stack 2026-05-02.
//
// The constraint set keeps chat-bubble density tight:
//   - <ol>/<ul> get a visible indent + 4px gap between items
//   - inline <code> sits on surface-2 in mono
//   - <pre>/<code> blocks render as muted mono on surface-2 (small)
//   - <a> uses the info accent + hover underline
//   - <strong>/<em> keep the body color — no extra emphasis tone
//   - <h1>/<h2>/<h3> collapse to bold inline (CEO replies don't get
//     headings; if claude emits one we don't want it shouting)
//   - GFM tables / strikethrough via remark-gfm

import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import type { Components } from 'react-markdown';

const COMPONENTS: Components = {
  ol: ({ children, ...rest }) => (
    <ol className="list-decimal pl-5 my-1 flex flex-col gap-1" {...rest}>
      {children}
    </ol>
  ),
  ul: ({ children, ...rest }) => (
    <ul className="list-disc pl-5 my-1 flex flex-col gap-1" {...rest}>
      {children}
    </ul>
  ),
  li: ({ children, ...rest }) => (
    <li className="leading-snug" {...rest}>
      {children}
    </li>
  ),
  p: ({ children, ...rest }) => (
    <p className="my-1 first:mt-0 last:mb-0" {...rest}>
      {children}
    </p>
  ),
  code: ({ children, className, ...rest }) => {
    const isBlock = (className ?? '').includes('language-');
    if (isBlock) {
      return (
        <code className={`${className ?? ''} block`} {...rest}>
          {children}
        </code>
      );
    }
    return (
      <code
        className="font-mono text-[11.5px] text-text-2 bg-surface-3 border border-border-1 rounded px-1.5 py-px"
        {...rest}
      >
        {children}
      </code>
    );
  },
  pre: ({ children, ...rest }) => (
    <pre
      className="font-mono text-[11.5px] text-text-2 bg-surface-3 border border-border-1 rounded p-2 my-2 overflow-x-auto"
      {...rest}
    >
      {children}
    </pre>
  ),
  a: ({ children, href, ...rest }) => (
    <a
      href={href}
      className="text-info hover:underline underline-offset-2"
      target="_blank"
      rel="noopener noreferrer"
      {...rest}
    >
      {children}
    </a>
  ),
  strong: ({ children, ...rest }) => (
    <strong className="font-medium text-text-1" {...rest}>
      {children}
    </strong>
  ),
  em: ({ children, ...rest }) => <em {...rest}>{children}</em>,
  // Collapse h1/h2/h3 to bold inline — CEO replies shouldn't render
  // huge headings. If claude emits a heading, surface it as emphasized
  // text without breaking the message density.
  h1: ({ children }) => <strong className="font-medium text-text-1">{children}</strong>,
  h2: ({ children }) => <strong className="font-medium text-text-1">{children}</strong>,
  h3: ({ children }) => <strong className="font-medium text-text-1">{children}</strong>,
  hr: () => <hr className="my-2 border-border-1" />,
  blockquote: ({ children, ...rest }) => (
    <blockquote className="border-l-2 border-border-2 pl-2 my-1 text-text-2 italic" {...rest}>
      {children}
    </blockquote>
  ),
};

export function AssistantMarkdown({ content }: Readonly<{ content: string }>) {
  return (
    <div data-testid="assistant-markdown" className="space-y-1">
      <ReactMarkdown remarkPlugins={[remarkGfm]} components={COMPONENTS}>
        {content}
      </ReactMarkdown>
    </div>
  );
}
