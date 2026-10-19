// @vitest-environment jsdom
import { cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { orderReadClient, orderReadLink, type OrderReadGrant } from '../api/order-read';
import { OrderPage } from './OrderRead';
import { loadMyOrders } from './my-orders';

const grant: OrderReadGrant = { order_id: 'a'.repeat(32), order_read_token: 'A'.repeat(43), expires_at: '2099-10-19T00:00:00Z' };
const initialLang = document.documentElement.lang;

beforeEach(() => {
  localStorage.clear();
  loadMyOrders();
  window.history.replaceState({}, '', orderReadLink(grant));
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible');
  vi.spyOn(orderReadClient, 'status').mockResolvedValue({
    version: 1, display_state: 'STANDARD', payment_status: 'PAID', fulfillment_status: 'SERVED',
    total_gross_jpy: '980', effective_total_gross_jpy: '980', effective_lines: [],
    adjusted: false, refunds: [], refunded_total_jpy: '0', currency: 'JPY', is_terminal: true,
  });
});

afterEach(() => {
  cleanup();
  localStorage.clear();
  loadMyOrders();
  window.history.replaceState({}, '', '/');
  document.documentElement.lang = initialLang;
  vi.restoreAllMocks();
});

describe('ORDER_READ demo notice', () => {
  it('shows the demo notice before order prices in ja and updates it after switching to zh', async () => {
    const user = userEvent.setup();
    render(<OrderPage demo />);

    const banner = screen.getByRole('note', { name: 'DEMO' });
    expect(banner.textContent).toContain('DEMO · 博多餃子酒場 シロナガスクジラ');
    expect(banner.textContent).toContain('参考価格 · 現行店内価格未確認 · 実店舗への注文・支払いは行われません');
    expect(banner.compareDocumentPosition(screen.getByRole('region', { name: '注文状況' })) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(await screen.findByText('¥980')).toBeTruthy();

    await user.click(screen.getByRole('button', { name: '繁體中文' }));

    const translatedBanner = screen.getByRole('note', { name: 'DEMO' });
    expect(translatedBanner.textContent).toContain('DEMO · 博多餃子酒場 シロナガスクジラ');
    expect(translatedBanner.textContent).toContain('參考價格 · 目前店內價格未確認 · 不會向實際店家送出訂單或付款');
    expect(translatedBanner.textContent).not.toContain('参考価格');
    expect(translatedBanner.compareDocumentPosition(screen.getByRole('region', { name: '訂單狀態' })) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(screen.getByText('¥980')).toBeTruthy();
  });

  it.each([{ demo: false }, {}])('omits the demo notice in both locales with props %j', async (props) => {
    const user = userEvent.setup();
    render(<OrderPage {...props} />);
    expect(await screen.findByText('¥980')).toBeTruthy();
    expect(screen.queryByRole('note', { name: 'DEMO' })).toBeNull();
    expect(screen.queryByText(/DEMO|参考価格|現行店内価格未確認/)).toBeNull();

    await user.click(screen.getByRole('button', { name: '繁體中文' }));

    expect(screen.getByText('¥980')).toBeTruthy();
    expect(screen.queryByRole('note', { name: 'DEMO' })).toBeNull();
    expect(screen.queryByText(/DEMO|參考價格|目前店內價格未確認/)).toBeNull();
  });
});
