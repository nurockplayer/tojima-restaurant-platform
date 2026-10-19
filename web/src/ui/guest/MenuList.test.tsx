// @vitest-environment jsdom
import { cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { GuestMenu } from '../../api/types';
import { MenuList } from './MenuList';

afterEach(() => { cleanup(); });

const categories = [{ key: 'main', name: '主食', position: 1, products: [
  { key: 'plain', name: '白飯', description: '炊きたて', effective_price_minor: '300', position: 1, availability: 'AVAILABLE', selectable: true, option_groups: [] },
  { key: 'soup', name: '味噌汁', effective_price_minor: '200', position: 2, availability: 'AVAILABLE', selectable: true, option_groups: [{ key: 'size', name: 'サイズ', min_select: 1, max_select: 1, position: 1, options: [{ key: 'large', name: '大', price_delta_minor: '50', position: 1, availability: 'AVAILABLE' }] }] },
] }] satisfies GuestMenu['categories'];

describe('MenuList', () => {
  it('renders text-first rows, directly adds optionless products and opens products with options', async () => {
    const onAdd = vi.fn();
    const onOpenOptions = vi.fn();
    render(<MenuList categories={categories} locale="ja" onAdd={onAdd} onOpenOptions={onOpenOptions} />);
    const plain = within(screen.getByRole('article', { name: '白飯' }));
    expect(plain.getByText('炊きたて')).toBeDefined();
    expect(plain.getByText('¥300')).toBeDefined();
    await userEvent.setup().click(plain.getByRole('button', { name: 'カートに追加' }));
    expect(onAdd).toHaveBeenCalledWith(categories[0]!.products[0]);
    await userEvent.setup().click(within(screen.getByRole('article', { name: '味噌汁' })).getByRole('button', { name: 'カートに追加' }));
    expect(onOpenOptions).toHaveBeenCalledWith(categories[0]!.products[1]);
    expect(onAdd).toHaveBeenCalledTimes(1);
  });

  it('only reserves the 72px media slot when licensed media is supplied and never renders an image', () => {
    const view = render(<MenuList categories={categories} locale="zh" />);
    expect(view.container.querySelector('[data-product-key="plain"] [data-menu-media]')).toBeNull();
    expect(view.container.querySelector('[data-product-key="plain"] img')).toBeNull();
    view.rerender(<MenuList categories={categories} locale="zh" mediaByProduct={{ plain: { src: '/licensed.jpg', alt: '白飯', licensed: true } }} />);
    expect(view.container.querySelector('[data-product-key="plain"] [data-menu-media]')).not.toBeNull();
    expect(view.container.querySelector('[data-product-key="plain"] img')?.getAttribute('alt')).toBe('白飯');
  });
});
