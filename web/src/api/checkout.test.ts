import { afterEach, describe, expect, it, vi } from 'vitest';
import { checkoutClient } from './checkout';
import { setCookieSessionUnauthorizedHandler } from './operations';

afterEach(() => {
  vi.unstubAllGlobals();
  vi.unstubAllEnvs();
});

describe('checkout proof browser API', () => {
  it('coalesces same-tab context bootstrap and sends only the bearer proof request', async () => {
    let finish!: (response: Response) => void;
    const fetchMock = vi.fn(() => new Promise<Response>((resolve) => { finish = resolve; }));
    vi.stubGlobal('fetch', fetchMock);

    const first = checkoutClient.ensureContext('session/1', 'table-join-secret');
    const second = checkoutClient.ensureContext('session/1', 'table-join-secret');
    await Promise.resolve();
    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe('/v1/guest/sessions/session%2F1/checkout-context');
    expect(init.method).toBe('POST');
    expect(init.credentials).toBe('same-origin');
    expect(init.cache).toBe('no-store');
    expect(init.referrerPolicy).toBe('no-referrer');
    expect(new Headers(init.headers).get('authorization')).toBe('Bearer table-join-secret');
    expect(init.body).toBeUndefined();

    finish(new Response(null, { status: 204 }));
    await Promise.all([first, second]);
  });

  it('serializes bootstrap for different sessions because the proof cookie is origin-wide', async () => {
    let finishFirst!: (response: Response) => void;
    const fetchMock = vi.fn()
      .mockImplementationOnce(() => new Promise<Response>((resolve) => { finishFirst = resolve; }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }));
    vi.stubGlobal('fetch', fetchMock);

    const first = checkoutClient.ensureContext('session-a', 'grant-a');
    const second = checkoutClient.ensureContext('session-b', 'grant-b');
    await Promise.resolve();
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(String(fetchMock.mock.calls[0]?.[0])).toContain('/session-a/');

    finishFirst(new Response(null, { status: 204 }));
    await Promise.all([first, second]);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(String(fetchMock.mock.calls[1]?.[0])).toContain('/session-b/');
  });

  it('reads safe status with table grant and resumes only through the explicit cookie request', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(json({ order_id: 'order-1', state: 'UNKNOWN', last_reconciled_at: null, payment_review_required: true, resume_available: false }))
      .mockResolvedValueOnce(json({ url: 'https://checkout.example.test/session-private' }));
    vi.stubGlobal('fetch', fetchMock);

    const status = await checkoutClient.status('session-1', 'table-join-secret', 'order-1');
    expect(status).toEqual({ order_id: 'order-1', state: 'UNKNOWN', last_reconciled_at: null, payment_review_required: true, resume_available: false });
    const [statusURL, statusInit] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(statusURL).toBe('/v1/guest/sessions/session-1/orders/order-1/checkout');
    expect(new Headers(statusInit.headers).get('authorization')).toBe('Bearer table-join-secret');

    const response = await checkoutClient.resume('session-1', 'order-1');
    expect(response.url).toBe('https://checkout.example.test/session-private');
    const [resumeURL, resumeInit] = fetchMock.mock.calls[1] as unknown as [string, RequestInit];
    expect(resumeURL).toBe('/v1/guest/sessions/session-1/orders/order-1/checkout/resume');
    expect(resumeInit.method).toBe('POST');
    expect(resumeInit.credentials).toBe('same-origin');
    expect(resumeInit.referrerPolicy).toBe('no-referrer');
    expect(new Headers(resumeInit.headers).has('authorization')).toBe(false);
  });

  it('uses the staff session payment paths and bearer token for status and reconcile', async () => {
    const safe = { order_id: 'order-1', state: 'PAID', last_reconciled_at: '2026-10-18T12:00:00Z', payment_review_required: false, resume_available: false };
    const fetchMock = vi.fn().mockResolvedValue(json(safe));
    vi.stubGlobal('fetch', fetchMock);
    const context = { tenantID: 'tenant-1', storeID: 'store-1' };

    await checkoutClient.staffStatus(context, 'staff-secret', 'table-1', 'session-1', 'order-1');
    await checkoutClient.reconcile(context, 'staff-secret', 'table-1', 'session-1', 'order-1');
    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      '/v1/staff/tenants/tenant-1/stores/store-1/tables/table-1/sessions/session-1/orders/order-1/payment',
      '/v1/staff/tenants/tenant-1/stores/store-1/tables/table-1/sessions/session-1/orders/order-1/payment/reconcile',
    ]);
    for (const [, init] of fetchMock.mock.calls as unknown as Array<[string, RequestInit]>) {
      expect(new Headers(init.headers).get('authorization')).toBe('Bearer staff-secret');
      expect(init.credentials).toBe('same-origin');
      expect(init.referrerPolicy).toBe('no-referrer');
    }
  });

  it('keeps cookie-authenticated staff payment calls same-origin and reports a 401', async () => {
    vi.stubEnv('VITE_API_BASE_URL', 'https://api.example.test/gateway/api/');
    const fetchMock = vi.fn().mockResolvedValue(json({ error: 'unauthenticated' }, 401));
    vi.stubGlobal('fetch', fetchMock);
    let expired = false;
    const unregister = setCookieSessionUnauthorizedHandler(() => { expired = true; });
    try {
      await expect(checkoutClient.staffStatus({ tenantID: 'tenant-1', storeID: 'store-1' }, '', 'table-1', 'session-1', 'order-1')).rejects.toMatchObject({ status: 401 });
      expect(fetchMock.mock.calls[0]?.[0]).toBe('/v1/staff/tenants/tenant-1/stores/store-1/tables/table-1/sessions/session-1/orders/order-1/payment');
      const init = fetchMock.mock.calls[0]?.[1] as RequestInit;
      expect(new Headers(init.headers).get('authorization')).toBeNull();
      expect(init.credentials).toBe('same-origin');
      expect(expired).toBe(true);
    } finally { unregister(); }
  });

  it('uses the configured same-origin API prefix for guest proof and staff calls', async () => {
    vi.stubEnv('VITE_API_BASE_URL', '/api');
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
      .mockResolvedValue(json({ order_id: 'order-1', state: 'OPEN', payment_review_required: false, resume_available: true }));
    vi.stubGlobal('fetch', fetchMock);

    await checkoutClient.ensureContext('session-1', 'join-secret');
    await checkoutClient.status('session-1', 'join-secret', 'order-1');
    await checkoutClient.resume('session-1', 'order-1');
    await checkoutClient.staffStatus({ tenantID: 'tenant-1', storeID: 'store-1' }, 'staff-secret', 'table-1', 'session-1', 'order-1');
    await checkoutClient.reconcile({ tenantID: 'tenant-1', storeID: 'store-1' }, 'staff-secret', 'table-1', 'session-1', 'order-1');

    expect(fetchMock.mock.calls.map(([url]) => String(url))).toEqual([
      'http://localhost/api/v1/guest/sessions/session-1/checkout-context',
      'http://localhost/api/v1/guest/sessions/session-1/orders/order-1/checkout',
      'http://localhost/api/v1/guest/sessions/session-1/orders/order-1/checkout/resume',
      'http://localhost/api/v1/staff/tenants/tenant-1/stores/store-1/tables/table-1/sessions/session-1/orders/order-1/payment',
      'http://localhost/api/v1/staff/tenants/tenant-1/stores/store-1/tables/table-1/sessions/session-1/orders/order-1/payment/reconcile',
    ]);
    const [contextInit, statusInit, resumeInit] = fetchMock.mock.calls.slice(0, 3).map(([, init]) => init as RequestInit);
    expect(new Headers(contextInit?.headers).get('authorization')).toBe('Bearer join-secret');
    expect(contextInit?.credentials).toBe('same-origin');
    expect(statusInit?.credentials).toBe('same-origin');
    expect(resumeInit?.credentials).toBe('same-origin');
    expect(new Headers(resumeInit?.headers).has('authorization')).toBe(false);
  });

  it('preserves an external staff API base path when composing payment routes', async () => {
    vi.stubEnv('VITE_API_BASE_URL', 'https://api.example.test/gateway/api/');
    const fetchMock = vi.fn().mockResolvedValue(json({ order_id: 'order-1', state: 'PAID', payment_review_required: false, resume_available: false }));
    vi.stubGlobal('fetch', fetchMock);

    await checkoutClient.staffStatus({ tenantID: 'tenant-1', storeID: 'store-1' }, 'staff-secret', 'table-1', 'session-1', 'order-1');
    expect(fetchMock.mock.calls[0]?.[0]).toBe('https://api.example.test/gateway/api/v1/staff/tenants/tenant-1/stores/store-1/tables/table-1/sessions/session-1/orders/order-1/payment');
  });

  it('keeps a double-leading-slash path on its configured same origin', async () => {
    vi.stubEnv('VITE_API_BASE_URL', 'http://localhost//foreign.example.test/api');
    const fetchMock = vi.fn().mockResolvedValue(json({ order_id: 'order-1', state: 'OPEN', payment_review_required: false, resume_available: false }));
    vi.stubGlobal('fetch', fetchMock);

    await checkoutClient.status('session-1', 'join-secret', 'order-1');
    expect(fetchMock.mock.calls[0]?.[0]).toBe('http://localhost//foreign.example.test/api/v1/guest/sessions/session-1/orders/order-1/checkout');
  });

  it.each([
    'https://other.example.test/api',
    '//other.example.test/api',
    '/\\other.example.test/api',
  ])('rejects cross-origin or unsupported guest API base %s before forwarding credentials', async (base) => {
    vi.stubEnv('VITE_API_BASE_URL', base);
    const fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);

    await expect(checkoutClient.ensureContext('session-1', 'join-secret')).rejects.toThrow('checkout_context_unavailable');
    await expect(checkoutClient.status('session-1', 'join-secret', 'order-1')).rejects.toThrow('checkout_context_unavailable');
    await expect(checkoutClient.resume('session-1', 'order-1')).rejects.toThrow('checkout_context_unavailable');
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });
}
