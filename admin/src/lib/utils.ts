import { clsx, type ClassValue } from 'clsx';
import { twMerge } from 'tailwind-merge';
import type { Snippet } from 'svelte';
export const cn = (...values: ClassValue[]) => twMerge(clsx(values));
export type WithElementRef<T, E = HTMLElement> = T & { ref?: E | null; children?: Snippet };
export const bytes = (n = 0) => {
	const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
	let i = 0;
	while (n >= 1024 && i < 4) {
		n /= 1024;
		i++;
	}
	return `${n.toFixed(i ? 2 : 0)} ${units[i]}`;
};
export const date = (value?: string) => (!value || value.startsWith('0001-') ? '' : new Date(value).toLocaleString());
export const title = (media?: { title?: string; name?: string } | null) => media?.title || media?.name || '';
export const poster = (value?: string) =>
	/^\/[a-zA-Z0-9_.-]+\.(jpg|png|webp)$/.test(value || '') ? `https://image.tmdb.org/t/p/w342${value}` : '';
export const message = (error: unknown) => (error instanceof Error ? error.message : String(error));
export const status = (value: string) =>
	({
		pending: 'Awaiting approval',
		planned: 'Verifying download',
		stalled: 'Stalled',
		paused: 'Paused',
		checking: 'Checking files',
		metadata: 'Fetching metadata',
		queued: 'Queued',
		searching: 'Finding a release',
		waiting: 'Waiting for a release',
		downloading: 'Downloading',
		complete: 'Ready',
		monitoring: 'Monitoring',
		error: 'Needs attention',
		failed: 'Failed',
		declined: 'Declined',
		cancelled: 'Canceled',
	})[value] || value;
export const percent = (n = 0) => Math.round(Math.max(0, Math.min(1, n)) * 100);
export const safeURL = (value?: string) => {
	try {
		const u = new URL(value || '');
		return ['http:', 'https:'].includes(u.protocol) && !u.username && !u.password ? u.href : '';
	} catch {
		return '';
	}
};
export function move<T>(items: T[], index: number, offset: number) {
	const next = index + offset;
	if (next < 0 || next >= items.length) return;
	[items[index], items[next]] = [items[next], items[index]];
}
