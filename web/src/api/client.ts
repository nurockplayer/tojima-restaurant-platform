import type {
  ApiErrorBody,
  Cart,
  CommitResult,
  GuestGrant,
  GuestMenu,
  OptionSelection,
  Preview,
  PublicTable,
  Order,
} from './types';

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly body: ApiErrorBody,
  ) {
    super(body.error || `Request failed (${status})`);
    this.name = 'ApiError';
  }
}

export interface CartClient {
  status(locator: string): Promise<PublicTable>;
  joinDirect(locator: string, expectedSessionID: string): Promise<GuestGrant>;
  joinSession(sessionID: string, expectedSessionID: string, pin: string): Promise<GuestGrant>;
  menuForTable(locator: string): Promise<GuestMenu>;
  menuForSession(sessionID: string, token: string): Promise<GuestMenu>;
  readCart(sessionID: string, token: string): Promise<Cart>;
  addLine(sessionID: string, token: string, cart: Cart, productKey: string, quantity: number, options: OptionSelection[], key: string): Promise<Cart>;
  setLine(sessionID: string, token: string, cart: Cart, lineID: string, quantity: number, options: OptionSelection[], key: string): Promise<Cart>;
  removeLine(sessionID: string, token: string, cart: Cart, lineID: string, key: string): Promise<Cart>;
  preview(sessionID: string, token: string, cart: Cart): Promise<Preview>;
  commit(sessionID: string, token: string, preview: Preview, paymentMethod: 'STRIPE' | 'CASH', key: string): Promise<CommitResult>;
  orders(sessionID: string, token: string): Promise<Order[]>;
}

function apiBase(): string {
  return (import.meta.env.VITE_API_BASE_URL as string | undefined)?.replace(/\/$/, '') ?? '';
}

async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
  let response: Response;
  try {
    response = await fetch(`${apiBase()}${path}`, {
      ...options,
      headers: { accept: 'application/json', ...options.headers },
    });
  } catch {
    // Keep the error generic. URLs may contain public table locators.
    throw new Error('network_unavailable');
  }
  const body = (await response.json().catch(() => ({}))) as ApiErrorBody;
  if (!response.ok) throw new ApiError(response.status, body);
  return body as T;
}

function auth(token: string, key?: string): HeadersInit {
  return {
    authorization: `Bearer ${token}`,
    ...(key ? { 'Idempotency-Key': key } : {}),
  };
}

function expected(cart: Cart) {
  return { expected_generation: cart.generation, expected_version: cart.version };
}

export const cartClient: CartClient = {
  status: (locator) => request(`/v1/guest/tables/${encodeURIComponent(locator)}`),
  joinDirect: (locator, expectedSessionID) => request(`/v1/guest/tables/${encodeURIComponent(locator)}/join`, {
    method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ expected_session_id: expectedSessionID }),
  }),
  joinSession: (sessionID, expectedSessionID, pin) => request(`/v1/guest/sessions/${encodeURIComponent(sessionID)}/join`, {
    method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ expected_session_id: expectedSessionID, ...(pin ? { pin } : {}) }),
  }),
  menuForTable: (locator) => request(`/v1/guest/tables/${encodeURIComponent(locator)}/menu?amounts=string`),
  menuForSession: (sessionID, token) => request(`/v1/guest/sessions/${encodeURIComponent(sessionID)}/menu?amounts=string`, { headers: auth(token) }),
  readCart: (sessionID, token) => request(`/v1/guest/sessions/${encodeURIComponent(sessionID)}/cart`, { headers: auth(token) }),
  addLine: (sessionID, token, cart, productKey, quantity, options, key) => request(`/v1/guest/sessions/${encodeURIComponent(sessionID)}/cart/lines`, {
    method: 'POST', headers: { ...auth(token, key), 'content-type': 'application/json' },
    body: JSON.stringify({ ...expected(cart), product_key: productKey, quantity, options }),
  }),
  setLine: (sessionID, token, cart, lineID, quantity, options, key) => request(`/v1/guest/sessions/${encodeURIComponent(sessionID)}/cart/lines/${encodeURIComponent(lineID)}`, {
    method: 'PUT', headers: { ...auth(token, key), 'content-type': 'application/json' },
    body: JSON.stringify({ ...expected(cart), quantity, options: options.map(({ group_key, option_key }) => ({ group_key, option_key })) }),
  }),
  removeLine: (sessionID, token, cart, lineID, key) => request(`/v1/guest/sessions/${encodeURIComponent(sessionID)}/cart/lines/${encodeURIComponent(lineID)}?generation=${cart.generation}&version=${cart.version}`, {
    method: 'DELETE', headers: auth(token, key),
  }),
  preview: (sessionID, token, cart) => request(`/v1/guest/sessions/${encodeURIComponent(sessionID)}/cart/preview`, {
    method: 'POST', headers: { ...auth(token), 'content-type': 'application/json' }, body: JSON.stringify(expected(cart)),
  }),
  commit: (sessionID, token, preview, paymentMethod, key) => request(`/v1/guest/sessions/${encodeURIComponent(sessionID)}/cart/commit`, {
    method: 'POST', headers: { ...auth(token, key), 'content-type': 'application/json' },
    body: JSON.stringify({ expected_generation: preview.generation, expected_version: preview.version, preview_fingerprint: preview.fingerprint, payment_method: paymentMethod }),
  }),
  orders: async (sessionID, token) => (await request<{ orders: Order[] }>(`/v1/guest/sessions/${encodeURIComponent(sessionID)}/orders`, { headers: auth(token) })).orders,
};

export function newOperationKey(): string {
  return globalThis.crypto.randomUUID();
}

export function amountJPY(amount: string | number | undefined): string {
  if (amount === undefined) return '—';
  if (typeof amount === 'number' && !Number.isSafeInteger(amount)) return '—';
  const text = String(amount);
  if (!/^-?\d+$/.test(text)) return '—';
  try {
    return `¥${BigInt(text).toLocaleString('ja-JP')}`;
  } catch {
    return '—';
  }
}
