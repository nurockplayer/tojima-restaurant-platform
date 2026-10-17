import { afterEach, describe, expect, it, vi } from 'vitest';
import { catalogClient } from './catalog';

afterEach(() => vi.unstubAllGlobals());

describe('catalog wire normalization', () => {
  it('normalizes omitted option_groups and completes a no-options save, preview, and publish flow', async () => {
    const wireMenu = { categories: [{ key: 'food', name: '料理', position: 1, products: [{ key: 'miso-ramen', name: '味噌ラーメン', description: '', base_price_minor: '900', position: 1, active: true }] }] };
    const calls: Array<{ url: string; init?: RequestInit }> = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = init?.method ?? 'GET';
      calls.push({ url, init });
      if (url.endsWith('/menu/published?amounts=string')) return json({ revision: 4, ...wireMenu });
      if (url.endsWith('/stores/store-1/menu?amounts=string')) return json({ revision: 4, mode: 'LINKED', can_pause_availability: true, can_confirm_availability: false, can_edit_prices: false, can_detach: false, ...wireMenu });
      if (url.endsWith('/stores/store-1/menu/draft?amounts=string')) return json({ version: 7, ...wireMenu });
      if (url.endsWith('/menu/draft?amounts=string')) return json({ version: 7, ...wireMenu });
      if (url.endsWith('/menu/draft') && method === 'PUT') return json({ saved: true });
      if (url.endsWith('/menu/preview')) return json({ draft_version: 7, published_revision: 4, linked_store_count: 2, can_publish: true, issues: [], preview_fingerprint: 'no-options-checked' });
      if (url.endsWith('/menu/publish')) return json({ revision: 5, draft_version: 7 });
      return json({ error: 'unexpected_request' }, 404);
    });
    vi.stubGlobal('fetch', fetchMock);

    const published = await catalogClient.companyPublished('tenant-1', 'token');
    const draft = await catalogClient.companyDraft('tenant-1', 'token');
    const store = await catalogClient.storeMenu({ tenantID: 'tenant-1', storeID: 'store-1' }, 'token');
    const storeDraft = await catalogClient.storeDraft({ tenantID: 'tenant-1', storeID: 'store-1' }, 'token');

    expect(published.categories[0]!.products[0]!.option_groups).toEqual([]);
    expect(draft.categories[0]!.products[0]!.option_groups).toEqual([]);
    expect(store.categories[0]!.products[0]!.option_groups).toEqual([]);
    expect(storeDraft.categories[0]!.products[0]!.option_groups).toEqual([]);

    await catalogClient.saveCompanyDraft('tenant-1', 'token', draft);
    const saved = calls.find((call) => call.url.endsWith('/menu/draft') && call.init?.method === 'PUT');
    expect(JSON.parse(String(saved?.init?.body)).categories[0].products[0].option_groups).toEqual([]);

    const preview = await catalogClient.previewCompany('tenant-1', 'token', draft.version);
    expect(preview.can_publish).toBe(true);
    const publishedResult = await catalogClient.publishCompany('tenant-1', 'token', preview.draft_version, preview.preview_fingerprint);
    expect(publishedResult).toEqual({ revision: 5, draft_version: 7 });
    expect(fetchMock).toHaveBeenCalledTimes(7);
  });

  it('reads and changes emergency hold state with the version and idempotency key in the frozen body', async () => {
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => json({ product_key: 'meal/1', active: true, version: 4, reason: 'safety review', published_revision: 9, linked_store_count: 3 }));
    vi.stubGlobal('fetch', fetchMock);

    const state = await catalogClient.companyProductEmergencyHold('tenant/1', 'meal/1', 'admin-token');
    expect(state).toMatchObject({ active: true, version: 4, linked_store_count: 3 });
    expect(fetchMock.mock.calls[0]?.[0]).toBe('/v1/admin/tenants/tenant%2F1/menu/products/meal%2F1/emergency-hold');
    expect(new Headers(fetchMock.mock.calls[0]?.[1]?.headers).get('authorization')).toBe('Bearer admin-token');

    const change = { active: false, reason: 'review complete', expected_version: 4, idempotency_key: 'hold-off-key' };
    await catalogClient.setCompanyProductEmergencyHold('tenant/1', 'meal/1', 'admin-token', change);
    expect(fetchMock.mock.calls[1]?.[0]).toBe('/v1/admin/tenants/tenant%2F1/menu/products/meal%2F1/emergency-hold');
    expect(fetchMock.mock.calls[1]?.[1]?.method).toBe('PUT');
    expect(JSON.parse(String(fetchMock.mock.calls[1]?.[1]?.body))).toEqual(change);
    expect(new Headers(fetchMock.mock.calls[1]?.[1]?.headers).get('authorization')).toBe('Bearer admin-token');
  });
});

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });
}
