import type { NextConfig } from 'next';

const nextConfig: NextConfig = {
  // Standalone output is required for the production Docker image (T019).
  // The Dockerfile copies .next/standalone + .next/static + public into the
  // runtime image; the resulting bundle launches via `node server.js`.
  output: 'standalone',
  // Pin turbopack's root to the dashboard/ directory so its lockfile +
  // node_modules search doesn't drift up into the monorepo and confuse
  // tailwindcss / postcss resolution when sibling lockfiles exist at
  // the repo root.
  turbopack: {
    root: __dirname,
  },
};

export default nextConfig;
