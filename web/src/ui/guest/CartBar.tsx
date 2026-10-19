import { useLayoutEffect, useRef, type ReactNode } from 'react';
import type { Cart, GuestMenu } from '../../api/types';
import { cartEstimate } from './cart-estimate';
import styles from './CartBar.module.css';

const ja = {
  open: 'カートを開く',
  count: '点',
  estimate: '税込・目安',
  shared: '共有カート',
};
const copy: Record<'ja' | 'zh', Record<keyof typeof ja, string>> = {
  ja,
  zh: { open: '開啟購物車', count: '件', estimate: '含稅・預估', shared: '共用購物車' },
};

export interface CartBarProps {
  active: boolean;
  cart: Cart | null;
  menu: GuestMenu;
  locale: 'ja' | 'zh';
  onOpen: () => void;
  onHeightChange?: (height: number) => void;
  sync?: ReactNode;
}

function formatYen(value: string | null): string {
  if (value === null) return '—';
  return `¥${BigInt(value).toLocaleString('ja-JP')}`;
}

export function CartBar({ active, cart, menu, locale, onOpen, onHeightChange, sync }: CartBarProps) {
  const bar = useRef<HTMLDivElement>(null);
  const shown = active && !!cart?.lines.length;
  useLayoutEffect(() => {
    if (!shown || !bar.current || !onHeightChange) return;
    const measure = () => onHeightChange(Math.ceil(bar.current?.getBoundingClientRect().height ?? 0));
    const observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(measure);
    observer?.observe(bar.current);
    window.addEventListener('resize', measure);
    measure();
    return () => { observer?.disconnect(); window.removeEventListener('resize', measure); onHeightChange(0); };
  }, [shown, onHeightChange]);
  if (!shown || !cart) return null;

  const labels = copy[locale];
  const count = cart.lines.reduce((sum, line) => sum + line.quantity, 0);
  const estimate = formatYen(cartEstimate(cart, menu));
  return <div ref={bar} className={styles.bar} data-testid="cart-bar">
    <button type="button" className={styles.button} onClick={onOpen}>
      <span className={styles.visuallyHidden}>{labels.open}</span>{' '}
      <span className={styles.count}>{count} {labels.count}</span>{' '}
      <span className={styles.total}><span>{labels.estimate}</span>{' '}<strong>{estimate}</strong></span>{' '}
      <span className={styles.sync}>{sync ?? labels.shared}</span>
    </button>
  </div>;
}
