import type { CheckoutStatus } from '../../api/checkout';
import type { GuestOrderStatus } from '../../api/operations';
import { orderTracks } from './order-tracks';
import styles from './OrderTracks.module.css';

const ja = { tracks: '注文状況', cooking: '調理', payment: 'お支払い' };
const zh: Record<keyof typeof ja, string> = { tracks: '訂單狀態', cooking: '製作', payment: '付款' };
const copy: Record<'ja' | 'zh', Record<keyof typeof ja, string>> = { ja, zh };

type Props = {
  order: GuestOrderStatus | undefined;
  paymentMethod: 'CASH' | 'STRIPE';
  checkout: CheckoutStatus | null | undefined;
  operationsStale: boolean;
  locale: 'ja' | 'zh';
  statusLabel: string;
};

export function OrderTracks({ order, paymentMethod, checkout, operationsStale, locale, statusLabel }: Props) {
  const tracks = orderTracks(order, paymentMethod, checkout, operationsStale, locale);
  const labels = copy[locale];
  if (!tracks) return <p className="guest-order-status" data-order-status role="status">{statusLabel}</p>;
  return <>
    <p className={'guest-order-status ' + styles.visuallyHidden} data-order-status data-status-presentation="hidden" role="status">{statusLabel}</p>
    <div className={styles.tracks} aria-label={labels.tracks}>
      <section aria-label={labels.cooking}>
        <h4>{labels.cooking}</h4>
        <ol role="list">{tracks.cooking.map((step, index) => <li key={index} data-step-state={step.state} aria-current={step.state === 'current' ? 'step' : undefined}>{step.label}</li>)}</ol>
      </section>
      <section aria-label={labels.payment}>
        <h4>{labels.payment}</h4>
        <ol role="list">{tracks.payment.map((step, index) => <li key={index} data-step-state={step.state} aria-current={step.state === 'current' ? 'step' : undefined}>{step.label}</li>)}</ol>
      </section>
    </div>
  </>;
}
