import { useLayoutEffect, useRef } from 'react';
import { amountJPY } from '../../api/client';
import type { Cart, CartLine, GuestMenu, OptionSelection, Preview } from '../../api/types';
import { GuestSheet } from './GuestSheet';
import type { ReactNode } from 'react';
import styles from './CartSheet.module.css';

type Product = GuestMenu['categories'][number]['products'][number];
const ja = {
  cart: '共有カート', empty: 'カートはまだ空です。', refresh: '共有内容を更新', remove: '削除',
  available: '注文できます', unavailable: '現在注文できません', emergencyHold: '販売停止中', itemMissing: 'メニューで確認できない商品です。削除してください。',
  noOption: '選択しない', unavailableLines: '注文できない商品があります。削除または内容を変更してから確認してください。',
  preview: '金額を確認する', previewHint: '注文を確定する前に最新の金額と内容をご確認ください。',
  total: '税込合計', tax: '内消費税', method: '今回のお支払い方法', stripe: 'クレジットカード', cash: '店頭で現金',
  demoCash: '現金（デモ・テスト専用）', stripeNote: '注文を確定しても、お支払いは完了しません。', cashNote: '現金のお支払いはスタッフが確認します。',
  confirm: 'この内容で注文を確定', working: '送信中…', loading: '確認しています…', back: 'カートへ戻る',
  amountError: '金額を安全に表示できません。注文を続けられません。', retry: '前回の操作を照合して再試行',
  stale: 'カートまたは価格が更新されました。最新内容で金額を確認し直してください。',
};
const copy: Record<'ja' | 'zh', Record<keyof typeof ja, string>> = { ja, zh: {
  cart: '共用購物車', empty: '購物車目前是空的。', refresh: '更新共用內容', remove: '移除',
  available: '可點選', unavailable: '目前無法點選', emergencyHold: '暫停販售', itemMissing: '菜單中找不到此商品，請移除。',
  noOption: '不選擇', unavailableLines: '有無法點餐的品項，請移除或調整後再確認。',
  preview: '確認金額', previewHint: '送出前請確認最新品項和金額。', total: '含稅總額', tax: '其中消費稅', method: '本次付款方式',
  stripe: '信用卡', cash: '店內現金', demoCash: '現金（展示測試專用）', stripeNote: '確認訂單後，付款仍未完成。', cashNote: '現金付款由服務人員確認收款。',
  confirm: '確認並送出訂單', working: '送出中…', loading: '正在確認…', back: '返回購物車', amountError: '無法安全顯示金額，暫時不能繼續送單。',
  retry: '查核並重試上一項操作', stale: '購物車或價格已更新，請重新確認最新內容與金額。',
} };

export interface CartSheetProps {
  isOpen: boolean;
  onOpenChange: (open: boolean) => void;
  cart: Cart | null;
  preview: Preview | null;
  products: Map<string, Product>;
  locale: 'ja' | 'zh';
  demo: boolean;
  demoNotice?: ReactNode;
  syncNotice?: ReactNode;
  syncReasonID?: string;
  degraded?: boolean;
  payment: 'CASH' | 'STRIPE';
  cashEnabled: boolean;
  onPayment: (payment: 'CASH' | 'STRIPE') => void;
  busy: boolean;
  uncertain: boolean;
  unavailable: boolean;
  previewMatchesCart: boolean;
  previewAmountsSafe: boolean;
  error: string;
  notice: string;
  onPreview: () => void;
  onClearPreview: () => void;
  onInvalidated: () => void;
  onConfirm: () => void;
  onRefresh: () => void;
  onRetry: () => void;
  onRemove: (line: CartLine) => void;
  onSetLine: (line: CartLine, quantity: number, options: OptionSelection[]) => void;
  fallbackFocus: () => HTMLElement | null;
}

function safeAmount(amount: string | number | undefined): string {
  return typeof amount === 'number' && !Number.isSafeInteger(amount) ? '—' : amountJPY(amount);
}

export function CartSheet(props: CartSheetProps) {
  const { isOpen, onOpenChange, cart, preview, products, locale, demo, demoNotice, syncNotice, syncReasonID, degraded = false, payment, cashEnabled, onPayment, busy, uncertain, unavailable,
    previewMatchesCart, previewAmountsSafe, error, notice, onPreview, onClearPreview, onInvalidated, onConfirm, onRefresh, onRetry, onRemove, onSetLine, fallbackFocus } = props;
  const t = copy[locale];
  const heading = useRef<HTMLHeadingElement>(null);
  const previewButton = useRef<HTMLButtonElement>(null);
  const previousPreview = useRef<Preview | null>(null);
  const returning = useRef(false);
  useLayoutEffect(() => {
    if (isOpen && preview && !previousPreview.current) {
      heading.current?.focus();
    } else if (isOpen && !preview && previousPreview.current) {
      if (returning.current) {
        (previewButton.current && !previewButton.current.disabled ? previewButton.current : heading.current)?.focus();
      } else {
        onInvalidated();
        heading.current?.focus();
      }
    }
    previousPreview.current = preview;
    returning.current = false;
  }, [isOpen, preview, onInvalidated]);
  useLayoutEffect(() => {
    // Removing the focused cart row must leave keyboard navigation in the sheet.
    if (isOpen && document.activeElement === document.body) heading.current?.focus();
  }, [isOpen, cart, products]);
  const visibleError = error;

  return <GuestSheet isOpen={isOpen} onOpenChange={onOpenChange} title={preview ? t.preview : t.cart} titleRef={heading}
    locale={locale} notice={demoNotice} fallbackFocus={fallbackFocus}>
    <div className={styles.content}>
      {syncNotice}
      {preview ? <>
        <p>{t.previewHint}</p>
        <div className={styles.quoteLines}>{preview.quote.lines.map((line) => <article className={styles.quoteLine} key={line.line_id}>
          <div><strong>{line.name}</strong><span>{line.options.map((option) => option.name).join(' · ')}</span><small>{safeAmount(line.base_gross_jpy)} × {line.quantity}</small></div>
          <strong>{safeAmount(line.base_gross_jpy)} × {line.quantity}</strong>
          {line.options.map((option) => <small className={styles.quoteOption} key={`${line.line_id}-${option.group_key}-${option.option_key}`}>＋ {option.name} · {safeAmount(option.price_gross_jpy)} × {line.quantity}</small>)}
        </article>)}</div>
        <div className={styles.total}><span>{t.total}</span><strong>{safeAmount(preview.quote.tax.total_gross_jpy)}</strong></div>
        <div className={styles.total}><span>{t.tax}</span><span>{safeAmount(preview.quote.tax.total_tax_jpy)}</span></div>
        <fieldset className={styles.payment}><legend>{t.method}</legend>
          {!demo && <label><input type="radio" name="payment" checked={payment === 'STRIPE'} onChange={() => onPayment('STRIPE')} />{t.stripe}</label>}
          {cashEnabled && <label><input type="radio" name="payment" checked={payment === 'CASH'} onChange={() => onPayment('CASH')} />{demo ? t.demoCash : t.cash}</label>}
        </fieldset>
        <p>{payment === 'STRIPE' ? t.stripeNote : demo ? t.demoCash : t.cashNote}</p>
        {!previewAmountsSafe && <p className={styles.error} role="alert">{t.amountError}</p>}
        {previewMatchesCart && <button className={styles.primary} aria-describedby={degraded ? syncReasonID : undefined} disabled={busy || unavailable || uncertain || !previewAmountsSafe || degraded} onClick={onConfirm}>{busy ? t.working : t.confirm}</button>}
        <button className={styles.control} disabled={busy} onClick={() => { returning.current = true; onClearPreview(); }}>{t.back}</button>
      </> : <>
        <button className={styles.control} aria-label={t.refresh} disabled={busy} onClick={onRefresh}>↻</button>
        {!cart?.lines.length ? <p>{t.empty}</p> : <div className={styles.cartLines}>{cart.lines.map((line) => {
          const product = products.get(line.product_key);
          if (!product) return <article className={styles.line} data-cart-line={line.line_id} key={line.line_id}>
            <strong>{line.product_name || line.product_key}</strong><p>{line.unavailable_code === 'COMPANY_EMERGENCY_HOLD' ? t.emergencyHold : t.itemMissing}</p>
            <p>{line.options.map((option) => option.option_name).join(' · ')}</p>
            <button className={styles.control} disabled={busy || uncertain} onClick={() => onRemove(line)}>{t.remove}</button>
          </article>;
          const selected = line.options;
          const held = line.unavailable_code === 'COMPANY_EMERGENCY_HOLD' || product.company_emergency_hold_active === true;
          const lineAvailable = line.product_available && product.selectable && !held && line.options.every((option) => option.available);
          return <article className={styles.line} data-cart-line={line.line_id} key={line.line_id}>
            <div className={styles.lineHead}><div><strong>{line.product_name || product.name}</strong><p>{held ? t.emergencyHold : lineAvailable ? t.available : t.unavailable}</p>
              {selected.length > 0 && <p>{selected.map((option) => `${option.group_name}: ${option.option_name}${option.available ? '' : ` · ${t.unavailable}`}`).join('、')}</p>}
            </div><button className={styles.control} disabled={busy || uncertain} onClick={() => onRemove(line)}>{t.remove}</button></div>
            {product.option_groups.map((group) => <fieldset className={styles.options} data-cart-option-group={group.key} key={group.key}><legend>{group.name}</legend>
              {group.options.map((option) => <label key={option.key}><input type={group.max_select === 1 ? 'radio' : 'checkbox'} name={`${line.line_id}-${group.key}`}
                checked={selected.some((item) => item.group_key === group.key && item.option_key === option.key)}
                disabled={busy || uncertain || !product.selectable || held || option.availability !== 'AVAILABLE'} onChange={() => {
                  const exists = selected.some((item) => item.group_key === group.key && item.option_key === option.key);
                  const groupSelections = selected.filter((item) => item.group_key !== group.key);
                  const sameGroup = selected.filter((item) => item.group_key === group.key);
                  const next = exists ? selected.filter((item) => !(item.group_key === group.key && item.option_key === option.key)) : [...groupSelections, ...sameGroup.slice(0, Math.max(0, group.max_select - 1)), { group_key: group.key, option_key: option.key }];
                  const count = next.filter((item) => item.group_key === group.key).length;
                  if (count >= group.min_select && count <= group.max_select) onSetLine(line, line.quantity, next);
                }} />{option.name}</label>)}
              {group.min_select === 0 && group.max_select === 1 && <button className={styles.control} disabled={busy || uncertain} onClick={() => onSetLine(line, line.quantity, selected.filter((item) => item.group_key !== group.key))}>{t.noOption}</button>}
            </fieldset>)}
            <div className={styles.lineFoot}><span>{amountJPY(product.effective_price_minor)}</span><div className={styles.stepper}>
              <button aria-label={`${product.name} −`} disabled={busy || uncertain || line.quantity <= 1} onClick={() => onSetLine(line, line.quantity - 1, line.options)}>−</button>
              <span data-quantity>{line.quantity}</span>
              <button aria-label={`${product.name} +`} disabled={busy || uncertain || line.quantity >= 99} onClick={() => onSetLine(line, line.quantity + 1, line.options)}>＋</button>
            </div></div>
          </article>;
        })}</div>}
        {unavailable && <p>{t.unavailableLines}</p>}
        <button className={styles.primary} ref={previewButton} aria-describedby={degraded ? syncReasonID : undefined} disabled={!cart?.lines.length || busy || uncertain || unavailable || degraded} onClick={onPreview}>{busy ? t.loading : t.preview}</button>
      </>}
      {uncertain && <button className={styles.control} disabled={busy} onClick={onRetry}>{t.retry}</button>}
      {visibleError && <p className={styles.error} role="alert">{visibleError}</p>}
      {notice && <p role="status">{notice}</p>}
    </div>
  </GuestSheet>;
}
