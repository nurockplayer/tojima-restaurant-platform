import { afterEach, describe, expect, it, vi } from 'vitest';
import { amountJPY, ApiError, cartClient, newOperationKey } from './client';
import type { Cart } from './types';

const cart: Cart = { session_id: 'session/one', generation: 4, version: 7, lines: [] };
const response = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });

afterEach(() => vi.unstubAllGlobals());

describe('guest cart API', () => {
  it('sends the current generation, version, bearer grant and stable idempotency key for an add', async () => {
    const fetch = vi.fn().mockResolvedValue(response({ ...cart, version: 8 }));
    vi.stubGlobal('fetch', fetch);
    const key = newOperationKey();

    await cartClient.addLine('session/one', 'join-secret', cart, 'meal', 2, [{ group_key: 'size', option_key: 'large' }], key);

    expect(fetch).toHaveBeenCalledOnce();
    const [url, init] = fetch.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/v1/guest/sessions/session%2Fone/cart/lines');
    expect(new Headers(init.headers).get('authorization')).toBe('Bearer join-secret');
    expect(new Headers(init.headers).get('Idempotency-Key')).toBe(key);
    expect(JSON.parse(String(init.body))).toEqual({ expected_generation: 4, expected_version: 7, product_key: 'meal', quantity: 2, options: [{ group_key: 'size', option_key: 'large' }] });
    expect(url).not.toContain('join-secret');
    expect(String(init.body)).not.toContain('join-secret');
  });

  it('returns the server latest cart on a conflict without converting it into a mutation retry', async () => {
    const latest = { ...cart, version: 8, lines: [{ line_id: 'line-1', product_key: 'meal', product_name: 'Meal', product_available: true, quantity: 1, options: [] }] };
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response({ error: 'cart_conflict', cart: latest }, 409)));

    await expect(cartClient.removeLine('session/one', 'join-secret', cart, 'line-1', 'same-key'))
      .rejects.toMatchObject({ status: 409, body: { error: 'cart_conflict', cart: latest } } satisfies Partial<ApiError>);
  });

  it('keeps large JPY amounts exact and rejects unsafe numeric JSON values', () => {
    expect(amountJPY('900719925474099312345')).toBe('¥900,719,925,474,099,312,345');
    expect(amountJPY(9007199254740992)).toBe('—');
    expect(amountJPY(1200)).toBe('¥1,200');
  });

  it('normalizes products whose wire contract omits option_groups', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response({
      currency: 'JPY', price_mode: 'TAX_INCLUSIVE',
      categories: [{ key: 'food', name: '食事', position: 0, products: [{
        key: 'meal', name: '定食', effective_price_minor: '960', position: 0,
        availability: 'AVAILABLE', selectable: true,
      }] }],
    })));

    const menu = await cartClient.menuForTable('table-1');

    expect(menu.categories[0]?.products[0]?.option_groups).toEqual([]);
  });

  it('lets the caller retry the exact mutation payload with the original idempotency key', async () => {
    const first = newOperationKey();
    const fetch = vi.fn().mockRejectedValueOnce(new TypeError('connection closed')).mockResolvedValueOnce(response(cart));
    vi.stubGlobal('fetch', fetch);
    const edit = () => cartClient.addLine('session/one', 'join-secret', cart, 'meal', 2, [], first);

    await expect(edit()).rejects.toThrow('network_unavailable');
    await edit();

    const calls = fetch.mock.calls as Array<[string, RequestInit]>;
    const firstCall = calls[0]!;
    const retryCall = calls[1]!;
    expect(firstCall[0]).toBe(retryCall[0]);
    expect(firstCall[1]?.body).toBe(retryCall[1]?.body);
    expect(new Headers(firstCall[1]?.headers).get('Idempotency-Key')).toBe(first);
    expect(new Headers(retryCall[1]?.headers).get('Idempotency-Key')).toBe(first);
  });
});
