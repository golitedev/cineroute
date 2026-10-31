import type { Subtitle, Metrics } from './types';
export const subtitleStatus = (status: string) =>
	({
		pending: 'Needs subtitles',
		has_targets: 'Has subtitles',
		has_swedish: 'Has subtitles',
		added_review: 'Added (review)',
		partial: 'Partly added',
		needs_review: 'Needs review',
		no_match: 'No match',
		no_reference: 'No reference',
	})[status] || status;
export const reference = (item: Subtitle) =>
	item.reference_kind === 'external'
		? `external ${item.reference_lang || 'unknown'} subtitle`
		: item.reference_kind === 'embedded'
			? `embedded ${item.reference_lang || 'unknown'} stream #${item.reference_stream}`
			: 'not chosen yet';
export const metrics = (m?: Metrics) =>
	m
		? `${Math.round((m.within_2s || 0) * 100)}% within 2s · p90 ${(m.p90 || 0).toFixed(2)}s · gaps ${(m.start_gap_minutes || 0).toFixed(1)}/${(m.end_gap_minutes || 0).toFixed(1)}m · overrun ${(m.overrun_minutes || 0).toFixed(1)}m · score ${(m.score || 0).toFixed(0)}`
		: '';
export function subtitleFilter(item: Subtitle, filter: string) {
	const done = ['added', 'added_review', 'has_targets', 'has_swedish'].includes(item.status);
	switch (filter) {
		case 'all':
			return true;
		case 'added':
			return ['added', 'added_review', 'partial'].includes(item.status);
		case 'has_targets':
			return done;
		case 'pending':
			return ['pending', 'processing', 'downloading', 'syncing'].includes(item.status);
		case 'no_external':
			return !item.has_external_subtitle && !done;
		case 'not_analyzed':
			return !item.probed && !done;
		case 'has_external':
			return !!item.has_external_subtitle && !done;
		default:
			return item.status === filter;
	}
}
