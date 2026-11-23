# Changelog

All notable changes to GPS Tracker are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.0.6] - 2026-11-23

### Fixed
- **No layout jump when switching periods on mobile**: The route summary keeps the same height while a day, month or year loads or has no points, so the map no longer resizes when you step between periods.

### Removed
- **Immich photos panel on mobile**: Photos are shown only as markers on the map on phones, which leaves more room for the map. Tapping a marker still opens the photo preview. The panel remains in the desktop sidebar.

## [0.0.5] - 2026-11-23

### Security
- **Whole app behind Authentik**: The server now refuses to serve the web app (HTML, scripts, styles, service worker) without a valid session. Signed-out page loads redirect straight to Authentik and return to the requested page after sign-in. Only `/api/upload` (device keys), `/healthz`, `/auth/*`, the manifest, and the app icons remain public.
- **Server-side session renewal on page load**: An expired session is renewed from the refresh token when a page loads, so returning users don't have to sign in again.
- **Fail closed without OIDC**: When Authentik is not configured, the app shows a configuration notice instead of the UI.
- **Private asset caching**: Hashed assets now use `Cache-Control: private` so shared proxies don't serve them to signed-out visitors.
- **Refresh reuse grace window**: A refresh token reused within 30 seconds of rotation (e.g. several tabs reopening at once) is rejected without revoking the whole token family.

### Added
- **Full-screen map**: A button above the zoom controls expands the map to fill the screen, using the browser's Fullscreen API where available and a full-viewport layout otherwise (e.g. iPhone Safari). Press Esc or the button again to exit. Opening a photo from the map leaves full screen to show the lightbox.

### Changed
- **Single-screen mobile layout**: The page no longer scrolls on phones. The route panel sits under the map at the height its content needs, and the map fills the rest of the screen. Only a panel taller than about half the screen (route editing) scrolls inside itself. Pull-to-refresh and overscroll bounce are disabled to prevent accidental scrolling.
- **Sign-in and sign-out pages**: A failed sign-in shows an error page with **Try again** instead of redirecting back to the app, which would loop through Authentik. **Sign out** now leads to a signed-out page.
- The in-app welcome screen with the **Sign in with Authentik** button is replaced by an automatic redirect.
- Immich photo errors and the "choose a shorter range" hint now appear in the Immich photos panel.

### Removed
- **Recent points card on the home screen**: The last 10 uploads remain available on the Profile page.
- **Map layers panel and legend in the sidebar**: The base map is still chosen with the switcher on the map. The route, gap and photo visibility toggles are gone, so all three are always shown.
- **Expand panel button on mobile**: No longer needed now that the panel sits fully under the map.

## [0.0.4] - 2026-11-21

### Changed
- **Preserve map zoom in edit mode**: Entering or leaving route edit mode no longer resets the current zoom level or map view.
- **Direct live point & route editing**: Dragging a point or editing its coordinates now moves both the point and its attached route segments in real time, supporting edits across multiple points before saving.
- **New Designe**: Rework the color theme and layout.
- **Edit panel controls**: Moved Save and Cancel buttons to the top of the Edit route panel, and updated **Done** to automatically save all pending edited points and newly placed waypoints.
- **Solid palette styling**: Replaced all CSS gradients across the topbar, avatars, and progress bar with solid colors from the `#060517`, `#cf5c36`, and `#efc88b` palette.
- **Single-page Profile view**: Removed the section tabs so all profile sections (Import, Export GPX, GPSLogger devices, Immich, and Recent uploads) appear together on one centered page.

### Removed
- **CARTO map layers**: Removed the CARTO Voyager and CARTO Dark map views and their CSP `img-src` host (retaining OpenStreetMap, OpenTopoMap, and Esri Satellite).
- **Route summary Export GPX button**: Removed the duplicate Export GPX button from the Route summary panel (GPX export remains in the Profile page).
- **Profile Appearance section**: Removed the redundant Appearance section and theme button from the Profile page (the theme toggle remains in the topbar).

## [0.0.3] - 2026-11-21

### Added
- **GPX export for custom date periods** (`GET /api/export`): Export full-resolution GPX 1.1 tracks (preserving streams, segment breaks, and edited points) for any chosen date/time period via the Profile page (with presets for Current view, Today, Last 7 days, This month, and This year) or directly from the Route summary panel.
- **GPX/CSV test & import progress view**: Streaming NDJSON progress (`?stream=1`) for both preview/testing and importing, displaying live upload bytes, parsing progress, validation/save stages, progress bar, and live counters.
- **Immich image previews & full-screen lightbox**: Added a sidebar thumbnail preview strip, click-to-enlarge map photo popups, higher-resolution preview loading (`?size=preview`), and a full-screen lightbox with keyboard navigation and locate-on-map support.
- **Full editing of captured points**: Added `edited_time` support (`PATCH /api/points/{id}`) so captured points can have their latitude, longitude, and recording timestamp edited (synced live with the draggable marker) and restored to their original values without breaking duplicate detection.
- **Insert points between existing points**: Added an **Add between** mode and quick **Before this / After this** actions on any selected point; supports tapping any solid or dashed segment between consecutive points, dragging draft waypoints, editing their coordinates/timestamps, and auto-inserting midpoints.
- **Recent uploads history (last 10 points)** (`GET /api/points/recent`): Tracks the last 10 points sent to `/api/upload` (including device name, recorded/received timestamps, coordinates, and duplicate status) in both the Profile page and a collapsible sidebar card, with click-to-jump on the map.
- **Dark & light theme toggle**: Added dark and light themes built around `#060517`, `#cf5c36`, and `#efc88b`, persisted in `localStorage`, with toggles in the topbar and Profile page.
- **Map layer selector**: Added selectable base map layers (OpenStreetMap, CARTO Voyager Light, CARTO Dark Matter, OpenTopoMap, and Esri Satellite) available from both an on-map switcher and the sidebar, alongside overlay toggles for routes, gaps, and Immich photos.
- **Session and refresh tokens** (`POST /auth/refresh`): Replaced single long-lived sessions with short-lived session tokens (15m) and rotating refresh tokens (30d) featuring reuse-detection family revocation, automatic client-side 401 refresh retry, and proactive pre-expiry refresh.

### Changed
- **Combined centered Profile page**: Moved Settings and Import controls out of the topbar and into a single larger, centered Profile view combining devices, recent upload history, GPX/CSV import & export, Immich settings, and theme preferences.
- **Masked API key input**: Displays stars (`********************`) in the Immich API key input box when a key is configured, and masked indicators next to active device keys.
- **Mobile-friendly UI**: Improved responsive layouts, touch target sizes, safe-area padding, topbar accessibility, wrapping of setup snippets, and added a mobile panel expand/collapse toggle.
- **Database schema v2**: Added `refresh_tokens`, `upload_history`, and `points.edited_time` with automatic migration and indexing.
- **Content Security Policy**: Allowed configured map tile providers in `img-src` and enabled gzip compression for `application/gpx+xml` exports.

## [0.0.2] - 2026-11-20

### Added
- Database schema versioning (`PRAGMA user_version`), security headers, gzip compression, and point deletion support.
- Dependency version pinning and Renovate configuration.

## [0.0.1] - 2026-11-20

### Added
- Initial release of GPS Tracker with Go backend, SQLite storage, Svelte/Leaflet frontend, Authentik OIDC sign-in, GPSLogger uploads, route editing, GPX/CSV import, Immich photo overlay, and Docker/PWA support.
