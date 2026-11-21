<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { RefreshCw } from 'lucide-svelte';
	import PageHeader from '../components/PageHeader.svelte';
	import Notice from '../components/Notice.svelte';
	import DownloadDetails from '../components/DownloadDetails.svelte';
	import { Resource } from '../lib/resource.svelte';
	import { bytes, title, date, status, percent } from '../lib/utils';
	import type { RequestHistory } from '../lib/types';
	const resource = new Resource<RequestHistory>();
	let query = $state(''),
		filter = $state('all'),
		page = $state(1);
	const url = () => `/requests/history?page=${page}&status=${encodeURIComponent(filter)}&q=${encodeURIComponent(query)}`;
	$effect(() => {
		const path = url();
		const timer = setTimeout(() => {
			void resource.load(path);
		}, 300);
		return () => clearTimeout(timer);
	});
	onMount(() => {
		const timer = setInterval(() => {
			if (!resource.loading) void resource.load(url());
		}, 15000);
		return () => clearInterval(timer);
	});
	onDestroy(() => resource.destroy());
	const counts = $derived(resource.data?.counts || {});
	const criteria: Record<string, string> = {
		language: 'Language',
		quality: 'Quality',
		codec: 'Codec',
		text_subtitles: 'Text subtitles',
		keywords: 'Keywords',
		pack: 'Preferred release type',
		priority: 'Tracker priority',
		video_bitrate: 'Video bitrate',
		seeders: 'Seeders',
	};
	function score(criterion: string, value: number) {
		if (criterion === 'quality' || criterion === 'codec') return 1 - value;
		if (criterion === 'priority') return -value;
		if (criterion === 'video_bitrate') return value ? `${(value / 1e6).toFixed(2)} Mbps` : '—';
		return value;
	}
</script>

<PageHeader title="Request history" description="Requests from every user and all recorded download attempts."
	><button disabled={resource.loading} onclick={() => resource.load(url())}><RefreshCw class="size-4" />Refresh history</button></PageHeader
><Notice error={resource.error} />
<div class="stats">
	{#each [['complete', 'Ready'], ['downloading', 'Downloading'], ['queued', 'Queued'], ['stalled', 'Stalled'], ['waiting', 'Waiting'], ['error', 'Need attention']] as [key, label]}<div
		>
			<strong>{counts[key] || 0}</strong><span>{label}</span>
		</div>{/each}
</div>
<div class="search-row">
	<input
		type="search"
		aria-label="Search history"
		maxlength="200"
		placeholder="Title, user, tracker, disk or torrent hash…"
		bind:value={query}
		oninput={() => (page = 1)}
	/><select aria-label="Status" bind:value={filter} onchange={() => (page = 1)}
		>{#each ['all', 'pending', 'queued', 'searching', 'planned', 'downloading', 'stalled', 'paused', 'checking', 'metadata', 'waiting', 'monitoring', 'error', 'complete', 'cancelled', 'declined'] as key}<option
				value={key}>{key === 'all' ? 'All statuses' : status(key)}</option
			>{/each}</select
	>
</div>
{#if resource.data?.as_of}<p class="muted text-xs">Updated {date(resource.data.as_of)} · Refreshes every 15 seconds</p>{/if}
<div class="cards">
	{#each resource.data?.entries || [] as row (row.request.id)}{@const job = row.request}{@const state = row.status || job.status}
		<article class="panel">
			<div class="section-heading">
				<div>
					<h2>{title(job.media)}</h2>
					<p>
						Requested by <strong>{row.owner}</strong> · {job.media.media_type === 'tv' ? 'TV' : 'Movie'} · {row.destination}{#if job.seasons?.length}
							· {job.seasons.map((n) => `S${String(n).padStart(2, '0')}`).join(', ')}{/if}
					</p>
				</div>
				<span class="badge" class:good={state === 'complete'}>{status(state)}</span>
			</div>
			<p class="muted text-xs">
				Requested {date(job.created_at)}{#if row.next_search && ['waiting', 'queued', 'monitoring'].includes(job.status)}
					· Next check {date(row.next_search)}{/if}
			</p>
			<p class="muted">{job.message}</p>
			{#if ['downloading', 'stalled', 'paused', 'checking', 'metadata'].includes(state)}<div class="progress-row">
					<progress max="100" value={percent(job.progress)} aria-label="Request progress"></progress><span>{percent(job.progress)}%</span>
				</div>{/if}{#if row.diagnostic}<div class="notice">{row.diagnostic}</div>{/if}
			<div class="cards">
				{#each row.downloads || [] as download}<DownloadDetails {download} />{:else}<p class="muted text-xs">
						{job.status === 'complete'
							? 'No download recorded; the title was found in the library.'
							: 'No download has started for this title.'}
					</p>{/each}
			</div>
			{#if row.selection_reports?.length}
				<details>
					<summary>Release selection</summary>
					{#each [...row.selection_reports].reverse() as report (report.at)}
						<p class="text-sm">{report.queries.join(' · ')}</p>
						<p class="muted text-xs">{date(report.at)} · Ranking order {report.order.map((key) => criteria[key] || key).join(' → ')}</p>
						{#each report.candidates as candidate (candidate.rank)}
							<p class="text-sm">
								<strong>#{candidate.rank} {candidate.title}</strong> · {report.trackers.find(
									(tracker) => tracker.indexer_id === candidate.indexer_id,
								)?.name || `Tracker ${candidate.indexer_id}`} · {candidate.outcome}
								{#if candidate.reason}<span> · {candidate.reason}</span>{/if}
							</p>
							<p class="muted text-xs">
								{candidate.quality} · {candidate.codec} · {bytes(candidate.size)} · {report.order
									.map((key) => `${criteria[key] || key} ${score(key, candidate.scores[key])}`)
									.join(' · ')}
							</p>
						{:else}
							<p class="muted text-xs">No candidates passed the filters.</p>
						{/each}
						{#each report.trackers as tracker (tracker.indexer_id)}
							<p class="muted text-xs">
								{tracker.name || `Tracker ${tracker.indexer_id}`} · {tracker.accepted} eligible{#each Object.entries(tracker.rejected || {}) as [reason, count]}
									· {count} × {reason}{/each}
							</p>
						{/each}
					{/each}
				</details>
			{/if}
		</article>{:else}<p class="empty">
			{resource.loading
				? 'Loading request history…'
				: query || filter !== 'all'
					? 'No requests match these filters.'
					: 'No requests yet. Requests and their downloads will appear here.'}
		</p>{/each}
</div>
{#if resource.data}<div class="pager">
		<button disabled={page <= 1 || resource.loading} onclick={() => page--}>Previous</button><span
			>Page {resource.data.page || 1} of {resource.data.total_pages || 1} · {resource.data.total || 0} matching requests</span
		><button disabled={page >= (resource.data.total_pages || 1) || resource.loading} onclick={() => page++}>Next</button>
	</div>{/if}
