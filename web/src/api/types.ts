export type Amount = string | number;
export type Availability = 'AVAILABLE' | 'PENDING_CONFIRMATION' | 'PAUSED';

export interface PublicTable {
  mode: 'STAFF_OPEN' | 'SESSION_PIN' | 'DIRECT_SCAN';
  current_session_id?: string;
  open: boolean;
  expired?: boolean;
}

export interface GuestGrant {
  table_join_token: string;
  expires_at: string;
  session: { id: string; status?: string; mode?: PublicTable['mode'] };
}

export interface GuestMenu {
  currency: 'JPY';
  price_mode: 'TAX_INCLUSIVE';
  categories: Array<{
    key: string;
    name: string;
    position: number;
    products: Array<{
      key: string;
      name: string;
      description?: string;
      effective_price_minor: Amount;
      position: number;
      availability: Availability;
      selectable: boolean;
      option_groups: Array<{
        key: string;
        name: string;
        min_select: number;
        max_select: number;
        position: number;
        options: Array<{
          key: string;
          name: string;
          price_delta_minor: Amount;
          position: number;
          availability: Availability;
        }>;
      }>;
    }>;
  }>;
}

export interface OptionSelection {
  group_key: string;
  option_key: string;
}

export interface CartLine {
  line_id: string;
  product_key: string;
  product_name: string;
  product_available: boolean;
  unavailable_code?: string;
  quantity: number;
  options: Array<OptionSelection & {
    group_name: string;
    option_name: string;
    available: boolean;
    unavailable_code?: string;
  }>;
}

export interface Cart {
  session_id: string;
  generation: number;
  version: number;
  lines: CartLine[];
}

export interface TaxTotals {
  gross_jpy: Amount;
  net_jpy: Amount;
  tax_jpy: Amount;
}

export interface TaxSnapshot {
  currency: 'JPY';
  price_mode: 'INCLUSIVE';
  total_gross_jpy: Amount;
  total_net_jpy: Amount;
  total_tax_jpy: Amount;
  buckets: Array<{
    rate_numerator: Amount;
    rate_denominator: Amount;
    gross_jpy: Amount;
    net_jpy: Amount;
    tax_jpy: Amount;
  }>;
}

export interface QuoteLine {
  line_id: string;
  product_key: string;
  name: string;
  base_gross_jpy: Amount;
  options: Array<{
    group_key: string;
    option_key: string;
    name: string;
    price_gross_jpy: Amount;
  }>;
  quantity: number;
}

export interface Quote {
  lines: QuoteLine[];
  tax: TaxSnapshot;
}

export interface Preview {
  session_id: string;
  generation: number;
  version: number;
  fingerprint: string;
  quote: Quote;
}

export interface Order {
  order_id: string;
  session_id?: string;
  generation: number;
  payment_method: 'STRIPE' | 'CASH';
  quote?: Quote;
  total_gross_jpy?: Amount;
  requires_staff_acceptance?: boolean;
  created_at: string;
}

export interface CommitResult {
  outcome: 'COMMITTED' | 'ALREADY_COMMITTED';
  order: Order;
  cart: Cart;
}

export interface ApiErrorBody {
  error: string;
  cart?: Cart;
  quote?: Preview;
}
