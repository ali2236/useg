# AGENTS.md

## UI

- Admin and portal are server-rendered Go `html/template` pages in `internal/web/templates/`.
- UI follows the Astryx design system (Meta): https://astryx.atmeta.com/ — consult its docs and `/components` for tokens, spacing, radii, states and patterns. Adopt its visual language, not its React code.
- Styling is our own CSS in `internal/web/assets/app.css` (tokens + components), served from the embedded assets. No CDN, no CSS framework, no JS framework. Use the `light-dark()` tokens and RTL-safe logical properties, and build from the existing classes; do not add another CSS or component library.
- JavaScript is plain vanilla and served from this server (embedded assets or the templates). No external CDN and no third-party script host — every `<script src>` points at a path this binary serves.
- Persian is the default language (`dir="rtl"`). Put every user-facing string in `internal/web/i18n.go`.
