<script lang="ts">
	import { onMount } from 'svelte';
	import { Plus, RefreshCw } from 'lucide-svelte';
	import PageHeader from '../components/PageHeader.svelte';
	import Notice from '../components/Notice.svelte';
	import { Resource } from '../lib/resource.svelte';
	import { api } from '../lib/client';
	import { date, message } from '../lib/utils';

	type Settings = { country: string; limit: number; name: string; enabled: boolean };
	type Track = { spotify_id: string; rank: number; title: string; artists: string[]; state: string; reason?: string; album?: string };
	type Chart = {
		id: string;
		settings: Settings;
		display_name: string;
		chart_date?: string;
		published_date?: string;
		published_at?: string;
		published: number;
		ready: number;
		fetch_error?: string;
		publish_error?: string;
		tracks: Track[];
	};
	type Charts = { available: boolean; reason?: string; markets: { code: string; label: string }[]; playlists: Chart[] };
	type History = {
		imports: {
			job_id: string;
			album: string;
			artist: string;
			imported_at: string;
			partial: boolean;
			sources: { playlist_id: string; playlist: string; chart_date: string; spotify_id: string; track: string }[];
		}[];
		more: boolean;
		offset: number;
	};

	const chartsPath = '/music/charts';
	const charts = new Resource<Charts>();
	const history = new Resource<History>();
	let editor = $state('');
	let country = $state('global');
	let limit = $state<number | undefined>(50);
	let name = $state('');
	let enabled = $state(true);
	let busy = $state(false);
	let error = $state('');
	let success = $state('');
	const statusLabels: Record<string, string> = {
		pending: 'Waiting for lookup',
		waiting: 'Waiting for the queue',
		queued: 'Queued',
		downloading: 'Downloading album',
		verifying: 'Verifying album',
		importing: 'Importing album',
		indexing: 'Waiting for Navidrome',
		retrying: 'Will retry',
		needs_review: 'Needs review',
		failed: 'Download failed',
		cancelled: 'Download canceled',
		ready: 'Playable',
	};

	function loadHistory(offset = 0) {
		return history.load(`${chartsPath}/imports?offset=${offset}`);
	}

	onMount(() => {
		void charts.load(chartsPath);
		void loadHistory();
		const timer = setInterval(() => {
			if (!busy && !charts.loading) void charts.load(chartsPath);
			if (!history.loading && !history.data?.offset) void loadHistory();
		}, 15_000);
		return () => {
			clearInterval(timer);
			charts.destroy();
			history.destroy();
		};
	});

	function edit(chart?: Chart) {
		editor = chart?.id || 'new';
		country = chart?.settings.country || 'global';
		limit = chart?.settings.limit ?? 50;
		name = chart?.settings.name || '';
		enabled = chart?.settings.enabled ?? true;
		error = '';
		success = '';
	}

	async function mutate(path: string, method: string, notice: string, settings?: Settings) {
		busy = true;
		error = '';
		success = '';
		try {
			await api(chartsPath + path, {
				method,
				signal: charts.signal,
				...(settings ? { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(settings) } : {}),
			});
			success = notice;
			await charts.load(chartsPath);
			return true;
		} catch (e) {
			if (!charts.signal.aborted) {
				error = message(e);
				await charts.load(chartsPath);
			}
			return false;
		} finally {
			busy = false;
		}
	}

	async function save(event: SubmitEvent) {
		event.preventDefault();
		if (limit === undefined) return;
		const adding = editor === 'new';
		if (await mutate(adding ? '' : '/' + editor, adding ? 'POST' : 'PUT', 'Playlist settings saved.', { country, limit, name, enabled })) {
			editor = '';
		}
	}

	function remove(chart: Chart) {
		if (confirm(`Remove ${chart.display_name} from CineRoute and Navidrome? Imported albums will stay in the library.`)) {
			void mutate('/' + chart.id, 'DELETE', 'Playlist removed.').then((removed) => {
				if (removed && editor === chart.id) editor = '';
			});
		}
	}
</script>

<PageHeader title="Music" description="Manage daily Spotify charts in Navidrome and track automatic album imports.">
	<button type="button" class="primary" disabled={!charts.data?.available || busy} onclick={() => edit()}>
		<Plus class="size-4" /> Add chart playlist
	</button>
</PageHeader>

<Notice error={error || charts.error} {success} />
{#if charts.data?.reason}<p class="notice">{charts.data.reason}</p>{/if}

{#if editor && charts.data}
	<section class="panel">
		<h2>{editor === 'new' ? 'Add chart playlist' : 'Edit chart playlist'}</h2>
		<form class="form-grid" onsubmit={save}>
			<label>
				Country or Global
				<select bind:value={country} disabled={busy}>
					{#each charts.data.markets as market (market.code)}
						<option value={market.code}>{market.label}</option>
					{/each}
				</select>
			</label>
			<label>
				Track count
				<input type="number" min="1" max="200" required bind:value={limit} disabled={busy} />
			</label>
			<label>
				Playlist name (optional)
				<input
					maxlength="120"
					placeholder={`Top ${limit || 50} ${charts.data.markets.find((market) => market.code === country)?.label || ''}`}
					bind:value={name}
					disabled={busy}
				/>
			</label>
			<label>
				Daily sync
				<select bind:value={enabled} disabled={busy}>
					<option value={true}>Enabled</option>
					<option value={false}>Paused</option>
				</select>
			</label>
			<div class="actions">
				<button class="primary" disabled={busy || (enabled && !charts.data.available)}>Save playlist</button>
				<button type="button" disabled={busy} onclick={() => (editor = '')}>Cancel</button>
			</div>
		</form>
	</section>
{/if}

<section aria-label="Chart playlists" class="cards">
	{#each charts.data?.playlists || [] as chart (chart.id)}
		<article class="panel">
			<div class="section-heading">
				<div>
					<h2>{chart.display_name}</h2>
					<p>
						{charts.data?.markets.find((market) => market.code === chart.settings.country)?.label} ·
						{chart.ready} of {chart.settings.limit} playable
						{#if !chart.settings.enabled}
							· Paused{/if}
					</p>
				</div>
				<div class="actions">
					<button
						type="button"
						disabled={busy || !chart.settings.enabled || !charts.data?.available}
						onclick={() => mutate('/' + chart.id + '/sync', 'POST', 'Sync requested.')}><RefreshCw class="size-4" /> Sync now</button
					>
					<button type="button" disabled={busy} onclick={() => edit(chart)}>Edit</button>
					<button type="button" class="danger" disabled={busy} onclick={() => remove(chart)}>Remove</button>
				</div>
			</div>
			<p class="muted">
				{chart.chart_date ? 'Kworb chart ' + chart.chart_date : 'Waiting for the first chart'}
				{#if chart.published_at}
					· Published {chart.published} for {chart.published_date} · {date(chart.published_at)}
				{/if}
			</p>
			{#if chart.fetch_error}<Notice error={`Chart fetch: ${chart.fetch_error}`} />{/if}
			{#if chart.publish_error}<Notice error={`Navidrome: ${chart.publish_error}`} />{/if}
			{#if chart.tracks.length}
				<details>
					<summary>Track status</summary>
					<ol class="chart-tracks">
						{#each chart.tracks as track (track.spotify_id)}
							<li value={track.rank} class="history-line">
								<a href={`https://open.spotify.com/track/${track.spotify_id}`} target="_blank" rel="noreferrer">
									<strong>{track.title}</strong>
								</a>
								<span> · {track.artists.join(', ')}</span>
								<p class="muted">
									{statusLabels[track.state] || track.state}{track.reason ? ' · ' + track.reason : ''}
									{track.album ? ' · ' + track.album : ''}
								</p>
							</li>
						{/each}
					</ol>
				</details>
			{/if}
		</article>
	{:else}
		{#if charts.data?.available}<p class="empty">No chart playlists configured.</p>{/if}
	{/each}
</section>

<section class="panel" aria-label="Automatic imports">
	<h2>Automatic imports</h2>
	<Notice error={history.error} />
	{#each history.data?.imports || [] as item (item.job_id)}
		<div class="history-line">
			<strong>{item.album}</strong> · {item.artist}
			<p class="muted">{date(item.imported_at)}{item.partial ? ' · Partial album' : ''}</p>
			<ul>
				{#each item.sources as source}
					<li>{source.playlist} · {source.track} · {source.chart_date}</li>
				{/each}
			</ul>
		</div>
	{:else}
		{#if !history.loading && !history.error}<p class="muted">No automatic albums imported yet.</p>{/if}
	{/each}
	{#if history.data && (history.data.offset || history.data.more)}
		<div class="pager">
			<button disabled={history.loading || !history.data.offset} onclick={() => loadHistory(Math.max(0, (history.data?.offset || 0) - 25))}>
				Newer
			</button>
			<button disabled={history.loading || !history.data.more} onclick={() => loadHistory((history.data?.offset || 0) + 25)}>
				Older
			</button>
		</div>
	{/if}
</section>

<style>
	.chart-tracks {
		list-style: decimal;
		padding-inline-start: 2.5rem;
	}
</style>
