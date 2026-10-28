// _routes.json sends only the API, staff auth and health paths here; every
// other path is a static SPA asset. Forward them unchanged to the tunnel
// origin, which rewrites Host to the Pages origin so the server's Host,
// Origin and __Host- cookie checks see one first-party origin.
const EDGE_HEADERS = {
  'Strict-Transport-Security': 'max-age=31536000',
  'X-Robots-Tag': 'noindex, nofollow',
  'X-Content-Type-Options': 'nosniff',
  'Referrer-Policy': 'no-referrer',
};

export async function onRequest({ request, env }) {
  const url = new URL(request.url);
  const target = new URL(url.pathname + url.search, env.API_ORIGIN);
  const upstream = await fetch(target, {
    method: request.method,
    headers: request.headers,
    body: request.body,
    redirect: 'manual',
  });
  // Pages applies _headers only to static assets, not Function responses.
  const response = new Response(upstream.body, upstream);
  for (const [name, value] of Object.entries(EDGE_HEADERS)) {
    if (!response.headers.has(name)) response.headers.set(name, value);
  }
  return response;
}
