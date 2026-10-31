<script lang="ts">
	import Notice from './Notice.svelte';
	import { date, status, percent, safeURL, bytes } from '../lib/utils';
	import type { Download } from '../lib/types';
	let { download }: { download: Download } = $props();
	const groups = [
		['audio', 'Reported audio'],
		['subtitles', 'Reported full subtitles'],
		['forced_subtitles', 'Reported forced-only subtitles'],
		['text_subtitles', 'Reported text-based subtitles'],
		['image_subtitles', 'Reported image-based subtitles'],
	] as const;
	const language = (code: string) => {
		try {
			return code === 'original' ? 'Original language' : new Intl.DisplayNames(['en'], { type: 'language' }).of(code) || code;
		} catch {
			return code;
		}
	};
	const link = $derived(safeURL(download.tracker_url));
</script>

<div class="download-detail">
	<div class="section-heading">
		<h3>{download.title || 'Recorded torrent'}</h3>
		<span class="badge" class:good={download.status === 'complete'}>{status(download.status)}</span>
	</div>
	<p class="muted text-xs">
		{download.tracker || 'Tracker not recorded'} · {download.drive || 'Disk not recorded'}{#if download.quality}
			· {download.quality}{/if}{#if download.size > 0}
			· {bytes(download.size)}{/if}
	</p>
	<p class="muted text-xs">{download.requested_by ? `Download started for ${download.requested_by}` : 'Download initiator not recorded'}</p>
	{#if download.episodes?.length}<p class="muted text-xs">Episodes: {download.episodes.join(', ')}</p>{/if}
	{#if download.languages?.status === 'reported'}{@const metadata = download.languages}
		<div class="language-evidence">
			{#each groups as [key, label]}{#if metadata[key]?.length}<p>
						{label}: {metadata[key]!.map(language).join(', ')}
					</p>{/if}{/each}{#if metadata.has_text_subtitles && !metadata.text_subtitles?.length}<p>
					Reported text-based subtitles: language not reported
				</p>{/if}{#if metadata.has_image_subtitles && !metadata.image_subtitles?.length}<p>
					Reported image-based subtitles: language not reported
				</p>{/if}{#if metadata.video_bitrate_bps && metadata.video_bitrate_bps > 0}<p>
					Reported video bitrate: {(metadata.video_bitrate_bps / 1000000).toFixed(2)} Mbps
				</p>{/if}
			<p>Language preference points when selected: {download.language_score || 0}</p>
			<p>Text subtitle preference points when selected: {download.subtitle_format_score || 0}</p>
		</div>{:else if download.languages?.status}<p class="muted text-xs">
			Tracker language metadata {download.languages.status === 'unsupported' ? 'not supported' : 'unavailable'}; no language preference
			points.
		</p>{/if}
	{#if download.status !== 'complete'}<div class="progress-row">
			<progress max="100" value={percent(download.progress)} aria-label="Torrent progress"></progress><span
				>{percent(download.progress)}%</span
			>
		</div>{/if}
	<p class="muted text-xs">
		{#if download.added_at}Added {date(download.added_at)}{/if}{#if download.completed_at}
			· Completed {date(download.completed_at)}{/if}{#if download.failed_at}
			· Failed {date(download.failed_at)}{/if}
	</p>
	{#if download.status !== 'complete' && download.last_progress}<p class="muted text-xs">
			Last progress {date(download.last_progress)} · Checked {date(download.observed_at)}
		</p>{/if}{#if download.torrent_state}<p class="muted text-xs">qBittorrent: {download.torrent_state}</p>{/if}{#if download.error}<Notice
			error={download.error}
		/>{/if}
	{#if link}<a href={link} target="_blank" rel="noopener noreferrer">Open torrent on tracker ↗</a>{:else}<span class="muted text-xs"
			>Tracker page not recorded for this attempt.</span
		>{/if}
</div>
