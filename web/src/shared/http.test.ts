import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiError, cartClient } from '../api/client';
import { operationsClient } from '../api/operations';

const jsonResponse = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });

afterEach(() => {
  vi.unstubAllGlobals();
  vi.unstubAllEnvs();
});

describe('shared HTTP core', () => {
  it('prefixes guest cart requests with the configured API base URL', async () => {
    vi.stubEnv('VITE_API_BASE_URL', 'https://api.example.test/');
    const fetch = vi.fn().mockResolvedValue(jsonResponse({ open: true }));
    vi.stubGlobal('fetch', fetch);

    await cartClient.readCart('session-1', 'guest-token');

    expect(fetch.mock.calls[0]?.[0]).toBe('https://api.example.test/v1/guest/sessions/session-1/cart');
    const init = fetch.mock.calls[0]?.[1] as RequestInit;
    expect(new Headers(init.headers).get('accept')).toBe('application/json');
    expect(new Headers(init.headers).get('authorization')).toBe('Bearer guest-token');
  });

  it('prefixes operations requests with the configured API base URL', async () => {
    vi.stubEnv('VITE_API_BASE_URL', 'https://api.example.test/');
    const fetch = vi.fn().mockResolvedValue(jsonResponse({ can_cash: true, can_serve: true, can_kitchen: true, can_manage: true }));
    vi.stubGlobal('fetch', fetch);

    await operationsClient.access({ tenantID: 'tenant-1', storeID: 'store-1' }, 'staff-token');

    expect(fetch.mock.calls[0]?.[0]).toBe('https://api.example.test/v1/staff/tenants/tenant-1/stores/store-1/operations/access');
    const init = fetch.mock.calls[0]?.[1] as RequestInit;
    expect(new Headers(init.headers).get('accept')).toBe('application/json');
    expect(new Headers(init.headers).get('authorization')).toBe('Bearer staff-token');
  });

  it('prefixes guest order-status requests, which share the operations client, with the configured API base URL', async () => {
    vi.stubEnv('VITE_API_BASE_URL', 'https://api.example.test');
    const fetch = vi.fn().mockResolvedValue(jsonResponse({ orders: [], cash_enabled: true }));
    vi.stubGlobal('fetch', fetch);

    await operationsClient.guest('session/1', 'guest-token');

    expect(fetch.mock.calls[0]?.[0]).toBe('https://api.example.test/v1/guest/sessions/session%2F1/operations');
    expect(new Headers((fetch.mock.calls[0]?.[1] as RequestInit).headers).get('authorization')).toBe('Bearer guest-token');
  });

  it('keeps same-origin paths when no API base URL is configured', async () => {
    vi.stubEnv('VITE_API_BASE_URL', '');
    const fetch = vi.fn().mockResolvedValue(jsonResponse({ open: true }));
    vi.stubGlobal('fetch', fetch);

    await cartClient.status('table-1');

    expect(fetch.mock.calls[0]?.[0]).toBe('/v1/guest/tables/table-1');
  });

  it('does not expose the URL in network failure messages', async () => {
    vi.stubEnv('VITE_API_BASE_URL', 'https://private.example.test');
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('socket failed')));

    await expect(cartClient.status('sensitive-locator')).rejects.toMatchObject({ message: 'network_unavailable' });
  });

  it('throws the re-exported ApiError with a parsed response body', async () => {
    vi.stubGlobal('fetch', vi.fn().mockImplementation(() => Promise.resolve(jsonResponse({ error: 'denied', detail: 'closed' }, 403))));

    await expect(cartClient.status('table-1')).rejects.toBeInstanceOf(ApiError);
    await expect(cartClient.status('table-1')).rejects.toMatchObject({
      name: 'ApiError', status: 403, message: 'denied', body: { error: 'denied', detail: 'closed' },
    });
  });

  it.each([
    { status: 200, expectedBody: {}, expectedError: undefined },
    { status: 502, expectedBody: {}, expectedError: 'Request failed (502)' },
  ])('uses an empty object when a $status response body is not JSON', async ({ status, expectedBody, expectedError }) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('not json', { status })));

    if (expectedError) {
      await expect(cartClient.status('table-1')).rejects.toMatchObject({
        name: 'ApiError', status, body: expectedBody, message: expectedError,
      });
    } else {
      await expect(cartClient.status('table-1')).resolves.toEqual(expectedBody);
    }
  });
});
