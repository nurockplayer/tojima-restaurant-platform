const COOKIE = "__Host-tojima_demo";
const CSRF_COOKIE = "__Host-tojima_demo_csrf";
const TTL_SECONDS = 12 * 60 * 60;
const CSP = "default-src 'none'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'; upgrade-insecure-requests";
const DEMO_LABEL = "DEMO / 参考価格 / 現行店内価格未確認";

function configured(env) {
  return typeof env.DEMO_PASSCODE === "string" && env.DEMO_PASSCODE.length > 0 &&
    typeof env.DEMO_COOKIE_KEY === "string" && env.DEMO_COOKIE_KEY.length >= 32 &&
    typeof env.DEMO_GATE_SECRET === "string" && new TextEncoder().encode(env.DEMO_GATE_SECRET).length >= 32 && /^[\x20-\x7e]+$/.test(env.DEMO_GATE_SECRET) &&
    typeof env.DEMO_ORIGIN === "string" && /^https:\/\/[a-z0-9-]+\.trycloudflare\.com$/.test(env.DEMO_ORIGIN);
}

function htmlResponse(body, status = 200, extraHeaders = []) {
  const headers = new Headers({
    "content-type": "text/html; charset=utf-8",
    "cache-control": "no-store",
    "x-robots-tag": "noindex, nofollow",
    "content-security-policy": CSP,
    "x-content-type-options": "nosniff",
    "referrer-policy": "no-referrer",
    "x-frame-options": "DENY",
  });
  for (const [name, value] of extraHeaders) headers.append(name, value);
  return new Response(`<!doctype html><html lang="ja"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Tojima demo</title></head><body>${body}</body></html>`, { status, headers });
}

function offline() {
  return htmlResponse(`<main><h1>DEMO は現在利用できません</h1><p>デモ停止中（オフライン）です。しばらくしてから再度お試しください。</p><hr><h1>示範目前無法使用</h1><p>示範目前離線，請稍後再試。</p></main>`, 503);
}

function cookieValues(header = "", name) {
  return (header || "").split(";").map((part) => part.trim()).filter((part) => {
    const at = part.indexOf("=");
    return at >= 0 && part.slice(0, at).trim() === name;
  }).map((part) => part.slice(part.indexOf("=") + 1).trim());
}

async function equalBytes(a, b) {
  const aa = new TextEncoder().encode(a);
  const bb = new TextEncoder().encode(b);
  let diff = aa.length ^ bb.length;
  const [da, db] = await Promise.all([crypto.subtle.digest("SHA-256", aa), crypto.subtle.digest("SHA-256", bb)]);
  const xa = new Uint8Array(da);
  const xb = new Uint8Array(db);
  for (let i = 0; i < xa.length; i++) diff |= xa[i] ^ xb[i];
  return diff === 0;
}

async function signingKey(keyText, usage) {
  return crypto.subtle.importKey("raw", new TextEncoder().encode(keyText), { name: "HMAC", hash: "SHA-256" }, false, usage);
}

function decodeBase64Url(value) {
  if (!/^[A-Za-z0-9_-]{43}$/.test(value)) return null;
  try {
    const raw = atob(value.replace(/-/g, "+").replace(/_/g, "/") + "=");
    return Uint8Array.from(raw, (char) => char.charCodeAt(0));
  } catch { return null; }
}

async function signedValue(key, value) {
  return new Uint8Array(await crypto.subtle.sign("HMAC", key, new TextEncoder().encode(value)));
}

async function validSession(request, env) {
  const values = cookieValues(request.headers.get("cookie"), COOKIE);
  if (values.length !== 1 || !values[0] || values[0].length > 128) return false;
  const token = values[0];
  const dot = token.lastIndexOf(".");
  if (dot <= 0) return false;
  const payload = token.slice(0, dot);
  const signature = token.slice(dot + 1);
  const signatureBytes = decodeBase64Url(signature);
  if (!signatureBytes) return false;
  const key = await signingKey(env.DEMO_COOKIE_KEY, ["verify"]);
  if (!await crypto.subtle.verify("HMAC", key, signatureBytes, new TextEncoder().encode(payload))) return false;
  const expiry = Number(payload);
  const now = Math.floor(Date.now() / 1000);
  return /^\d{1,12}$/.test(payload) && Number.isSafeInteger(expiry) && expiry > now && expiry <= now + TTL_SECONDS;
}

function sameOrigin(request) {
  const origin = request.headers.get("origin");
  return !!origin && origin === new URL(request.url).origin;
}

function loginPage(error = false, csrf = "") {
  return htmlResponse(`<main><h1>Tojima デモ</h1><p>${DEMO_LABEL}</p>${error ? "<p role=alert>認証できませんでした。入力を確認してください。</p>" : ""}<form method="post" action="/__demo/login"><input type="hidden" name="csrf" value="${csrf}"><label>デモ用パスコード <input name="passcode" type="password" autocomplete="current-password" required></label><button>ログイン</button></form><hr><h1>Tojima 示範</h1><p>參考價格，尚未確認店內現行價格。</p><p>請輸入示範通行碼。</p></main>`);
}

function guidePage() {
  return htmlResponse(`<main><h1>博多餃子酒場 シロナガスクジラ — デモ案内</h1><p><strong>${DEMO_LABEL}</strong></p>
<p>独立したテスト店舗の5号卓を使う操作デモです。現行メニューや実店舗運用の承認を示しません。注文、現金、レシートはすべて合成テストデータです。</p>
<nav><a href="/?table=demo-shironagasukujira-table-05">ゲスト / 訪客（5号桌）</a> · <a href="/staff">スタッフ / 店員</a> · <a href="/kitchen">仮想KDS / 廚房</a> · <a href="/catalog">カタログ / 商品目錄</a></nav>
<h2>5つの確認手順 / 五個操作步驟</h2><ol>
<li>同じ5号卓URLを3つのゲスト画面で開き、定食、餃子、小皿を追加します。別画面から数量とご飯の選択を変え、削除後に共有内容がそろうことを確認します。</li>
<li>カタログ画面で商品を理由付きで一時停止し、ゲストから選択・注文できないことを確認して、許可された管理者で再開します。</li>
<li>税込JPY合計を確認してテスト現金注文を確定します。同じカート世代の競合確定が1件の注文になることを確認します。</li>
<li>スタッフ画面で受付し、仮想KDSの1枚のカードを調理中・完成へ進め、スタッフで提供済みにします。</li>
<li>合成現金を確認・精算し、テストレシートを確認して卓を閉じます。未払いまたは未提供注文がある場合は閉卓できないことを確認します。</li></ol>
<h2>繁體中文操作步驟</h2><ol lang="zh-Hant">
<li>在三個訪客畫面開啟同一個 5 號桌連結，加入定食、餃子和小菜；從另一個畫面修改數量與白飯選項，再移除一項，確認內容同步。</li>
<li>在商品目錄暫停一項商品並填寫原因，確認訪客不能選購或送出；再由獲授權的管理員恢復供應。</li>
<li>確認含稅日圓總額，以測試現金方式建立訂單；同一購物車版本的重複送出應只建立一筆訂單。</li>
<li>由店員接單，在虛擬 KDS 將訂單卡片推進至製作中和完成，再由店員標記為已送達。</li>
<li>檢視並結清合成現金、查看測試收據後關閉桌位；仍有未付款或未送達訂單時不得關桌。</li></ol>
<h2>テスト用スタッフ識別子 / 測試店員代碼</h2><p>cashier（受付・テスト現金）, kitchen（KDS）, manager または catalog_admin（許可された商品公開状態操作）。各テストアクセスコードは利用担当者へ別途案内します。URLに入れたり、この案内に記載したりしないでください。測試存取代碼會另行提供給操作人員，請勿放入網址或本指南。</p>
<p>このデモはMacで起動した独立テスト環境が稼働中の場合に利用できます。停止後の再起動ではテストデータをリセットし、新しい状態から始めます。Stripeや決済ネットワークには接続せず、現金とレシートは合成データです。</p>
<p lang="zh-Hant">僅在 Mac 上啟動的獨立測試環境運作時可使用。停止後重新啟動會重置測試資料，並從全新狀態開始。本示範不連接 Stripe 或其他付款網路；現金與收據均為模擬資料。</p>
<p>アプリ内価格は参考データです。現行の実店舗価格、営業時間、酒類方針、写真利用権は未確認です。</p>
<form method="post" action="/__demo/logout"><button>ログアウト / 登出</button></form></main>`);
}

function cookie(name, value, maxAge) {
  return `${name}=${value}; Path=/; Max-Age=${maxAge}; Secure; HttpOnly; SameSite=Lax`;
}

function responseWithCookies(body, cookies, status = body.status) {
  const response = body;
  const headers = new Headers(response.headers);
  for (const value of cookies) headers.append("set-cookie", value);
  return new Response(response.body, { status, headers });
}

async function handleRequest({ request, env }) {
  if (!configured(env)) return offline();
  const url = new URL(request.url);
  if (url.pathname === "/__demo/login") {
    if (request.method === "GET") {
      const csrf = btoa(String.fromCharCode(...crypto.getRandomValues(new Uint8Array(24)))).replace(/=+$/g, "");
      return responseWithCookies(loginPage(false, csrf), [`${CSRF_COOKIE}=${csrf}; Path=/; Max-Age=600; Secure; SameSite=Lax`]);
    }
    if (request.method !== "POST" || !sameOrigin(request)) return htmlResponse("<h1>不正な要求 / 無效要求</h1>", 403);
    const body = await readBoundedBody(request, 16 * 1024);
    if (body.overflow) return htmlResponse("<h1>要求が大きすぎます / 要求內容過大</h1>", 413);
    const formHeaders = new Headers(request.headers);
    formHeaders.delete("content-length");
    const formRequest = new Request(request.url, { method: "POST", headers: formHeaders, body: body.bytes });
    const form = await formRequest.formData().catch(() => null);
    const csrfCookies = cookieValues(request.headers.get("cookie"), CSRF_COOKIE);
    const csrf = String(form?.get("csrf") || "");
    const validCSRF = !!csrf && csrfCookies.length === 1 && await equalBytes(csrf, csrfCookies[0] || "");
    const passcode = String(form?.get("passcode") || "");
    const passcodeKey = await signingKey(env.DEMO_COOKIE_KEY, ["sign", "verify"]);
    const configuredPasscodeSignature = await signedValue(passcodeKey, env.DEMO_PASSCODE);
    const validPasscode = await crypto.subtle.verify("HMAC", passcodeKey, configuredPasscodeSignature, new TextEncoder().encode(passcode));
    if (!validCSRF || !validPasscode) {
      await new Promise((resolve) => setTimeout(resolve, 350));
      const newCsrf = btoa(String.fromCharCode(...crypto.getRandomValues(new Uint8Array(24)))).replace(/=+$/g, "");
      return responseWithCookies(loginPage(true, newCsrf), [`${CSRF_COOKIE}=${newCsrf}; Path=/; Max-Age=600; Secure; SameSite=Lax`], 401);
    }
    const expiry = Math.floor(Date.now() / 1000) + TTL_SECONDS;
    const payload = String(expiry);
    const key = await signingKey(env.DEMO_COOKIE_KEY, ["sign"]);
    const signature = btoa(String.fromCharCode(...await signedValue(key, payload))).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/g, "");
    const token = `${payload}.${signature}`;
    return responseWithCookies(new Response(null, { status: 303, headers: { location: "/__demo/guide", "cache-control": "no-store" } }), [cookie(COOKIE, token, TTL_SECONDS), `${CSRF_COOKIE}=; Path=/; Max-Age=0; Secure; SameSite=Lax`]);
  }
  if (url.pathname === "/__demo/logout") {
    if (request.method !== "POST" || !sameOrigin(request)) return htmlResponse("<h1>不正な要求 / 無效要求</h1>", 403);
    return responseWithCookies(new Response(null, { status: 303, headers: { location: "/__demo/login", "cache-control": "no-store" } }), [`${COOKIE}=; Path=/; Max-Age=0; Secure; HttpOnly; SameSite=Lax`]);
  }
  if (url.pathname === "/__demo/guide") {
    if (request.method !== "GET") return htmlResponse("<h1>Not found</h1>", 404);
    return await validSession(request, env) ? guidePage() : new Response(null, { status: 303, headers: { location: "/__demo/login", "cache-control": "no-store" } });
  }
  if (!await validSession(request, env)) return new Response(null, { status: 303, headers: { location: "/__demo/login", "cache-control": "no-store" } });
  if (/^\/(?:test|functions)(?:\/|$)/.test(url.pathname) || /^\/(?:package\.json|wrangler\.toml|_headers|_routes\.json)$/.test(url.pathname)) return new Response("Not found", { status: 404 });
  const headers = new Headers(request.headers);
  headers.delete("x-tojima-demo-gate");
  headers.delete("host");
  const forwardedCookies = (headers.get("cookie") || "").split(";").map((part) => part.trim()).filter((part) => {
    const at = part.indexOf("=");
    const name = at < 0 ? "" : part.slice(0, at).trim();
    return name && name !== COOKIE && name !== CSRF_COOKIE;
  }).join("; ");
  if (forwardedCookies) headers.set("cookie", forwardedCookies);
  else headers.delete("cookie");
  headers.set("x-tojima-demo-gate", env.DEMO_GATE_SECRET);
  const upstreamURL = `${env.DEMO_ORIGIN}${url.pathname}${url.search}`;
  let upstream;
  try {
    upstream = await fetch(new Request(upstreamURL, { method: request.method, headers, body: ["GET", "HEAD"].includes(request.method) ? undefined : request.body, redirect: "manual", duplex: "half" }));
  } catch {
    return offline();
  }
  // A stopped Quick Tunnel commonly returns an HTTP gateway error rather than
  // rejecting fetch. Keep provider diagnostics out of the owner-facing page.
  if (upstream.status >= 500) {
    if (upstream.body) await upstream.body.cancel();
    return offline();
  }
  const outHeaders = new Headers();
  for (const [name, value] of upstream.headers) if (name.toLowerCase() !== "set-cookie") outHeaders.append(name, value);
  const setCookies = readSetCookies(upstream.headers);
  for (const value of setCookies) {
    const name = value.slice(0, value.indexOf("=")).trim();
    if (name !== COOKIE && name !== CSRF_COOKIE) outHeaders.append("set-cookie", value);
  }
  if ([301, 302, 303, 307, 308].includes(upstream.status)) {
    const location = outHeaders.get("location");
    if (location) {
      try {
        const target = new URL(location, env.DEMO_ORIGIN);
        if (target.origin === env.DEMO_ORIGIN) outHeaders.set("location", `${target.pathname}${target.search}${target.hash}`);
      } catch { outHeaders.delete("location"); }
    }
  }
  return new Response(upstream.body, { status: upstream.status, headers: outHeaders });
}

async function readBoundedBody(request, limit) {
  const declaredLength = Number(request.headers.get("content-length"));
  if (Number.isFinite(declaredLength) && declaredLength > limit) return { overflow: true };
  if (!request.body) return { bytes: new Uint8Array(), overflow: false };
  const reader = request.body.getReader();
  const chunks = [];
  let size = 0;
  while (true) {
    const { done, value } = await reader.read();
    if (done) break;
    size += value.byteLength;
    if (size > limit) {
      await reader.cancel();
      return { overflow: true };
    }
    chunks.push(value);
  }
  const bytes = new Uint8Array(size);
  let offset = 0;
  for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.byteLength; }
  return { bytes, overflow: false };
}

function readSetCookies(headers) {
  if (typeof headers.getSetCookie === "function") {
    const values = headers.getSetCookie();
    if (values.length) return values;
  }
  if (typeof headers.getAll === "function") {
    try {
      const values = headers.getAll("Set-Cookie");
      if (values.length) return values;
    } catch { /* use the combined-header fallback below */ }
  }
  const combined = headers.get("Set-Cookie");
  return combined ? combined.split(/,(?=\s*[^;,=\s]+=[^;,]*)/g).map((value) => value.trim()) : [];
}

export async function onRequest(context) {
  try {
    const response = await handleRequest(context);
    const headers = new Headers(response.headers);
    headers.set("cache-control", "no-store");
    headers.set("x-robots-tag", "noindex, nofollow");
    headers.set("referrer-policy", "no-referrer");
    headers.set("x-content-type-options", "nosniff");
    headers.set("x-frame-options", "DENY");
    return new Response(response.body, { status: response.status, statusText: response.statusText, headers });
  } catch {
    return offline();
  }
}
