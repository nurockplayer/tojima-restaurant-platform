import { describe, expect, it } from 'vitest';
import type { Cart, CartLine, Preview } from '../api/types';
import { createReplayOperation, guestCartReducer, initialGuestCartState } from './cart-state';

const line = (available = true): CartLine => ({
  line_id: 'line-1', product_key: 'meal', product_name: 'Meal', product_available: available,
  quantity: 1, options: [],
});
const cart = (generation: number, version: number, lines: CartLine[] = [line()]): Cart => ({
  session_id: 'session-1', generation, version, lines,
});
const preview = (generation: number, version: number): Preview => ({
  session_id: 'session-1', generation, version, fingerprint: `quote-${generation}-${version}`,
  quote: { lines: [], tax: { currency: 'JPY', price_mode: 'INCLUSIVE', total_gross_jpy: '110', total_net_jpy: '100', total_tax_jpy: '10', buckets: [] } },
});

describe('guest cart UI state', () => {
  it('does not let a slow poll overwrite a newer mutation or cart generation', async () => {
    let resolvePoll!: (snapshot: Cart) => void;
    let state = guestCartReducer(initialGuestCartState, { type: 'cart-received', cart: cart(8, 12) });
    const slowPoll = new Promise<Cart>((resolve) => { resolvePoll = resolve; });
    const applyPoll = slowPoll.then((snapshot) => { state = guestCartReducer(state, { type: 'cart-received', cart: snapshot }); });

    state = guestCartReducer(state, { type: 'cart-received', cart: cart(8, 13) });
    resolvePoll(cart(8, 12));
    await applyPoll;
    expect(state.cart).toMatchObject({ generation: 8, version: 13 });

    state = guestCartReducer(state, { type: 'cart-received', cart: cart(9, 0) });
    state = guestCartReducer(state, { type: 'cart-received', cart: cart(8, 14) });
    expect(state.cart).toMatchObject({ generation: 9, version: 0 });
  });

  it('discards a preview that resolves after the cart has been edited', () => {
    const requested = cart(2, 4);
    let state = guestCartReducer(initialGuestCartState, { type: 'cart-received', cart: requested });
    state = guestCartReducer(state, { type: 'cart-received', cart: cart(2, 5) });
    state = guestCartReducer(state, { type: 'preview-received', requested, preview: preview(2, 4) });
    expect(state.preview).toBeNull();
  });

  it('accepts same-version availability updates and clears a quote when a line becomes unavailable', () => {
    const current = cart(3, 6);
    let state = guestCartReducer(initialGuestCartState, { type: 'cart-received', cart: current });
    state = guestCartReducer(state, { type: 'preview-received', requested: current, preview: preview(3, 6) });
    expect(state.preview).not.toBeNull();

    const changedAvailability = cart(3, 6, [line(false)]);
    state = guestCartReducer(state, { type: 'cart-received', cart: changedAvailability });
    expect(state.cart?.lines[0]?.product_available).toBe(false);
    expect(state.preview).toBeNull();
  });

  it('replays a commit with the original method and quote even if caller state changes', async () => {
    const request = { preview: preview(4, 2), payment: 'CASH' as 'CASH' | 'STRIPE' };
    const seen: Array<{ fingerprint: string; payment: string }> = [];
    const pending = createReplayOperation('stable-key', request, (original) => {
      seen.push({ fingerprint: original.preview.fingerprint, payment: original.payment });
      return Promise.resolve(undefined);
    });

    request.payment = 'STRIPE';
    request.preview.fingerprint = 'changed-after-timeout';
    await pending.run();
    await pending.run();

    expect(pending.key).toBe('stable-key');
    expect(seen).toEqual([
      { fingerprint: 'quote-4-2', payment: 'CASH' },
      { fingerprint: 'quote-4-2', payment: 'CASH' },
    ]);
  });
});
