// VoltDrive service worker — auto-updating, no stale cache.
//
// Strategy:
//   - HTML navigations + same-origin JS/CSS: NETWORK-FIRST. Online users
//     always get the newest version; cache is only an offline fallback.
//   - Static assets (icons, car images): stale-while-revalidate (fast, but
//     refreshed in the background).
//   - Live data (Firebase / backend / fonts CDN): always network.
//
// On a new deploy, bump VERSION → old caches are deleted and every open tab
// is reloaded automatically (see index.html update handler).
const VERSION = "voltdrive-v63";
const SHELL = [
  "/index.html",
  "/app-live.js",
  "/lib/voltdrive-core.js",
  "/voltdrive.config.js",
  "/manifest.webmanifest",
  "/icons/icon-192.png",
  "/icons/icon-512.png",
  "/icons/apple-touch-icon.png",
  "/project/cars/voyah.jpg",
  "/project/cars/deepal.jpg",
  "/project/cars/dongfeng.jpg",
];

self.addEventListener("install", (event) => {
  // Activate the new worker immediately, don't wait for old tabs to close.
  self.skipWaiting();
  event.waitUntil(
    caches.open(VERSION).then((cache) =>
      // Best-effort precache; never fail install if one asset is missing.
      Promise.allSettled(SHELL.map((u) => cache.add(u)))
    )
  );
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== VERSION).map((k) => caches.delete(k))))
      .then(() => self.clients.claim())
  );
});

// Allow the page to force-activate a waiting worker.
self.addEventListener("message", (e) => {
  if (e.data === "SKIP_WAITING") self.skipWaiting();
});

self.addEventListener("fetch", (event) => {
  const req = event.request;
  if (req.method !== "GET") return;
  const url = new URL(req.url);

  // 1) Live data sources — always network (never cache auth/telemetry).
  const liveHosts = ["firebaseio.com", "googleapis.com", "identitytoolkit", "run.app", "gstatic.com",
    "nominatim.openstreetmap.org", "router.project-osrm.org", "overpass-api.de", "basemaps.cartocdn.com"];
  if (liveHosts.some((h) => url.hostname.includes(h))) {
    event.respondWith(fetch(req).catch(() => caches.match(req)));
    return;
  }

  const sameOrigin = url.origin === self.location.origin;

  // 2) HTML navigations + same-origin scripts/styles — network-first.
  const isNav = req.mode === "navigate";
  const isCode = sameOrigin && /\.(js|css|webmanifest)$/.test(url.pathname);
  if (isNav || isCode) {
    event.respondWith(
      fetch(req)
        .then((res) => {
          if (res && res.status === 200 && sameOrigin) {
            const copy = res.clone();
            caches.open(VERSION).then((c) => c.put(req, copy));
          }
          return res;
        })
        .catch(() => caches.match(req).then((c) => c || caches.match("/index.html")))
    );
    return;
  }

  // 3) Static assets — stale-while-revalidate.
  event.respondWith(
    caches.match(req).then((cached) => {
      const network = fetch(req)
        .then((res) => {
          if (res && res.status === 200 && sameOrigin) {
            const copy = res.clone();
            caches.open(VERSION).then((c) => c.put(req, copy));
          }
          return res;
        })
        .catch(() => cached);
      return cached || network;
    })
  );
});
