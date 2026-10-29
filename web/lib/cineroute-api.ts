import { hasKey, type T, type Params } from './i18n/core';
import axios from 'axios';

const apiBase = (process.env.NEXT_PUBLIC_API_BASE || '').replace(/\/$/, '');
export const catalog = axios.create({ baseURL: apiBase });

export class ApiError extends Error {
    constructor(
        message: string,
        public status: number,
        public code?: string
    ) {
        super(message);
    }
}

export const isUnauthorized = (error: unknown) =>
    (error instanceof ApiError && error.status === 401) || (axios.isAxiosError(error) && error.response?.status === 401);

function redirectToLogin() {
    if (typeof window === 'undefined' || window.location.pathname === '/login' || window.location.pathname === '/login/') return;
    window.location.replace(`/login?next=${encodeURIComponent(window.location.pathname + window.location.search)}`);
}

catalog.interceptors.response.use(
    (response) => response,
    (error: unknown) => {
        if (isUnauthorized(error)) redirectToLogin();
        if (axios.isAxiosError(error) && error.response) {
            const data = error.response.data;
            return Promise.reject(new ApiError(data?.error || error.message, error.response.status, data?.code));
        }
        return Promise.reject(error);
    }
);

export type JobStatus =
    | 'queued'
    | 'downloading'
    | 'verifying'
    | 'importing'
    | 'completed'
    | 'failed'
    | 'skipped'
    | 'cancelled'
    | 'pending'
    | 'searching'
    | 'waiting'
    | 'monitoring'
    | 'declined';
export type Job = {
    kind?: 'music' | 'movie' | 'tv';
    media?: Media;
    progress?: number;
    seasons?: number[];
    id: string;
    album_id: string;
    title: string;
    artist: string;
    requested_by: string;
    year: string;
    cover: string;
    tracks_total: number;
    tracks_done: number;
    status: JobStatus;
    message: string;
    message_code?: string;
    message_args?: Params;
    problem_items?: ProblemItem[];
    problems: string[];
    attempt: number;
    max_attempts: number;
    library_path: string;
    created_at: string;
    started_at: string | null;
    finished_at: string | null;
};

export const isActive = (job: Job) => ['searching', 'downloading', 'verifying', 'importing'].includes(job.status);
export const isPending = (job: Job) => !['completed', 'failed', 'skipped', 'cancelled', 'declined'].includes(job.status);

let csrf = '';
let sessionPromise: Promise<Session> | null = null;
export async function portalRequest<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
    if (!['GET', 'HEAD'].includes(method) && path !== '/api/login' && !csrf) await getMe();
    const response = await fetch(`${apiBase}${path}`, {
        method,
        headers: { ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}), ...(csrf ? { 'X-CineRoute-CSRF': csrf } : {}) },
        body: body !== undefined ? JSON.stringify(body) : undefined,
        cache: 'no-store'
    });
    if (response.status === 401) {
        csrf = '';
        sessionPromise = null;
        redirectToLogin();
    }
    const result = await response.json().catch(() => ({}));
    if (!response.ok) throw new ApiError(result.error || `Request failed (HTTP ${response.status})`, response.status, result.code);
    return result as T;
}
async function request<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
    const result = await portalRequest<{ success: boolean; data: T; error?: string }>(`/api/music/${path}`, method, body);
    if (!result.success) throw new ApiError(result.error || 'Request failed', 500);
    return result.data;
}
export type Media = {
    id: number;
    media_type: 'movie' | 'tv';
    title?: string;
    name?: string;
    release_date?: string;
    first_air_date?: string;
    poster_path?: string;
    overview?: string;
};
export type VideoJob = {
    id: string;
    media: Media;
    status: string;
    message: string;
    progress: number;
    seasons: number[];
    created_at: string;
};
export const mediaTitle = (media: Media) => media.title || media.name || '';
export const mediaYear = (media: Media) => (media.release_date || media.first_air_date || '').slice(0, 4);
export const posterURL = (path?: string) => (/^\/[A-Za-z0-9_.-]+\.(jpg|png|webp)$/i.test(path || '') ? `https://image.tmdb.org/t/p/w342${path}` : '');
export function videoJob(job: VideoJob): Job {
    return {
        ...job,
        kind: job.media.media_type,
        album_id: '',
        title: mediaTitle(job.media),
        artist: job.media.media_type === 'tv' ? (job.seasons || []).map((s) => `S${String(s).padStart(2, '0')}`).join(', ') : mediaYear(job.media),
        year: mediaYear(job.media),
        cover: posterURL(job.media.poster_path),
        tracks_total: 0,
        tracks_done: 0,
        status: ({ complete: 'completed', error: 'failed' }[job.status] || job.status) as JobStatus,
        requested_by: '',
        problems: [],
        attempt: 1,
        max_attempts: 1,
        library_path: '',
        started_at: null,
        finished_at: null
    };
}
export const getJobs = async () => {
    const [music, video] = await Promise.all([request<{ jobs: Job[] }>('jobs'), portalRequest<{ requests: VideoJob[] }>('/api/user/requests')]);
    return {
        jobs: [...music.jobs.map((job) => ({ ...job, kind: 'music' as const })), ...video.requests.map(videoJob)].sort((a, b) =>
            b.created_at.localeCompare(a.created_at)
        )
    };
};
export const queueAlbum = (album_id: string) => request<Job>('jobs', 'POST', { album_id });
export const cancelJob = (id: string) => request<Job>(`jobs/${encodeURIComponent(id)}/cancel`, 'POST');
export const retryJob = (id: string) => request<Job>(`jobs/${encodeURIComponent(id)}/retry`, 'POST');
export const cancelRequest = (job: Job) =>
    job.kind === 'music' ? cancelJob(job.id) : portalRequest(`/api/user/requests/${encodeURIComponent(job.id)}/cancel`, 'POST', {});
export const dismissJob = (id: string) => request<null>(`jobs/${encodeURIComponent(id)}`, 'DELETE');
export const getInfo = () => request<{ enabled: boolean; qobuz_ok: boolean; qobuz_error: string; qobuz_error_code?: string }>('info');
export const refreshJobs = () => window.dispatchEvent(new Event('cineroute:jobs'));
export type Session = { user: string; auth: boolean; role: string };
export function getMe(): Promise<Session> {
    if (!sessionPromise)
        sessionPromise = portalRequest<{ account: { name: string; role: string }; csrf_token: string }>('/api/session')
            .then((result) => {
                csrf = result.csrf_token;
                if (result.account.role !== 'user') throw new ApiError('Use a user account.', 403);
                return { user: result.account.name, auth: true, role: result.account.role };
            })
            .catch((error) => {
                sessionPromise = null;
                throw error;
            });
    return sessionPromise;
}
export const login = async (username: string, password: string) => {
    csrf = '';
    sessionPromise = null;
    await portalRequest('/api/login', 'POST', { username, password });
    return getMe();
};
export const logout = async () => {
    await portalRequest('/api/logout', 'POST', {});
    csrf = '';
    sessionPromise = null;
};

export function loginDestination() {
    const next = new URLSearchParams(window.location.search).get('next');
    if (next?.startsWith('/') && !next.startsWith('//')) {
        try {
            const destination = new URL(next, window.location.origin);
            if (destination.origin === window.location.origin) return destination.pathname + destination.search + destination.hash;
        } catch {
            return '/';
        }
    }
    return '/';
}

export type ProblemItem = {
    disc: number;
    track: number;
    title: string;
    reason: string;
    detail: string;
    unavailable: boolean;
    text: string;
};

export function formatApiError(error: unknown, t: T): string {
    if (error instanceof ApiError && error.code) {
        const key = `api.${error.code}`;
        if (hasKey(key)) return t(key, { error: error.message });
    }
    return error instanceof Error ? error.message : t('requestFailed');
}

export function formatAccountError(info: { qobuz_error: string; qobuz_error_code?: string }, t: T): string {
    const key = `account.${info.qobuz_error_code}`;
    return hasKey(key) ? t(key, { error: info.qobuz_error }) : info.qobuz_error || t('accountUnavailable');
}

export function formatJobMessage(job: Job, t: T): string {
    const args = job.message_args || {};
    const key = `job.${job.message_code}`;
    // Unknown codes preserve the server text; structured suffixes still apply.
    let message = job.message_code === 'checking' ? t('job.checking', { count: args.tracks ?? 0 }) : hasKey(key) ? t(key, args) : job.message;
    if (job.message_code === 'added' && (args.format || args.bits || args.khz)) {
        if (args.format && args.bits && args.khz) message += t('job.quality', args);
        else message += ` (${[args.format, args.bits ? t('bits', { bits: args.bits }) : '', args.khz ? `${args.khz} kHz` : ''].filter(Boolean).join(' / ')})`;
    }
    if (args.rate_limited === true) message += ` ${t('job.failure_rate_limited')}`;
    if (typeof args.tries === 'number') message += ` ${t('job.tries', { count: args.tries })}`;
    if (typeof args.kept_path === 'string' && args.kept_path) message += ` ${t('job.kept', { path: args.kept_path })}`;
    return message;
}

export function formatProblem(item: ProblemItem, t: T): string {
    const key = `problem.${item.reason}`;
    if (!hasKey(key)) return item.text;
    const unavailable = item.unavailable ? ` (${t('problem.unavailable')})` : '';
    if (item.reason === 'more') return t('problem.more', { count: item.detail }) + unavailable;
    if (item.reason === 'streamrip') return t('problem.streamrip', { detail: item.detail }) + unavailable;
    const location = [
        item.disc > 1 ? t('problem.disc', { disc: item.disc }) : '',
        item.track > 0 ? t('problem.track', { track: item.track }) : '',
        item.title ? `"${item.title}"` : ''
    ]
        .filter(Boolean)
        .join(' ');
    const line = `${location ? location + ': ' : ''}${t(key)}${item.detail ? ` (${item.detail})` : ''}${unavailable}`;
    return line.charAt(0).toLocaleUpperCase() + line.slice(1);
}
