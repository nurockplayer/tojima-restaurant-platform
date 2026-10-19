import type { Cart, GuestMenu } from '../../api/types';

function integerAmount(value: string | number): bigint | null {
  if (typeof value === 'number') {
    if (!Number.isSafeInteger(value)) return null;
    return BigInt(value);
  }
  if (!/^-?\d+$/.test(value)) return null;
  try {
    return BigInt(value);
  } catch {
    return null;
  }
}

/** Return the exact estimated gross total in minor units, or null when any line cannot be priced safely. */
export function cartEstimate(cart: Cart | null, menu: GuestMenu): string | null {
  if (!cart || cart.lines.length === 0) return null;

  const products = new Map(menu.categories.flatMap((category) => category.products.map((product) => [product.key, product] as const)));
  let total = 0n;

  for (const line of cart.lines) {
    const product = products.get(line.product_key);
    if (!product || !line.product_available || product.availability !== 'AVAILABLE' || !product.selectable || product.company_emergency_hold_active) return null;
    if (!Number.isSafeInteger(line.quantity) || line.quantity <= 0) return null;

    const base = integerAmount(product.effective_price_minor);
    if (base === null || base < 0n) return null;

    let unitPrice = base;
    const selections = new Set<string>();
    for (const selected of line.options) {
      const identity = `${selected.group_key}\u0000${selected.option_key}`;
      if (selections.has(identity)) return null;
      selections.add(identity);
      const group = product.option_groups.find((candidate) => candidate.key === selected.group_key);
      const option = group?.options.find((candidate) => candidate.key === selected.option_key);
      if (!option || option.availability !== 'AVAILABLE' || !selected.available) return null;
      const delta = integerAmount(option.price_delta_minor);
      if (delta === null) return null;
      unitPrice += delta;
    }

    for (const group of product.option_groups) {
      const count = line.options.filter((selection) => selection.group_key === group.key).length;
      if (count < group.min_select || count > group.max_select) return null;
    }
    if (unitPrice < 0n) return null;
    total += unitPrice * BigInt(line.quantity);
  }

  return total.toString();
}
