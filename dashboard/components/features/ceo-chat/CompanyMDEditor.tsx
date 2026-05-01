'use client';

// M5.4 leaf editor component. CodeMirror 6 wrapper with markdown
// syntax-highlighting. Pure presentation: no Server Action calls,
// no save state, no error blocks. Parent (CompanyMDTab) owns those.
//
// Theme: oneDark in dark mode (the M3 design tokens don't yet have a
// Markdown-friendly mapping — defensible default per plan §"Open
// questions remaining for /garrison-tasks").

import CodeMirror from '@uiw/react-codemirror';
import { markdown } from '@codemirror/lang-markdown';
import { oneDark } from '@codemirror/theme-one-dark';

export interface CompanyMDEditorProps {
  readonly value: string;
  readonly onChange: (next: string) => void;
  readonly readOnly: boolean;
}

export function CompanyMDEditor({ value, onChange, readOnly }: Readonly<CompanyMDEditorProps>) {
  return (
    <div data-testid="company-md-editor" data-readonly={readOnly ? 'true' : 'false'}>
      <CodeMirror
        value={value}
        editable={!readOnly}
        readOnly={readOnly}
        extensions={[markdown()]}
        theme={oneDark}
        onChange={onChange}
        basicSetup={{
          lineNumbers: false,
          foldGutter: false,
          dropCursor: true,
          highlightActiveLine: !readOnly,
          highlightActiveLineGutter: false,
        }}
      />
    </div>
  );
}
