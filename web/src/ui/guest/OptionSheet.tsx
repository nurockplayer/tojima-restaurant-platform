import { useId, useRef, useState, type ReactNode } from 'react';
import type { OptionSelection } from '../../api/types';
import { amountJPY } from '../../api/client';
import { GuestSheet } from './GuestSheet';
import { isMenuAmountSafe } from './MenuList';
import type { GuestProduct } from './MenuList';
import styles from './OptionSheet.module.css';

type Locale = 'ja' | 'zh';

export interface OptionSheetProps {
  product: GuestProduct | null;
  isOpen: boolean;
  onOpenChange: (isOpen: boolean) => void;
  locale: Locale;
  selected: OptionSelection[];
  quantity: number;
  onChoose: (groupKey: string, optionKey: string, maxSelect: number) => void;
  onClear?: (groupKey: string) => void;
  onQuantity: (quantity: number) => void;
  onAdd: () => void;
  busy?: boolean;
  uncertain?: boolean;
  amountSafe?: boolean;
  fallbackFocus?: () => HTMLElement | null;
  children?: ReactNode;
  notice?: ReactNode;
}

const ja = {
  required: '必須', requiredError: '必須オプションを選択してください。', noOption: '選択しない',
  quantity: '数量', add: 'カートに追加', available: '注文できます', unavailable: '現在注文できません',
  pending: '店舗確認中', emergency: '販売停止中', amountError: '金額を安全に表示できません。注文を続けられません。',
};
const copy: Record<Locale, Record<keyof typeof ja, string>> = {
  ja,
  zh: { required: '必選', requiredError: '請選擇必選項目。', noOption: '不選擇', quantity: '數量', add: '加入購物車', available: '可點選', unavailable: '目前無法點選', pending: '門店確認中', emergency: '暫停販售', amountError: '無法安全顯示金額，暫時不能繼續送單。' },
};

function safeAmount(value: string | number) {
  return typeof value === 'number' && !Number.isSafeInteger(value) ? '—' : amountJPY(value);
}

export function OptionSheet({ product, isOpen, onOpenChange, locale, selected, quantity, onChoose, onClear, onQuantity, onAdd, busy = false, uncertain = false, amountSafe = true, fallbackFocus, children, notice }: OptionSheetProps) {
  const strings = copy[locale];
  const id = useId();
  const [showRequiredErrors, setShowRequiredErrors] = useState(false);
  const groupRefs = useRef(new Map<string, HTMLFieldSetElement>());
  const productSafe = product ? isMenuAmountSafe(product.effective_price_minor) && product.option_groups.every((group) => group.options.every((option) => isMenuAmountSafe(option.price_delta_minor))) : true;
  const unavailable = !product || !product.selectable || product.company_emergency_hold_active === true;
  const errorGroups = product?.option_groups.filter((group) => showRequiredErrors && selected.filter((item) => item.group_key === group.key).length < group.min_select) ?? [];

  const add = () => {
    if (!product) return;
    const missing = product.option_groups.filter((group) => selected.filter((item) => item.group_key === group.key).length < group.min_select);
    if (missing.length > 0) {
      setShowRequiredErrors(true);
      const firstSelectable = groupRefs.current.get(missing[0]!.key)?.querySelector<HTMLInputElement>('input:not(:disabled)');
      firstSelectable?.focus();
      return;
    }
    setShowRequiredErrors(false);
    onAdd();
  };

  return <GuestSheet
    fallbackFocus={fallbackFocus}
    isOpen={isOpen}
    locale={locale}
    notice={notice}
    onOpenChange={(open) => { if (!open) setShowRequiredErrors(false); onOpenChange(open); }}
    title={product?.name ?? ''}
  >
    {product && <div className={styles.content}>
      {product.description && <p className={styles.description}>{product.description}</p>}
      <p className={styles.price}>{safeAmount(product.effective_price_minor)}</p>
      <p className={product.selectable && !product.company_emergency_hold_active ? styles.available : styles.unavailable}>
        {product.company_emergency_hold_active ? strings.emergency : product.selectable ? strings.available : product.availability === 'PENDING_CONFIRMATION' ? strings.pending : strings.unavailable}
      </p>
      {product.option_groups.map((group) => {
        const chosen = selected.filter((item) => item.group_key === group.key);
        const invalid = errorGroups.some((error) => error.key === group.key);
        const describedBy = invalid ? `${id}-required-${group.key}` : undefined;
        return <fieldset
          aria-describedby={describedBy}
          aria-invalid={invalid || undefined}
          className={styles.group}
          data-required-group={group.key}
          key={group.key}
          ref={(element) => { if (element) groupRefs.current.set(group.key, element); else groupRefs.current.delete(group.key); }}
        >
          <legend>{group.name} {group.min_select > 0 && <span className={styles.required}>{strings.required}</span>}</legend>
          <div className={styles.options}>
            {group.options.map((option) => {
              const checked = chosen.some((item) => item.option_key === option.key);
              const disabled = !product.selectable || product.company_emergency_hold_active === true || option.availability !== 'AVAILABLE' || (group.max_select !== 1 && !checked && chosen.length >= group.max_select);
              return <label className={styles.option} key={option.key}>
                <input
                  checked={checked}
                  disabled={disabled}
                  name={`${id}-${group.key}`}
                  onChange={() => onChoose(group.key, option.key, group.max_select)}
                  type={group.max_select === 1 ? 'radio' : 'checkbox'}
                />
                <span>{option.name}</span><small>{safeAmount(option.price_delta_minor)}</small>
              </label>;
            })}
          </div>
          {group.min_select === 0 && group.max_select === 1 && onClear && <button className={styles.clear} onClick={() => onClear(group.key)} type="button">{strings.noOption}</button>}
          {invalid && <p className={styles.error} id={describedBy} role="alert">{strings.requiredError}</p>}
        </fieldset>;
      })}
      <label className={styles.quantity}>{strings.quantity}
        <select aria-label={`${strings.quantity} ${product.name}`} onChange={(event) => onQuantity(Number(event.currentTarget.value))} value={quantity}>
          {Array.from({ length: 10 }, (_, index) => index + 1).map((value) => <option key={value} value={value}>{value}</option>)}
        </select>
      </label>
      {!productSafe && <p className={styles.error} role="alert">{strings.amountError}</p>}
      {children}
      <button className={styles.add} disabled={busy || uncertain || unavailable || !productSafe || !amountSafe} onClick={add} type="button">{strings.add}</button>
    </div>}
  </GuestSheet>;
}
