// @vitest-environment jsdom
import { useRef, useState } from 'react';
import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { GuestSheet } from './GuestSheet';

afterEach(() => { cleanup(); vi.restoreAllMocks(); });

function Harness({ removeTrigger = false, locale = 'ja' }: { removeTrigger?: boolean; locale?: 'ja' | 'zh' }) {
  const [open, setOpen] = useState(false);
  const heading = useRef<HTMLHeadingElement>(null);
  return <>
    <h1 ref={heading} tabIndex={-1}>メニュー</h1>
    {(!removeTrigger || !open) && <button onClick={() => setOpen(true)}>開く</button>}
    <button>背景操作</button>
    <GuestSheet isOpen={open} onOpenChange={setOpen} title="オプション" locale={locale} fallbackFocus={() => heading.current}>
      <label>数量<input /></label><button>追加</button>
    </GuestSheet>
  </>;
}

async function openSheet(props = {}) {
  const user = userEvent.setup();
  const view = render(<Harness {...props} />);
  const trigger = screen.getByRole('button', { name: '開く' });
  await user.click(trigger);
  const dialog = screen.getByRole('dialog', { name: 'オプション' });
  return { user, dialog, trigger, ...view };
}

describe('GuestSheet', () => {
  it('moves focus inside and keeps forward and backward Tab cycles inside', async () => {
    const { user, dialog } = await openSheet();
    expect(dialog.contains(document.activeElement)).toBe(true);
    const close = within(dialog).getByRole('button', { name: '閉じる' });
    const last = within(dialog).getByRole('button', { name: '追加' });
    close.focus();
    await user.tab({ shift: true });
    expect(document.activeElement).toBe(last);
    await user.tab();
    expect(document.activeElement).toBe(close);
    for (let i = 0; i < 20; i++) {
      await user.tab();
      expect(dialog.contains(document.activeElement)).toBe(true);
      await user.tab({ shift: true });
      expect(dialog.contains(document.activeElement)).toBe(true);
    }
  });

  it('closes with Escape and restores focus to the trigger', async () => {
    const { user, trigger } = await openSheet();
    await user.keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    await waitFor(() => expect(document.activeElement).toBe(trigger));
  });

  it.each([['ja', '閉じる'], ['zh', '關閉']] as const)('closes with the %s text button', async (locale, name) => {
    const { user, dialog, trigger } = await openSheet({ locale });
    await user.click(within(dialog).getByRole('button', { name }));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    await waitFor(() => expect(document.activeElement).toBe(trigger));
  });

  it('focuses the caller fallback when the original trigger is removed', async () => {
    const { user, trigger } = await openSheet({ removeTrigger: true });
    expect(trigger.isConnected).toBe(false);
    await user.keyboard('{Escape}');
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('heading', { name: 'メニュー' })));
  });

  it('restores fallback focus when a trigger disappears after the sheet opens', async () => {
    const { user, trigger, rerender } = await openSheet();
    rerender(<Harness removeTrigger />);
    expect(trigger.isConnected).toBe(false);
    await user.keyboard('{Escape}');
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('heading', { name: 'メニュー' })));
  });

  it('hides the background while open and clears aria-hidden and inert after close', async () => {
    const { user, container } = await openSheet();
    expect(container.closest('[aria-hidden="true"], [inert]')).not.toBeNull();
    expect(screen.queryByRole('button', { name: '背景操作' })).toBeNull();
    await user.keyboard('{Escape}');
    await waitFor(() => expect(container.closest('[aria-hidden="true"], [inert]')).toBeNull());
    expect(screen.getByRole('button', { name: '背景操作' })).toBeDefined();
  });

  it('renders the caller notice and action footer inside the dialog', () => {
    render(<GuestSheet isOpen onOpenChange={() => undefined} title="共有カート" locale="ja"
      notice={<p role="note">お知らせ</p>} footer={<button>金額を確認する</button>}>
      <p>カート内容</p>
    </GuestSheet>);
    const dialog = screen.getByRole('dialog', { name: '共有カート' });
    expect(within(dialog).getByRole('note').textContent).toBe('お知らせ');
    expect(within(dialog).getByRole('button', { name: '金額を確認する' })).toBeDefined();
  });

  it('cleans up the background and restores fallback focus after an exit animation', async () => {
    let finish!: () => void;
    const finished = new Promise<void>((resolve) => { finish = resolve; });
    Object.defineProperty(Element.prototype, 'getAnimations', {
      configurable: true, writable: true, value: () => [],
    });
    const animations = vi.spyOn(Element.prototype, 'getAnimations').mockImplementation(function (this: Element) {
      return this.hasAttribute('data-exiting') ? [{ finished } as unknown as Animation] : [];
    });
    try {
      const { user, container, dialog } = await openSheet({ removeTrigger: true });
      await user.keyboard('{Escape}');
      expect(dialog.isConnected).toBe(true);
      await act(async () => { finish(); await finished; });
      await waitFor(() => expect(dialog.isConnected).toBe(false));
      await waitFor(() => expect(container.closest('[aria-hidden="true"], [inert]')).toBeNull());
      await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('heading', { name: 'メニュー' })));
    } finally { animations.mockRestore(); delete (Element.prototype as Partial<Element>).getAnimations; }
  });
});
