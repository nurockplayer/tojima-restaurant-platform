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

function renderSheet({ selected = [] as OptionSelection[], onChoose = vi.fn<OptionSheetProps['onChoose']>(), onClear = vi.fn<NonNullable<OptionSheetProps['onClear']>>(), onAdd = vi.fn<OptionSheetProps['onAdd']>(), isOpen = true }: { selected?: OptionSelection[]; onChoose?: OptionSheetProps['onChoose']; onClear?: NonNullable<OptionSheetProps['onClear']>; onAdd?: OptionSheetProps['onAdd']; isOpen?: boolean } = {}) {
  const result = render(<OptionSheet product={product} isOpen={isOpen} onOpenChange={() => undefined} locale="ja" selected={selected}
    quantity={1} onChoose={onChoose} onClear={onClear} onQuantity={() => undefined} onAdd={onAdd} />);
  return { ...result, onChoose, onClear, onAdd };
}

describe('OptionSheet', () => {
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
});
