import type { NextConfig } from 'next';

const nextConfig: NextConfig = {
  // Standalone output is required for the production Docker image (T019).
  // The Dockerfile copies .next/standalone + .next/static + public into the
  // runtime image; the resulting bundle launches via `node server.js`.
  output: 'standalone',
};

export default nextConfig;
