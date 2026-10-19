import { describe, expect, it } from 'vitest';
import type { Cart, GuestMenu } from '../../api/types';
import { cartEstimate } from './cart-estimate';

const menu: GuestMenu = {
  currency: 'JPY',
  price_mode: 'TAX_INCLUSIVE',
  categories: [{ key: 'main', name: '主菜', position: 0, products: [{
    key: 'meal', name: '定食', effective_price_minor: 960, position: 0, availability: 'AVAILABLE', selectable: true,
    option_groups: [{ key: 'size', name: 'サイズ', min_select: 0, max_select: 1, position: 0, options: [
      { key: 'large', name: '大盛', price_delta_minor: 20, position: 0, availability: 'AVAILABLE' },
      { key: 'unavailable', name: '終了', price_delta_minor: 0, position: 1, availability: 'PAUSED' },
    ] }],
  }] }],
};
const category = menu.categories[0]!;
const product = category.products[0]!;

function cart(quantity = 2, options: Cart['lines'][number]['options'] = []): Cart {
  return { session_id: 'session', generation: 1, version: 1, lines: [{
    line_id: 'line', product_key: 'meal', product_name: '定食', product_available: true, quantity, options,
  }] };
}

describe('cartEstimate', () => {
  it('calculates base and selected option price times quantity exactly', () => {
    expect(cartEstimate(cart(2, [{ group_key: 'size', option_key: 'large', group_name: 'サイズ', option_name: '大盛', available: true }]), menu)).toBe('1960');
  });

  it.each([
    ['missing cart', null, menu],
    ['missing product', cart(), { ...menu, categories: [] }],
    ['line unavailable', { ...cart(), lines: [{ ...cart().lines[0], product_available: false }] }, menu],
    ['menu product unavailable', cart(), { ...menu, categories: [{ ...category, products: [{ ...product, availability: 'PAUSED' }] }] }],
    ['company hold', cart(), { ...menu, categories: [{ ...category, products: [{ ...product, company_emergency_hold_active: true }] }] }],
    ['missing selected option', cart(1, [{ group_key: 'size', option_key: 'missing', group_name: 'サイズ', option_name: '不明', available: true }]), menu],
    ['unavailable selected option', cart(1, [{ group_key: 'size', option_key: 'unavailable', group_name: 'サイズ', option_name: '終了', available: true }]), menu],
    ['unsafe numeric price', cart(), { ...menu, categories: [{ ...category, products: [{ ...product, effective_price_minor: Number.MAX_SAFE_INTEGER + 1 }] }] }],
    ['fractional numeric price', cart(), { ...menu, categories: [{ ...category, products: [{ ...product, effective_price_minor: 1.5 }] }] }],
    ['invalid quantity', cart(Number.POSITIVE_INFINITY), menu],
  ] as const)('returns null for %s', (_label, value, sourceMenu) => {
    expect(cartEstimate(value as Cart | null, sourceMenu as GuestMenu)).toBeNull();
  });

  it('accepts exact integer decimal strings above MAX_SAFE_INTEGER', () => {
    const largeMenu: GuestMenu = { ...menu, categories: [{ ...category, products: [{ ...product, effective_price_minor: '9007199254740993' }] }] };
    expect(cartEstimate(cart(1), largeMenu)).toBe('9007199254740993');
  });
});
