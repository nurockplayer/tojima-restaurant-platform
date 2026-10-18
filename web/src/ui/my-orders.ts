import { isOrderReadGrant, type OrderReadGrant } from '../api/order-read';

const key = 'tojima.my-orders.v1';
export const myOrdersChanged = 'tojima:my-orders-changed';
// Retain the current page's links even when storage is disabled/full. No claim
// of durable history: this is a bounded convenience list of bearer links.
let fallback: OrderReadGrant[] = [];
let lastStoredValue: string | null | undefined;
export function loadMyOrders(): OrderReadGrant[] {
  try {
    const stored = localStorage.getItem(key);
    // Keep an unsuccessful write in memory while storage still holds the same
    // older value. A genuine cross-tab storage change replaces that snapshot.
    if (stored !== lastStoredValue) {
      lastStoredValue = stored;
      let raw: unknown;
      try { raw = JSON.parse(stored ?? '[]'); } catch { raw = []; }
      fallback = Array.isArray(raw) ? raw.filter(isOrderReadGrant).slice(0, 100) : [];
    }
  } catch { /* Private browsing/storage denial: use the in-memory list. */ }
  return fallback.filter((g) => Date.parse(g.expires_at) > Date.now());
}
export function rememberOrderGrant(grant: OrderReadGrant): void {
  fallback = [grant, ...loadMyOrders().filter((g) => g.order_id !== grant.order_id)].slice(0, 100);
  try { const serialized = JSON.stringify(fallback); localStorage.setItem(key, serialized); lastStoredValue = serialized; } catch { /* In-memory fallback remains usable. */ }
  window.dispatchEvent(new Event(myOrdersChanged));
}
