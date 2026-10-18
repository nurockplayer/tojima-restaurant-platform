export const paidUnderReview = {
  ja: 'お支払いを確認しました。店舗でご注文を確認中です',
  zh: '已確認付款，店家正在確認您的訂單',
};

// Explicitly allow supported projections. Future/refund/missing states remain
// neutral even if their raw payment or kitchen fields appear successful.
export function guestDisplayKind(status: { display_state: string; payment_status: string; fulfillment_status: string }): 'standard' | 'review' | 'unknown' {
  switch (status.display_state) {
    case 'PAID_UNDER_REVIEW': return 'review';
    case 'STANDARD':
      return ['UNPAID', 'PENDING', 'PAID'].includes(status.payment_status)
        && ['WAITING', 'ACCEPTED', 'PREPARING', 'READY', 'SERVED', 'CANCELLED'].includes(status.fulfillment_status)
        ? 'standard' : 'unknown';
    default: return 'unknown';
  }
}
