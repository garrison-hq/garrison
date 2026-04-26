// Tailwind v4 ships its PostCSS plugin as a separate package
// (@tailwindcss/postcss). Importing the plugin object directly
// avoids the "resolve tailwindcss as a string from a different
// cwd" failure that turbopack hits when it walks up from
// dashboard/ to the repo root looking for a package.json/lockfile.
import tailwind from '@tailwindcss/postcss';

export default {
  plugins: [tailwind()],
};
