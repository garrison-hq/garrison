import type { SVGProps } from 'react';

// Thin-line icon set lifted from .workspace/m3-mocks/garrison-reference/
// icons.jsx. 14×14 default. Stroke uses `currentColor` so the calling
// element's `text-*` color drives the icon hue without per-icon props.

type Props = SVGProps<SVGSVGElement>;
const base: Pick<SVGProps<SVGSVGElement>, 'viewBox' | 'fill' | 'width' | 'height'> = {
  viewBox: '0 0 16 16',
  fill: 'none',
  width: 14,
  height: 14,
};

export const HomeIcon = (p: Props) => (
  <svg {...base} {...p}>
    <path d="M2.5 6.5L8 2l5.5 4.5V13a1 1 0 0 1-1 1H3.5a1 1 0 0 1-1-1V6.5Z" stroke="currentColor" strokeWidth="1.2" />
  </svg>
);

export const DeptIcon = (p: Props) => (
  <svg {...base} {...p}>
    <rect x="2.5" y="3" width="4" height="4" rx="0.5" stroke="currentColor" strokeWidth="1.2" />
    <rect x="9.5" y="3" width="4" height="4" rx="0.5" stroke="currentColor" strokeWidth="1.2" />
    <rect x="2.5" y="9" width="4" height="4" rx="0.5" stroke="currentColor" strokeWidth="1.2" />
    <rect x="9.5" y="9" width="4" height="4" rx="0.5" stroke="currentColor" strokeWidth="1.2" />
  </svg>
);

export const ActivityIcon = (p: Props) => (
  <svg {...base} {...p}>
    <path d="M2 8h2.5l1.5-4 3 8 1.5-4H14" stroke="currentColor" strokeWidth="1.2" strokeLinejoin="round" strokeLinecap="round" />
  </svg>
);

// M5.2 — thin-line speech-bubble for the CEO chat sidebar entry.
export const ChatIcon = (p: Props) => (
  <svg {...base} {...p}>
    <path
      d="M3 4h10a1 1 0 0 1 1 1v6a1 1 0 0 1-1 1H7l-3 2.5V12H3a1 1 0 0 1-1-1V5a1 1 0 0 1 1-1Z"
      stroke="currentColor"
      strokeWidth="1.2"
      strokeLinejoin="round"
    />
  </svg>
);

export const HygieneIcon = (p: Props) => (
  <svg {...base} {...p}>
    <path d="M8 2l6 11H2L8 2Z" stroke="currentColor" strokeWidth="1.2" strokeLinejoin="round" />
    <path d="M8 6v3M8 11v.01" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" />
  </svg>
);

export const VaultIcon = (p: Props) => (
  <svg {...base} {...p}>
    <rect x="3" y="6" width="10" height="8" rx="1" stroke="currentColor" strokeWidth="1.2" />
    <path d="M5.5 6V4.5a2.5 2.5 0 0 1 5 0V6" stroke="currentColor" strokeWidth="1.2" />
    <circle cx="8" cy="10" r="1" fill="currentColor" />
  </svg>
);

export const AgentIcon = (p: Props) => (
  <svg {...base} {...p}>
    <rect x="3" y="4" width="10" height="8" rx="1" stroke="currentColor" strokeWidth="1.2" />
    <circle cx="6" cy="8" r="0.8" fill="currentColor" />
    <circle cx="10" cy="8" r="0.8" fill="currentColor" />
    <path d="M8 2v2" stroke="currentColor" strokeWidth="1.2" />
  </svg>
);

export const AdminIcon = (p: Props) => (
  <svg {...base} {...p}>
    <circle cx="8" cy="6" r="2.5" stroke="currentColor" strokeWidth="1.2" />
    <path
      d="M3 14c0-2.8 2.2-5 5-5s5 2.2 5 5"
      stroke="currentColor"
      strokeWidth="1.2"
      strokeLinecap="round"
      fill="none"
    />
  </svg>
);

export const GearIcon = (p: Props) => (
  <svg {...base} {...p}>
    <circle cx="8" cy="8" r="2" stroke="currentColor" strokeWidth="1.2" />
    <path
      d="M8 2v2M8 12v2M2 8h2M12 8h2M4 4l1.4 1.4M10.6 10.6L12 12M4 12l1.4-1.4M10.6 5.4L12 4"
      stroke="currentColor"
      strokeWidth="1.2"
      strokeLinecap="round"
    />
  </svg>
);
