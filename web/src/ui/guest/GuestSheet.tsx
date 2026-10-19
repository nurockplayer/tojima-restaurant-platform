import { useCallback, useEffect, useRef, type ReactNode, type Ref } from 'react';
import { Dialog, Heading, Modal, ModalOverlay } from 'react-aria-components';
import styles from './GuestSheet.module.css';
import { restoreGuestFocus } from './restore-focus';

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
    return () => { mounted.current = false; };
  }, []);
  const overlayRef = useCallback((element: HTMLDivElement | null) => {
    const version = ++restoration.current;
    if (element) {
      stopWaiting.current?.();
      trigger.current = document.activeElement;
    }
    overlay.current = element;
    if (!element) {
      stopWaiting.current?.();
      stopWaiting.current = restoreGuestFocus(() => fallback.current?.() ?? null, {
        opener: trigger.current,
        cancelled: () => !!overlay.current || restoration.current !== version,
        waitForOpener: () => mounted.current,
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
