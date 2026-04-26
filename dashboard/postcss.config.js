// Tailwind v4 ships its PostCSS plugin as a separate package
// (@tailwindcss/postcss). Listing it here is the required entry point;
// the actual theme tokens live in app/globals.css under @theme blocks
// keyed off the [data-theme] attribute on <html>.
export default {
  plugins: {
    '@tailwindcss/postcss': {},
  },
};
