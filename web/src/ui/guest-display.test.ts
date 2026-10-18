import { describe, expect, it } from 'vitest';
import { guestDisplayKind, guestRefundKind } from './guest-display';

describe('guestDisplayKind', () => {
  it.each(['UNPAID', 'PENDING', 'PAID'])(
    'accepts supported STANDARD state with payment %s', (payment_status) => {
      expect(guestDisplayKind({ display_state: 'STANDARD', payment_status, fulfillment_status: 'SERVED' })).toBe('standard');
    },
  );
  it.each(['WAITING', 'ACCEPTED', 'PREPARING', 'READY', 'SERVED'])(
    'accepts supported STANDARD fulfillment %s', (fulfillment_status) => {
      expect(guestDisplayKind({ display_state: 'STANDARD', payment_status: 'PAID', fulfillment_status })).toBe('standard');
    },
  );
  it('accepts paid review and validates cancelled states as a separate kind', () => {
    expect(guestDisplayKind({ display_state: 'PAID_UNDER_REVIEW', payment_status: 'PAID', fulfillment_status: 'WAITING' })).toBe('review');
    for (const payment_status of ['UNPAID', 'PENDING', 'PAID', 'UNKNOWN', 'REFUND_PENDING', 'REFUNDED']) {
      expect(guestDisplayKind({ display_state: 'CANCELLED', payment_status, fulfillment_status: 'CANCELLED' })).toBe('cancelled');
    }
  });
  it.each([
    { display_state: 'STANDARD', payment_status: 'FUTURE', fulfillment_status: 'SERVED' },
    { display_state: 'STANDARD', payment_status: 'PAID', fulfillment_status: 'CANCELLED' },
    { display_state: 'FUTURE_SUCCESS', payment_status: 'PAID', fulfillment_status: 'SERVED' },
    { display_state: 'FUTURE_SUCCESS', payment_status: 'PAID', fulfillment_status: 'CANCELLED' },
    { display_state: 'CANCELLED', payment_status: 'PAID', fulfillment_status: 'SERVED' },
    { display_state: 'CANCELLED', payment_status: 'FUTURE', fulfillment_status: 'CANCELLED' },
  ])('keeps unsupported projection neutral: $display_state / $payment_status / $fulfillment_status', (state) => {
    expect(guestDisplayKind(state)).toBe('unknown');
  });
});

describe('guestRefundKind', () => {
  it.each([
    ['REQUESTED', 'CASH', 'processing'], ['PROCESSING', 'CASH', 'processing'], ['FAILED', 'CASH', 'failed'], ['SUCCEEDED', 'CASH', 'succeeded'],
    ['REQUESTED', 'STRIPE', 'processing'], ['PROCESSING', 'STRIPE', 'processing'], ['FAILED', 'STRIPE', 'failed'], ['SUCCEEDED', 'STRIPE', 'succeeded'],
  ] as const)('maps %s %s to %s', (status, method, expected) => {
    expect(guestRefundKind({ status, method })).toBe(expected);
  });
  it.each([
    { status: 'FUTURE', method: 'CASH' }, { status: 'SUCCEEDED', method: 'FUTURE' },
  ])('keeps unknown refund summary neutral: $status / $method', (refund) => {
    expect(guestRefundKind(refund)).toBe('unknown');
  });
});
