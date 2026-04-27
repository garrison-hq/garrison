import type { NextConfig } from 'next';
import createNextIntlPlugin from 'next-intl/plugin';

// next-intl's plugin wires lib/i18n/request.ts into the build so the
// catalog for the active locale resolves on every request. The
// plugin is the only published path for the App Router; configuring
// next-intl manually requires reimplementing what this plugin does.
const withNextIntl = createNextIntlPlugin('./lib/i18n/request.ts');

const nextConfig: NextConfig = {
  // Standalone output is required for the production Docker image (T019).
  // The Dockerfile copies .next/standalone + .next/static + public into the
  // runtime image; the resulting bundle launches via `node server.js`.
  output: 'standalone',
  // Emit browser-side source maps in production builds. Required so
  // monocart-coverage-reports can map V8 coverage (collected via
  // Playwright's page.coverage API) back to the source files for Sonar
  // ingestion. The cost is ~10% larger .next/static/ output and exposed
  // source paths in browser DevTools — neither concerning for this
  // operator-only deployment.
  productionBrowserSourceMaps: true,
  // Pin turbopack's root to the dashboard/ directory so its lockfile +
  // node_modules search doesn't drift up into the monorepo and confuse
  // tailwindcss / postcss resolution when sibling lockfiles exist at
  // the repo root.
  turbopack: {
    root: __dirname,
  },
};

export default withNextIntl(nextConfig);
