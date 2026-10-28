import { useT } from '@/lib/i18n';
import React from 'react';
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '../ui/dialog';
import QueueView from './queue-view';

const QueueDialog = ({ open, setOpen }: { open: boolean; setOpen: (open: boolean) => void }) => {
    const t = useT();
    return (
        <Dialog open={open} onOpenChange={setOpen}>
            <DialogContent className='queue-dialog sm:max-w-[600px]'>
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
