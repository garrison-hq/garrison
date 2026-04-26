import { defineConfig } from 'vitest/config';

// Vitest config for unit tests colocated with the code they exercise
// (lib/**/*.test.ts, components/**/*.test.tsx).
//
// Playwright integration tests live under tests/integration/ and are
// excluded — they run via `bunx playwright test`.
export default defineConfig({
  test: {
    environment: 'node',
    include: [
      'lib/**/*.{test,spec}.{ts,tsx}',
      'components/**/*.{test,spec}.{ts,tsx}',
      'app/**/*.{test,spec}.{ts,tsx}',
    ],
    exclude: [
      'node_modules/**',
      '.next/**',
      'tests/integration/**',
    ],
  },
  resolve: {
    alias: {
      '@': new URL('.', import.meta.url).pathname.replace(/\/$/, ''),
    },
  },
});
