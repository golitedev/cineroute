import { useT } from '@/lib/i18n';
import React from 'react';
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '../ui/dialog';
import QueueView from './queue-view';

const QueueDialog = ({ open, setOpen }: { open: boolean; setOpen: (open: boolean) => void }) => {
    const t = useT();
    const contentRef = React.useRef<HTMLDivElement>(null);
    return (
        <Dialog open={open} onOpenChange={setOpen}>
            <DialogContent
                ref={contentRef}
                tabIndex={-1}
                className='queue-dialog sm:max-w-[600px] outline-none'
                // Radix otherwise focuses the first focusable child (the search
                // input), which opens the mobile keyboard as soon as the dialog
                // appears. Focus the dialog instead; the user taps the search
                // field when they actually want to type.
                onOpenAutoFocus={(event) => {
                    event.preventDefault();
                    contentRef.current?.focus();
                }}
            >
                <DialogHeader>
                    <DialogTitle>{t('queue')}</DialogTitle>
                    <DialogDescription>{t('queueDescription')}</DialogDescription>
                </DialogHeader>
                <QueueView />
            </DialogContent>
        </Dialog>
    );
};
export default QueueDialog;
