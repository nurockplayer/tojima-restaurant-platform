import type { Cart, Preview } from '../api/types';

export interface GuestCartState {
  cart: Cart | null;
  preview: Preview | null;
}

export type GuestCartAction =
  | { type: 'cart-received'; cart: Cart }
  | { type: 'preview-received'; requested: Cart; preview: Preview }
  | { type: 'preview-cleared' }
  | { type: 'session-cleared' };

export const initialGuestCartState: GuestCartState = { cart: null, preview: null };

function isUnavailable(cart: Cart): boolean {
  return cart.lines.some((line) => !line.product_available || line.options.some((option) => !option.available));
}

export function guestCartReducer(state: GuestCartState, action: GuestCartAction): GuestCartState {
  if (action.type === 'session-cleared') return initialGuestCartState;
  if (action.type === 'preview-cleared') return state.preview ? { ...state, preview: null } : state;

  if (action.type === 'preview-received') {
    const current = state.cart;
    if (!current || current.generation !== action.requested.generation || current.version !== action.requested.version) return state;
    if (action.preview.generation !== current.generation || action.preview.version !== current.version || isUnavailable(current)) return state;
    return { ...state, preview: action.preview };
  }

  const current = state.cart;
  if (current && (action.cart.generation < current.generation || (action.cart.generation === current.generation && action.cart.version < current.version))) return state;

  const versionChanged = !!current && (action.cart.generation !== current.generation || action.cart.version !== current.version);
  // Availability can change as a menu is paused without changing the shared cart version.
  // Accept equal-version snapshots, but never leave a now-unavailable quote confirmable.
  const invalidatePreview = !!state.preview && (versionChanged || isUnavailable(action.cart));
  return {
    cart: action.cart,
    preview: invalidatePreview ? null : state.preview,
  };
}

export interface ReplayOperation<T> {
  key: string;
  run: () => Promise<T>;
}

/** Bind an idempotent retry to an immutable copy of its original request data. */
export function createReplayOperation<P, T>(key: string, payload: P, execute: (payload: P) => Promise<T>): ReplayOperation<T> {
  const original = structuredClone(payload);
  return { key, run: () => execute(original) };
}
