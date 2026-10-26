import type { ApiErrorBody } from '../api/types';

export function apiBase(): string {
  return (import.meta.env.VITE_API_BASE_URL as string | undefined)?.replace(/\/$/, '') ?? '';
}

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly body: ApiErrorBody,
  ) {
    super(body.error || `Request failed (${status})`);
    this.name = 'ApiError';
  }
}

export async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  let response: Response;
  try {
    const url = init.credentials === 'same-origin' ? path : `${apiBase()}${path}`;
    response = await fetch(url, {
      ...init,
      headers: { accept: 'application/json', ...init.headers },
    });
  } catch {
    // Keep the error generic. URLs may contain public table locators.
    throw new Error('network_unavailable');
  }
  const body = (await response.json().catch(() => ({}))) as ApiErrorBody;
  if (!response.ok) throw new ApiError(response.status, body);
  return body as T;
}

export function bearerHeaders(token: string, key?: string): HeadersInit {
  return {
    ...(token ? { authorization: `Bearer ${token}` } : {}),
    ...(key ? { 'Idempotency-Key': key } : {}),
  };
}
