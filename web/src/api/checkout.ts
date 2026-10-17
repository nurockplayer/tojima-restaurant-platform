import { ApiError } from './client';
import type { StaffContext } from './operations';

export type CheckoutState = 'CREATE_PENDING' | 'CREATING' | 'UNKNOWN' | 'OPEN' | 'PROCESSING' | 'PAID' | 'EXPIRED' | 'FAILED' | 'REVIEW_REQUIRED';

export interface CheckoutStatus {
  order_id: string;
  state: CheckoutState;
  checkout_status?: string;
  payment_status?: string;
  provider_expires_at?: string;
  last_reconciled_at?: string | null;
  payment_review_required: boolean;
  resume_available: boolean;
}

export interface CheckoutResume { url: string; expires_at?: string }

const inFlightBootstraps = new Map<string, Promise<void>>();
let fallbackBootstrapChain: Promise<void> = Promise.resolve();

export const checkoutClient = {
  ensureContext(sessionID: string, tableJoinToken: string): Promise<void> {
    const existing = inFlightBootstraps.get(sessionID);
    if (existing) return existing;
    const pending = withContextLock(() => bootstrap(sessionID, tableJoinToken));
    inFlightBootstraps.set(sessionID, pending);
    void pending.finally(() => {
      if (inFlightBootstraps.get(sessionID) === pending) inFlightBootstraps.delete(sessionID);
    }).catch(() => undefined);
    return pending;
  },

  status(sessionID: string, tableJoinToken: string, orderID: string): Promise<CheckoutStatus> {
    return request<CheckoutStatus>(`${guestOrderPath(sessionID, orderID)}/checkout`, {
      headers: { authorization: `Bearer ${tableJoinToken}` },
    });
  },

  resume(sessionID: string, orderID: string): Promise<CheckoutResume> {
    return request<CheckoutResume>(`${guestOrderPath(sessionID, orderID)}/checkout/resume`, { method: 'POST' });
  },

  staffStatus(context: StaffContext, token: string, tableID: string, sessionID: string, orderID: string): Promise<CheckoutStatus> {
    return request<CheckoutStatus>(`${staffOrderPath(context, tableID, sessionID, orderID)}/payment`, {
      headers: { authorization: `Bearer ${token}` },
    });
  },

  reconcile(context: StaffContext, token: string, tableID: string, sessionID: string, orderID: string): Promise<CheckoutStatus> {
    return request<CheckoutStatus>(`${staffOrderPath(context, tableID, sessionID, orderID)}/payment/reconcile`, {
      method: 'POST',
      headers: { authorization: `Bearer ${token}`, 'content-type': 'application/json' },
      body: '{}',
    });
  },
};

// Kept as a tiny boundary so UI tests can assert that only the private resume
// response is navigated to without asking jsdom to perform external navigation.
export const checkoutNavigation = {
  assign(url: string): void { window.location.assign(url); },
};

async function bootstrap(sessionID: string, tableJoinToken: string): Promise<void> {
  const response = await fetch(`/v1/guest/sessions/${enc(sessionID)}/checkout-context`, {
    method: 'POST',
    credentials: 'same-origin',
    cache: 'no-store',
    referrerPolicy: 'no-referrer',
    headers: { accept: 'application/json', authorization: `Bearer ${tableJoinToken}` },
  }).catch(() => { throw new Error('network_unavailable'); });
  if (!response.ok) {
    const body = await response.json().catch(() => ({}));
    throw new ApiError(response.status, body as { error: string });
  }
  if (response.status !== 204) throw new Error('checkout_context_unavailable');
}

async function request<T>(path: string, init: RequestInit): Promise<T> {
  let response: Response;
  try {
    response = await fetch(path, {
      ...init,
      credentials: 'same-origin',
      cache: 'no-store',
      referrerPolicy: 'no-referrer',
      headers: { accept: 'application/json', ...init.headers },
    });
  } catch {
    throw new Error('network_unavailable');
  }
  const body = await response.json().catch(() => ({}));
  if (!response.ok) throw new ApiError(response.status, body as { error: string });
  return body as T;
}

async function withContextLock<T>(run: () => Promise<T>): Promise<T> {
  const locks = typeof navigator === 'undefined' ? undefined : (navigator as Navigator & { locks?: LockManager }).locks;
  if (locks) return locks.request('tojima-checkout-context', { mode: 'exclusive' }, run);

  // The cookie is origin-wide. Serialize different sessions too when Web Locks
  // are unavailable, so a late bootstrap cannot replace the winning context.
  const pending = fallbackBootstrapChain.then(run);
  fallbackBootstrapChain = pending.then(() => undefined, () => undefined);
  return pending;
}

function guestOrderPath(sessionID: string, orderID: string): string {
  return `/v1/guest/sessions/${enc(sessionID)}/orders/${enc(orderID)}`;
}

function staffOrderPath(context: StaffContext, tableID: string, sessionID: string, orderID: string): string {
  return `/v1/staff/tenants/${enc(context.tenantID)}/stores/${enc(context.storeID)}/tables/${enc(tableID)}/sessions/${enc(sessionID)}/orders/${enc(orderID)}`;
}

function enc(value: string): string { return encodeURIComponent(value); }
