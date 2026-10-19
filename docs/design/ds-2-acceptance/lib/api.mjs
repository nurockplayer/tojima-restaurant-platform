// Server-side helpers for the acceptance harness: seed / reset the shared table cart and accept orders as staff.
// Used so state preparation never depends on the (changing) guest UI. Tokens are kept in memory only and never printed.
import { randomUUID } from 'node:crypto';

export function makeApi(creds) {
  const base = creds.base_url;
  const j = async (path, init = {}) => {
    const res = await fetch(base + path, init);
    const text = await res.text();
    let body; try { body = JSON.parse(text); } catch { body = text; }
    return { status: res.status, body };
  };
  const guestAuth = (token, key) => ({ authorization: `Bearer ${token}`, ...(key ? { 'Idempotency-Key': key } : {}) });
  const staffCtx = `/v1/staff/tenants/${creds.tenant_id}/stores/${creds.store_id}`;
  const staffAuth = (role = 'cashier') => ({ authorization: `Bearer ${creds.credentials[role].token}` });
  const locator = creds.table_locator;

  const api = {
    base, locator,
    async menu() {
      const r = await j(`/v1/guest/tables/${encodeURIComponent(locator)}/menu?amounts=string`);
      if (r.status !== 200) throw new Error('menu ' + r.status);
      return r.body;
    },
    /** Classify the demo menu so flows do not hard-code product names. */
    async products() {
      const menu = await api.menu();
      const all = [];
      for (const c of menu.categories) for (const p of c.products) all.push({ category: c.name, key: p.key, name: p.name, selectable: p.selectable, groups: p.option_groups ?? [] });
      const plain = all.filter((p) => p.selectable && p.groups.length === 0);
      const required = all.find((p) => p.selectable && p.groups.some((g) => g.min_select > 0));
      return { all, plain, required, categories: menu.categories.map((c) => ({ name: c.name, count: c.products.length })) };
    },
    async join() {
      const st = await j(`/v1/guest/tables/${encodeURIComponent(locator)}`);
      const expected = st.body?.session?.id ?? st.body?.session_id ?? '';
      const r = await j(`/v1/guest/tables/${encodeURIComponent(locator)}/join`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ expected_session_id: expected }) });
      if (r.status !== 200) throw new Error('join ' + r.status);
      return { sessionID: r.body.session.id, token: r.body.table_join_token };
    },
    async cart() {
      const g = await api.join();
      const r = await j(`/v1/guest/sessions/${encodeURIComponent(g.sessionID)}/cart`, { headers: guestAuth(g.token) });
      return { ...g, cart: r.body, status: r.status };
    },
    async cartLineCount() { const { cart } = await api.cart(); return cart.lines?.length ?? 0; },
    async resetCart() {
      for (let i = 0; i < 30; i++) {
        const { sessionID, token, cart } = await api.cart();
        if (!cart.lines || cart.lines.length === 0) return { sessionID, cleared: true, generation: cart.generation, version: cart.version };
        const line = cart.lines[0];
        await j(`/v1/guest/sessions/${encodeURIComponent(sessionID)}/cart/lines/${encodeURIComponent(line.line_id)}?generation=${cart.generation}&version=${cart.version}`, { method: 'DELETE', headers: guestAuth(token, randomUUID()) });
      }
      throw new Error('could not empty the cart');
    },
    async addLine(productKey, quantity = 1, options = []) {
      const { sessionID, token, cart } = await api.cart();
      const r = await j(`/v1/guest/sessions/${encodeURIComponent(sessionID)}/cart/lines`, { method: 'POST', headers: { ...guestAuth(token, randomUUID()), 'content-type': 'application/json' }, body: JSON.stringify({ expected_generation: cart.generation, expected_version: cart.version, product_key: productKey, quantity, options }) });
      return r;
    },
    async removeLine(lineID) {
      const { sessionID, token, cart } = await api.cart();
      return j(`/v1/guest/sessions/${encodeURIComponent(sessionID)}/cart/lines/${encodeURIComponent(lineID)}?generation=${cart.generation}&version=${cart.version}`, { method: 'DELETE', headers: guestAuth(token, randomUUID()) });
    },
    /** Simulate another guest changing the shared cart: add one plain line, then remove it again (restores line count). */
    async touchCart(plainKey) {
      const before = await api.cart();
      const add = await api.addLine(plainKey, 1, []);
      const after = await api.cart();
      const added = (after.cart.lines || []).find((l) => !(before.cart.lines || []).some((b) => b.line_id === l.line_id));
      return { add: add.status, addedLine: added?.line_id ?? null };
    },

    // ----- staff -----
    async staffTables() { return (await j(`${staffCtx}/operations/tables`, { headers: staffAuth() })).body.tables ?? []; },
    async sessionOrders() {
      const table = (await api.staffTables()).find((t) => t.table_id === creds.table_id) ?? (await api.staffTables()).find((t) => t.session_id);
      if (!table?.session_id) return { table: null, orders: [] };
      const r = await j(`${staffCtx}/tables/${encodeURIComponent(table.table_id)}/sessions/${encodeURIComponent(table.session_id)}/operations`, { headers: staffAuth() });
      return { table, orders: r.body.orders ?? [] };
    },
    /** Accept one order (by id) or, when orderID is omitted, every order that still waits for acceptance. Returns the accepted ids. */
    async acceptOrders(orderID) {
      const accepted = [];
      const { table, orders } = await api.sessionOrders();
      if (!table) return accepted;
      for (const o of orders) {
        if (orderID && o.id !== orderID) continue;
        if (!(o.requires_staff_acceptance && !o.staff_accepted)) continue;
        const r = await j(`${staffCtx}/tables/${encodeURIComponent(table.table_id)}/sessions/${encodeURIComponent(table.session_id)}/orders/${encodeURIComponent(o.id)}/accept`, {
          method: 'POST', headers: { ...staffAuth(), 'content-type': 'application/json', 'Idempotency-Key': randomUUID() }, body: JSON.stringify({ expected_version: o.version }),
        });
        accepted.push({ id: o.id, status: r.status });
      }
      return accepted;
    },
    async assetList() {
      const html = await (await fetch(base + '/')).text();
      return [...html.matchAll(/(?:src|href)="([^"]+\.(?:js|css))"/g)].map((m) => m[1]);
    },
  };
  return api;
}
