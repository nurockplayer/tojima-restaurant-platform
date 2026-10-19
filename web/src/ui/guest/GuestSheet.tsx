import { useCallback, useRef, type ReactNode } from 'react';
import { Dialog, Heading, Modal, ModalOverlay } from 'react-aria-components';
import styles from './GuestSheet.module.css';

const ja = { close: '閉じる' };
const copy: Record<'ja' | 'zh', Record<keyof typeof ja, string>> = { ja, zh: { close: '關閉' } };

export interface GuestSheetProps {
  isOpen: boolean;
  onOpenChange: (isOpen: boolean) => void;
  title: ReactNode;
  locale: 'ja' | 'zh';
  children: ReactNode;
  notice?: ReactNode;
  footer?: ReactNode;
  // Resolve after the exit animation, when a replacement heading has mounted.
  fallbackFocus?: () => HTMLElement | null;
}

export function GuestSheet({ isOpen, onOpenChange, title, locale, children, notice, footer, fallbackFocus }: GuestSheetProps) {
  const overlay = useRef<HTMLDivElement | null>(null);
  const trigger = useRef<Element | null>(null);
  const fallback = useRef(fallbackFocus);
  fallback.current = fallbackFocus;
  const overlayRef = useCallback((element: HTMLDivElement | null) => {
    if (element) trigger.current = document.activeElement;
    overlay.current = element;
    if (!element) {
      requestAnimationFrame(() => {
        if (!overlay.current && (trigger.current === document.body || !trigger.current?.isConnected)) fallback.current?.()?.focus();
      });
    }
  }, []);

  return <ModalOverlay ref={overlayRef} isOpen={isOpen} onOpenChange={onOpenChange} isDismissable className={styles.overlay}>
    <Modal className={styles.modal}>
      <Dialog className={styles.dialog}>
        <header className={styles.header}>
          <Heading slot="title" className={styles.title}>{title}</Heading>
          <button type="button" className={styles.close} onClick={() => onOpenChange(false)}>{copy[locale].close}</button>
        </header>
        {notice}
        <div className={styles.body}>{children}</div>
        {footer && <footer className={styles.footer}>{footer}</footer>}
      </Dialog>
    </Modal>
  </ModalOverlay>;
}
