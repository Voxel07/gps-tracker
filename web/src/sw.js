const cacheName = __TRACKER_CACHE__;
const assetList = __TRACKER_ASSETS__;
const assets = new Set(assetList);

self.addEventListener('install', (event) => {
  event.waitUntil(
    caches.open(cacheName)
      .then((cache) => cache.addAll(assetList))
      .then(() => self.skipWaiting()),
  );
});

self.addEventListener('activate', (event) => {
  event.waitUntil((async () => {
    const names = await caches.keys();
    await Promise.all(names.filter((name) => name.startsWith('tracker-shell-') && name !== cacheName).map((name) => caches.delete(name)));
    await self.clients.claim();
  })());
});

self.addEventListener('fetch', (event) => {
  const { request } = event;
  const url = new URL(request.url);
  if (request.method !== 'GET' || url.origin !== self.location.origin) return;

  if (request.mode === 'navigate' && url.pathname === '/') {
    event.respondWith((async () => {
      try {
        const response = await fetch(request);
        if (response.status >= 502 && response.status <= 504) {
          return (await caches.open(cacheName)).match('/') ?? response;
        }
        return response;
      } catch {
        return (await caches.open(cacheName)).match('/') ?? Response.error();
      }
    })());
  } else if (!url.search && assets.has(url.pathname)) {
    event.respondWith((async () => (await (await caches.open(cacheName)).match(request)) ?? fetch(request))());
  }
});
