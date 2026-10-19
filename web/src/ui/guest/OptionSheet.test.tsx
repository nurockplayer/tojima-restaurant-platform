// @vitest-environment jsdom
import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { GuestMenu, OptionSelection } from '../../api/types';
import { OptionSheet } from './OptionSheet';
import type { OptionSheetProps } from './OptionSheet';
import type { GuestProduct } from './MenuList';

afterEach(() => { cleanup(); vi.restoreAllMocks(); });

const product = {
  key: 'noodles', name: 'うどん', description: '説明', effective_price_minor: '800', position: 1,
  availability: 'AVAILABLE', selectable: true,
  option_groups: [
    { key: 'soup', name: 'スープ', min_select: 1, max_select: 1, position: 1, options: [
      { key: 'unavailable', name: '終了', price_delta_minor: '0', position: 1, availability: 'PAUSED' },
      { key: 'miso', name: '味噌', price_delta_minor: '20', position: 2, availability: 'AVAILABLE' },
    ] },
    { key: 'topping', name: 'トッピング', min_select: 0, max_select: 1, position: 2, options: [
      { key: 'egg', name: '卵', price_delta_minor: '100', position: 1, availability: 'AVAILABLE' },
    ] },
    { key: 'sauce', name: 'たれ', min_select: 1, max_select: 1, position: 3, options: [
      { key: 'soy', name: '醤油', price_delta_minor: '0', position: 1, availability: 'AVAILABLE' },
    ] },
  ],
} as GuestProduct satisfies GuestMenu['categories'][number]['products'][number];

function renderSheet({ selected = [] as OptionSelection[], onChoose = vi.fn<OptionSheetProps['onChoose']>(), onClear = vi.fn<NonNullable<OptionSheetProps['onClear']>>(), onAdd = vi.fn<OptionSheetProps['onAdd']>(), isOpen = true, locale = 'ja' as const, menuProduct = product }: { selected?: OptionSelection[]; onChoose?: OptionSheetProps['onChoose']; onClear?: NonNullable<OptionSheetProps['onClear']>; onAdd?: OptionSheetProps['onAdd']; isOpen?: boolean; locale?: 'ja' | 'zh'; menuProduct?: GuestProduct } = {}) {
  const result = render(<OptionSheet product={menuProduct} isOpen={isOpen} onOpenChange={() => undefined} locale={locale} selected={selected}
    quantity={1} onChoose={onChoose} onClear={onClear} onQuantity={() => undefined} onAdd={onAdd} />);
  return { ...result, onChoose, onClear, onAdd };
}

describe('OptionSheet', () => {
  const invalidCopy = {
    ja: { error: '選択内容を確認し、選び直してください。', reset: '選び直す', add: 'カートに追加' },
    zh: { error: '請檢查選項並重新選擇。', reset: '重新選擇', add: '加入購物車' },
  } as const;

  const multiSelectProduct = {
    ...product,
    option_groups: [
      { key: 'topping', name: 'トッピング', min_select: 0, max_select: 2, position: 1, options: [
        { key: 'egg', name: '卵', price_delta_minor: '100', position: 1, availability: 'AVAILABLE' },
        { key: 'cheese', name: 'チーズ', price_delta_minor: '120', position: 2, availability: 'AVAILABLE' },
        { key: 'nori', name: 'のり', price_delta_minor: '30', position: 3, availability: 'AVAILABLE' },
      ] },
      ...product.option_groups.filter((group) => group.key !== 'topping'),
    ],
  } as GuestProduct;

  it.each(['ja', 'zh'] as const)('blocks an over-limit selection after a menu refresh, then reset allows add (%s)', async (locale) => {
    const selected: OptionSelection[] = [
      { group_key: 'topping', option_key: 'egg' },
      { group_key: 'topping', option_key: 'cheese' },
      { group_key: 'soup', option_key: 'miso' },
      { group_key: 'sauce', option_key: 'soy' },
    ];
    const onAdd = vi.fn();
    const onClear = vi.fn();
    const view = renderSheet({ locale, menuProduct: multiSelectProduct, selected, onAdd, onClear });

    const loweredLimit = {
      ...multiSelectProduct,
      option_groups: multiSelectProduct.option_groups.map((group) => group.key === 'topping' ? { ...group, max_select: 1 } : group),
    } as GuestProduct;
    view.rerender(<OptionSheet product={loweredLimit} isOpen locale={locale} selected={selected} quantity={1}
      onChoose={() => undefined} onClear={onClear} onQuantity={() => undefined} onAdd={onAdd} onOpenChange={() => undefined} />);

    const dialog = screen.getByRole('dialog', { name: 'うどん' });
    const group = dialog.querySelector('[data-required-group="topping"]') as HTMLFieldSetElement;
    await userEvent.setup().click(within(dialog).getByRole('button', { name: invalidCopy[locale].add }));
    expect(onAdd).not.toHaveBeenCalled();
    expect(group.getAttribute('aria-invalid')).toBe('true');
    expect(group.getAttribute('aria-describedby')).toBeTruthy();
    expect(within(group).getByRole('alert').textContent).toBe(invalidCopy[locale].error);
    expect(document.activeElement).toBe(within(group).getByRole('radio', { name: /卵/ }));
    await userEvent.setup().click(within(group).getByRole('button', { name: invalidCopy[locale].reset }));
    expect(onClear).toHaveBeenCalledWith('topping');

    const validSelection = selected.filter((item) => item.group_key !== 'topping');
    view.rerender(<OptionSheet product={loweredLimit} isOpen locale={locale} selected={validSelection} quantity={1}
      onChoose={() => undefined} onClear={onClear} onQuantity={() => undefined} onAdd={onAdd} onOpenChange={() => undefined} />);
    await userEvent.setup().click(within(dialog).getByRole('button', { name: invalidCopy[locale].add }));
    expect(onAdd).toHaveBeenCalledOnce();
  });

  it.each(['ja', 'zh'] as const)('blocks a now-unavailable selected option and provides a reset path (%s)', async (locale) => {
    const selected: OptionSelection[] = [
      { group_key: 'topping', option_key: 'egg' },
      { group_key: 'soup', option_key: 'miso' },
      { group_key: 'sauce', option_key: 'soy' },
    ];
    const changedAvailability = {
      ...multiSelectProduct,
      option_groups: multiSelectProduct.option_groups.map((group) => group.key === 'topping'
        ? { ...group, options: group.options.map((option) => option.key === 'egg' ? { ...option, availability: 'PAUSED' as const } : option) }
        : group),
    } as GuestProduct;
    const onAdd = vi.fn();
    const onClear = vi.fn();
    renderSheet({ locale, menuProduct: changedAvailability, selected, onAdd, onClear });
    const dialog = screen.getByRole('dialog', { name: 'うどん' });
    const group = dialog.querySelector('[data-required-group="topping"]') as HTMLFieldSetElement;
    await userEvent.setup().click(within(dialog).getByRole('button', { name: invalidCopy[locale].add }));
    expect(onAdd).not.toHaveBeenCalled();
    expect(group.getAttribute('aria-invalid')).toBe('true');
    expect(within(group).getByRole('alert').textContent).toBe(invalidCopy[locale].error);
    expect(document.activeElement).toBe(within(group).getByRole('checkbox', { name: /チーズ/ }));
    await userEvent.setup().click(within(group).getByRole('button', { name: invalidCopy[locale].reset }));
    expect(onClear).toHaveBeenCalledWith('topping');
  });

  it.each(['ja', 'zh'] as const)('blocks a selection whose option disappeared from the refreshed group (%s)', async (locale) => {
    const selected: OptionSelection[] = [
      { group_key: 'topping', option_key: 'egg' },
      { group_key: 'soup', option_key: 'miso' },
      { group_key: 'sauce', option_key: 'soy' },
    ];
    const changedOptions = {
      ...multiSelectProduct,
      option_groups: multiSelectProduct.option_groups.map((group) => group.key === 'topping'
        ? { ...group, options: group.options.filter((option) => option.key !== 'egg') }
        : group),
    } as GuestProduct;
    const onAdd = vi.fn();
    const onClear = vi.fn();
    renderSheet({ locale, menuProduct: changedOptions, selected, onAdd, onClear });
    const dialog = screen.getByRole('dialog', { name: 'うどん' });
    const group = dialog.querySelector('[data-required-group="topping"]') as HTMLFieldSetElement;
    await userEvent.setup().click(within(dialog).getByRole('button', { name: invalidCopy[locale].add }));
    expect(onAdd).not.toHaveBeenCalled();
    expect(group.getAttribute('aria-invalid')).toBe('true');
    expect(within(group).getByRole('alert').textContent).toBe(invalidCopy[locale].error);
    expect(document.activeElement).toBe(within(group).getByRole('checkbox', { name: /チーズ/ }));
    await userEvent.setup().click(within(group).getByRole('button', { name: invalidCopy[locale].reset }));
    expect(onClear).toHaveBeenCalledWith('topping');
  });

  it.each(['ja', 'zh'] as const)('blocks and clears a stale selection when its group disappears (%s)', async (locale) => {
    const selected: OptionSelection[] = [
      { group_key: 'topping', option_key: 'egg' },
      { group_key: 'soup', option_key: 'miso' },
      { group_key: 'sauce', option_key: 'soy' },
    ];
    const refreshedProduct = {
      ...multiSelectProduct,
      option_groups: multiSelectProduct.option_groups.filter((group) => group.key !== 'topping'),
    } as GuestProduct;
    const onAdd = vi.fn();
    const onChoose = vi.fn();
    const view = renderSheet({ locale, menuProduct: refreshedProduct, selected, onAdd, onChoose });
    const dialog = screen.getByRole('dialog', { name: 'うどん' });
    await userEvent.setup().click(within(dialog).getByRole('button', { name: invalidCopy[locale].add }));
    expect(onAdd).not.toHaveBeenCalled();
    expect(within(dialog).getByRole('alert').textContent).toBe(invalidCopy[locale].error);
    await userEvent.setup().click(within(dialog).getByRole('button', { name: invalidCopy[locale].reset }));
    expect(onChoose).toHaveBeenCalledWith('topping', 'egg', 0);

    const validSelection = selected.filter((item) => item.group_key !== 'topping');
    view.rerender(<OptionSheet product={refreshedProduct} isOpen locale={locale} selected={validSelection} quantity={1}
      onChoose={onChoose} onQuantity={() => undefined} onAdd={onAdd} onOpenChange={() => undefined} />);
    await userEvent.setup().click(within(dialog).getByRole('button', { name: invalidCopy[locale].add }));
    expect(onAdd).toHaveBeenCalledOnce();
  });

  it('keeps add enabled when a required group is missing, blocks add locally and focuses the first selectable option', async () => {
    const { onAdd } = renderSheet();
    const dialog = screen.getByRole('dialog', { name: 'うどん' });
    const add = within(dialog).getByRole('button', { name: 'カートに追加' });
    expect(add.hasAttribute('disabled')).toBe(false);
    await userEvent.setup().click(add);
    expect(onAdd).not.toHaveBeenCalled();
    const fieldset = dialog.querySelector('[data-required-group="soup"]') as HTMLFieldSetElement;
    expect(fieldset.getAttribute('aria-invalid')).toBe('true');
    expect(fieldset.getAttribute('aria-describedby')).toBeTruthy();
    expect(within(fieldset).getByRole('alert').textContent).toBe('必須オプションを選択してください。');
    expect(document.activeElement).toBe(within(fieldset).getByRole('radio', { name: /味噌/ }));
    expect(dialog.querySelector('[data-required-group="sauce"]')?.getAttribute('aria-invalid')).toBe('true');
  });

  it('calls add after required options are selected and retains optional clear and quantity controls', async () => {
    const selected: OptionSelection[] = [{ group_key: 'soup', option_key: 'miso' }, { group_key: 'sauce', option_key: 'soy' }];
    const onAdd = vi.fn();
    renderSheet({ selected, onAdd });
    const dialog = screen.getByRole('dialog', { name: 'うどん' });
    await userEvent.setup().click(within(dialog).getByRole('button', { name: '選択しない' }));
    await userEvent.setup().click(within(dialog).getByRole('button', { name: 'カートに追加' }));
    expect(onAdd).toHaveBeenCalledOnce();
    expect(within(dialog).getByRole('combobox', { name: '数量 うどん' })).toBeDefined();
  });

  it('keeps disabled conditions for unavailable products and uncertain requests', () => {
    const view = render(<OptionSheet product={{ ...product, selectable: false }} isOpen locale="zh" selected={[]} quantity={1} onChoose={() => undefined} onQuantity={() => undefined} onAdd={() => undefined} onOpenChange={() => undefined} />);
    expect(within(screen.getByRole('dialog', { name: 'うどん' })).getByRole('button', { name: '加入購物車' }).hasAttribute('disabled')).toBe(true);
    view.rerender(<OptionSheet product={product} isOpen locale="zh" selected={[]} quantity={1} uncertain onChoose={() => undefined} onQuantity={() => undefined} onAdd={() => undefined} onOpenChange={() => undefined} />);
    expect(within(screen.getByRole('dialog', { name: 'うどん' })).getByRole('button', { name: '加入購物車' }).hasAttribute('disabled')).toBe(true);
  });

  it('renders parent feedback and notice within the same dialog', async () => {
    render(<OptionSheet product={product} isOpen locale="ja" selected={[]} quantity={1} onChoose={() => undefined} onQuantity={() => undefined} onAdd={() => undefined} onOpenChange={() => undefined} notice={<p role="note">DEMO</p>}>
      <p role="alert">通信結果を確認できません。</p>
    </OptionSheet>);
    const dialog = screen.getByRole('dialog', { name: 'うどん' });
    expect(within(dialog).getByRole('note').textContent).toBe('DEMO');
    expect(within(dialog).getByRole('alert').textContent).toBe('通信結果を確認できません。');
    await waitFor(() => expect(dialog.isConnected).toBe(true));
  });

  it('keeps focus inside when a menu update removes the focused option', async () => {
    const view = renderSheet();
    await userEvent.setup().click(screen.getByRole('radio', { name: /味噌/ }));
    const updated = { ...product, option_groups: product.option_groups.slice(1) };
    view.rerender(<OptionSheet product={updated} isOpen locale="ja" selected={[]} quantity={1}
      onChoose={() => undefined} onQuantity={() => undefined} onAdd={() => undefined} onOpenChange={() => undefined} />);
    const dialog = screen.getByRole('dialog', { name: 'うどん' });
    await waitFor(() => expect(dialog.contains(document.activeElement)).toBe(true));
    await userEvent.setup().tab();
    expect(document.activeElement).toBe(within(screen.getByRole('dialog')).getByRole('button', { name: '閉じる' }));
  });
});
