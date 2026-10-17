import { ApiError } from './client';

export type Amount = string | number;
export type Availability = 'PENDING_CONFIRMATION' | 'AVAILABLE' | 'PAUSED';

export interface CatalogOption {
  key: string;
  name: string;
  price_delta_minor: Amount;
  position: number;
  active: boolean;
  availability?: Availability;
  availability_version?: number;
}

export interface CatalogOptionGroup {
  key: string;
  name: string;
  min_select: number;
  max_select: number;
  position: number;
  options: CatalogOption[];
}

export interface CatalogProduct {
  key: string;
  name: string;
  description?: string;
  base_price_minor: Amount;
  effective_price_minor?: Amount;
  override_active?: boolean;
  override_price_minor?: Amount | null;
  price_override_version?: number;
  position: number;
  active: boolean;
  availability?: Availability;
  availability_version?: number;
  selectable?: boolean;
  option_groups: CatalogOptionGroup[];
}

export interface CatalogCategory {
  key: string;
  name: string;
  position: number;
  products: CatalogProduct[];
}

export interface CatalogData { categories: CatalogCategory[] }
export interface CompanyDraft extends CatalogData { version: number }
export interface CompanyPublished extends CatalogData { revision: number }
export interface StoreCatalog extends CompanyPublished {
  mode: 'LINKED' | 'DETACHED';
  can_pause_availability: boolean;
  can_confirm_availability: boolean;
  can_edit_prices: boolean;
  can_detach: boolean;
}

export interface PublicationIssue {
  code: string;
  product_key: string;
  group_key?: string;
  store_id?: string;
  store_name?: string;
  required_count?: number;
  available_count?: number;
}

export interface CompanyPreview {
  draft_version: number;
  published_revision: number;
  linked_store_count: number;
  can_publish: boolean;
  issues: PublicationIssue[];
  preview_fingerprint: string;
}

export interface PublishResult { revision: number; draft_version: number }
export interface PriceOverride { product_key: string; price_minor: string; active: boolean; version: number }
export interface AvailabilityResult { status: Availability; version: number }

export interface CatalogContext { tenantID: string; storeID: string }

async function request<T>(path: string, token: string, init: RequestInit = {}): Promise<T> {
  let response: Response;
  try {
    const base = (import.meta.env.VITE_API_BASE_URL as string | undefined)?.replace(/\/$/, '') ?? '';
    response = await fetch(`${base}${path}`, {
      ...init,
      headers: { accept: 'application/json', authorization: `Bearer ${token}`, ...init.headers },
    });
  } catch {
    throw new Error('network_unavailable');
  }
  const body = await response.json().catch(() => ({}));
  if (!response.ok) throw new ApiError(response.status, body as { error: string; [key: string]: unknown });
  return body as T;
}

const json = (body: unknown): RequestInit => ({
  method: 'POST',
  headers: { 'content-type': 'application/json' },
  body: JSON.stringify(body),
});

const putJSON = (body: unknown): RequestInit => ({ ...json(body), method: 'PUT' });
const tenantBase = (tenantID: string) => `/v1/admin/tenants/${enc(tenantID)}`;
const storeBase = (ctx: CatalogContext) => `${tenantBase(ctx.tenantID)}/stores/${enc(ctx.storeID)}`;

export const catalogClient = {
  companyPublished: (tenantID: string, token: string) => request<CompanyPublished>(`${tenantBase(tenantID)}/menu/published?amounts=string`, token),
  companyDraft: (tenantID: string, token: string) => request<CompanyDraft>(`${tenantBase(tenantID)}/menu/draft?amounts=string`, token),
  saveCompanyDraft: (tenantID: string, token: string, draft: CompanyDraft) => request<unknown>(`${tenantBase(tenantID)}/menu/draft`, token, putJSON(toNumericDraft(draft))),
  previewCompany: (tenantID: string, token: string, expectedVersion: number) => request<CompanyPreview>(`${tenantBase(tenantID)}/menu/preview`, token, json({ expected_version: expectedVersion })),
  publishCompany: (tenantID: string, token: string, expectedVersion: number, fingerprint: string) => request<PublishResult>(`${tenantBase(tenantID)}/menu/publish`, token, json({ expected_version: expectedVersion, preview_fingerprint: fingerprint })),
  storeMenu: (ctx: CatalogContext, token: string) => request<StoreCatalog>(`${storeBase(ctx)}/menu?amounts=string`, token),
  storeDraft: (ctx: CatalogContext, token: string) => request<CompanyDraft>(`${storeBase(ctx)}/menu/draft?amounts=string`, token),
  saveStoreDraft: (ctx: CatalogContext, token: string, draft: CompanyDraft) => request<unknown>(`${storeBase(ctx)}/menu/draft`, token, putJSON(toNumericDraft(draft))),
  publishStoreDraft: (ctx: CatalogContext, token: string, expectedVersion: number) => request<PublishResult>(`${storeBase(ctx)}/menu/publish`, token, json({ expected_version: expectedVersion })),
  detachStore: (ctx: CatalogContext, token: string, expectedRevision: number) => request<PublishResult>(`${storeBase(ctx)}/menu/detach`, token, json({ expected_revision: expectedRevision })),
  setOverride: (ctx: CatalogContext, token: string, productKey: string, priceMinor: string, expectedVersion: number) => request<PriceOverride>(`${storeBase(ctx)}/menu/prices/${enc(productKey)}`, token, {
    method: 'PUT', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ price_minor: numericAmount(priceMinor), expected_version: expectedVersion }),
  }),
  resetOverride: (ctx: CatalogContext, token: string, productKey: string, expectedVersion: number) => request<PriceOverride>(`${storeBase(ctx)}/menu/prices/${enc(productKey)}/reset`, token, json({ expected_version: expectedVersion })),
  setAvailability: (ctx: CatalogContext, token: string, change: { target: { kind: 'PRODUCT' | 'OPTION'; product_key: string; group_key?: string; option_key?: string }; status: Availability; reason?: string; expected_version: number }) => request<AvailabilityResult>(`${storeBase(ctx)}/menu/availability`, token, putJSON(change)),
};

export function toNumericDraft(draft: CompanyDraft): CompanyDraft {
  return {
    version: draft.version,
    categories: draft.categories.map((category) => ({
      ...category,
      products: category.products.map((product) => ({
        ...product,
        base_price_minor: numericAmount(product.base_price_minor),
        option_groups: product.option_groups.map((group) => ({
          ...group,
          options: group.options.map((option) => ({ ...option, price_delta_minor: numericAmount(option.price_delta_minor) })),
        })),
      })),
    })),
  };
}

function numericAmount(value: Amount): number {
  const text = String(value);
  if (!/^\d+$/.test(text)) throw new Error('invalid_amount');
  const amount = BigInt(text);
  if (amount > BigInt(Number.MAX_SAFE_INTEGER)) throw new Error('amount_precision_unsupported');
  return Number(amount);
}

function enc(value: string) { return encodeURIComponent(value); }
