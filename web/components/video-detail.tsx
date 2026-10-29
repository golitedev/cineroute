'use client';

import { useEffect, useState } from 'react';
import { LoaderCircle, TriangleAlert } from 'lucide-react';
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog';
import { Button } from '@/components/ui/button';
import { useT } from '@/lib/i18n';
import { type Media, type VideoJob, mediaTitle, mediaYear, posterURL, portalRequest } from '@/lib/cineroute-api';
import { useStatusBar } from '@/lib/status-bar/context';

type Detail = {
    media: Media;
    seasons: { season_number: number; name: string; episode_count: number; air_date: string }[];
    auto_approve: boolean;
    digitally_available: boolean;
};

// The catalog returns null for movies and shows without announced seasons.
type DetailResponse = Omit<Detail, 'seasons'> & { seasons?: Detail['seasons'] | null };

export default function VideoDetail({ media, onClose }: { media: Media | null; onClose: () => void }) {
    const t = useT();
    const { jobs, refresh } = useStatusBar();
    const [detail, setDetail] = useState<Detail | null>(null);
    const [seasons, setSeasons] = useState<number[]>([]);
    const [saving, setSaving] = useState(false);
    const [error, setError] = useState('');
    const [message, setMessage] = useState('');
    const existing = media
        ? jobs.find((j) => j.kind === media.media_type && j.media?.id === media.id && !['cancelled', 'declined'].includes(j.status))
        : undefined;
    useEffect(() => {
        let active = true;
        setDetail(null);
        setError('');
        setMessage('');
        setSaving(false);
        if (media)
            void portalRequest<DetailResponse>(`/api/catalog/${media.media_type}/${media.id}`)
                .then((data) => {
                    if (!active) return;
                    const normalized = { ...data, seasons: data.seasons ?? [] };
                    setDetail(normalized);
                    setSeasons(
                        normalized.seasons
                            .filter((s) => s.episode_count > 0 && (!s.air_date || s.air_date <= new Date().toISOString().slice(0, 10)))
                            .map((s) => s.season_number)
                    );
                })
                .catch((e) => {
                    if (active) setError(e.message);
                });
        return () => {
            active = false;
        };
    }, [media?.id, media?.media_type]);
    async function submit() {
        if (!media || !detail || saving) return;
        if (media.media_type === 'tv' && !seasons.length) {
            setError(t('chooseSeasons'));
            return;
        }
        setSaving(true);
        setError('');
        setMessage('');
        try {
            const result = await portalRequest<{ request: VideoJob }>('/api/user/requests', 'POST', {
                media_type: media.media_type,
                tmdb_id: media.id,
                ...(media.media_type === 'tv' ? { seasons } : {})
            });
            setMessage(result.request.message);
            // The request is accepted; dismiss the dialog so the shared status
            // bar takes over. Refreshing in the background must not reopen it
            // or replace a successful request with a status-bar error.
            onClose();
            void refresh().catch(() => {});
        } catch (e) {
            setError(e instanceof Error ? e.message : t('requestFailed'));
        } finally {
            setSaving(false);
        }
    }
    const disabled = saving || !detail || (media?.media_type === 'movie' && !!existing) || (media?.media_type === 'tv' && !seasons.length);
    return (
        <Dialog
            open={!!media}
            onOpenChange={(open) => {
                if (!open) onClose();
            }}
        >
            <DialogContent className='video-dialog max-w-2xl max-h-[90dvh] overflow-y-auto'>
                <DialogTitle className='pr-6'>{media ? mediaTitle(media) : t('requestDetails')}</DialogTitle>
                <DialogDescription>{media ? `${media.media_type === 'tv' ? t('tv') : t('movies')} · ${mediaYear(media)}` : ''}</DialogDescription>
                {!detail && !error && <LoaderCircle className='mx-auto animate-spin' aria-label={t('search')} />}
                {detail && (
                    <>
                        <div className='flex gap-5 items-start'>
                            {posterURL(detail.media.poster_path) && (
                                <img src={posterURL(detail.media.poster_path)} alt='' className='w-24 sm:w-32 aspect-[2/3] object-cover rounded-md shrink-0' />
                            )}
                            <p className='text-sm leading-relaxed text-muted-foreground'>{detail.media.overview || t('noDescription')}</p>
                        </div>
                        {detail.digitally_available === false && (
                            <div
                                role='note'
                                aria-label={t('availabilityNotice')}
                                className='flex items-start gap-3 rounded-md border border-amber-500/30 bg-amber-500/5 p-3 text-sm leading-relaxed text-amber-200'
                            >
                                <TriangleAlert className='mt-0.5 size-4 shrink-0' aria-hidden='true' />
                                <p>{t(media?.media_type === 'tv' ? 'tvAvailabilityWarning' : 'movieAvailabilityWarning')}</p>
                            </div>
                        )}
                        {media?.media_type === 'tv' && (
                            <fieldset disabled={saving} className='space-y-3'>
                                <legend className='font-semibold mb-3'>{t('seasons')}</legend>
                                <div className='grid grid-cols-2 sm:grid-cols-3 gap-2'>
                                    {detail.seasons
                                        .filter((s) => s.episode_count > 0)
                                        .map((season) => (
                                            <label
                                                key={season.season_number}
                                                className='flex items-center gap-2 rounded-md border p-3 text-sm cursor-pointer hover:bg-accent'
                                            >
                                                <input
                                                    type='checkbox'
                                                    checked={seasons.includes(season.season_number)}
                                                    onChange={(e) =>
                                                        setSeasons((values) =>
                                                            e.target.checked
                                                                ? [...values, season.season_number]
                                                                : values.filter((n) => n !== season.season_number)
                                                        )
                                                    }
                                                />
                                                <span>
                                                    {season.name}
                                                    <small className='block text-muted-foreground'>
                                                        {season.episode_count} {t('episodes')}
                                                    </small>
                                                </span>
                                            </label>
                                        ))}
                                </div>
                                {!detail.seasons.length && <p className='text-sm text-muted-foreground'>{t('noSeasons')}</p>}
                            </fieldset>
                        )}
                        <div className='flex flex-col sm:flex-row gap-3 sm:items-center'>
                            <Button disabled={disabled} onClick={() => void submit()}>
                                {saving && <LoaderCircle className='size-4 animate-spin' />}
                                {existing
                                    ? media?.media_type === 'tv'
                                        ? t('addSeasons')
                                        : t(`status.${existing.status}`)
                                    : detail.auto_approve
                                      ? t('addLibrary')
                                      : t('requestApproval')}
                            </Button>
                            <p className='text-xs text-muted-foreground'>{detail.auto_approve ? t('autoHint') : t('approvalHint')}</p>
                        </div>
                    </>
                )}
                {error && (
                    <p role='alert' className='text-sm text-destructive'>
                        {error}
                    </p>
                )}
                {message && (
                    <p role='status' className='text-sm'>
                        {message}
                    </p>
                )}
            </DialogContent>
        </Dialog>
    );
}
