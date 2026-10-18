export const paidUnderReview = {
  ja: 'お支払いを確認しました。店舗でご注文を確認中です',
  zh: '已確認付款，店家正在確認您的訂單',
};

export function guestRefundKind(refund: { status: string; method: string }): 'processing' | 'failed' | 'succeeded' | 'unknown' {
  if (refund.method !== 'CASH' && refund.method !== 'STRIPE') return 'unknown';
  if (refund.status === 'REQUESTED' || refund.status === 'PROCESSING') return 'processing';
  if (refund.status === 'FAILED') return 'failed';
  if (refund.status === 'SUCCEEDED') return 'succeeded';
  return 'unknown';
}

// Explicitly allow supported projections. Future/refund/missing states remain
// neutral even if their raw payment or kitchen fields appear successful.
export function guestDisplayKind(status: { display_state: string; payment_status: string; fulfillment_status: string }): 'standard' | 'review' | 'cancelled' | 'unknown' {
  if (status.display_state === 'CANCELLED' && status.fulfillment_status === 'CANCELLED'
    && ['UNPAID', 'PENDING', 'PAID', 'UNKNOWN', 'VOIDED', 'REFUND_PENDING', 'REFUNDED'].includes(status.payment_status)) return 'cancelled';
  switch (status.display_state) {
    case 'PAID_UNDER_REVIEW': return 'review';
    case 'STANDARD':
      return ['UNPAID', 'PENDING', 'PAID'].includes(status.payment_status)
        && ['WAITING', 'ACCEPTED', 'PREPARING', 'READY', 'SERVED'].includes(status.fulfillment_status)
        ? 'standard' : 'unknown';
    default: return 'unknown';
  }
}
