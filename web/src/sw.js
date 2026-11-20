const cacheName = __TRACKER_CACHE__;
const assetList = __TRACKER_ASSETS__;
const assets = new Set(assetList);

self.addEventListener('install', (event) => {
  event.waitUntil(caches.open(cacheName).then((cache) => cache.addAll(assetList)));
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
    event.respondWith(fetch(request).catch(async () => (await caches.open(cacheName)).match('/')));
  } else if (!url.search && assets.has(url.pathname)) {
    event.respondWith((async () => (await (await caches.open(cacheName)).match(request)) ?? fetch(request))());
  }
});
