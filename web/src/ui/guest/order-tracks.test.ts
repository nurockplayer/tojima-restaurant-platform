import { describe, expect, it } from 'vitest';
import type { CheckoutStatus } from '../../api/checkout';
import type { GuestOrderStatus } from '../../api/operations';
import { orderTracks } from './order-tracks';

const fulfillment = ['WAITING', 'ACCEPTED', 'PREPARING', 'READY', 'SERVED', 'CANCELLED'] as const;
const payments = ['UNPAID', 'PAID', 'PENDING', 'UNKNOWN', 'VOIDED', 'REFUND_PENDING', 'REFUNDED'] as const;
const checkouts = ['CREATE_PENDING', 'CREATING', 'UNKNOWN', 'OPEN', 'PROCESSING', 'PAID', 'EXPIRED', 'FAILED', 'REVIEW_REQUIRED'] as const;
const displays = ['STANDARD', 'PAID_UNDER_REVIEW', 'CANCELLED', 'FUTURE_STATE'] as const;
const methods = ['CASH', 'STRIPE'] as const;

function order(overrides: Partial<GuestOrderStatus> = {}): GuestOrderStatus {
  return {
    id: 'order-1', display_state: 'STANDARD', payment_status: 'UNPAID',
    fulfillment_status: 'WAITING', requires_staff_acceptance: false, staff_accepted: false,
    total_gross_jpy: '100', effective_total_gross_jpy: '100', effective_lines: [],
    adjusted: false, refunds: [], refunded_total_jpy: '0', ...overrides,
  };
}

function checkout(state: typeof checkouts[number], id = 'order-1'): CheckoutStatus {
  return {
    order_id: id, state, payment_review_required: false, resume_available: false,
  };
}

describe('orderTracks', () => {
  it('covers every fulfillment, payment, acceptance, method, checkout, display state and locale combination', () => {
    for (const locale of ['ja', 'zh'] as const) {
      for (const display_state of displays) {
        for (const fulfillment_status of fulfillment) {
          for (const payment_status of payments) {
            for (const requires_staff_acceptance of [false, true]) {
              for (const staff_accepted of [false, true]) {
              for (const operationsStale of [false, true]) {
              for (const paymentMethod of methods) {
                for (const checkoutState of [null, ...checkouts] as (typeof checkouts[number] | null)[]) {
                  const source = order({ display_state, fulfillment_status, payment_status, requires_staff_acceptance, staff_accepted });
                  const result = orderTracks(source, paymentMethod, checkoutState ? checkout(checkoutState) : null, operationsStale, locale);
                  const supported = display_state === 'STANDARD'
                    && ['UNPAID', 'PENDING', 'PAID'].includes(payment_status)
                    && fulfillment_status !== 'CANCELLED';
                  if (!supported) {
                    expect(result).toBeNull();
                    continue;
                  }

                  expect(result).not.toBeNull();
                  const cookingIndex = requires_staff_acceptance && !staff_accepted
                    ? 0
                    : fulfillment.indexOf(fulfillment_status);
                  const paymentIndex = payment_status === 'PAID' ? 2
                    : payment_status === 'PENDING' ? 1
                      : paymentMethod === 'STRIPE' && checkoutState && !operationsStale
                        ? ['PROCESSING', 'UNKNOWN', 'PAID', 'REVIEW_REQUIRED'].includes(checkoutState) ? 1 : 0
                        : 0;
                  expect(result!.cooking.filter((step) => step.state === 'current')).toHaveLength(1);
                  expect(result!.payment.filter((step) => step.state === 'current')).toHaveLength(1);
                  expect(result!.cooking.findIndex((step) => step.state === 'current')).toBe(cookingIndex);
                  expect(result!.payment.findIndex((step) => step.state === 'current')).toBe(paymentIndex);
                  expect(result!.cooking.map((step) => step.state)).toEqual(result!.cooking.map((_, index) => index < cookingIndex ? 'complete' : index === cookingIndex ? 'current' : 'upcoming'));
                  const firstCooking = requires_staff_acceptance && !staff_accepted
                    ? locale === 'ja' ? '店員の受付待ち' : '等待店員接單'
                    : locale === 'ja' ? '調理開始待ち' : '等待開始製作';
                  expect(result!.cooking[0]!.label).toBe(firstCooking);
                  expect(result!.payment.some((step) => step.label === (locale === 'ja' ? '支払済み' : '已付款'))).toBe(payment_status === 'PAID');
                }
              }
              }
              }
            }
          }
        }
      }
    }
  });

  it('uses Checkout only for the matching order, unpaid Stripe, and fresh operations', () => {
    const base = order();
    expect(orderTracks(base, 'STRIPE', checkout('PROCESSING', 'other'), false, 'ja')!.payment[0]!.state).toBe('current');
    expect(orderTracks(order({ payment_status: 'PENDING' }), 'STRIPE', checkout('OPEN'), false, 'ja')!.payment[1]!.state).toBe('current');
    expect(orderTracks(order({ payment_status: 'PAID' }), 'STRIPE', checkout('OPEN'), false, 'ja')!.payment[2]!.state).toBe('current');
    expect(orderTracks(base, 'STRIPE', checkout('PROCESSING'), true, 'ja')!.payment[0]!.state).toBe('current');
    expect(orderTracks(base, 'CASH', checkout('PROCESSING'), false, 'ja')!.payment[0]!.state).toBe('current');
    expect(orderTracks(undefined, 'CASH', null, false, 'ja')).toBeNull();
  });

  it('keeps the staff acceptance and first payment labels localized', () => {
    expect(orderTracks(order({ requires_staff_acceptance: true }), 'CASH', null, false, 'ja')!.cooking[0]!.label).toBe('店員の受付待ち');
    expect(orderTracks(order({ requires_staff_acceptance: true }), 'CASH', null, false, 'zh')!.cooking[0]!.label).toBe('等待店員接單');
    expect(orderTracks(order(), 'CASH', null, false, 'ja')!.payment[0]!.label).toBe('未払い');
    expect(orderTracks(order(), 'STRIPE', null, false, 'ja')!.payment[0]!.label).toBe('手続き待ち');
    expect(orderTracks(order(), 'CASH', null, false, 'zh')!.payment[0]!.label).toBe('尚未付款');
    expect(orderTracks(order(), 'STRIPE', null, false, 'zh')!.payment[0]!.label).toBe('等待付款');
    expect(orderTracks(order({ payment_status: 'PENDING' }), 'CASH', null, false, 'zh')!.payment[1]!.label).toBe('確認中');
  });
});
