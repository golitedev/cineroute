'use client';

import { useT } from '@/lib/i18n';
import { useEffect, useRef } from 'react';
import { useToast } from '@/hooks/use-toast';
import { Toast, ToastClose, ToastDescription, ToastProvider, ToastTitle, ToastViewport } from '@/components/ui/toast';

export function Toaster() {
    const t = useT();
    const { toasts } = useToast();
    const viewportRef = useRef<HTMLOListElement>(null);

    useEffect(() => {
        const viewport = viewportRef.current;
        if (!viewport) return;
        const update = () => {
            document.documentElement.style.setProperty(
                '--toast-space',
                toasts.some((toast) => toast.open) ? `${viewport.getBoundingClientRect().height}px` : '0px'
            );
        };
        const observer = new ResizeObserver(update);
        observer.observe(viewport);
        update();
        return () => {
            observer.disconnect();
            document.documentElement.style.removeProperty('--toast-space');
        };
    }, [toasts]);

    return (
        <ToastProvider label={t('notification')}>
            {toasts.map(function ({ id, title, description, action, localized, ...props }) {
                const text = localized ? localized(t) : { title, description };
                return (
                    <Toast key={id} {...props}>
                        <div className='min-w-0 grid gap-1'>
                            {text.title && <ToastTitle>{text.title}</ToastTitle>}
                            {text.description && <ToastDescription>{text.description}</ToastDescription>}
                        </div>
                        {action}
                        <ToastClose />
                    </Toast>
                );
            })}
            <ToastViewport ref={viewportRef} />
        </ToastProvider>
    );
}
