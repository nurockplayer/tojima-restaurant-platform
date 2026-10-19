import type { CheckoutStatus } from '../../api/checkout';
import type { GuestOrderStatus } from '../../api/operations';
import { guestDisplayKind } from '../guest-display';

export type OrderTrackLocale = 'ja' | 'zh';
export type OrderTrackStep = { label: string; state: 'complete' | 'current' | 'upcoming' };
export type OrderTracks = { cooking: OrderTrackStep[]; payment: OrderTrackStep[] };

const ja = {
  waiting: '調理開始待ち', accepted: '受付済み', preparing: '調理中', ready: '配膳待ち', served: '提供済み',
  staffWaiting: '店員の受付待ち', cashUnpaid: '未払い', stripeUnpaid: '手続き待ち', paymentPending: '確認中', paid: '支払済み',
};
const zh: Record<keyof typeof ja, string> = {
  waiting: '等待開始製作', accepted: '待製作', preparing: '製作中', ready: '待送餐', served: '已送達',
  staffWaiting: '等待店員接單', cashUnpaid: '尚未付款', stripeUnpaid: '等待付款', paymentPending: '確認中', paid: '已付款',
};
const copy: Record<OrderTrackLocale, Record<keyof typeof ja, string>> = { ja, zh };

/** The two customer-facing progress tracks. Exception states deliberately have no projection. */
export function orderTracks(
  order: GuestOrderStatus | undefined,
  paymentMethod: 'CASH' | 'STRIPE',
  checkout: CheckoutStatus | null | undefined,
  operationsStale: boolean,
  locale: OrderTrackLocale,
): OrderTracks | null {
  if (!order || guestDisplayKind(order) !== 'standard') return null;

  let cookingIndex = ['WAITING', 'ACCEPTED', 'PREPARING', 'READY', 'SERVED'].indexOf(order.fulfillment_status);
  if (cookingIndex < 0) return null;
  const labels = copy[locale];
  const cookingLabels = [labels.waiting, labels.accepted, labels.preparing, labels.ready, labels.served];
  if (order.requires_staff_acceptance && !order.staff_accepted) {
    cookingLabels[0] = labels.staffWaiting;
    cookingIndex = 0;
  }

  let paymentIndex = order.payment_status === 'PAID' ? 2 : order.payment_status === 'PENDING' ? 1 : 0;
  const selectedCheckout = !operationsStale && checkout?.order_id === order.id ? checkout : undefined;
  if (order.payment_status === 'UNPAID' && paymentMethod === 'STRIPE' && selectedCheckout) {
    if (['PROCESSING', 'UNKNOWN', 'PAID', 'REVIEW_REQUIRED'].includes(selectedCheckout.state)) paymentIndex = 1;
    else if (['CREATE_PENDING', 'CREATING', 'OPEN', 'EXPIRED', 'FAILED'].includes(selectedCheckout.state)) paymentIndex = 0;
  }

  const mark = (labels: readonly string[], current: number): OrderTrackStep[] => labels.map((label, index) => ({
    label,
    state: index < current ? 'complete' : index === current ? 'current' : 'upcoming',
  }));

  const paymentLabels = [
    paymentMethod === 'CASH' ? labels.cashUnpaid : labels.stripeUnpaid,
    labels.paymentPending,
    ...(order.payment_status === 'PAID' ? [labels.paid] : []),
  ];
  return {
    cooking: mark(cookingLabels, cookingIndex),
    payment: mark(paymentLabels, paymentIndex),
  };
}
