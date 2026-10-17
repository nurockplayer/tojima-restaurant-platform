// @vitest-environment jsdom
import { act } from 'react';
import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiError } from '../api/client';
import { catalogClient, type CompanyDraft, type StoreCatalog } from '../api/catalog';
import { CatalogApp } from './CatalogApp';

let user: ReturnType<typeof userEvent.setup>;

afterEach(() => {
  cleanup();
  sessionStorage.clear(); vi.restoreAllMocks(); vi.unstubAllGlobals();
});

describe('catalog admin UI', () => {
  it('keeps the document language aligned with the Japanese and Traditional Chinese toggle', async () => {
    user = userEvent.setup();
    render(<CatalogApp />);
    await waitFor(() => expect(document.documentElement.lang).toBe('ja'));
    await user.click(screen.getByRole('button', { name: /繁體中文/ }));
    await waitFor(() => expect(document.documentElement.lang).toBe('zh-Hant'));
    await user.click(screen.getByRole('button', { name: /日本語/ }));
    await waitFor(() => expect(document.documentElement.lang).toBe('ja'));
  });

  it('does not display a delayed publication preview after the draft changes', async () => {
    const pending = deferred<Awaited<ReturnType<typeof catalogClient.previewCompany>>>();
    installContext();
    vi.spyOn(catalogClient, 'companyPublished').mockResolvedValue({ revision: 4, categories: draft().categories });
    vi.spyOn(catalogClient, 'companyDraft').mockResolvedValue(draft());
    vi.spyOn(catalogClient, 'storeMenu').mockRejectedValue(new ApiError(403, { error: 'forbidden' }));
    const preview = vi.spyOn(catalogClient, 'previewCompany').mockReturnValue(pending.promise);
    await mount();

    await user.click(screen.getByRole('button', { name: '公開前チェック' }));
    expect(preview).toHaveBeenCalledWith('tenant-1', 'catalog-token', 7);
    const description = screen.getByLabelText('説明');
    await user.clear(description);
    await user.type(description, '最新の説明');
    await act(async () => { pending.resolve({ draft_version: 7, published_revision: 4, linked_store_count: 2, can_publish: true, issues: [], preview_fingerprint: 'stale-fingerprint' }); await pending.promise; });

    expect(screen.queryByLabelText('公開前チェック')).toBeNull();
    expect((screen.getByRole('button', { name: '確認した内容を公開' }) as HTMLButtonElement).disabled).toBe(true);
  });

  it('summarizes nonblocking additions, text and price changes from the saved candidate with linked-store impact', async () => {
    installContext();
    const published = draft();
    const candidate = draft();
    candidate.version = 8;
    candidate.categories[0]!.name = '料理と新着';
    candidate.categories[0]!.products[0]!.base_price_minor = '1000';
    candidate.categories[0]!.products[0]!.option_groups.push({ key: 'size', name: 'サイズ', min_select: 0, max_select: 1, position: 0, options: [{ key: 'large', name: '大盛り', price_delta_minor: '100', position: 0, active: true }] });
    vi.spyOn(catalogClient, 'companyPublished').mockResolvedValue({ revision: 4, categories: published.categories });
    vi.spyOn(catalogClient, 'companyDraft').mockResolvedValue(candidate);
    vi.spyOn(catalogClient, 'storeMenu').mockRejectedValue(new ApiError(403, { error: 'forbidden' }));
    vi.spyOn(catalogClient, 'previewCompany').mockResolvedValue({ draft_version: 8, published_revision: 4, linked_store_count: 3, can_publish: true, issues: [], preview_fingerprint: 'checked-8' });
    await mount();

    await user.click(screen.getByRole('button', { name: '公開前チェック' }));

    const summary = await screen.findByLabelText('公開内容の変更概要');
    expect(summary.textContent).toContain('料理 → 料理と新着');
    expect(summary.textContent).toContain('定食: ¥900 → ¥1,000');
    expect(summary.textContent).toContain('定食 / サイズ');
    expect(summary.textContent).toContain('定食 / 大盛り');
    expect(summary.textContent).toContain('連動中の店舗: 3');
    expect(summary.textContent).toContain('公開可否は下記のサーバー検証結果');
    expect(screen.getByRole('button', { name: '確認した内容を公開' })).not.toHaveProperty('disabled', true);

    await user.clear(screen.getByLabelText('説明'));
    await user.type(screen.getByLabelText('説明'), '編集後の説明');
    expect(screen.queryByLabelText('公開内容の変更概要')).toBeNull();
    expect((screen.getByRole('button', { name: '確認した内容を公開' }) as HTMLButtonElement).disabled).toBe(true);
  });

  it('identifies a required-choice rule change separately while server issues remain the publish authority', async () => {
    installContext();
    const published = draft();
    published.categories[0]!.products[0]!.option_groups = [{ key: 'size', name: 'サイズ', min_select: 1, max_select: 1, position: 0, options: [{ key: 'normal', name: '普通', price_delta_minor: '0', position: 0, active: true }] }];
    const candidate = structuredClone(published);
    candidate.version = 8;
    candidate.categories[0]!.products[0]!.option_groups[0]!.min_select = 2;
    candidate.categories[0]!.products[0]!.option_groups[0]!.max_select = 2;
    vi.spyOn(catalogClient, 'companyPublished').mockResolvedValue({ revision: 4, categories: published.categories });
    vi.spyOn(catalogClient, 'companyDraft').mockResolvedValue(candidate);
    vi.spyOn(catalogClient, 'storeMenu').mockRejectedValue(new ApiError(403, { error: 'forbidden' }));
    vi.spyOn(catalogClient, 'previewCompany').mockResolvedValue({ draft_version: 8, published_revision: 4, linked_store_count: 3, can_publish: false, issues: [{ code: 'REQUIRED_GROUP_MIN_INCREASED', product_key: 'meal', group_key: 'size' }], preview_fingerprint: 'blocked-8' });
    await mount();

    await user.click(screen.getByRole('button', { name: '公開前チェック' }));

    const summary = await screen.findByLabelText('公開内容の変更概要');
    expect(summary.textContent).toContain('必須選択ルールの変更 (1)');
    expect(summary.textContent).toContain('定食 / サイズ: 1–1 → 2–2');
    expect(screen.getByText('必須選択数を増やすと既存店舗で注文できなくなる可能性があります。')).toBeTruthy();
    expect((screen.getByRole('button', { name: '確認した内容を公開' }) as HTMLButtonElement).disabled).toBe(true);
  });

  it('adds option groups and options through the dynamic structured editor', async () => {
    installContext();
    vi.spyOn(catalogClient, 'companyPublished').mockResolvedValue({ revision: 4, categories: draft().categories });
    vi.spyOn(catalogClient, 'companyDraft').mockResolvedValue(draft());
    vi.spyOn(catalogClient, 'storeMenu').mockRejectedValue(new ApiError(403, { error: 'forbidden' }));
    await mount();

    await user.click(screen.getByRole('button', { name: '選択肢グループを追加' }));
    await user.type(screen.getByLabelText('グループ名'), 'サイズ');
    await user.click(screen.getByRole('button', { name: '選択肢を追加' }));
    const options = screen.getAllByLabelText('選択肢名');
    expect(options).toHaveLength(2);
    await user.type(options[0]!, '普通');
    await user.type(options[1]!, '大盛り');
    expect(screen.getByLabelText('グループ名')).toHaveProperty('value', 'サイズ');
    expect(screen.getAllByLabelText('選択肢名').map((input) => (input as HTMLInputElement).value)).toEqual(['普通', '大盛り']);
  });

  it('keeps product insertion available after deleting the last product and in a newly added category', async () => {
    installContext();
    vi.spyOn(catalogClient, 'companyPublished').mockResolvedValue({ revision: 4, categories: draft().categories });
    vi.spyOn(catalogClient, 'companyDraft').mockResolvedValue(draft());
    vi.spyOn(catalogClient, 'storeMenu').mockRejectedValue(new ApiError(403, { error: 'forbidden' }));
    await mount();

    const firstProduct = screen.getByLabelText('商品名').closest('fieldset');
    expect(firstProduct).not.toBeNull();
    await user.click(within(firstProduct!).getByRole('button', { name: '削除' }));
    expect(screen.queryByLabelText('商品名')).toBeNull();

    let categories = screen.getAllByLabelText('カテゴリ名').map((input) => input.closest('fieldset'));
    await user.click(within(categories[0]!).getByRole('button', { name: '商品を追加' }));
    expect(screen.getAllByLabelText('商品名')).toHaveLength(1);

    await user.click(screen.getByRole('button', { name: 'カテゴリを追加' }));
    categories = screen.getAllByLabelText('カテゴリ名').map((input) => input.closest('fieldset'));
    await user.click(within(categories[1]!).getByRole('button', { name: '商品を追加' }));
    expect(screen.getAllByLabelText('商品名')).toHaveLength(2);
  });

  it('resets the editor after authoritative refresh even when the draft version is unchanged', async () => {
    installContext();
    const serverDraft = draft();
    vi.spyOn(catalogClient, 'companyPublished').mockResolvedValue({ revision: 4, categories: serverDraft.categories });
    vi.spyOn(catalogClient, 'companyDraft').mockResolvedValue(serverDraft);
    vi.spyOn(catalogClient, 'storeMenu').mockRejectedValue(new ApiError(403, { error: 'forbidden' }));
    await mount();

    await user.clear(screen.getByLabelText('説明'));
    await user.type(screen.getByLabelText('説明'), '未儲存的畫面內容');
    expect(screen.getByLabelText('説明')).toHaveProperty('value', '未儲存的畫面內容');
    expect((screen.getByRole('button', { name: '公開前チェック' }) as HTMLButtonElement).disabled).toBe(true);

    await user.click(screen.getByRole('button', { name: '最新状態に更新' }));

    await waitFor(() => expect(screen.getByLabelText('説明')).toHaveProperty('value', '昼食'));
    expect(catalogClient.companyDraft).toHaveBeenCalledTimes(2);
    expect((screen.getByRole('button', { name: '公開前チェック' }) as HTMLButtonElement).disabled).toBe(false);
  });

  it('uses server capabilities to expose staff pause without manager price, confirm, or detach actions', async () => {
    installContext();
    vi.spyOn(catalogClient, 'companyPublished').mockRejectedValue(new ApiError(403, { error: 'forbidden' }));
    vi.spyOn(catalogClient, 'companyDraft').mockRejectedValue(new ApiError(403, { error: 'forbidden' }));
    vi.spyOn(catalogClient, 'storeMenu').mockResolvedValue(storeMenu({ can_pause_availability: true, can_confirm_availability: false, can_edit_prices: false, can_detach: false }));
    await mount();

    expect(screen.getByText('定食')).toBeTruthy();
    expect(screen.getByRole('button', { name: '販売を一時停止' })).toBeTruthy();
    expect(screen.queryByRole('button', { name: '販売を確認' })).toBeNull();
    expect(screen.queryByRole('button', { name: '店舗価格を保存' })).toBeNull();
    expect(screen.queryByRole('button', { name: '専用メニューに切り替える' })).toBeNull();
  });

  it('shows tenant-admin actions from server capabilities and requires explicit detach confirmation', async () => {
    installContext();
    vi.spyOn(catalogClient, 'companyPublished').mockRejectedValue(new ApiError(403, { error: 'forbidden' }));
    vi.spyOn(catalogClient, 'companyDraft').mockRejectedValue(new ApiError(403, { error: 'forbidden' }));
    vi.spyOn(catalogClient, 'storeMenu').mockResolvedValue(storeMenu({ can_pause_availability: true, can_confirm_availability: true, can_edit_prices: true, can_detach: true }, 'PENDING_CONFIRMATION'));
    const detach = vi.spyOn(catalogClient, 'detachStore').mockResolvedValue({ revision: 5, draft_version: 0 });
    await mount();

    expect(screen.getByRole('button', { name: '販売を確認' })).toBeTruthy();
    expect(screen.getByRole('button', { name: '店舗価格を保存' })).toBeTruthy();
    await user.click(screen.getByRole('button', { name: '専用メニューに切り替える' }));
    expect(detach).not.toHaveBeenCalled();
    expect(screen.getAllByText('現在の公開メニューを複製します。以後、本部の公開はこの店舗に反映されません。')).toHaveLength(2);
    await user.click(screen.getByRole('button', { name: '複製して切り替える' }));
    expect(detach).toHaveBeenCalledWith({ tenantID: 'tenant-1', storeID: 'store-1', token: 'catalog-token' }, 'catalog-token', 4);
  });

  it('keeps detached availability readable and pauseable for staff while omitting linked price controls', async () => {
    installContext();
    vi.spyOn(catalogClient, 'companyPublished').mockRejectedValue(new ApiError(403, { error: 'forbidden' }));
    vi.spyOn(catalogClient, 'companyDraft').mockRejectedValue(new ApiError(403, { error: 'forbidden' }));
    vi.spyOn(catalogClient, 'storeMenu').mockResolvedValue(storeMenu({ can_pause_availability: true, can_confirm_availability: false, can_edit_prices: false, can_detach: false }, 'AVAILABLE', 'DETACHED'));
    await mount();
    expect(screen.getByText('この店舗の専用メニュー')).toBeTruthy();
    expect(screen.getByText('店舗メニューの基本価格')).toBeTruthy();
    expect(screen.getByRole('button', { name: '販売を一時停止' })).toBeTruthy();
    expect(screen.queryByRole('button', { name: '店舗価格を保存' })).toBeNull();
    expect(screen.queryByRole('button', { name: '販売を確認' })).toBeNull();
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

    await user.click(screen.getByRole('button', { name: '店舗価格を保存' }));

    expect(writePrice).toHaveBeenCalledTimes(1);
    expect(readStore).toHaveBeenCalledTimes(2);
    expect(screen.getByText('内容が更新されています。最新情報を読み込み、もう一度確認してください。')).toBeTruthy();
  });

  it('keeps the unpublished company editor available when only its published snapshot is absent', async () => {
    installContext();
    vi.spyOn(catalogClient, 'companyPublished').mockRejectedValue(new ApiError(404, { error: 'not_found' }));
    vi.spyOn(catalogClient, 'companyDraft').mockResolvedValue(draft());
    vi.spyOn(catalogClient, 'storeMenu').mockRejectedValue(new ApiError(403, { error: 'forbidden' }));
    await mount();
    expect(screen.getByRole('button', { name: '本部メニュー' })).toBeTruthy();
    expect(screen.getByRole('textbox', { name: '商品名' })).toHaveProperty('value', '定食');
  });

  it('saves, previews, and publishes a product when the server omits option_groups', async () => {
    installContext();
    let serverVersion = 7;
    let serverName = '味噌ラーメン';
    const calls: Array<{ url: string; init?: RequestInit }> = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = init?.method ?? 'GET';
      calls.push({ url, init });
      if (url.includes('/stores/store-1/menu?')) return jsonResponse({ error: 'forbidden' }, 403);
      if (url.endsWith('/menu/published?amounts=string')) return jsonResponse({ revision: 4, categories: [wireCategory(serverName)] });
      if (url.endsWith('/menu/draft?amounts=string')) return jsonResponse({ version: serverVersion, categories: [wireCategory(serverName)] });
      if (url.endsWith('/menu/draft') && method === 'PUT') {
        const body = JSON.parse(String(init?.body)) as { categories: Array<{ products: Array<{ name: string; option_groups?: unknown[] }> }> };
        expect(body.categories[0]!.products[0]!.option_groups).toEqual([]);
        serverName = body.categories[0]!.products[0]!.name;
        serverVersion = 8;
        return jsonResponse({ saved: true });
      }
      if (url.endsWith('/menu/preview')) return jsonResponse({ draft_version: 8, published_revision: 4, linked_store_count: 1, can_publish: true, issues: [], preview_fingerprint: 'no-options-preview' });
      if (url.endsWith('/menu/publish')) return jsonResponse({ revision: 5, draft_version: 8 });
      return jsonResponse({ error: 'unexpected_request' }, 404);
    });
    vi.stubGlobal('fetch', fetchMock);
    await mount();

    await user.clear(screen.getByLabelText('商品名'));
    await user.type(screen.getByLabelText('商品名'), '味噌ラーメン改');
    await user.click(screen.getByRole('button', { name: '草稿を保存' }));
    await waitFor(() => expect(screen.getByText('草稿を保存しました。公開前チェックを実行してください。')).toBeTruthy());
    await user.click(screen.getByRole('button', { name: '公開前チェック' }));
    expect(await screen.findByLabelText('公開内容の変更概要')).toBeTruthy();
    expect(screen.getByText('味噌ラーメン → 味噌ラーメン改')).toBeTruthy();
    await user.click(screen.getByRole('button', { name: '確認した内容を公開' }));

    await waitFor(() => expect(screen.getByText('メニューを公開しました。')).toBeTruthy());
    expect(calls.some((call) => call.url.endsWith('/menu/preview'))).toBe(true);
    expect(calls.some((call) => call.url.endsWith('/menu/publish'))).toBe(true);
  });
});

async function mount() {
  user = userEvent.setup();
  render(<CatalogApp />);
  await screen.findByRole('button', { name: '切断' });
  await waitFor(() => expect((screen.getByRole('button', { name: '最新状態に更新' }) as HTMLButtonElement).disabled).toBe(false));
}

function installContext() {
  sessionStorage.setItem('tojima.catalog.fixture-context', JSON.stringify({ tenantID: 'tenant-1', storeID: 'store-1', token: 'catalog-token' }));
}

function draft(): CompanyDraft {
  return { version: 7, categories: [{ key: 'food', name: '料理', position: 1, products: [{ key: 'meal', name: '定食', description: '昼食', base_price_minor: '900', position: 1, active: true, option_groups: [] }] }] };
}

function wireCategory(productName: string) {
  return { key: 'food', name: '料理', position: 1, products: [{ key: 'miso-ramen', name: productName, description: '', base_price_minor: '900', position: 1, active: true }] };
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });
}

function storeMenu(capabilities: Pick<StoreCatalog, 'can_pause_availability' | 'can_confirm_availability' | 'can_edit_prices' | 'can_detach'>, availability: 'PENDING_CONFIRMATION' | 'AVAILABLE' = 'AVAILABLE', mode: StoreCatalog['mode'] = 'LINKED'): StoreCatalog {
  return { revision: 4, mode, ...capabilities, categories: [{ key: 'food', name: '料理', position: 1, products: [{ key: 'meal', name: '定食', description: '', base_price_minor: '900', effective_price_minor: '900', override_active: false, override_price_minor: null, price_override_version: 2, position: 1, active: true, availability, availability_version: 3, option_groups: [] }] }] };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}
