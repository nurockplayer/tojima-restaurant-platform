import test from "node:test";
import assert from "node:assert/strict";
import { createHmac } from "node:crypto";
import { onRequest } from "../functions/_middleware.js";

const env = {
  DEMO_PASSCODE: "fixture-passcode-for-tests",
  DEMO_COOKIE_KEY: "test-only-cookie-key-that-is-at-least-32-bytes",
  DEMO_GATE_SECRET: "test-only-gateway-secret-that-is-at-least-32-bytes",
  DEMO_ORIGIN: "https://demo-123.trycloudflare.com",
};
const base = "https://preview.example";
const request = (path, init = {}) => new Request(`${base}${path}`, init);
const run = (path, init = {}, envOverride = env) => onRequest({ request: request(path, init), env: envOverride });

async function withFetch(stub, fn) {
  const originalFetch = globalThis.fetch;
  globalThis.fetch = stub;
  try { return await fn(); } finally { globalThis.fetch = originalFetch; }
}

async function login() {
  const get = await run("/__demo/login");
  const csrfCookie = get.headers.getSetCookie().find((value) => value.startsWith("__Host-tojima_demo_csrf="));
  const csrf = csrfCookie.match(/^[^=]+=([^;]+)/)[1];
  const response = await run("/__demo/login", {
    method: "POST",
    headers: { origin: base, cookie: `__Host-tojima_demo_csrf=${csrf}`, "content-type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({ csrf, passcode: env.DEMO_PASSCODE }),
  });
  const cookie = response.headers.getSetCookie().find((value) => value.startsWith("__Host-tojima_demo="));
  return { response, cookie: cookie.match(/^[^=]+=([^;]+)/)[1] };
}

function signedCookie(expiry) {
  const payload = String(expiry);
  const signature = createHmac("sha256", env.DEMO_COOKIE_KEY).update(payload).digest("base64url");
  return `${payload}.${signature}`;
}

test("missing variables and invalid origins return bilingual offline 503 without network access", async () => {
  const overrides = [
    { ...env, DEMO_PASSCODE: undefined }, { ...env, DEMO_COOKIE_KEY: undefined },
    { ...env, DEMO_GATE_SECRET: undefined }, { ...env, DEMO_ORIGIN: undefined },
    { ...env, DEMO_GATE_SECRET: `${env.DEMO_GATE_SECRET}\r\nInjected: yes` },
    { ...env, DEMO_GATE_SECRET: `${env.DEMO_GATE_SECRET}\u0100` },
    ...["https://example.com", "http://demo-1.trycloudflare.com", "https://x.trycloudflare.com.evil.test", "https://u@demo.trycloudflare.com", "https://demo.trycloudflare.com/path", "https://demo.trycloudflare.com?x=1", "https://DEMO.trycloudflare.com"].map((origin) => ({ ...env, DEMO_ORIGIN: origin })),
  ];
  let fetchCount = 0;
  await withFetch(async () => { fetchCount++; return new Response("unexpected proxy"); }, async () => {
    for (const override of overrides) {
      const response = await run("/", {}, override);
      const body = await response.text();
      assert.equal(response.status, 503);
      assert.match(body, /オフライン/);
      assert.match(body, /離線/);
      assert.match(body, /デモ停止中/);
      assert.equal(response.headers.get("x-robots-tag"), "noindex, nofollow");
      assert.equal(response.headers.get("cache-control"), "no-store");
      assert.match(response.headers.get("content-security-policy"), /default-src 'none'/);
    }
  });
  assert.equal(fetchCount, 0);
});

test("unreachable proxy is a sanitized offline 503", async () => {
  await withFetch(async () => { throw new Error("secret internal detail"); }, async () => {
    const response = await run("/", { headers: { cookie: `__Host-tojima_demo=${signedCookie(Math.floor(Date.now() / 1000) + 60)}` } });
    assert.equal(response.status, 503);
    assert.doesNotMatch(await response.text(), /secret internal detail/);
    assert.equal(response.headers.get("x-robots-tag"), "noindex, nofollow");
    assert.equal(response.headers.get("cache-control"), "no-store");
  });
});

test("successful same-origin login issues a 12-hour secure session and the guide is complete", async () => {
  const first = await run("/__demo/login");
  assert.equal(first.status, 200);
  assert.match(first.headers.get("content-security-policy"), /frame-ancestors 'none'/);
  const { response, cookie } = await login();
  assert.equal(response.status, 303);
  assert.equal(response.headers.get("location"), "/__demo/guide");
  const session = response.headers.getSetCookie().find((value) => value.startsWith("__Host-tojima_demo="));
  assert.match(session, /Max-Age=43200/);
  assert.match(session, /Secure/);
  assert.match(session, /HttpOnly/);
  assert.match(session, /SameSite=Lax/);
  assert.match(session, /Path=\//);
  const guide = await run("/__demo/guide", { headers: { cookie: `__Host-tojima_demo=${cookie}` } });
  const body = await guide.text();
  assert.equal(guide.status, 200);
  for (const phrase of ["DEMO / 参考価格 / 現行店内価格未確認", "demo-shironagasukujira-table-05", "cashier", "catalog_admin", "Mac", "Stripe", "重複送出", "重置測試資料", "重新啟動"]) assert.ok(body.includes(phrase), `guide missing ${phrase}`);
  assert.doesNotMatch(body, /operationstest|filesystem|credential file/i);
  for (const response of [first, guide]) {
    assert.equal(response.headers.get("x-robots-tag"), "noindex, nofollow");
    assert.equal(response.headers.get("cache-control"), "no-store");
  }
});

test("bad passcodes, cross-origin, no-origin, and missing-CSRF login attempts are rejected", async () => {
  const get = await run("/__demo/login");
  const csrfCookie = get.headers.getSetCookie()[0].match(/^[^=]+=([^;]+)/)[1];
  for (const init of [
    { method: "POST", headers: { origin: base, cookie: `__Host-tojima_demo_csrf=${csrfCookie}` }, body: new URLSearchParams({ csrf: csrfCookie, passcode: "wrong" }) },
    { method: "POST", headers: { origin: "https://attacker.example", cookie: `__Host-tojima_demo_csrf=${csrfCookie}` }, body: new URLSearchParams({ csrf: csrfCookie, passcode: env.DEMO_PASSCODE }) },
    { method: "POST", headers: { cookie: `__Host-tojima_demo_csrf=${csrfCookie}` }, body: new URLSearchParams({ csrf: csrfCookie, passcode: env.DEMO_PASSCODE }) },
    { method: "POST", headers: { origin: base }, body: new URLSearchParams({ csrf: csrfCookie, passcode: env.DEMO_PASSCODE }) },
  ]) {
    const response = await run("/__demo/login", init);
    assert.ok(response.status === 401 || response.status === 403);
    assert.equal(response.headers.get("x-robots-tag"), "noindex, nofollow");
    assert.equal(response.headers.get("cache-control"), "no-store");
    assert.equal(response.headers.getSetCookie().some((value) => value.startsWith("__Host-tojima_demo=")), false);
  }
});

test("tampered, expired, overlong, and too-far-future sessions cannot reach guide or proxy", async () => {
  let fetchCount = 0;
  await withFetch(async () => { fetchCount++; return new Response("unexpected proxy"); }, async () => {
  for (const token of ["bad.signature", signedCookie(Math.floor(Date.now() / 1000) - 1), signedCookie(Math.floor(Date.now() / 1000) + 13 * 60 * 60), `${signedCookie(Math.floor(Date.now() / 1000) + 60)}x`, "x".repeat(129)]) {
    const guide = await run("/__demo/guide", { headers: { cookie: `__Host-tojima_demo=${token}` } });
    const proxy = await run("/staff", { headers: { cookie: `__Host-tojima_demo=${token}` } });
    for (const response of [guide, proxy]) {
      assert.equal(response.status, 303);
      assert.equal(response.headers.get("location"), "/__demo/login");
      assert.equal(response.headers.get("x-robots-tag"), "noindex, nofollow");
      assert.equal(response.headers.get("cache-control"), "no-store");
    }
  }
  });
  assert.equal(fetchCount, 0);
});

test("proxy strips gate credentials, preserves all other cookie segments and repeated Set-Cookie, and drops caller Host", async () => {
  const { cookie } = await login();
  let forwarded;
  await withFetch(async (input) => {
    forwarded = input;
    const headers = new Headers();
    headers.append("set-cookie", "duplicate=one; Expires=Wed, 21 Oct 2030 07:28:00 GMT; Path=/");
    headers.append("set-cookie", "duplicate=two; Path=/; HttpOnly");
    headers.append("set-cookie", "app_two=2; Path=/; SameSite=Lax");
    headers.append("set-cookie", "__Host-tojima_demo=attacker; Path=/");
    headers.append("set-cookie", "__Host-tojima_demo_csrf=attacker; Path=/");
    return new Response("upstream", { status: 200, headers });
  }, async () => {
    const response = await run("/v1/orders?x=1", {
      method: "POST",
      headers: { cookie: `app_auth=keep; duplicate=first; app_auth=second; __Host-tojima_demo=${cookie}; __Host-tojima_demo_csrf=csrf-secret`, "x-tojima-demo-gate": "caller-value", "content-type": "text/plain", origin: base, referer: `${base}/staff`, authorization: "Bearer synthetic-app-token", host: "attacker.invalid" },
      body: "payload",
    });
    assert.equal(response.status, 200);
    assert.equal(await response.text(), "upstream");
    assert.equal(forwarded.url, `${env.DEMO_ORIGIN}/v1/orders?x=1`);
    assert.equal(forwarded.method, "POST");
    assert.equal(forwarded.redirect, "manual");
    assert.equal(await forwarded.text(), "payload");
    assert.equal(forwarded.headers.get("x-tojima-demo-gate"), env.DEMO_GATE_SECRET);
    assert.equal(forwarded.headers.get("cookie"), "app_auth=keep; duplicate=first; app_auth=second");
    assert.notEqual(forwarded.headers.get("host"), "attacker.invalid");
    assert.equal(forwarded.headers.get("origin"), base);
    assert.equal(forwarded.headers.get("referer"), `${base}/staff`);
    assert.equal(forwarded.headers.get("authorization"), "Bearer synthetic-app-token");
    assert.deepEqual(response.headers.getSetCookie(), ["duplicate=one; Expires=Wed, 21 Oct 2030 07:28:00 GMT; Path=/", "duplicate=two; Path=/; HttpOnly", "app_two=2; Path=/; SameSite=Lax"]);
    assert.equal(response.headers.get("x-robots-tag"), "noindex, nofollow");
    assert.equal(response.headers.get("cache-control"), "no-store");
  });
});

test("logout requires same-origin POST, and deployment internals are blocked", async () => {
  const logout = await run("/__demo/logout", { method: "POST", headers: { origin: base } });
  assert.equal(logout.status, 303);
  assert.equal(logout.headers.get("location"), "/__demo/login");
  assert.match(logout.headers.getSetCookie()[0], /Max-Age=0/);
  const foreign = await run("/__demo/logout", { method: "POST", headers: { origin: "https://attacker.example" } });
  assert.equal(foreign.status, 403);
  const { cookie } = await login();
  const internal = await run("/test/middleware.test.mjs", { headers: { cookie: `__Host-tojima_demo=${cookie}` } });
  assert.equal(internal.status, 404);
});

test("public absolute redirect locations survive and tunnel locations are rewritten", async () => {
  const token = signedCookie(Math.floor(Date.now() / 1000) + 60);
  for (const [location, expected] of [
    ["https://preview.example/staff/login?next=%2F", "https://preview.example/staff/login?next=%2F"],
    [`${env.DEMO_ORIGIN}/staff/login?next=%2F`, "/staff/login?next=%2F"],
  ]) {
    await withFetch(async () => new Response(null, { status: 302, headers: { location } }), async () => {
      const response = await run("/staff", { headers: { cookie: `__Host-tojima_demo=${token}` } });
      assert.equal(response.headers.get("location"), expected);
      assert.equal(response.headers.get("x-robots-tag"), "noindex, nofollow");
      assert.equal(response.headers.get("cache-control"), "no-store");
    });
  }
});

test("combined Set-Cookie fallback preserves Expires commas and duplicate names", async () => {
  const token = signedCookie(Math.floor(Date.now() / 1000) + 60);
  await withFetch(async () => {
    const response = new Response("ok", { headers: [
      ["set-cookie", "same=one; Expires=Wed, 21 Oct 2030 07:28:00 GMT; Path=/"],
      ["set-cookie", "same=two; Path=/; HttpOnly"],
    ] });
    response.headers.getSetCookie = undefined;
    response.headers.getAll = undefined;
    return response;
  }, async () => {
    const response = await run("/staff", { headers: { cookie: `__Host-tojima_demo=${token}` } });
    assert.deepEqual(response.headers.getSetCookie(), ["same=one; Expires=Wed, 21 Oct 2030 07:28:00 GMT; Path=/", "same=two; Path=/; HttpOnly"]);
  });
});

test("oversized login body is rejected before form parsing", async () => {
  const csrf = "c".repeat(32);
  const response = await run("/__demo/login", {
    method: "POST",
    headers: { origin: base, cookie: `__Host-tojima_demo_csrf=${csrf}`, "content-type": "application/x-www-form-urlencoded" },
    body: `csrf=${csrf}&passcode=${"x".repeat(16 * 1024)}`,
  });
  assert.equal(response.status, 413);
  assert.equal(response.headers.get("x-robots-tag"), "noindex, nofollow");
});


test("tunnel HTTP failures and request stream failures return only the offline page", async () => {
  const token = signedCookie(Math.floor(Date.now() / 1000) + 60);
  for (const status of [500, 502, 503, 504, 530]) {
    await withFetch(async () => new Response("private provider diagnostic", { status }), async () => {
      const response = await run("/staff", { headers: { cookie: `__Host-tojima_demo=${token}` } });
      assert.equal(response.status, 503);
      assert.match(await response.text(), /デモ停止中/);
      assert.equal(response.headers.get("x-robots-tag"), "noindex, nofollow");
    });
  }
  const body = new ReadableStream({ start(controller) { controller.error(new Error("private stream error")); } });
  const response = await run("/__demo/login", { method: "POST", headers: { origin: base }, body, duplex: "half" });
  assert.equal(response.status, 503);
  assert.doesNotMatch(await response.text(), /private stream error/);
  assert.equal(response.headers.get("x-robots-tag"), "noindex, nofollow");
});
