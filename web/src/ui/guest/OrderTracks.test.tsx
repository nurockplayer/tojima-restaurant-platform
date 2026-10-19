// @vitest-environment jsdom
import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';
import type { GuestOrderStatus } from '../../api/operations';
import { OrderTracks } from './OrderTracks';

afterEach(cleanup);

const standard: GuestOrderStatus = {
  id: 'order-1', display_state: 'STANDARD', payment_status: 'UNPAID',
  fulfillment_status: 'WAITING', requires_staff_acceptance: true, staff_accepted: false,
  total_gross_jpy: '100', effective_total_gross_jpy: '100', effective_lines: [],
  adjusted: false, refunds: [], refunded_total_jpy: '0',
};

describe('OrderTracks', () => {
  it('keeps the concatenated status as the only status live region while showing two tracks', () => {
    render(<OrderTracks order={standard} paymentMethod="STRIPE" checkout={null} operationsStale={false} locale="ja" statusLabel="店員の受付待ち · 未払い" />);
    const liveStatus = screen.getByRole('status');
    expect(liveStatus.textContent).toBe('店員の受付待ち · 未払い');
    expect(liveStatus.getAttribute('data-status-presentation')).toBe('hidden');
    expect(screen.getByRole('heading', { name: '調理' })).toBeTruthy();
    expect(screen.getByRole('heading', { name: 'お支払い' })).toBeTruthy();
    expect(document.querySelectorAll('[aria-current="step"]')).toHaveLength(2);
    expect(document.querySelector('[role="status"] [aria-current="step"]')).toBeNull();
  });

  it('keeps exception text visible without drawing either track', () => {
    render(<OrderTracks order={{ ...standard, display_state: 'PAID_UNDER_REVIEW', payment_status: 'PAID' }} paymentMethod="STRIPE" checkout={null} operationsStale={false} locale="zh" statusLabel="已確認付款，店家正在確認您的訂單" />);
    expect(screen.getByRole('status').textContent).toBe('已確認付款，店家正在確認您的訂單');
    expect(document.querySelector('[aria-current="step"]')).toBeNull();
    expect(screen.queryByRole('heading', { name: '製作' })).toBeNull();
  });
});
