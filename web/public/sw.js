// WakeHub Service Worker（修复 F-N3：旧版无任何缓存，离线连 HTML 都拿不到）。
// 策略：
// - /api/ 请求一律走网络，不拦截（管理操作必须实时确认）；
// - 导航请求：网络优先，失败回缓存，再失败回离线提示页；
// - 同源静态资源（JS/CSS/图标）：缓存优先，后台更新。
// 注意：本 SW 不缓存任何 API 响应，不缓存登录态相关内容。
const CACHE_NAME = 'wakehub-shell-v1';
const PRECACHE = ['/', '/manifest.json', '/icons/icon-192.png', '/icons/icon-512.png'];

self.addEventListener('install', (event) => {
  event.waitUntil(
    caches.open(CACHE_NAME).then((cache) => cache.addAll(PRECACHE)).catch(() => {
      // 预缓存失败不阻塞安装（离线能力降级，线上仍可用）
    }),
  );
  self.skipWaiting();
});

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches.keys().then((keys) =>
      Promise.all(keys.filter((k) => k !== CACHE_NAME).map((k) => caches.delete(k))),
    ).then(() => self.clients.claim()),
  );
});

self.addEventListener('fetch', (event) => {
  const url = new URL(event.request.url);
  if (url.pathname.startsWith('/api/')) return; // API 请求不拦截
  if (event.request.method !== 'GET') return;

  if (event.request.mode === 'navigate') {
    event.respondWith(
      fetch(event.request)
        .then((resp) => {
          const copy = resp.clone();
          caches.open(CACHE_NAME).then((cache) => cache.put('/', copy)).catch(() => {});
          return resp;
        })
        .catch(() =>
          caches.match('/').then(
            (cached) =>
              cached ||
              new Response('WakeHub 当前处于离线状态，设备操作需连接服务端确认。', {
                headers: { 'Content-Type': 'text/plain; charset=utf-8' },
              }),
          ),
        ),
    );
    return;
  }

  // 同源静态资源：缓存优先
  if (url.origin === self.location.origin) {
    event.respondWith(
      caches.match(event.request).then((cached) => {
        const network = fetch(event.request)
          .then((resp) => {
            if (resp.ok) {
              const copy = resp.clone();
              caches.open(CACHE_NAME).then((cache) => cache.put(event.request, copy)).catch(() => {});
            }
            return resp;
          })
          .catch(() => cached);
        return cached || network;
      }),
    );
  }
});
