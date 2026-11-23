# GPS Tracker

GPS Tracker shows your private GPSLogger history on an OpenStreetMap map. It opens to the current local day and also supports month, year, and custom date ranges. The Go server embeds a Svelte frontend built with Bun and stores the data in SQLite.

## Run with Docker Compose

1. Copy `.env.example` to `.env`. Set `APP_URL` to the public HTTPS origin that browsers and GPSLogger will use.
2. In Authentik, create an OAuth2/OpenID Connect application with a confidential client. Set its strict redirect URI to `https://tracker.example.com/auth/callback`. Grant access with the application's Authentik policies or group bindings.
3. Set `OIDC_ISSUER` to the issuer published by Authentik, usually `https://auth.example.com/application/o/gps-tracker/`, and fill in `OIDC_CLIENT_ID` and `OIDC_CLIENT_SECRET`.
4. Run `docker compose up -d --build`. Point your HTTPS reverse proxy at the port published by Compose, `127.0.0.1:8080` by default. If you import large GPS files, set the proxy's body limit to at least 100 MiB and allow enough time to upload them.

### Sign-in protection

The whole web app is behind Authentik. A browser without a valid session is redirected to Authentik before the page, its scripts or any data load. After sign-in it returns to the page it asked for. An expired 15-minute session is renewed on the next page load as long as the 30-day refresh token is valid. If Authentik is not configured, the tracker refuses to serve the app and shows a configuration notice instead.

Only these paths work without signing in:

- `/api/upload`, which GPSLogger calls with a device key
- `/healthz`, for container health checks
- `/auth/*`, the sign-in, callback and signed-out pages
- `/manifest.webmanifest` and `/icons/*`, which Chrome fetches without cookies when installing the app

**Sign out** ends the tracker session and shows a signed-out page. Your Authentik session stays active, so **Sign in again** may not ask for your password. To end it, sign out in Authentik as well.

The Compose volume `tracker-data` contains the database. Back it up with SQLite's online backup tool or stop the tracker before copying the volume. The Docker image runs as UID 10001. When substituting a bind mount, give that user write access to the mounted directory.

To use a release image, set `TRACKER_IMAGE` to `ghcr.io/<owner>/<repo>:<version>`, then run `docker compose pull` and `docker compose up -d --no-build`. GitHub Actions runs on version tags such as `v0.1.0` to verify the build and publish Linux AMD64 and ARM64 images.

## Connect a phone

1. Sign in with Authentik and open **Settings → GPSLogger devices**. Enter a device name and generate a key. Copy the key while it is visible.
2. In GPSLogger on Android, enable **Log to custom URL** and choose **POST**. Copy the URL, headers, and JSON body displayed by the tracker. Leave **Discard offline locations** off to retain queued fixes.
3. Record a fix and check the map. Key activity is shown in Settings. Revoke a lost key there and generate a new key for future uploads.

## Edit and import

- Use **Edit route** to drag a fix and save its corrected position. Select an edited point to restore the imported or recorded coordinates.
- Choose **Add waypoints**, then select a dashed gap or the fix immediately before the intended gap. Tap the map in route order and adjust the suggested recording times if needed. The added points are estimates.
- Use **Import** to preview and add GPX tracks or GPSLogger CSV exports. CSV headers must identify time, latitude, and longitude. Supported timestamps include ISO 8601 values with a time zone. Imports accept up to 100 MiB and 500,000 points. A repeat import skips fixes already stored for your account.
- For detailed editing in dense history, use a shorter custom date range. The overview samples long routes while the editor uses the individual recorded fixes.

## Connect Immich

Set `IMMICH_URL` to the base URL of the instance serving your users. Use the URL without the trailing `/api`. Generate a base64-encoded 32-byte value with `openssl rand -base64 32` and set it as `APP_ENCRYPTION_KEY`. Keep a copy of this key with your backups to recover the encrypted Immich credentials.

Each user connects their own Immich API key in **Settings → Immich photos**. Grant the key `asset.read` and `asset.view`. The tracker looks for photos in the selected date range and near the route. It can estimate the location of a photo without a geotag when its capture time falls between two close points. Tap a photo marker to load its thumbnail.

## Install on Android

Open the HTTPS tracker URL in Chrome and choose **Install app** from the Chrome menu. Launch it from the home screen and sign in with Authentik. The installed app displays an offline screen if the connection drops. GPSLogger records and queues fixes independently.

## Local checks

```sh
cd web
bun install --frozen-lockfile
bun run check
bun run build
cd ..
go test ./...
go run .
```

Local defaults use `http://localhost:8080` and `tracker.sqlite`. Set the OIDC variables in the shell when running outside Compose. Without them, the web app stays locked. For local use, register `http://localhost:8080/auth/callback` as an additional redirect URI in Authentik. To test the import and route APIs against a local GPX file, set `GPS_TRACKER_TEST_GPX` to its path and run `go test -v -run TestRealGPXImport`. The test creates a temporary database and reports counts and timings without printing coordinates.
