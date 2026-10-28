// _routes.json sends only the API, staff auth and health paths here; every
// other path is a static SPA asset. Forward them unchanged to the tunnel
// origin, which rewrites Host to the Pages origin so the server's Host,
// Origin and __Host- cookie checks see one first-party origin.
export async function onRequest({ request, env }) {
  const url = new URL(request.url);
  const target = new URL(url.pathname + url.search, env.API_ORIGIN);
  return fetch(target, {
    method: request.method,
    headers: request.headers,
    body: request.body,
    redirect: 'manual',
  });
}
