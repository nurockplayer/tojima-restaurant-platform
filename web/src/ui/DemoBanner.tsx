import styles from './DemoBanner.module.css';

export interface DemoSurfaceProps {
  demo?: boolean;
  initialView?: 'tables' | 'kds';
}

export function DemoBanner({ demo = false, locale }: { demo?: boolean; locale: 'ja' | 'zh' }) {
  if (!demo) return null;

  return <aside className={styles.banner} role="note" aria-label="DEMO">
    <strong className={styles.identity}>DEMO · 博多餃子酒場 シロナガスクジラ</strong>
    <span className={styles.notice}>{locale === 'ja' ? '参考価格 · 現行店内価格未確認 · 実店舗への注文・支払いは行われません' : '參考價格 · 目前店內價格未確認 · 不會向實際店家送出訂單或付款'}</span>
  </aside>;
}
