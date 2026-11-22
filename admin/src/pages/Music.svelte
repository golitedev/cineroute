<script lang="ts">
	import { onMount } from 'svelte';
	import { FolderClock, Plus, RefreshCw } from 'lucide-svelte';
	import PageHeader from '../components/PageHeader.svelte';
	import Notice from '../components/Notice.svelte';
	import { Resource } from '../lib/resource.svelte';
	import { api } from '../lib/client';
	import { date, message } from '../lib/utils';
	let { onhistory }: { onhistory: () => void } = $props();

	type Settings = { country: string; limit: number; name: string; enabled: boolean };
	type Track = {
		spotify_id: string;
		rank: number;
		title: string;
		artists: string[];
		state: string;
		reason?: string;
		album?: string;
		manual_match?: boolean;
		qobuz_match?: QobuzTrack;
	};
	type QobuzTrack = { id: string; title: string; artist: string; album: string; disc: number; number: number; duration: number };
	type Chart = {
		id: string;
		settings: Settings;
		display_name: string;
		chart_date?: string;
		published_date?: string;
		published_at?: string;
		published: number;
		ready: number;
		pending_checks?: number;
		sync_requested?: boolean;
		sync_phase?: 'fetching' | 'matching' | 'publishing';
		fetch_error?: string;
		publish_error?: string;
		tracks: Track[];
	};
	type Charts = { available: boolean; reason?: string; markets: { code: string; label: string }[]; playlists: Chart[] };
	const chartsPath = '/music/charts';
	const charts = new Resource<Charts>();
	let editor = $state('');
	let qobuzEditor = $state('');
	let reference = $state('');
	let qobuzTracks = $state<QobuzTrack[]>([]);
	let qobuzTrackID = $state('');
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
	const phaseLabels = { fetching: 'Fetching Spotify chart', matching: 'Matching tracks', publishing: 'Updating Navidrome playlist' };
	const downloadStates = new Set(['queued', 'downloading', 'verifying', 'importing']);
	const reviewStates = new Set(['needs_review', 'failed', 'cancelled']);

	function syncProgress(chart: Chart, available: boolean) {
		const rechecks = chart.pending_checks ?? 0;
		const downloads = chart.tracks.filter((track) => downloadStates.has(track.state)).length;
		const indexing = chart.tracks.filter((track) => track.state === 'indexing').length;
		const review = chart.tracks.filter((track) => reviewStates.has(track.state)).length;
		const settled = chart.tracks.filter((track) => track.state === 'ready').length + review;
		const waiting = chart.tracks.length - settled - downloads - indexing;
		const details = [
			`${chart.ready} playable`,
			rechecks > 0 && `${rechecks} queued for recheck`,
			downloads > 0 && `${downloads} queued or downloading`,
			indexing > 0 && `${indexing} waiting for Navidrome`,
			waiting > 0 && `${waiting} waiting for a match or retry`,
			review > 0 && `${review} need review`,
		]
			.filter(Boolean)
			.join(' · ');
		let status = 'Sync complete';
		if (!chart.settings.enabled) status = 'Sync paused';
		else if (!available) status = 'Sync unavailable';
		else if (chart.sync_phase) status = phaseLabels[chart.sync_phase];
		else if (chart.sync_requested) status = 'Sync queued';
		else if (chart.fetch_error || chart.publish_error) status = 'Sync will retry';
		else if (!chart.chart_date) status = 'Waiting for the first chart';
		else if (chart.ready > 0 && (chart.published !== chart.ready || chart.published_date !== chart.chart_date))
			status = 'Waiting to publish tracks';
		else if (downloads || indexing || waiting > 0) status = 'Adding tracks';
		else if (rechecks > 0) status = 'Checking tracks';
		else if (review) status = 'Sync complete with tracks needing review';
		return { status, details, settled: settled - rechecks };
	}

	onMount(() => {
		void charts.load(chartsPath);
		const chartsTimer = setInterval(() => {
			if (!busy && !charts.loading) void charts.load(chartsPath);
		}, 5_000);
		return () => {
			clearInterval(chartsTimer);
			charts.destroy();
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

	async function mutate(path: string, method: string, notice: string, body?: Settings | { reference: string; track_id: string }) {
		busy = true;
		error = '';
		success = '';
		try {
			await api(chartsPath + path, {
				method,
				signal: charts.signal,
				...(body ? { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) } : {}),
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

	function qobuzPath(chart: Chart, track: Track) {
		return `/${chart.id}/tracks/${track.spotify_id}/qobuz`;
	}

	async function lookUpQobuz(chart: Chart, track: Track) {
		busy = true;
		error = '';
		success = '';
		try {
			const result = await api<{ tracks: QobuzTrack[] }>(chartsPath + qobuzPath(chart, track), {
				method: 'POST',
				signal: charts.signal,
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ reference: reference.trim() }),
			});
			qobuzTracks = result.tracks;
			qobuzTrackID = qobuzTracks.length === 1 ? qobuzTracks[0].id : '';
		} catch (e) {
			if (!charts.signal.aborted) error = message(e);
		} finally {
			busy = false;
		}
	}

	async function saveQobuz(chart: Chart, track: Track, clear = false) {
		const path = qobuzPath(chart, track);
		const saved = clear
			? await mutate(path, 'DELETE', 'Automatic matching restored.')
			: await mutate(path, 'PUT', 'Qobuz track saved. Track check queued.', { reference: reference.trim(), track_id: qobuzTrackID });
		if (saved) qobuzEditor = '';
	}

	function qobuzTrackLabel(track: QobuzTrack) {
		const duration = track.duration > 0 ? ` · ${Math.floor(track.duration / 60)}:${String(track.duration % 60).padStart(2, '0')}` : '';
		return `${track.disc}.${track.number} ${track.title}${track.artist ? ` · ${track.artist}` : ''}${duration}`;
	}

	function remove(chart: Chart) {
		if (confirm(`Remove ${chart.display_name} from CineRoute and Navidrome? Imported albums will stay in the library.`)) {
			void mutate('/' + chart.id, 'DELETE', 'Playlist removed.').then((removed) => {
				if (removed && editor === chart.id) editor = '';
			});
		}
	}
</script>

<PageHeader title="Music" description="Manage daily Spotify charts in Navidrome.">
	<button type="button" onclick={onhistory}><FolderClock class="size-4" /> Import history</button>
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
		{@const progress = syncProgress(chart, charts.data?.available ?? false)}
		<article class="panel">
			<div class="section-heading">
				<div>
					<h2>{chart.display_name}</h2>
					<p>
						{charts.data?.markets.find((market) => market.code === chart.settings.country)?.label} ·
						{chart.published} of {chart.settings.limit} published
					</p>
				</div>
				<div class="actions">
					<button
						type="button"
						disabled={busy || !chart.settings.enabled || !charts.data?.available || !!chart.sync_requested || !!chart.sync_phase}
						onclick={() => mutate('/' + chart.id + '/sync', 'POST', 'Sync requested.')}><RefreshCw class="size-4" /> Sync now</button
					>
					<button type="button" disabled={busy} onclick={() => edit(chart)}>Edit</button>
					<button type="button" class="danger" disabled={busy} onclick={() => remove(chart)}>Remove</button>
				</div>
			</div>
			<p role="status">
				<strong>{progress.status}</strong>
				{#if chart.tracks.length}<span class="muted"> · {progress.details}</span>{/if}
			</p>
			{#if chart.tracks.length}
				<progress aria-label="Tracks processed" max={chart.tracks.length} value={progress.settled}></progress>
			{/if}
			{#if chart.chart_date}
				<p class="muted">
					Spotify chart date: {chart.chart_date}
					{#if chart.published_at}· Last published {date(chart.published_at)}{/if}
				</p>
			{/if}
			{#if chart.fetch_error}<Notice error={`Chart fetch: ${chart.fetch_error}`} />{/if}
			{#if chart.publish_error}<Notice error={`Navidrome: ${chart.publish_error}`} />{/if}
			{#if chart.tracks.length}
				<details>
					<summary>Track status</summary>
					<ol class="chart-tracks">
						{#each chart.tracks as track (track.spotify_id)}
							{@const trackKey = `${chart.id}/${track.spotify_id}`}
							<li value={track.rank} class="history-line">
								<a href={`https://open.spotify.com/track/${track.spotify_id}`} target="_blank" rel="noreferrer">
									<strong>{track.title}</strong>
								</a>
								<span> · {track.artists.join(', ')}</span>
								<p class="muted">
									{statusLabels[track.state] || track.state}{track.reason ? ' · ' + track.reason : ''}
									{track.album ? ' · ' + track.album : ''}
									{track.qobuz_match ? ' · Qobuz match: ' + track.qobuz_match.title : track.manual_match ? ' · Saved manual match' : ''}
								</p>
								{#if qobuzEditor === trackKey}
									<form
										class="qobuz-match-form"
										onsubmit={(event) => {
											event.preventDefault();
											if (qobuzTracks.length) void saveQobuz(chart, track);
											else void lookUpQobuz(chart, track);
										}}
									>
										<div class="actions">
											<label
												>Qobuz link or album ID
												<input
													bind:value={reference}
													oninput={() => {
														qobuzTracks = [];
														qobuzTrackID = '';
													}}
													required
													maxlength="512"
													disabled={busy}
												/>
											</label>
											{#if qobuzTracks.length === 0}
												<button type="submit" disabled={busy || !reference.trim()}>Find tracks</button>
											{:else if qobuzTracks.length === 1}
												<p>{qobuzTrackLabel(qobuzTracks[0])}</p>
											{:else}
												<label
													>Qobuz track
													<select bind:value={qobuzTrackID} required disabled={busy}>
														<option value="">Choose a track</option>
														{#each qobuzTracks as choice (choice.id)}
															<option value={choice.id}>{qobuzTrackLabel(choice)}</option>
														{/each}
													</select>
												</label>
											{/if}
											{#if qobuzTracks.length}
												<button type="submit" disabled={busy || !qobuzTrackID}>Use this track</button>
											{/if}
											{#if track.manual_match}
												<button type="button" disabled={busy} onclick={() => saveQobuz(chart, track, true)}>Use automatic match</button>
											{/if}
											<button type="button" disabled={busy} onclick={() => (qobuzEditor = '')}>Cancel</button>
										</div>
									</form>
								{:else if !downloadStates.has(track.state) && track.state !== 'indexing' && (track.manual_match || reviewStates.has(track.state) || track.state === 'retrying')}
									<button
										type="button"
										disabled={busy}
										onclick={() => {
											qobuzEditor = trackKey;
											reference = track.qobuz_match ? 'https://play.qobuz.com/track/' + track.qobuz_match.id : '';
											qobuzTracks = [];
											qobuzTrackID = '';
										}}
									>
										{track.manual_match ? 'Edit Qobuz match' : 'Use Qobuz link'}
									</button>
								{/if}
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

<style>
	.chart-tracks {
		list-style: decimal;
		padding-inline-start: 2.5rem;
	}
	.qobuz-match-form {
		width: min(100%, 36rem);
	}
	.qobuz-match-form label {
		flex: 0 0 100%;
		min-width: 0;
	}
	.qobuz-match-form input,
	.qobuz-match-form select {
		width: 100%;
		min-width: 0;
	}
</style>
