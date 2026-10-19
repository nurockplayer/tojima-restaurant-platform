// @vitest-environment jsdom
import { act } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { restoreGuestFocus } from './restore-focus';

const cancelRestorations: Array<() => void> = [];
const hosts: HTMLElement[] = [];

function host() {
  const element = document.createElement('main');
  document.body.append(element);
  hosts.push(element);
  return element;
}

function heading(parent: HTMLElement, name: string) {
  const element = document.createElement('h2');
  element.tabIndex = -1;
  element.textContent = name;
  parent.append(element);
  return element;
}

function restore(fallback: () => HTMLElement | null, options?: Parameters<typeof restoreGuestFocus>[1]) {
  const cancel = restoreGuestFocus(fallback, options);
  cancelRestorations.push(cancel);
  return cancel;
}

async function frame(count = 1) {
  await act(async () => { await vi.advanceTimersByTimeAsync(16 * count); });
}

function emulateBrowserFocusBlocking() {
  const original = HTMLElement.prototype.focus;
  const blocked: HTMLElement[] = [];
  const focus = vi.spyOn(HTMLElement.prototype, 'focus').mockImplementation(function (this: HTMLElement, options?: FocusOptions) {
    if (this.closest('[inert], [aria-hidden="true"]') || document.querySelector('[data-guest-sheet]')) {
      blocked.push(this);
      return;
    }
    original.call(this, options);
  });
  return { focus, blocked };
}

afterEach(() => {
  cancelRestorations.splice(0).forEach((cancel) => cancel());
  hosts.splice(0).forEach((element) => element.remove());
  document.querySelectorAll('[data-guest-sheet]').forEach((element) => element.remove());
  vi.clearAllTimers();
  vi.restoreAllMocks();
  vi.useRealTimers();
});

describe('restoreGuestFocus', () => {
  it('waits until both the inert background and sheet overlay are gone', async () => {
    vi.useFakeTimers({ toFake: ['requestAnimationFrame', 'cancelAnimationFrame'] });
    const parent = host();
    parent.setAttribute('inert', '');
    const target = heading(parent, 'Orders');
    const overlay = document.createElement('div');
    overlay.dataset.guestSheet = '';
    document.body.append(overlay);
    const browser = emulateBrowserFocusBlocking();

    restore(() => target);
    await frame(3);
    expect(browser.focus.mock.contexts.filter((element) => element === target)).toHaveLength(0);
    expect(browser.blocked).toEqual([]);

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    parent.removeAttribute('inert');
    await frame(2);
    expect(browser.focus.mock.contexts.filter((element) => element === target)).toHaveLength(0);
    overlay.remove();
    await frame();

    expect(document.activeElement).toBe(target);
    expect(browser.blocked).toEqual([]);
    expect(browser.focus.mock.contexts.filter((element) => element === target)).toHaveLength(1);
  });

  it('retries when focus() is a no-op and verifies the active element', async () => {
    vi.useFakeTimers({ toFake: ['requestAnimationFrame', 'cancelAnimationFrame'] });
    const target = heading(host(), 'Menu');
    const original = HTMLElement.prototype.focus;
    let calls = 0;
    const focus = vi.spyOn(HTMLElement.prototype, 'focus').mockImplementation(function (this: HTMLElement, options?: FocusOptions) {
      if (this === target && calls++ === 0) return;
      original.call(this, options);
    });

    restore(() => target);
    await frame(2);

    expect(document.activeElement).toBe(target);
    expect(focus.mock.contexts.filter((element) => element === target)).toHaveLength(2);
  });

  it('stops after the bounded retry window when the destination stays inert', async () => {
    vi.useFakeTimers({ toFake: ['requestAnimationFrame', 'cancelAnimationFrame'] });
    const parent = host();
    parent.setAttribute('inert', '');
    const target = heading(parent, 'Entry');
    const browser = emulateBrowserFocusBlocking();

    restore(() => target);
    await frame(182);
    parent.removeAttribute('inert');
    await frame(2);

    expect(document.activeElement).not.toBe(target);
    expect(browser.focus.mock.contexts.filter((element) => element === target)).toHaveLength(0);
    expect(browser.blocked).toEqual([]);
  });

  it('uses the released fallback first, then waits for an enabled opener to leave inert', async () => {
    vi.useFakeTimers({ toFake: ['requestAnimationFrame', 'cancelAnimationFrame'] });
    const parent = host();
    parent.setAttribute('inert', '');
    const opener = document.createElement('button');
    opener.textContent = 'Open cart';
    opener.disabled = true;
    const fallback = heading(parent, 'Menu');
    parent.append(opener);
    const browser = emulateBrowserFocusBlocking();
    restore(() => fallback, { opener, waitForOpener: () => true });

    await frame(2);
    expect(browser.focus.mock.contexts.filter((element) => element === fallback || element === opener)).toHaveLength(0);
    parent.removeAttribute('inert');
    await frame();
    expect(document.activeElement).toBe(fallback);
    expect(browser.focus.mock.contexts.filter((element) => element === fallback)).toHaveLength(1);

    opener.disabled = false;
    opener.setAttribute('inert', '');
    await frame(2);
    expect(document.activeElement).toBe(fallback);
    expect(browser.focus.mock.contexts.filter((element) => element === opener)).toHaveLength(0);
    opener.removeAttribute('inert');
    await frame();
    expect(document.activeElement).toBe(opener);
    expect(browser.focus.mock.contexts.filter((element) => element === opener)).toHaveLength(1);
    expect(browser.blocked).toEqual([]);
  });

  it('cancels when the guest moves focus before the first restoration frame', async () => {
    vi.useFakeTimers({ toFake: ['requestAnimationFrame', 'cancelAnimationFrame'] });
    const parent = host();
    const target = heading(parent, 'Orders');
    const guestControl = document.createElement('button');
    guestControl.textContent = 'Language';
    parent.append(guestControl);
    const focus = vi.spyOn(HTMLElement.prototype, 'focus');

    restore(() => target);
    guestControl.focus();
    await frame(2);

    expect(document.activeElement).toBe(guestControl);
    expect(focus.mock.contexts.filter((element) => element === target)).toHaveLength(0);
  });

  it('uses the latest fallback when the destination changes while waiting', async () => {
    vi.useFakeTimers({ toFake: ['requestAnimationFrame', 'cancelAnimationFrame'] });
    const parent = host();
    const menu = heading(parent, 'Menu');
    menu.setAttribute('inert', '');
    const entry = heading(parent, 'Entry');
    let destination: HTMLElement = menu;
    const browser = emulateBrowserFocusBlocking();

    restore(() => destination);
    await frame(2);
    destination = entry;
    menu.remove();
    await frame();

    expect(document.activeElement).toBe(entry);
    expect(browser.focus.mock.contexts.filter((element) => element === menu)).toHaveLength(0);
    expect(browser.focus.mock.contexts.filter((element) => element === entry)).toHaveLength(1);
    expect(browser.blocked).toEqual([]);
  });
});
