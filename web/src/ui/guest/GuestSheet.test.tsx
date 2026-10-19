// @vitest-environment jsdom
import { useRef, useState } from 'react';
import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { GuestSheet } from './GuestSheet';

afterEach(() => { cleanup(); vi.restoreAllMocks(); });

function Harness({ removeTrigger = false, locale = 'ja', disabledTrigger = false, triggerPresent, fallbackTarget = 'menu' }: {
  removeTrigger?: boolean; locale?: 'ja' | 'zh'; disabledTrigger?: boolean; triggerPresent?: boolean; fallbackTarget?: 'menu' | 'entry';
}) {
  const [open, setOpen] = useState(false);
  const menuHeading = useRef<HTMLHeadingElement>(null);
  const entryHeading = useRef<HTMLHeadingElement>(null);
  const showTrigger = triggerPresent ?? (!removeTrigger || !open);
  return <>
    <h1 ref={menuHeading} tabIndex={-1}>メニュー</h1>
    <h2 ref={entryHeading} tabIndex={-1}>注文受付</h2>
    {showTrigger && <button disabled={disabledTrigger} onClick={() => setOpen(true)}>開く</button>}
    <button>背景操作</button>
    <GuestSheet isOpen={open} onOpenChange={setOpen} title="オプション" locale={locale}
      fallbackFocus={() => fallbackTarget === 'menu' ? menuHeading.current : entryHeading.current}>
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
    const focus = vi.spyOn(trigger, 'focus');
    await user.keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    await waitFor(() => expect(document.activeElement).toBe(trigger));
    await act(async () => { await new Promise((resolve) => requestAnimationFrame(resolve)); });
    expect(focus).toHaveBeenCalledTimes(1);
  });

  it('closes from the backdrop and restores focus to the trigger', async () => {
    const { user, trigger, dialog } = await openSheet();
    const backdrop = dialog.closest('[data-surface="guest"]');
    expect(backdrop).not.toBeNull();
    await user.click(backdrop!);
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
  it('uses fallback focus until a disabled opener becomes enabled', async () => {
    const { user, trigger, rerender } = await openSheet();
    rerender(<Harness disabledTrigger />);
    await user.keyboard('{Escape}');
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('heading', { name: 'メニュー' })));
    expect((trigger as HTMLButtonElement).disabled).toBe(true);
    rerender(<Harness />);
    await waitFor(() => expect(document.activeElement).toBe(trigger));
  });

  it('does not take focus back after the guest chooses another control while the opener is disabled', async () => {
    const { user, rerender } = await openSheet();
    rerender(<Harness disabledTrigger />);
    await user.keyboard('{Escape}');
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('heading', { name: 'メニュー' })));
    const other = screen.getByRole('button', { name: '背景操作' });
    await user.click(other);
    rerender(<Harness />);
    await act(async () => { await new Promise((resolve) => requestAnimationFrame(resolve)); });
    expect(document.activeElement).toBe(other);
  });

  it('keeps fallback focus when a disabled opener is removed after dismissal', async () => {
    const { user, rerender } = await openSheet();
    rerender(<Harness disabledTrigger />);
    await user.keyboard('{Escape}');
    const menuHeading = screen.getByRole('heading', { name: 'メニュー' });
    await waitFor(() => expect(document.activeElement).toBe(menuHeading));
    rerender(<Harness disabledTrigger triggerPresent={false} />);
    await waitFor(() => expect(document.activeElement).toBe(menuHeading));
    expect(document.activeElement).not.toBe(document.body);
  });

  it('does not steal focus if the guest moves before the close restoration frame', async () => {
    const { user } = await openSheet();
    const frames: FrameRequestCallback[] = [];
    const raf = vi.spyOn(window, 'requestAnimationFrame').mockImplementation((callback) => {
      frames.push(callback);
      return frames.length;
    });
    try {
      await user.keyboard('{Escape}');
      await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
      const other = screen.getByRole('button', { name: '背景操作' });
      other.focus();
      expect(frames.length).toBeGreaterThan(0);
      await act(async () => { for (const frame of frames.splice(0)) frame(performance.now()); });
      expect(document.activeElement).toBe(other);
    } finally { raf.mockRestore(); }
  });

  it('uses the latest caller fallback when the target view changes during exit', async () => {
    let finish!: () => void;
    const finished = new Promise<void>((resolve) => { finish = resolve; });
    const animations = vi.spyOn(Element.prototype, 'getAnimations').mockImplementation(function (this: Element) {
      return this.hasAttribute('data-exiting') ? [{ finished } as unknown as Animation] : [];
    });
    try {
      const { user, rerender, trigger } = await openSheet({ removeTrigger: true });
      await user.keyboard('{Escape}');
      const entryHeading = screen.getByRole('heading', { name: '注文受付' });
      rerender(<Harness removeTrigger fallbackTarget="entry" />);
      await act(async () => { finish(); await finished; });
      await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
      await waitFor(() => expect(document.activeElement).toBe(entryHeading));
      expect(document.activeElement).not.toBe(trigger);
    } finally { animations.mockRestore(); }
  });

  it('does not run a stale close restoration after the sheet reopens', async () => {
    const { user, trigger } = await openSheet();
    const frames: FrameRequestCallback[] = [];
    const raf = vi.spyOn(window, 'requestAnimationFrame').mockImplementation((callback) => {
      frames.push(callback);
      return frames.length;
    });
    try {
      await user.keyboard('{Escape}');
      await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
      expect(frames.length).toBeGreaterThan(0);
      await user.click(trigger);
      const dialog = screen.getByRole('dialog', { name: 'オプション' });
      await act(async () => { for (const frame of frames.splice(0)) frame(performance.now()); });
      expect(dialog.contains(document.activeElement)).toBe(true);
    } finally { raf.mockRestore(); }
  });

});
