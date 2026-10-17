// @vitest-environment jsdom
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiError } from '../api/client';
import { catalogClient, type CompanyDraft, type StoreCatalog } from '../api/catalog';
import { CatalogApp } from './CatalogApp';

let root: Root | undefined;
let host: HTMLDivElement | undefined;

afterEach(async () => {
  if (root) await act(async () => root?.unmount());
  root = undefined; host?.remove(); host = undefined;
  sessionStorage.clear(); vi.restoreAllMocks(); vi.unstubAllGlobals();
});

describe('catalog admin UI', () => {
  it('does not display a delayed publication preview after the draft changes', async () => {
    const pending = deferred<Awaited<ReturnType<typeof catalogClient.previewCompany>>>();
    installContext();
    vi.spyOn(catalogClient, 'companyPublished').mockResolvedValue({ revision: 4, categories: draft().categories });
    vi.spyOn(catalogClient, 'companyDraft').mockResolvedValue(draft());
    vi.spyOn(catalogClient, 'storeMenu').mockRejectedValue(new ApiError(403, { error: 'forbidden' }));
    const preview = vi.spyOn(catalogClient, 'previewCompany').mockReturnValue(pending.promise);
    await mount();

    await click('公開前チェック');
    expect(preview).toHaveBeenCalledWith('tenant-1', 'catalog-token', 7);
    const description = [...host!.querySelectorAll('label')].find((label) => label.textContent?.includes('説明'))?.querySelector('textarea');
    expect(description).toBeTruthy();
    await changeValue(description!, '最新の説明');
    await act(async () => { pending.resolve({ draft_version: 7, published_revision: 4, linked_store_count: 2, can_publish: true, issues: [], preview_fingerprint: 'stale-fingerprint' }); await pending.promise; });

    expect(host?.querySelector('.catalog-preview')).toBeNull();
    const publish = button('確認した内容を公開');
    expect(publish.disabled).toBe(true);
  });

  it('uses server capabilities to expose staff pause without manager price, confirm, or detach actions', async () => {
    installContext();
    vi.spyOn(catalogClient, 'companyPublished').mockRejectedValue(new ApiError(403, { error: 'forbidden' }));
    vi.spyOn(catalogClient, 'companyDraft').mockRejectedValue(new ApiError(403, { error: 'forbidden' }));
    vi.spyOn(catalogClient, 'storeMenu').mockResolvedValue(storeMenu({ can_pause_availability: true, can_confirm_availability: false, can_edit_prices: false, can_detach: false }));
    await mount();

    expect(host?.textContent).toContain('定食');
    expect(host?.textContent).toContain('販売を一時停止');
    expect(host?.textContent).not.toContain('販売を確認');
    expect(host?.textContent).not.toContain('店舗価格を保存');
    expect(host?.textContent).not.toContain('専用メニューに切り替える');
  });

  it('shows tenant-admin actions from server capabilities and requires explicit detach confirmation', async () => {
    installContext();
    vi.spyOn(catalogClient, 'companyPublished').mockRejectedValue(new ApiError(403, { error: 'forbidden' }));
    vi.spyOn(catalogClient, 'companyDraft').mockRejectedValue(new ApiError(403, { error: 'forbidden' }));
    vi.spyOn(catalogClient, 'storeMenu').mockResolvedValue(storeMenu({ can_pause_availability: true, can_confirm_availability: true, can_edit_prices: true, can_detach: true }, 'PENDING_CONFIRMATION'));
    const detach = vi.spyOn(catalogClient, 'detachStore').mockResolvedValue({ revision: 5, draft_version: 0 });
    await mount();

    expect(host?.textContent).toContain('販売を確認');
    expect(host?.textContent).toContain('店舗価格を保存');
    await click('専用メニューに切り替える');
    expect(detach).not.toHaveBeenCalled();
    expect(host?.textContent).toContain('現在の公開メニューを複製します');
    await click('複製して切り替える');
    expect(detach).toHaveBeenCalledWith({ tenantID: 'tenant-1', storeID: 'store-1', token: 'catalog-token' }, 'catalog-token', 4);
  });

  it('keeps detached availability readable and pauseable for staff while omitting linked price controls', async () => {
    installContext();
    vi.spyOn(catalogClient, 'companyPublished').mockRejectedValue(new ApiError(403, { error: 'forbidden' }));
    vi.spyOn(catalogClient, 'companyDraft').mockRejectedValue(new ApiError(403, { error: 'forbidden' }));
    vi.spyOn(catalogClient, 'storeMenu').mockResolvedValue(storeMenu({ can_pause_availability: true, can_confirm_availability: false, can_edit_prices: false, can_detach: false }, 'AVAILABLE', 'DETACHED'));
    await mount();
    expect(host?.textContent).toContain('この店舗の専用メニュー');
    expect(host?.textContent).toContain('店舗メニューの基本価格');
    expect(host?.textContent).toContain('販売を一時停止');
    expect(host?.textContent).not.toContain('店舗価格を保存');
    expect(host?.textContent).not.toContain('販売を確認');
  });

  it('sends the reviewed draft version with the checked publish fingerprint', async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ revision: 5, draft_version: 7 }), { status: 200 }));
    vi.stubGlobal('fetch', fetchMock);
    await catalogClient.publishCompany('tenant-1', 'catalog-token', 7, 'fingerprint-7');
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe('/v1/admin/tenants/tenant-1/menu/publish');
    expect(JSON.parse(String(init.body))).toEqual({ expected_version: 7, preview_fingerprint: 'fingerprint-7' });
    expect(new Headers(init.headers).get('authorization')).toBe('Bearer catalog-token');
  });

  it('reloads a conflicted store write without replaying the write', async () => {
    installContext();
    vi.spyOn(catalogClient, 'companyPublished').mockRejectedValue(new ApiError(403, { error: 'forbidden' }));
    vi.spyOn(catalogClient, 'companyDraft').mockRejectedValue(new ApiError(403, { error: 'forbidden' }));
    const readStore = vi.spyOn(catalogClient, 'storeMenu').mockResolvedValue(storeMenu({ can_pause_availability: true, can_confirm_availability: true, can_edit_prices: true, can_detach: true }));
    const writePrice = vi.spyOn(catalogClient, 'setOverride').mockRejectedValue(new ApiError(409, { error: 'version_conflict' }));
    await mount();
    expect(readStore).toHaveBeenCalledTimes(1);

    await click('店舗価格を保存');

    expect(writePrice).toHaveBeenCalledTimes(1);
    expect(readStore).toHaveBeenCalledTimes(2);
    expect(host?.textContent).toContain('内容が更新されています');
  });

  it('keeps the unpublished company editor available when only its published snapshot is absent', async () => {
    installContext();
    vi.spyOn(catalogClient, 'companyPublished').mockRejectedValue(new ApiError(404, { error: 'not_found' }));
    vi.spyOn(catalogClient, 'companyDraft').mockResolvedValue(draft());
    vi.spyOn(catalogClient, 'storeMenu').mockRejectedValue(new ApiError(403, { error: 'forbidden' }));
    await mount();
    expect(host?.textContent).toContain('本部メニュー');
    expect([...host!.querySelectorAll('input')].some((input) => input.value === '定食')).toBe(true);
  });
});

async function mount() {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
  host = document.createElement('div'); document.body.append(host); root = createRoot(host);
  await act(async () => { root?.render(<CatalogApp />); await new Promise((resolve) => setTimeout(resolve, 0)); });
}

function installContext() {
  sessionStorage.setItem('tojima.catalog.fixture-context', JSON.stringify({ tenantID: 'tenant-1', storeID: 'store-1', token: 'catalog-token' }));
}

function draft(): CompanyDraft {
  return { version: 7, categories: [{ key: 'food', name: '料理', position: 1, products: [{ key: 'meal', name: '定食', description: '昼食', base_price_minor: '900', position: 1, active: true, option_groups: [] }] }] };
}

function storeMenu(capabilities: Pick<StoreCatalog, 'can_pause_availability' | 'can_confirm_availability' | 'can_edit_prices' | 'can_detach'>, availability: 'PENDING_CONFIRMATION' | 'AVAILABLE' = 'AVAILABLE', mode: StoreCatalog['mode'] = 'LINKED'): StoreCatalog {
  return { revision: 4, mode, ...capabilities, categories: [{ key: 'food', name: '料理', position: 1, products: [{ key: 'meal', name: '定食', description: '', base_price_minor: '900', effective_price_minor: '900', override_active: false, override_price_minor: null, price_override_version: 2, position: 1, active: true, availability, availability_version: 3, option_groups: [] }] }] };
}

function button(label: string) {
  const found = [...(host?.querySelectorAll('button') ?? [])].find((item) => item.textContent?.includes(label));
  if (!found) throw new Error(`Missing button: ${label}`);
  return found;
}

async function click(label: string) { await act(async () => { button(label).click(); await new Promise((resolve) => setTimeout(resolve, 0)); }); }

async function changeValue(element: HTMLInputElement | HTMLTextAreaElement, value: string) {
  const prototype = element instanceof HTMLTextAreaElement ? window.HTMLTextAreaElement.prototype : window.HTMLInputElement.prototype;
  const setter = Object.getOwnPropertyDescriptor(prototype, 'value')?.set;
  await act(async () => { setter?.call(element, value); element.dispatchEvent(new Event('input', { bubbles: true })); element.dispatchEvent(new Event('change', { bubbles: true })); });
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}
