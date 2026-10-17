import { afterEach, describe, expect, it, vi } from 'vitest';
import { checkoutClient } from './checkout';

afterEach(() => {
  vi.unstubAllGlobals();
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
});

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });
}
