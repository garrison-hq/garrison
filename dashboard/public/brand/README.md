# Garrison brand assets

Logos and favicons for the Operator OS dashboard.

## Folder structure

```
brand-bundle/
├── lockup/                          full lockup (mark + wordmark)
│   ├── garrison-lockup-dark.svg     for dark backgrounds
│   └── garrison-lockup-light.svg    for light backgrounds
├── mark/                            mark only (no wordmark)
│   ├── garrison-mark-dark.svg
│   └── garrison-mark-light.svg
└── favicon/
    ├── favicon.svg                  adaptive — auto-swaps via prefers-color-scheme
    ├── favicon-dark.svg             explicit dark version
    ├── favicon-light.svg            explicit light version
    ├── favicon-{16,32,48,64,128,192,256,512}-dark.png
    ├── favicon-{16,32,48,64,128,192,256,512}-light.png
    └── apple-touch-icon-180.png     iOS home screen, dark bg
```

## Using in the dashboard

The dashboard has both light and dark themes. Two patterns:

### A) Render the right SVG based on the active theme (recommended)

```jsx
// pseudo-code — wire to your theme state
import lockupDark from './brand/lockup/garrison-lockup-dark.svg';
import lockupLight from './brand/lockup/garrison-lockup-light.svg';

const Logo = () => {
  const { theme } = useTheme();
  return <img src={theme === 'dark' ? lockupDark : lockupLight} alt="garrison" height="28" />;
};
```

### B) Use a single SVG with `currentColor` and let CSS color it

If you want one file, edit the SVG to replace `#EDEDED`/`#0A0A0A` with `currentColor` and the pip with a CSS variable. Then color via the parent's `color` property and a CSS var for accent. Less files, slightly more setup.

## Favicon — recommended `<head>` setup

Drop the contents of `favicon/` into your `public/` folder, then in your HTML head:

```html
<!-- Modern browsers: adaptive SVG that auto-swaps with system theme -->
<link rel="icon" type="image/svg+xml" href="/favicon.svg">

<!-- Fallback PNGs (default to dark version since most browsers/OS chrome is dark) -->
<link rel="icon" type="image/png" sizes="32x32" href="/favicon-32-dark.png">
<link rel="icon" type="image/png" sizes="16x16" href="/favicon-16-dark.png">

<!-- iOS home screen -->
<link rel="apple-touch-icon" sizes="180x180" href="/apple-touch-icon-180.png">
```

If you want the favicon to flip with the *dashboard's* theme rather than the OS, swap the `<link rel="icon">` href in JS when the user toggles theme.

## Color values

If you need to use them elsewhere:

| Token        | Dark              | Light             |
|--------------|-------------------|-------------------|
| Foreground   | `#EDEDED`         | `#0A0A0A`         |
| Accent (pip) | `#5DD3A8`         | `#1F8E63`         |

The accent on dark is `oklch(0.78 0.14 160)` ≈ `#5DD3A8`. On light, the same hue is shifted darker for contrast: `oklch(0.55 0.15 160)` ≈ `#1F8E63`.

## Clear space and minimum size

- **Lockup**: keep clear space equal to the height of the mark on all sides. Minimum width 120px.
- **Mark**: minimum 16px. Use the dedicated 16/32 PNG renders for sub-48px sizes — the SVG strokes are tuned for ≥48px.
