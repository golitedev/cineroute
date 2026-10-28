'use client';
import { useT } from '@/lib/i18n';
import QueueDialog from './queue-dialog';
import React, { useState } from 'react';
import { AnimatePresence } from 'motion/react';
import { Button } from '../ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '../ui/card';
import { ChevronDown, ChevronUp, DotIcon, List as QueueIcon, X } from 'lucide-react';
import { motion } from 'motion/react';
import { Progress } from '../ui/progress';
import { useStatusBar } from '@/lib/status-bar/context';

export type StatusBarProps = {
    open: boolean;
    openPreference: boolean;
    title: string;
    description: string;
    progress: number;
    processing: boolean;
    onCancel?: () => void;
};

const StatusBar = () => {
    const t = useT();
    const { statusBar, setStatusBar } = useStatusBar();
    const [queueOpen, setQueueOpen] = useState<boolean>(false);

    return (
        <>
            <AnimatePresence>
                {statusBar.open && (
                    <motion.div
                        key='progress'
                        className='w-full z-[20] absolute bottom-0 left-0 pointer-events-auto'
                        initial={{ y: 100, opacity: 0 }}
                        animate={{ y: 0, opacity: 1 }}
                        transition={{ type: 'spring' }}
                        exit={{ y: 100, opacity: 0 }}
                    >
                        <Card>
                            <CardHeader className='status-header flex items-center flex-row justify-between transition-[height] pt-4 overflow-hidden pb-2'>
                                <Button variant='ghost' size='icon' onClick={() => setQueueOpen(true)} aria-label={t('openQueue')} className='size-6'>
                                    <QueueIcon className='w-4 h-4' />
                                </Button>
                                <div className='flex flex-col justify-center text-center items-center overflow-x-hidden px-2'>
                                    <CardTitle className='status-title text-nowrap max-w-full truncate p-1'>{statusBar.title || t('noItems')}</CardTitle>
                                    <AnimatePresence>
                                        {statusBar.description && (
                                            <motion.div
                                                initial={{ opacity: 0, y: -10, maxHeight: 0 }}
                                                animate={{ opacity: 1, y: 0, maxHeight: 100 }}
                                                exit={{ opacity: 0, y: 10, maxHeight: 0 }}
                                                className='max-w-full'
                                            >
                                                <CardDescription className='status-description text-nowrap max-w-full truncate'>
                                                    {statusBar.description}
                                                </CardDescription>
                                            </motion.div>
                                        )}
                                    </AnimatePresence>
                                </div>
                                <Button
                                    variant='ghost'
                                    size='icon'
                                    onClick={() =>
                                        setStatusBar({
                                            ...statusBar,
                                            open: false,
                                            openPreference: false
                                        })
                                    }
                                    aria-label={t('collapseStatus')}
                                    className='size-6'
                                >
                                    <ChevronDown className='w-4 h-4' />
                                </Button>
                            </CardHeader>
                            <CardContent className='status-content flex items-center justify-center gap-2'>
                                <Progress aria-label={t('downloadProgress')} value={statusBar.progress} />
                                <Button
                                    variant='ghost'
                                    size='icon'
                                    className='size-6 pointer-events-auto'
                                    disabled={!statusBar.processing}
                                    aria-label={t('cancelDownload')}
                                    onClick={statusBar.onCancel}
                                >
                                    <X className='w-4 h-4' />
                                </Button>
                            </CardContent>
                        </Card>
                    </motion.div>
                )}
            </AnimatePresence>
            {!statusBar.open && (
                <motion.div
                    key='open'
                    className='w-full z-[20] flex items-end justify-center absolute bottom-0 h-full'
                    initial={{ y: 100, opacity: 0 }}
                    animate={{ y: 0, opacity: 1 }}
                    transition={{ type: 'spring', stiffness: 260, damping: 20 }}
                >
                    <div className='flex items-end gap-2 pointer-events-auto'>
                        {
                            <Button variant='outline' size='icon' aria-label={t('openQueue')} onClick={() => setQueueOpen(true)}>
                                <QueueIcon className='size-4' />
                            </Button>
                        }
                        {statusBar.processing && (
                            <Button
                                variant='outline'
                                size='sm'
                                onClick={() => setStatusBar({ ...statusBar, open: true, openPreference: true })}
                                aria-label={t('expandStatus')}
                                className='status-expand max-w-[80vw] flex items-center justify-center flex-col py-4 gap-1 h-auto pointer-events-auto'
                            >
                                <ChevronUp className='w-4 h-4' />
                                <AnimatePresence>
                                    {statusBar.description && (
                                        <motion.div
                                            initial={{ opacity: 0, y: -10, maxHeight: 0 }}
                                            animate={{ opacity: 1, y: 0, maxHeight: 20 }}
                                            exit={{ opacity: 0, y: 10, maxHeight: 0 }}
                                            className='flex items-center justify-center text-center'
                                        >
                                            <p>{statusBar.description}</p>
                                            <DotIcon className='min-w-[20px] min-h-[20px] size-[20px]' />
                                            <p>{Math.round(statusBar.progress)}%</p>
                                        </motion.div>
                                    )}
                                </AnimatePresence>
                            </Button>
                        )}
                    </div>
                </motion.div>
            )}

            <QueueDialog open={queueOpen} setOpen={setQueueOpen} />
        </>
    );
};

export default StatusBar;
