// @vitest-environment jsdom
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { waitFor, within } from '@testing-library/dom';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { StaffAuthState } from '../api/staff-auth';
import { StaffApp } from './StaffApp';

let root: Root | undefined;
let host: HTMLDivElement | undefined;
const ctx = { tenantID: 'tenant-1', storeID: 'store-1' };
const signedIn: StaffAuthState = { kind: 'signed-in', subjectID: 'staff-1', mfa: true, stepUpFresh: true, expiresAt: '2026-10-27T00:00:00Z' };

beforeEach(() => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
  window.history.replaceState({}, '', '/kitchen?shift=morning');
  host = document.createElement('div'); document.body.append(host); root = createRoot(host);
});

afterEach(async () => {
  if (root) await act(async () => root?.unmount());
  root = undefined; host?.remove(); host = undefined;
  document.querySelectorAll('form[action="/staff/auth/logout"]').forEach((form) => form.remove());
  sessionStorage.clear(); vi.restoreAllMocks(); vi.unstubAllGlobals();
});

async function render(auth: StaffAuthState, route: (url: string, init: RequestInit) => Promise<Response>, initialView: 'tables' | 'kds' = 'tables') {
  vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL, init: RequestInit = {}) => route(String(input), init)));
  await act(async () => { root?.render(<StaffApp staffAuth={auth} initialView={initialView} />); await new Promise((resolve) => setTimeout(resolve, 0)); });
}

async function click(label: string) {
  const button = [...host!.querySelectorAll('button')].find((item) => item.textContent?.includes(label));
  if (!button) throw new Error(`Missing button ${label}`);
  await act(async () => { button.click(); await new Promise((resolve) => setTimeout(resolve, 0)); });
}

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });

describe('staff cookie authentication UI', () => {
  it('shows a sign-in link with encoded return path and no token field when signed out', async () => {
    await render({ kind: 'signed-out' }, async () => json({ error: 'unexpected' }));
    const link = within(host!).getByRole('link', { name: 'ログイン' }) as HTMLAnchorElement;
    expect(link.getAttribute('href')).toBe('/staff/auth/login?return=%2Fkitchen%3Fshift%3Dmorning');
    expect(host!.querySelector('input[type="password"]')).toBeNull();
  });

  it('fails closed in unavailable mode and lets the operator retry the session probe', async () => {
    const calls: string[] = [];
    sessionStorage.setItem('tojima.staff.access-token', 'stale-test-token');
    await render({ kind: 'unavailable' }, async (url) => { calls.push(url); return json({ error: 'temporary' }, 503); });
    expect(host!.querySelector('input[type="password"]')).toBeNull();
    expect(host!.textContent).toContain('ログイン状態を確認できません。しばらくしてから再試行してください。');
    expect(sessionStorage.getItem('tojima.staff.access-token')).toBeNull();
    await click('再試行');
    await waitFor(() => expect(calls).toContain('/staff/auth/session'));
    expect(host!.querySelector('input[type="password"]')).toBeNull();
  });

  it('connects with the cookie session and sends no authorization header', async () => {
    const calls: Array<{ url: string; headers: Headers }> = [];
    await render(signedIn, async (url, init) => {
      calls.push({ url, headers: new Headers(init.headers) });
      if (url.endsWith('/operations/access')) return json({ can_cash: true, can_serve: false, can_kitchen: false, can_manage: false });
      if (url.endsWith('/operations/tables')) return json({ tables: [] });
      throw new Error(`Unexpected ${url}`);
    });
    await userEvent.type(within(host!).getByLabelText('企業ID'), 'tenant-1');
    await userEvent.type(within(host!).getByLabelText('店舗ID'), 'store-1');
    await click('接続');
    await waitFor(() => expect(calls.some((call) => call.url.endsWith('/operations/access'))).toBe(true));
    expect(calls.every((call) => call.headers.get('authorization') === null)).toBe(true);
    expect(host!.querySelector('input[type="password"]')).toBeNull();
  });

  it('offers login when the saved cookie session expires before access completes', async () => {
    sessionStorage.setItem('tojima.staff.context', JSON.stringify(ctx));
    await render(signedIn, async (url) => {
      if (url.endsWith('/operations/access')) return json({ error: 'unauthenticated' }, 401);
      if (url === '/staff/auth/session') return json({ error: 'unauthenticated' }, 401);
      throw new Error(`Unexpected ${url}`);
    });
    await waitFor(() => expect(within(host!).getByRole('link', { name: 'ログイン' })).toBeTruthy());
  });

  it('submits logout through the same-origin POST form after clearing local session state', async () => {
    sessionStorage.setItem('tojima.staff.context', JSON.stringify(ctx));
    const submit = vi.spyOn(HTMLFormElement.prototype, 'submit').mockImplementation(() => undefined);
    await render(signedIn, async (url) => {
      if (url.endsWith('/operations/access')) return json({ can_cash: false, can_serve: false, can_kitchen: false, can_manage: false });
      throw new Error(`Unexpected ${url}`);
    });
    await waitFor(() => expect(host!.querySelector('.welcome')).toBeTruthy());
    await click('ログアウト');
    const form = document.querySelector<HTMLFormElement>('form[action="/staff/auth/logout"]')!;
    expect(form.method).toBe('post'); expect(submit).toHaveBeenCalledOnce();
    expect(sessionStorage.getItem('tojima.staff.context')).toBeNull();
  });

  it('offers step-up after a write returns 403 and the session probe is not fresh', async () => {
    sessionStorage.setItem('tojima.staff.context', JSON.stringify(ctx));
    let sessionReads = 0;
    await render({ ...signedIn, stepUpFresh: false }, async (url) => {
      if (url === '/staff/auth/session') { sessionReads++; return json({ subject_id: 'staff-1', mfa: true, step_up_fresh: false, expires_at: '2026-10-27T00:00:00Z' }); }
      if (url.endsWith('/operations/access')) return json({ can_cash: false, can_serve: false, can_kitchen: true, can_manage: false });
      if (url.endsWith('/operations/kds')) return json({ orders: [{ id: 'order-1', fulfillment_status: 'ACCEPTED', created_at: '2026-10-27T00:00:00Z', table_name: 'T1', generation: 1, payment_status: 'PAID', payment_method: 'CASH', total_gross_jpy: '100', lines: [{ product_name: 'Ramen', quantity: 1, line_gross_jpy: '100', options: [] }] }] });
      if (url.endsWith('/fulfillment')) return json({ error: 'forbidden' }, 403);
      throw new Error(`Unexpected ${url}`);
    }, 'kds');
    await waitFor(() => expect(host!.querySelector('.staff-workspace')).toBeTruthy());
    const start = [...host!.querySelectorAll('button')].find((item) => item.textContent?.includes('調理開始'));
    expect(start).toBeTruthy();
    await act(async () => { start!.click(); await new Promise((resolve) => setTimeout(resolve, 0)); });
    await waitFor(() => expect(within(host!).getByRole('link', { name: '再認証' })).toBeTruthy());
    expect(host!.textContent).toContain('この操作には再認証が必要です。');
    expect(within(host!).getByRole('link', { name: '再認証' }).getAttribute('href')).toBe('/staff/auth/step-up?return=%2Fkitchen%3Fshift%3Dmorning');
    expect(sessionReads).toBe(1);
  });

  it('clears workspace data and preserves context when a cookie staff write returns 401', async () => {
    sessionStorage.setItem('tojima.staff.context', JSON.stringify(ctx));
    await render(signedIn, async (url) => {
      if (url.endsWith('/operations/access')) return json({ can_cash: false, can_serve: false, can_kitchen: true, can_manage: false });
      if (url.endsWith('/operations/kds')) return json({ orders: [{ id: 'order-1', fulfillment_status: 'ACCEPTED', created_at: '2026-10-27T00:00:00Z', table_name: 'T1', generation: 1, payment_status: 'PAID', payment_method: 'CASH', total_gross_jpy: '100', lines: [{ product_name: 'Ramen', quantity: 1, line_gross_jpy: '100', options: [] }] }] });
      if (url.endsWith('/fulfillment')) return json({ error: 'unauthenticated' }, 401);
      throw new Error(`Unexpected ${url}`);
    }, 'kds');
    await waitFor(() => expect(host!.querySelector('.staff-workspace')).toBeTruthy());
    const start = [...host!.querySelectorAll('button')].find((item) => item.textContent?.includes('調理開始'))!;
    await act(async () => { start.click(); await new Promise((resolve) => setTimeout(resolve, 0)); });
    await waitFor(() => expect(within(host!).getByRole('link', { name: 'ログイン' })).toBeTruthy());
    expect(host!.querySelector('.staff-workspace')).toBeNull();
    expect(host!.textContent).not.toContain('Ramen');
    expect(host!.textContent).not.toContain('T1');
    expect(sessionStorage.getItem('tojima.staff.context')).toBe(JSON.stringify(ctx));
    expect(sessionStorage.getItem('tojima.staff.access-token')).toBeNull();
  });

  it('clears workspace data when a 403 re-probe discovers a signed-out session', async () => {
    sessionStorage.setItem('tojima.staff.context', JSON.stringify(ctx));
    await render({ ...signedIn, stepUpFresh: false }, async (url) => {
      if (url === '/staff/auth/session') return json({ error: 'unauthenticated' }, 401);
      if (url.endsWith('/operations/access')) return json({ can_cash: false, can_serve: false, can_kitchen: true, can_manage: false });
      if (url.endsWith('/operations/kds')) return json({ orders: [{ id: 'order-1', fulfillment_status: 'ACCEPTED', created_at: '2026-10-27T00:00:00Z', table_name: 'T1', generation: 1, payment_status: 'PAID', payment_method: 'CASH', total_gross_jpy: '100', lines: [{ product_name: 'Ramen', quantity: 1, line_gross_jpy: '100', options: [] }] }] });
      if (url.endsWith('/fulfillment')) return json({ error: 'forbidden' }, 403);
      throw new Error(`Unexpected ${url}`);
    }, 'kds');
    await waitFor(() => expect(host!.querySelector('.staff-workspace')).toBeTruthy());
    const start = [...host!.querySelectorAll('button')].find((item) => item.textContent?.includes('調理開始'))!;
    await act(async () => { start.click(); await new Promise((resolve) => setTimeout(resolve, 0)); });
    await waitFor(() => expect(within(host!).getByRole('link', { name: 'ログイン' })).toBeTruthy());
    expect(host!.querySelector('.staff-workspace')).toBeNull();
    expect(host!.textContent).not.toContain('Ramen');
    expect(host!.textContent).not.toContain('T1');
    expect(sessionStorage.getItem('tojima.staff.context')).toBe(JSON.stringify(ctx));
  });

  it('keeps the existing token entry when auth is unsupported', async () => {
    await render({ kind: 'unsupported' }, async () => json({ error: 'unexpected' }));
    expect(host!.querySelector('input[type="password"]')).toBeTruthy();
    expect(host!.textContent).toContain('スタッフアクセストークン');
  });
});
