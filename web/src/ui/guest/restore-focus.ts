interface RestoreOptions {
  opener?: Element | null;
  from?: Element | null;
  cancelled?: () => boolean;
  waitForOpener?: () => boolean;
}

export function restoreGuestFocus(fallback: () => HTMLElement | null, { opener = null, from = null, cancelled, waitForOpener }: RestoreOptions = {}): () => void {
  let frame: number | undefined;
  let frames = 0;
  let stopped = false;
  let owned: HTMLElement | null = null;
  const ready = (target: Element | null): target is HTMLElement => target instanceof HTMLElement
    && target !== document.body && target !== document.documentElement && target.isConnected
    && !target.matches(':disabled, [aria-disabled="true"]')
    && !target.closest('[data-guest-sheet], [hidden], [inert], [aria-hidden="true"]');
  const cancel = () => {
    stopped = true;
    if (frame !== undefined) cancelAnimationFrame(frame);
    observer.disconnect();
    document.removeEventListener('focusin', moved);
  };
  const moved = () => {
    const active = document.activeElement;
    if (ready(active) && active !== opener && active !== from && active !== fallback() && active !== owned) cancel();
  };
  const queue = () => {
    if (stopped || frame !== undefined) return;
    if (frames++ >= 180) { cancel(); return; }
    frame = requestAnimationFrame(restore);
  };
  const restore = () => {
    frame = undefined;
    if (cancelled?.()) { cancel(); return; }
    moved();
    if (stopped) return;
    if (!document.querySelector('[data-guest-sheet]')) {
      for (const target of [opener, fallback()]) {
        if (!ready(target)) continue;
        if (document.activeElement !== target) target.focus({ preventScroll: true });
        if (document.activeElement !== target) continue;
        owned = target;
        if (target === opener || !opener?.isConnected || waitForOpener?.() === false) cancel();
        // A disabled replay opener may become available after the fallback is focused.
        return;
      }
    }
    queue();
  };
  const observer = new MutationObserver(queue);
  observer.observe(document.body, { subtree: true, childList: true, attributes: true, attributeFilter: ['disabled', 'aria-disabled', 'hidden', 'inert', 'aria-hidden', 'class', 'style'] });
  document.addEventListener('focusin', moved);
  queue();
  return cancel;
}
