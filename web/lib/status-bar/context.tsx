'use client';
import { useT } from '@/lib/i18n';
import React, { createContext, useCallback, useContext, useEffect, useRef, useState, ReactNode } from 'react';
import type { StatusBarProps } from '@/components/status-bar/status-bar';
import { cancelRequest, getJobs, isUnauthorized, isActive, isPending, formatApiError, formatJobMessage, Job } from '@/lib/cineroute-api';
import { toast } from '@/hooks/use-toast';

const StatusBarContext = createContext<
    | {
          statusBar: StatusBarProps;
          setStatusBar: React.Dispatch<React.SetStateAction<StatusBarProps>>;
          jobs: Job[];
          refresh: () => Promise<void>;
          act: (action: () => Promise<unknown>) => Promise<void>;
      }
    | undefined
>(undefined);

export const StatusBarProvider = ({ children }: { children: ReactNode }) => {
    const t = useT();
    const [jobs, setJobs] = useState<Job[]>([]);
    const [statusBar, setStatusBar] = useState<StatusBarProps>({
        title: '',
        open: false,
        openPreference: true,
        progress: 0,
        description: '',
        processing: false
    });
    const previous = useRef<Map<string, string> | null>(null);
    const sequence = useRef(0);
    const refresh = useCallback(async () => {
        const current = ++sequence.current;
        const { jobs } = await getJobs();
        if (current !== sequence.current) return;
        for (const job of jobs) {
            const before = previous.current?.get(job.id);
            if (!before || before === job.status) continue;
            if (job.status === 'failed')
                toast({
                    variant: 'destructive',
                    localized: (t) => ({ title: t('failedToast'), description: `${job.artist} – ${job.title}: ${formatJobMessage(job, t)}` })
                });
            if (job.status === 'completed') toast({ localized: (t) => ({ title: t('addedToast'), description: `${job.artist} – ${job.title}` }) });
        }
        previous.current = new Map(jobs.map((job) => [job.id, job.status]));
        setJobs(jobs);
    }, [t]);
    const act = useCallback(
        async (action: () => Promise<unknown>) => {
            try {
                await action();
                await refresh();
            } catch (error) {
                if (isUnauthorized(error)) return;
                toast({ variant: 'destructive', localized: (t) => ({ title: t('queueError'), description: formatApiError(error, t) }) });
            }
        },
        [refresh, t]
    );

    useEffect(() => {
        const update = () => {
            void refresh().catch(() => {});
        };
        update();
        window.addEventListener('cineroute:jobs', update);
        return () => window.removeEventListener('cineroute:jobs', update);
    }, [refresh]);
    const pending = jobs.some(isPending);
    useEffect(() => {
        const timer = setInterval(
            () => {
                void refresh().catch(() => {});
            },
            pending ? 2000 : 10000
        );
        return () => clearInterval(timer);
    }, [pending, refresh]);
    useEffect(() => {
        const job = jobs.find(isActive) || jobs.find((job) => job.status === 'queued');
        setStatusBar((prev) => ({
            ...prev,
            open: !!job && prev.openPreference,
            processing: !!job,
            title: job ? t('statusTitle', { status: t(`status.${job.status}`), title: job.title }) : '',
            description:
                job && job.kind !== 'music'
                    ? formatJobMessage(job, t)
                    : job
                      ? `${isActive(job) && job.status !== 'downloading' ? formatJobMessage(job, t) : `${job.artist} · ${t('trackProgress', { done: job.tracks_done, total: job.tracks_total })}`}${job.attempt > 1 ? ` (${t('attempt', { n: job.attempt, m: job.max_attempts })})` : ''}`
                      : '',
            progress:
                job && job.kind !== 'music'
                    ? Math.round((job.progress || 0) * 100)
                    : job && job.tracks_total > 0
                      ? Math.min(100, (job.tracks_done / job.tracks_total) * 100)
                      : 0,
            onCancel: job
                ? () => {
                      void act(() => cancelRequest(job));
                  }
                : undefined
        }));
    }, [jobs, act, t]);
    return <StatusBarContext.Provider value={{ statusBar, setStatusBar, jobs, refresh, act }}>{children}</StatusBarContext.Provider>;
};

export const useStatusBar = () => {
    const context = useContext(StatusBarContext);
    if (!context) throw new Error('useStatusBar must be used within a StatusBarProvider');
    return context;
};
