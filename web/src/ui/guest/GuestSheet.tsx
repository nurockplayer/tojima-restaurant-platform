import { useCallback, useEffect, useRef, type ReactNode, type Ref } from 'react';
import { Dialog, Heading, Modal, ModalOverlay } from 'react-aria-components';
import styles from './GuestSheet.module.css';

const ja = { close: '閉じる' };
const copy: Record<'ja' | 'zh', Record<keyof typeof ja, string>> = { ja, zh: { close: '關閉' } };

export interface GuestSheetProps {
  isOpen: boolean;
  onOpenChange: (isOpen: boolean) => void;
  title: ReactNode;
  titleRef?: Ref<HTMLHeadingElement>;
  locale: 'ja' | 'zh';
  children: ReactNode;
  notice?: ReactNode;
  footer?: ReactNode;
  // Resolve after the exit animation, when a replacement heading has mounted.
  fallbackFocus?: () => HTMLElement | null;
}

export function GuestSheet({ isOpen, onOpenChange, title, titleRef, locale, children, notice, footer, fallbackFocus }: GuestSheetProps) {
  const overlay = useRef<HTMLDivElement | null>(null);
  const trigger = useRef<Element | null>(null);
  const fallback = useRef(fallbackFocus);
  const mounted = useRef(true);
  const stopWaiting = useRef<(() => void) | null>(null);
  const restoration = useRef(0);
  fallback.current = fallbackFocus;
  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; stopWaiting.current?.(); };
  }, []);
  const overlayRef = useCallback((element: HTMLDivElement | null) => {
    const version = ++restoration.current;
    if (element) {
      stopWaiting.current?.();
      trigger.current = document.activeElement;
    }
    overlay.current = element;
    if (!element) {
      requestAnimationFrame(() => {
        if (overlay.current || restoration.current !== version) return;
        const opener = trigger.current;
        const interim = fallback.current?.();
        const active = document.activeElement;
        if (active instanceof HTMLElement && active !== document.body && active !== document.documentElement
          && active !== opener && active !== interim && active.isConnected && !active.closest('[hidden], [inert], [aria-hidden="true"]')) return;
        const restore = () => {
          if (!(opener instanceof HTMLElement) || opener === document.body || opener === document.documentElement || !opener.isConnected
            || opener.matches(':disabled, [aria-disabled="true"]') || opener.closest('[hidden], [inert], [aria-hidden="true"]')) return false;
          if (document.activeElement !== opener) opener.focus({ preventScroll: true });
          return document.activeElement === opener;
        };
        if (restore()) return;
        if (document.activeElement !== interim) interim?.focus({ preventScroll: true });
        if (!mounted.current || !opener?.isConnected || opener === document.body) return;

        // A replay can close the sheet before busy releases its opener.
        // Keep focus safe meanwhile, and stop if the guest moves elsewhere.
        const cancel = () => {
          observer.disconnect();
          document.removeEventListener('focusin', moved);
          document.removeEventListener('pointerdown', cancel, true);
          document.removeEventListener('keydown', cancel, true);
          stopWaiting.current = null;
        };
        const moved = () => { if (document.activeElement !== interim && document.activeElement !== document.body) cancel(); };
        const observer = new MutationObserver(() => {
          if (!opener.isConnected || overlay.current) { cancel(); return; }
          if (document.activeElement !== interim && document.activeElement !== document.body) { cancel(); return; }
          if (restore()) cancel();
        });
        observer.observe(document.body, { subtree: true, childList: true, attributes: true, attributeFilter: ['disabled', 'aria-disabled', 'hidden', 'inert', 'aria-hidden', 'class', 'style'] });
        document.addEventListener('focusin', moved);
        document.addEventListener('pointerdown', cancel, true);
        document.addEventListener('keydown', cancel, true);
        stopWaiting.current = cancel;
      });
    }
  }, []);

  return <ModalOverlay data-surface="guest" data-guest-sheet ref={overlayRef} isOpen={isOpen} onOpenChange={onOpenChange} isDismissable className={styles.overlay}>
    <Modal className={styles.modal}>
      <Dialog className={styles.dialog}>
        <header className={styles.header}>
          <Heading slot="title" ref={titleRef} tabIndex={-1} className={styles.title}>{title}</Heading>
          <button type="button" className={styles.close} onClick={() => onOpenChange(false)}>{copy[locale].close}</button>
        </header>
        {notice}
        <div className={styles.body}>{children}</div>
        {footer && <footer className={styles.footer}>{footer}</footer>}
      </Dialog>
    </Modal>
  </ModalOverlay>;
}
