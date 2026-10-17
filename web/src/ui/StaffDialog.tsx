import { useEffect, useRef } from 'react';

interface StaffDialogProps {
  labelledBy: string;
  dismissible: boolean;
  onDismiss: () => void;
  children: React.ReactNode;
}

export function StaffDialog({ labelledBy, dismissible, onDismiss, children }: StaffDialogProps) {
  const dialogRef = useRef<HTMLDialogElement>(null);
  const latest = useRef({ dismissible, onDismiss });
  latest.current = { dismissible, onDismiss };

  useEffect(() => {
    const dialog = dialogRef.current;
    if (!dialog) return;
    const show = () => {
      if (dialog.open) return;
      if (typeof dialog.showModal === 'function') dialog.showModal();
      else dialog.setAttribute('open', '');
    };
    const cancel = (event: Event) => {
      event.preventDefault();
      if (latest.current.dismissible) latest.current.onDismiss();
    };
    const close = () => {
      if (latest.current.dismissible) latest.current.onDismiss();
      else show();
    };
    dialog.addEventListener('cancel', cancel);
    dialog.addEventListener('close', close);
    show();
    return () => {
      dialog.removeEventListener('cancel', cancel);
      dialog.removeEventListener('close', close);
      if (dialog.open) {
        if (typeof dialog.close === 'function') dialog.close();
        else dialog.removeAttribute('open');
      }
    };
  }, []);

  return <dialog ref={dialogRef} className="confirm-dialog" aria-labelledby={labelledBy}>{children}</dialog>;
}
