// @vitest-environment jsdom
import { cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it } from 'vitest';
import { App } from './App';
import { demoStoreName } from './DemoBanner';

afterEach(() => {
  cleanup();
});

describe('guest header and footer', () => {
  it('shows the demo store before the table and exactly one platform footer', () => {
    render(<App demo />);

    const topbar = screen.getByRole('banner');
    const store = topbar.querySelector('[data-store-slot="demo"]');
    const table = topbar.querySelector('span');
    expect(store?.textContent).toBe(demoStoreName);
    expect(store && table && (store.compareDocumentPosition(table) & Node.DOCUMENT_POSITION_FOLLOWING)).toBeTruthy();
    expect(table?.textContent).toBe('テーブル');
    expect(screen.getAllByText('Powered by Tojima')).toHaveLength(1);
    expect(topbar.textContent).not.toContain('東島');
    expect(screen.queryByText(/TOJIMA KITCHEN|OPEN TABLE|TABLE ORDER|CONFIRMED/)).toBeNull();
  });

  it('has no store-name slot outside demo and labels the language switch with its target language', async () => {
    const user = userEvent.setup();
    render(<App />);

    const topbar = screen.getByRole('banner');
    expect(topbar.querySelector('[data-store-slot]')).toBeNull();
    expect(topbar.querySelector('span')?.textContent).toBe('テーブル');
    expect(screen.getAllByText('Powered by Tojima')).toHaveLength(1);
    const language = screen.getByRole('button', { name: '繁中' });
    expect(language.getAttribute('lang')).toBe('zh-Hant');
    await user.click(language);
    expect(screen.getByRole('button', { name: '日本語' }).getAttribute('lang')).toBe('ja');
  });
  it('uses target-sized controls for the PIN and join action in both languages', async () => {
    const user = userEvent.setup();
    render(<App />);
    expect((await screen.findByRole('textbox', { name: 'テーブル PIN' })).getAttribute('data-control')).toBe('guest-entry');
    expect(screen.getByRole('button', { name: '参加する' }).getAttribute('data-control')).toBe('guest-entry');
    await user.click(screen.getByRole('button', { name: '繁中' }));
    expect(screen.getByRole('textbox', { name: '桌次 PIN' }).getAttribute('data-control')).toBe('guest-entry');
    expect(screen.getByRole('button', { name: '加入桌次' }).getAttribute('data-control')).toBe('guest-entry');
  });

});
