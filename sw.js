const CACHE = 'paste-v1';

self.addEventListener('install', e => {
  self.skipWaiting();
});

self.addEventListener('activate', e => {
  e.waitUntil(clients.claim());
});

self.addEventListener('fetch', e => {
  const url = new URL(e.request.url);

  // Handle Share Target POST
  if (e.request.method === 'POST' && url.pathname === '/share-target') {
    e.respondWith(handleShare(e.request));
    return;
  }

  // Default: pass through (no caching — server is always live)
  e.respondWith(fetch(e.request));
});

async function handleShare(request) {
  try {
    const formData = await request.formData();
    const file = formData.get('file');
    const text = formData.get('text') || formData.get('title') || formData.get('url');

    if (file && file.size > 0) {
      const uploadData = new FormData();
      const name = file.name || `shared_${Date.now()}`;
      uploadData.append('file', file, name);
      const resp = await fetch('/api/upload', { method: 'POST', body: uploadData });
      const result = await resp.json();
      if (result.url) return Response.redirect(result.url, 303);
    }

    if (text) {
      const resp = await fetch('/api/text', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ content: text })
      });
      const result = await resp.json();
      if (result.url) return Response.redirect(result.url, 303);
    }

    return Response.redirect('/?shared=empty', 303);
  } catch (err) {
    return Response.redirect('/?shared=error', 303);
  }
}
