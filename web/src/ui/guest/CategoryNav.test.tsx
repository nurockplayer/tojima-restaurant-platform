// @vitest-environment jsdom
import { cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { GuestMenu } from '../../api/types';
import { CategoryNav, categoryHeadingId } from './CategoryNav';

afterEach(() => { cleanup(); vi.restoreAllMocks(); });

const categories = [
  { key: 'rice', name: 'ごはん', position: 1, products: [] },
  { key: 'drink', name: '飲み物', position: 2, products: [] },
] satisfies GuestMenu['categories'];

describe('CategoryNav', () => {
  it('marks the current category and scrolls to and focuses its heading', async () => {
    const scrollIntoView = vi.fn();
    HTMLElement.prototype.scrollIntoView = scrollIntoView;
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
      if (this.id === categoryHeadingId('rice')) return { top: 0, bottom: 24 } as DOMRect;
      if (this.id === categoryHeadingId('drink')) return { top: 500, bottom: 524 } as DOMRect;
      return { top: 0, bottom: 0 } as DOMRect;
    });
    render(<><CategoryNav categories={categories} locale="ja" />
      <h2 id={categoryHeadingId('rice')} tabIndex={-1}>ごはん</h2><h2 id={categoryHeadingId('drink')} tabIndex={-1}>飲み物</h2></>);

    expect(screen.getByRole('button', { name: 'ごはん' }).getAttribute('aria-current')).toBe('location');
    const drink = screen.getByRole('button', { name: '飲み物' });
    expect(drink.hasAttribute('aria-current')).toBe(false);
    await userEvent.setup().click(drink);
    expect(scrollIntoView).toHaveBeenCalledWith({ block: 'start' });
    expect(document.activeElement).toBe(screen.getByRole('heading', { name: '飲み物' }));
    expect(drink.getAttribute('aria-current')).toBe('location');
    expect(screen.getByRole('button', { name: 'ごはん' }).hasAttribute('aria-current')).toBe(false);
  });

  it('provides a named navigation when no categories are available', () => {
    render(<CategoryNav categories={[]} locale="zh" />);
    expect(screen.getByRole('navigation', { name: '分類' }).textContent).toContain('目前沒有分類');
  });
});
