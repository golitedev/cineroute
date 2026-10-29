'use client';

import { useEffect, useRef, useState } from 'react';
import { ArrowRight, Search, LoaderCircle, DiscAlbum, Disc3, Users, Film, Tv, Music2, ChevronDown } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { DropdownMenu, DropdownMenuContent, DropdownMenuRadioGroup, DropdownMenuRadioItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu';
import ReleaseCard from '@/components/release-card';
import VideoDetail from '@/components/video-detail';
import { useT } from '@/lib/i18n';
import { catalog, type Media, mediaTitle, mediaYear, posterURL, portalRequest, getInfo, formatApiError } from '@/lib/cineroute-api';
import { type FilterDataType, type QobuzSearchResults, type QobuzSearchFilters, filterExplicit } from '@/lib/qobuz-dl';
import { useStatusBar } from '@/lib/status-bar/context';

// Kept for the native artist and release dialogs.
export const filterData: (Omit<FilterDataType[number], 'value'> & { value: QobuzSearchFilters })[] = [
    { label: 'Albums', value: 'albums', icon: DiscAlbum },
    { label: 'Tracks', value: 'tracks', icon: Disc3 },
    { label: 'Artists', value: 'artists', icon: Users }
];
type Kind = 'music' | 'movie' | 'tv';
type VideoResults = { results: Media[]; page: number; total_pages: number; similar_matches?: boolean };

export default function SearchView() {
    const t = useT();
    const { jobs } = useStatusBar();
    const [kind, setKind] = useState<Kind>('movie');
    const [field, setField] = useState<QobuzSearchFilters>('albums');
    const [query, setQuery] = useState('');
    const [searched, setSearched] = useState('');
    const [music, setMusic] = useState<QobuzSearchResults | null>(null);
    const [video, setVideo] = useState<VideoResults | null>(null);
    const [selected, setSelected] = useState<Media | null>(null);
    const [loading, setLoading] = useState(false);
    const [error, setError] = useState('');
    const [enabled, setEnabled] = useState<boolean | null>(null);
    const token = useRef(0);
    const controller = useRef<AbortController | null>(null);
    useEffect(() => {
        void getInfo()
            .then((info) => setEnabled(info.enabled))
            .catch(() => {});
        return () => controller.current?.abort();
    }, []);
    const hasResults = !!music || !!video;
    async function search(value = query, more = false) {
        const trimmed = value.trim();
        if (trimmed.length < 2) return;
        const current = ++token.current;
        controller.current?.abort();
        const abort = new AbortController();
        controller.current = abort;
        setLoading(true);
        setError('');
        if (!more) {
            setMusic(null);
            setVideo(null);
        }
        setSearched(trimmed);
        try {
            if (kind === 'music') {
                const offset = more ? music?.[field]?.items.length || 0 : 0;
                const response = await catalog.get(`/api/music/get-music`, { params: { q: trimmed, offset }, signal: abort.signal });
                if (current !== token.current) return;
                const data = response.data.data as QobuzSearchResults;
                if (data.switchTo) setField(data.switchTo);
                setMusic((previous) =>
                    more && previous
                        ? {
                              ...data,
                              ...Object.fromEntries(
                                  filterData.map((f) => [f.value, { ...data[f.value], items: [...previous[f.value].items, ...data[f.value].items] }])
                              )
                          }
                        : data
                );
            } else {
                const page = more ? (video?.page || 1) + 1 : 1;
                const response = await portalRequest<VideoResults>(`/api/catalog/search?q=${encodeURIComponent(trimmed)}&type=${kind}&page=${page}`);
                if (current !== token.current) return;
                setVideo((previous) => (more && previous ? { ...response, results: [...previous.results, ...response.results] } : response));
            }
        } catch (e) {
            if (current === token.current && !abort.signal.aborted) setError(formatApiError(e, t));
        } finally {
            if (current === token.current) setLoading(false);
        }
    }
    function changeKind(next: Kind) {
        if (next === kind) return;
        token.current++;
        controller.current?.abort();
        setKind(next);
        setQuery('');
        setSearched('');
        setMusic(null);
        setVideo(null);
        setError('');
        setSelected(null);
        setLoading(false);
    }
    const MusicIcon = filterData.find((f) => f.value === field)?.icon || DiscAlbum;
    const items = music ? filterExplicit(music)[field].items : [];
    return (
        <>
            <section className='search-heading w-full max-w-[720px] space-y-5' data-results={hasResults}>
                <button
                    type='button'
                    className='mx-auto flex flex-col items-center gap-3 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring rounded-md'
                    aria-label='CineRoute'
                    onClick={() => {
                        token.current++;
                        controller.current?.abort();
                        setQuery('');
                        setSearched('');
                        setMusic(null);
                        setVideo(null);
                        setError('');
                        setLoading(false);
                    }}
                >
                    <h1 className='flex items-center justify-center gap-3'>
                        <img src='/favicon.svg' alt='' className='h-11 w-11 sm:h-12 sm:w-12' />
                        <img src='/logo.svg' alt='CineRoute' className='h-10 sm:h-12 w-auto max-w-[240px]' />
                    </h1>
                    <p className='text-sm sm:text-base text-muted-foreground font-medium text-center'>{t('tagline')}</p>
                </button>
                <form
                    className='search-bar flex w-full items-center gap-2'
                    onSubmit={(e) => {
                        e.preventDefault();
                        void search();
                    }}
                    role='search'
                >
                    <div className='flex flex-1 items-center border rounded-md bg-background px-3 py-1 min-w-0'>
                        <Search className='size-5 shrink-0 text-muted-foreground' aria-hidden='true' />
                        <Input
                            className='border-0 focus-visible:ring-0 shadow-none font-medium'
                            value={query}
                            onChange={(e) => setQuery(e.target.value)}
                            aria-label={t('search')}
                            placeholder={t('searchPlaceholder')}
                            minLength={2}
                            maxLength={200}
                            autoComplete='off'
                        />
                    </div>
                    <Button type='submit' size='icon' className='size-12 shrink-0' disabled={loading || query.trim().length < 2} aria-label={t('search')}>
                        {loading ? <LoaderCircle className='animate-spin' /> : <ArrowRight />}
                    </Button>
                </form>
                <nav className='flex items-center justify-start gap-1 sm:justify-center sm:gap-2 flex-wrap' aria-label={t('search')}>
                    <Button
                        variant={kind === 'movie' ? 'secondary' : 'outline'}
                        className='border px-2 aria-[pressed=true]:border-transparent sm:px-4'
                        aria-pressed={kind === 'movie'}
                        onClick={() => changeKind('movie')}
                    >
                        <Film className='size-4' />
                        {t('movies')}
                    </Button>
                    <Button
                        variant={kind === 'tv' ? 'secondary' : 'outline'}
                        className='border px-2 aria-[pressed=true]:border-transparent sm:px-4'
                        aria-pressed={kind === 'tv'}
                        onClick={() => changeKind('tv')}
                    >
                        <Tv className='size-4' />
                        {t('tv')}
                    </Button>
                    <div className='contents sm:relative sm:inline-flex sm:shrink-0 sm:items-center'>
                        <Button
                            variant={kind === 'music' ? 'secondary' : 'outline'}
                            className='border px-2 aria-[pressed=true]:border-transparent sm:px-4'
                            aria-pressed={kind === 'music'}
                            onClick={() => changeKind('music')}
                        >
                            <Music2 className='size-4' />
                            {t('music')}
                        </Button>
                        {kind === 'music' && (
                            <DropdownMenu>
                                <DropdownMenuTrigger asChild>
                                    <Button
                                        variant='ghost'
                                        size='sm'
                                        className='gap-1 px-1 text-muted-foreground sm:absolute sm:left-full sm:top-1/2 sm:ml-2 sm:-translate-y-1/2 sm:gap-2 sm:px-3'
                                        aria-label={`${t('music')} · ${t(field)}`}
                                    >
                                        <MusicIcon className='size-4' />
                                        {t(field)}
                                        <ChevronDown className='size-3' />
                                    </Button>
                                </DropdownMenuTrigger>
                                <DropdownMenuContent align='end'>
                                    <DropdownMenuRadioGroup value={field} onValueChange={(value) => setField(value as QobuzSearchFilters)}>
                                        {filterData.map((f) => (
                                            <DropdownMenuRadioItem key={f.value} value={f.value}>
                                                <f.icon className='size-4 mr-2' />
                                                {t(f.value)}
                                            </DropdownMenuRadioItem>
                                        ))}
                                    </DropdownMenuRadioGroup>
                                </DropdownMenuContent>
                            </DropdownMenu>
                        )}
                    </div>
                </nav>
                <div role='status' aria-live='polite' className='text-sm text-center text-muted-foreground'>
                    {kind === 'music' && enabled === false ? t('musicUnavailable') : ''}
                    {video?.similar_matches ? t('similarTitles') : ''}
                    {video && !video.results.length ? t('noTitles') : ''}
                    {music && !items.length ? t('noTitles') : ''}
                </div>
                {error && (
                    <p role='alert' className='text-center text-sm text-destructive'>
                        {error}
                    </p>
                )}
            </section>
            {hasResults && (
                <section className='search-results-wrapper w-full max-w-7xl mt-8' aria-label={t('search')} aria-busy={loading}>
                    <div className='grid grid-cols-2 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5 xl:grid-cols-6 gap-x-4 gap-y-6'>
                        {kind === 'music'
                            ? items.map((result, index) => <ReleaseCard key={`${result.id}-${index}`} result={result} resolvedTheme='dark' />)
                            : video?.results.map((media) => {
                                  const requested = jobs.find(
                                      (j) => j.media?.id === media.id && j.kind === media.media_type && !['cancelled', 'declined'].includes(j.status)
                                  );
                                  return (
                                      <button
                                          key={`${media.media_type}-${media.id}`}
                                          className='video-card group text-left min-w-0 rounded-md focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring'
                                          onClick={() => setSelected(media)}
                                      >
                                          <div className='aspect-[2/3] overflow-hidden rounded-md bg-muted relative'>
                                              {posterURL(media.poster_path) ? (
                                                  <img
                                                      src={posterURL(media.poster_path)}
                                                      alt=''
                                                      loading='lazy'
                                                      className='h-full w-full object-cover transition-transform duration-300 group-hover:scale-105'
                                                  />
                                              ) : (
                                                  <div className='h-full flex items-center justify-center text-muted-foreground p-4'>
                                                      <Film className='size-10' />
                                                  </div>
                                              )}
                                              <div className='absolute inset-0 bg-black/0 group-hover:bg-black/20 transition-colors' />
                                              {requested && (
                                                  <span className='absolute bottom-2 left-2 right-2 rounded bg-black/80 text-white text-xs py-1.5 px-2 text-center'>
                                                      {t(`status.${requested.status}`)}
                                                  </span>
                                              )}
                                          </div>
                                          <p className='font-medium text-sm truncate mt-2'>{mediaTitle(media)}</p>
                                          <p className='text-xs text-muted-foreground mt-1'>{mediaYear(media)}</p>
                                      </button>
                                  );
                              })}
                    </div>
                    {(kind === 'music' ? music && music[field].total > music[field].items.length : video && video.page < video.total_pages) && (
                        <div className='flex justify-center mt-8'>
                            <Button variant='outline' disabled={loading} onClick={() => void search(searched, true)}>
                                {loading && <LoaderCircle className='size-4 animate-spin' />}
                                {t('loadMore')}
                            </Button>
                        </div>
                    )}
                    {kind !== 'music' && (
                        <p className='text-[11px] text-muted-foreground text-center mt-8'>
                            Movie and TV metadata provided by TMDB. This product uses the TMDB API but is not endorsed or certified by TMDB.
                        </p>
                    )}
                </section>
            )}
            <VideoDetail media={selected} onClose={() => setSelected(null)} />
        </>
    );
}
