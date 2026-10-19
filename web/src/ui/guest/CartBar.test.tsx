// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Cart, GuestMenu } from '../../api/types';
import { CartBar } from './CartBar';

const menu: GuestMenu = {
  currency: 'JPY', price_mode: 'TAX_INCLUSIVE', categories: [{ key: 'main', name: '主菜', position: 0, products: [{
    key: 'meal', name: '定食', effective_price_minor: 960, position: 0, availability: 'AVAILABLE', selectable: true,
    option_groups: [{ key: 'size', name: 'サイズ', min_select: 0, max_select: 1, position: 0, options: [
      { key: 'large', name: '大盛', price_delta_minor: 20, position: 0, availability: 'AVAILABLE' },
    ] }],
  }] }],
};
const category = menu.categories[0]!;
const product = category.products[0]!;
const cart: Cart = { session_id: 'session', generation: 1, version: 1, lines: [{
  line_id: 'line', product_key: 'meal', product_name: '定食', product_available: true, quantity: 2,
  options: [{ group_key: 'size', option_key: 'large', group_name: 'サイズ', option_name: '大盛', available: true }],
}] };

afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

describe('CartBar', () => {
  it('shows count, estimated total and opens the shared cart', () => {
    const onOpen = vi.fn();
    render(<CartBar active cart={cart} menu={menu} locale="ja" onOpen={onOpen} />);
    expect(screen.getByText('2 点')).toBeDefined();
    expect(screen.getByText('¥1,960')).toBeDefined();
    expect(screen.getByText('共有カート')).toBeDefined();
    fireEvent.click(screen.getByRole('button', { name: 'カートを開く 2 点 税込・目安 ¥1,960 共有カート' }));
    expect(onOpen).toHaveBeenCalledOnce();
  });

  it('shows an em dash when the estimate cannot be priced', () => {
    const paused: GuestMenu = { ...menu, categories: [{ ...category, products: [{ ...product, availability: 'PAUSED' }] }] };
    render(<CartBar active cart={cart} menu={paused} locale="zh" onOpen={() => undefined} />);
    expect(screen.getByText('—')).toBeDefined();
    expect(screen.getByText('2 件')).toBeDefined();
  });

  it.each([
    ['ja', 'normal', 'テーブルで共有中・09:30', 'カートを開く 2 点 税込・目安 ¥1,960 テーブルで共有中・09:30'],
    ['ja', 'unpriced', 'テーブルで共有中・09:30', 'カートを開く 2 点 税込・目安 — テーブルで共有中・09:30'],
    ['ja', 'degraded', '同期待ち・09:30 時点', 'カートを開く 2 点 税込・目安 ¥1,960 同期待ち・09:30 時点'],
    ['zh', 'normal', '同桌共用中・09:30', '開啟購物車 2 件 含稅・預估 ¥1,960 同桌共用中・09:30'],
    ['zh', 'unpriced', '同桌共用中・09:30', '開啟購物車 2 件 含稅・預估 — 同桌共用中・09:30'],
    ['zh', 'degraded', '等待同步・09:30 的內容', '開啟購物車 2 件 含稅・預估 ¥1,960 等待同步・09:30 的內容'],
  ] as const)('exposes action, count, estimate and sync in its %s accessible name when %s', (locale, state, sync, name) => {
    const paused: GuestMenu = { ...menu, categories: [{ ...category, products: [{ ...product, availability: 'PAUSED' }] }] };
    const { container } = render(<CartBar active cart={cart} menu={state === 'unpriced' ? paused : menu} locale={locale} sync={sync} onOpen={() => undefined} />);
    expect(screen.getByRole('button', { name })).toBeDefined();
    expect(container.querySelector('[role="status"], [aria-live]')).toBeNull();
  });

  const emptyCart: Cart = { ...cart, lines: [] };
  it('reports the measured bar height again when its content resizes and clears it on close', () => {
    let height = 88;
    let resize!: () => void;
    const disconnect = vi.fn();
    vi.stubGlobal('ResizeObserver', class {
      constructor(callback: () => void) { resize = callback; }
      observe() {}
      disconnect = disconnect;
    });
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(() => ({ height }) as DOMRect);
    const onHeightChange = vi.fn();
    const view = render(<CartBar active cart={cart} menu={menu} locale="ja" onOpen={() => undefined} onHeightChange={onHeightChange} />);
    expect(onHeightChange).toHaveBeenLastCalledWith(88);
    height = 132.25;
    resize();
    expect(onHeightChange).toHaveBeenLastCalledWith(133);
    view.rerender(<CartBar active={false} cart={cart} menu={menu} locale="ja" onOpen={() => undefined} onHeightChange={onHeightChange} />);
    expect(onHeightChange).toHaveBeenLastCalledWith(0);
    expect(disconnect).toHaveBeenCalledOnce();
  });

  it.each<[string, boolean, Cart | null]>([
    ['orders view', false, cart],
    ['empty cart', true, emptyCart],
    ['missing cart', true, null],
  ])('does not render for %s', (_case, active, value) => {
    const { container } = render(<CartBar active={active} cart={value} menu={menu} locale="ja" onOpen={() => undefined} />);
    expect(container.firstChild).toBeNull();
  });
});
