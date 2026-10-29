'use client';

import { useState } from 'react';
import { LoaderCircle, X } from 'lucide-react';
import { useT } from '@/lib/i18n';
import { useStatusBar } from '@/lib/status-bar/context';
import { type Job, type Media, isPending, cancelRequest, retryJob, dismissJob, formatJobMessage, formatProblem } from '@/lib/cineroute-api';
import { Button } from '../ui/button';
import { Input } from '../ui/input';
import { Card, CardHeader } from '../ui/card';
import { Progress } from '../ui/progress';
import VideoDetail from '../video-detail';
import { cn } from '@/lib/utils';

export default function QueueView() {
    const t = useT();
    const { jobs, act } = useStatusBar();
    const [search, setSearch] = useState('');
    const [filter, setFilter] = useState('all');
    const [selected, setSelected] = useState<Media | null>(null);
    const [busy, setBusy] = useState<string | null>(null);
    const filtered = jobs.filter(
        (job) => `${job.title} ${job.artist}`.toLocaleLowerCase().includes(search.toLocaleLowerCase()) && (filter === 'all' || job.kind === filter)
    );
    const pending = filtered.filter(isPending),
        recent = filtered.filter((j) => !isPending(j));
    async function action(job: Job, perform: () => Promise<unknown>) {
        setBusy(job.id);
        await act(perform);
        setBusy(null);
    }
    function renderJob(job: Job) {
        const music = job.kind === 'music';
        const progress = music ? (job.tracks_total ? job.tracks_done / job.tracks_total : 0) : job.progress || 0;
        return (
            <Card key={`${job.kind}-${job.id}`} className={cn(job.status === 'failed' && 'border-destructive')}>
                <CardHeader className='p-3 space-y-3'>
                    <div className='flex gap-3 items-start'>
                        {job.cover && (
                            <img
                                src={job.cover}
                                alt=''
                                loading='lazy'
                                className={cn('w-12 object-cover rounded shrink-0', music ? 'aspect-square' : 'aspect-[2/3]')}
                            />
                        )}
                        <div className='flex-1 min-w-0'>
                            {job.media ? (
                                <button type='button' className='font-semibold text-left hover:underline break-words' onClick={() => setSelected(job.media!)}>
                                    {job.title}
                                </button>
                            ) : (
                                <p className='font-semibold break-words'>{job.title}</p>
                            )}
                            <p className='text-xs text-muted-foreground break-words mt-1'>
                                {music ? t('music') : job.kind === 'tv' ? t('tv') : t('movies')}
                                {job.artist ? ` · ${job.artist}` : ''}
                            </p>
                            <p className={cn('text-xs mt-1', job.status === 'failed' && 'text-destructive')}>{t(`status.${job.status}`)}</p>
                        </div>
                        {isPending(job) && (
                            <Button
                                variant='ghost'
                                size='icon'
                                className='shrink-0'
                                disabled={busy === job.id}
                                aria-label={t('cancelTitle', { title: job.title })}
                                onClick={() => {
                                    if (window.confirm(t('cancelConfirm'))) void action(job, () => cancelRequest(job));
                                }}
                            >
                                {busy === job.id ? <LoaderCircle className='size-4 animate-spin' /> : <X className='size-4' />}
                            </Button>
                        )}
                    </div>
                    {job.status === 'downloading' && <Progress value={Math.round(progress * 100)} aria-label={t('downloadProgress')} />}
                    {job.message && <p className='text-sm break-words text-muted-foreground'>{formatJobMessage(job, t)}</p>}
                    {music && job.status === 'failed' && (job.problem_items?.length || job.problems?.length) > 0 && (
                        <details className='text-sm'>
                            <summary className='cursor-pointer text-destructive'>
                                {t('problems', { count: job.problem_items?.length || job.problems.length })}
                            </summary>
                            <ul className='list-disc pl-5 space-y-1'>
                                {(job.problem_items?.length ? job.problem_items.map((item) => formatProblem(item, t)) : job.problems).map((problem, i) => (
                                    <li key={i} className='break-words'>
                                        {problem}
                                    </li>
                                ))}
                            </ul>
                        </details>
                    )}
                    {music && !isPending(job) && (
                        <div className='flex gap-2'>
                            {['failed', 'cancelled'].includes(job.status) && (
                                <Button size='sm' variant='outline' disabled={busy === job.id} onClick={() => void action(job, () => retryJob(job.id))}>
                                    {t('retry')}
                                </Button>
                            )}
                            <Button size='sm' variant='ghost' disabled={busy === job.id} onClick={() => void action(job, () => dismissJob(job.id))}>
                                {t('dismiss')}
                            </Button>
                        </div>
                    )}
                </CardHeader>
            </Card>
        );
    }
    return (
        <div className='queue-view space-y-4'>
            <Input aria-label={t('queueSearch')} placeholder={t('queueSearch')} value={search} onChange={(e) => setSearch(e.target.value)} />
            <div className='flex gap-1 flex-wrap'>
                {[
                    ['all', t('all')],
                    ['movie', t('movies')],
                    ['tv', t('tv')],
                    ['music', t('music')]
                ].map(([value, label]) => (
                    <Button
                        key={value}
                        size='sm'
                        variant={filter === value ? 'secondary' : 'ghost'}
                        onClick={() => setFilter(value)}
                        aria-pressed={filter === value}
                    >
                        {label}
                    </Button>
                ))}
            </div>
            <div className='queue-scroll max-h-[60dvh] overflow-y-auto space-y-3 pr-1'>
                <h3 className='font-semibold'>{t('queueActive')}</h3>
                {pending.length ? pending.map(renderJob) : <p className='text-sm text-muted-foreground'>{t('noQueued')}</p>}
                <h3 className='font-semibold pt-2'>{t('recent')}</h3>
                {recent.length ? recent.map(renderJob) : <p className='text-sm text-muted-foreground'>{t('noRecent')}</p>}
            </div>
            <VideoDetail media={selected} onClose={() => setSelected(null)} />
        </div>
    );
}
