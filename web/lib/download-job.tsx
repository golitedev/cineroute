'use client';

import type { T } from './i18n';
import { toast } from '@/hooks/use-toast';
import { QobuzAlbum, QobuzArtistResults, QobuzTrack } from './qobuz-dl';
import { catalog, formatApiError, isUnauthorized, queueAlbum, refreshJobs } from './cineroute-api';

// CineRoute only downloads whole releases. A track always queues the album,
// EP or single it belongs to.
export function downloadLabel(album: QobuzAlbum | undefined, t: T) {
    const kind = (album as (QobuzAlbum & { release_type?: string; product_type?: string }) | undefined)?.release_type;
    const word = kind === 'single' ? 'single' : kind === 'ep' || kind === 'epSingle' ? 'ep' : kind ? 'album' : 'release';
    const label = t(`download.${word}`);
    return album?.title ? t('downloadTitle', { label, title: album.title }) : label;
}

export async function createDownloadJob(result: QobuzAlbum | QobuzTrack, quiet = false) {
    const album = 'album' in result ? result.album : result;
    try {
        const job = await queueAlbum(String(album.id));
        refreshJobs();
        if (!quiet)
            toast({
                localized: (t) => ({ title: job.status === 'skipped' ? t('alreadyToast', { title: job.title }) : t('queuedToast', { title: job.title }) }),
                duration: 3000
            });
        return job;
    } catch (error) {
        if (quiet) throw error;
        if (isUnauthorized(error)) return;
        toast({ variant: 'destructive', localized: (t) => ({ title: t('queueFailed'), description: formatApiError(error, t) }) });
    }
}

export async function downloadArtistDiscography(artistResults: QobuzArtistResults, type: 'album' | 'epSingle' | 'live' | 'compilation' | 'all', t: T) {
    let queued = 0,
        skipped = 0,
        failed = 0;
    const types = type === 'all' ? (['album', 'epSingle', 'live', 'compilation'] as const) : [type];
    try {
        for (const category of types) {
            const releases = artistResults.artist.releases[category];
            if (!releases) continue;
            const albums = [...releases.items];
            let hasMore = releases.has_more;
            while (hasMore) {
                const response = await catalog.get('/api/music/get-releases', {
                    params: { artist_id: artistResults.artist.id, release_type: category, offset: albums.length, limit: 50 }
                });
                const page = response.data.data;
                if (!page.items.length) throw new Error(t('remainingFailed'));
                albums.push(...page.items);
                hasMore = page.has_more;
            }
            for (const album of albums) {
                try {
                    const job = await createDownloadJob(album, true);
                    if (job?.status === 'skipped') skipped++;
                    else queued++;
                } catch (error) {
                    if (isUnauthorized(error)) throw error;
                    failed++;
                }
            }
        }
        toast({
            variant: failed && !queued ? 'destructive' : 'default',
            localized: (t) => ({
                title: t('artistReleases'),
                description: [
                    t('summaryQueued', { count: queued }),
                    ...(skipped ? [t('summarySkipped', { count: skipped })] : []),
                    ...(failed ? [t('summaryFailed', { count: failed })] : [])
                ].join(', ')
            }),
            duration: 5000
        });
    } catch (error) {
        if (isUnauthorized(error)) return;
        toast({ variant: 'destructive', localized: (t) => ({ title: t('artistQueueFailed'), description: formatApiError(error, t) }) });
    }
}
