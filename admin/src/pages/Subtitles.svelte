<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { Subtitles, RefreshCw, Play, Square } from 'lucide-svelte';
	import PageHeader from '../components/PageHeader.svelte';
	import Notice from '../components/Notice.svelte';
	import Pager from '../components/Pager.svelte';
	import SubtitleTargets from '../components/SubtitleTargets.svelte';
	import { Resource } from '../lib/resource.svelte';
	import { api, post } from '../lib/client';
	import { message, date } from '../lib/utils';
	import { subtitleStatus, subtitleFilter, reference, metrics } from '../lib/subtitles';
	import type { SubtitleView } from '../lib/types';
	const resource = new Resource<SubtitleView>();
	let query = $state(''),
		filter = $state('pending'),
		page = $state(0),
		open = $state(''),
		busy = $state(false),
		success = $state(''),
		batchSize = $state(20),
		interval = $state(400),
		candidates = $state(5),
		settingsDirty = $state(false);
	const running = $derived(!!resource.data?.batch?.running);
	const pending = $derived(resource.data?.stats?.pending || 0);
	const filtered = $derived(
		(resource.data?.items || []).filter(
			(i) =>
				subtitleFilter(i, filter) &&
				[i.video_path, i.folder_name, i.title, i.drive_id].join(' ').toLowerCase().includes(query.toLowerCase()),
		),
	);
	const url = () => '/subtitles' + (open ? '?open=' + encodeURIComponent(open) : '');
	async function load() {
		await resource.load(url());
		if (resource.data && !settingsDirty) {
			batchSize = resource.data.settings.run_batch_size;
			interval = resource.data.settings.request_interval_ms;
			candidates = resource.data.settings.max_candidates;
		}
	}
	onMount(() => {
		void load();
		const timer = setInterval(() => {
			if (resource.data?.batch?.running && !resource.loading) void load();
		}, 2500);
		return () => clearInterval(timer);
	});
	onDestroy(() => resource.destroy());
	async function action(path: string) {
		if (path === '/work/clear' && !confirm('Delete downloaded and aligned work files? Movies and installed subtitles are kept.')) return;
		busy = true;
		resource.error = '';
		try {
			await post('/subtitles' + path);
			await load();
			if (path === '/work/clear') success = 'Work files cleared.';
		} catch (e) {
			resource.error = message(e);
		} finally {
			busy = false;
		}
	}
	async function save(e: SubmitEvent) {
		e.preventDefault();
		busy = true;
		try {
			resource.data = await api<SubtitleView>('/subtitles/settings', {
				method: 'PATCH',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ run_batch_size: batchSize, request_interval_ms: interval, max_candidates: candidates }),
			});
			settingsDirty = false;
			success = 'Subtitle settings saved.';
		} catch (e) {
			resource.error = message(e);
		} finally {
			busy = false;
		}
	}
	async function detail(id: string) {
		open = id;
		await load();
	}
</script>

<PageHeader
	title="Subtitles"
	description={`Find and align ${(resource.data?.target_languages || []).join(', ')} subtitles for remote movies.`}
	icon={Subtitles}
	><button disabled={busy || running} onclick={() => action('/scan')}><RefreshCw class="size-4" />Scan remote movies</button><button
		class="primary"
		disabled={busy || running || !pending}
		onclick={() => action('/run')}
		><Play class="size-4" />{running
			? 'Running…'
			: pending
				? `Add missing subtitles — next ${Math.min(batchSize, pending)}`
				: 'Nothing to process'}</button
	>{#if running}<button class="danger" disabled={busy} onclick={() => action('/run/cancel')}><Square class="size-4" />Cancel</button
		>{/if}</PageHeader
>
<Notice
	error={resource.error ||
		resource.data?.state_error ||
		(resource.data?.work_dir_error ? `Work directory ${resource.data.work_dir}: ${resource.data.work_dir_error}` : '')}
	{success}
/>{#if resource.data?.feature_error}<Notice error={resource.data.feature_error} />{/if}
{#if resource.data}<div class="stats">
		{#each Object.entries(resource.data.stats || {}) as [key, value]}<div>
				<strong>{value}</strong><span>{key.replaceAll('_', ' ')}</span>
			</div>{/each}
	</div>
	<p class="muted text-xs">
		{resource.data.quota?.known
			? `OpenSubtitles downloads left: ${resource.data.quota.remaining}`
			: 'OpenSubtitles quota: not checked yet'}{#if resource.data.quota?.reset_at}
			· Resets {date(resource.data.quota.reset_at)}{/if}
	</p>
	{#if running}<div class="panel">
			<p>
				{resource.data.batch.kind === 'scan' ? 'Scanning' : 'Processing'}
				{resource.data.batch.done} / {resource.data.batch.total} · {resource.data.batch.stage || ''}
			</p>
			<p class="mono">{resource.data.batch.current || ''}</p>
			<progress max={resource.data.batch.total || 1} value={resource.data.batch.done}></progress>
		</div>{:else if resource.data.batch?.error}<Notice error={resource.data.batch.error} />{:else if resource.data.batch?.canceled}<p
			class="muted"
		>
			Last job canceled after {resource.data.batch.done} / {resource.data.batch.total}
		</p>{/if}{/if}
<details class="panel">
	<summary>Processing settings</summary>
	<form class="form-grid" onsubmit={save}>
		<label
			>Batch size<input type="number" required min="1" max="1000" bind:value={batchSize} oninput={() => (settingsDirty = true)} /></label
		><label
			>Request delay (ms)<input
				type="number"
				required
				min="0"
				max="60000"
				bind:value={interval}
				oninput={() => (settingsDirty = true)}
			/></label
		><label
			>Candidates per movie<input
				type="number"
				required
				min="1"
				max="20"
				bind:value={candidates}
				oninput={() => (settingsDirty = true)}
			/></label
		>
		<div class="actions">
			<button disabled={busy || !settingsDirty}>Save settings</button><button
				type="button"
				class="danger"
				disabled={busy || running}
				onclick={() => action('/work/clear')}>Clear work files</button
			>
		</div>
	</form>
</details>
<div class="search-row">
	<input
		type="search"
		aria-label="Search subtitles"
		placeholder="Search movies, drives and paths…"
		bind:value={query}
		oninput={() => (page = 0)}
	/><button
		disabled={!query}
		onclick={() => {
			query = '';
			page = 0;
		}}>Clear</button
	>
</div>
<div class="tabs">
	{#each [['all', 'All'], ['pending', 'Needs subtitles'], ['added', 'Added'], ['needs_review', 'Needs review'], ['partial', 'Partly added'], ['no_match', 'No match'], ['no_external', 'No external SRT'], ['not_analyzed', 'Not analyzed'], ['has_external', 'Has external SRT'], ['no_reference', 'No reference'], ['has_targets', 'Has subtitles'], ['failed', 'Failed'], ['skipped', 'Skipped']] as [key, label]}<button
			class:active={filter === key}
			onclick={() => {
				filter = key;
				page = 0;
			}}>{label}</button
		>{/each}
</div>
{#if resource.data?.open_item}{@const item = resource.data.open_item}
	<section class="panel detail">
		<div class="section-heading">
			<div>
				<h2>{item.folder_name}</h2>
				<p class="mono">{item.video_path}</p>
			</div>
			<button onclick={() => detail('')}>Close</button>
		</div>
		<p>{subtitleStatus(item.status)} · Reference: {reference(item)}</p>
		<SubtitleTargets {item} />{#if item.work_dir}<p class="mono">Work: {item.work_dir}</p>{/if}
		<h3>External subtitles</h3>
		{#each item.external_subtitles || [] as sub}<p class="muted">
				{sub.file_name} · {sub.language || 'unknown'} · {sub.usable ? 'usable' : 'unusable'}
			</p>{:else}<p class="muted">No external subtitle files</p>{/each}
		<h3>Embedded streams</h3>
		{#each item.embedded_sub_streams || [] as stream}<p class="muted">
				#{stream.index}
				{stream.codec} · {stream.language || 'unknown'} · {stream.usable ? 'text' : 'image'}
			</p>{/each}
		<h3>Attempts</h3>
		{#each [...(resource.data.attempts || [])].reverse() as attempt}<div class="history-line">
				<p>{date(attempt.at)} · {attempt.status} · {attempt.language || ''} · file {attempt.file_id}</p>
				<p>{attempt.release || ''} {metrics(attempt.metrics)}</p>
				<p class="mono">{attempt.detail || ''}</p>
			</div>{:else}<p class="muted">No attempts yet.</p>{/each}
		<h3>Candidates</h3>
		{#each (resource.data.candidates || []).slice(0, 10) as c}<p class="muted">
				#{c.rank} · {c.language || '?'} · {c.variant || ''} · score {c.score} · {c.safe ? 'safe' : 'unsafe'} · {c.release}<br
				/>{c.reasons?.join('; ')}
			</p>{:else}<p class="muted">No cached candidates yet.</p>{/each}
	</section>{/if}
<div class="cards">
	{#each filtered.slice(page * 20, (page + 1) * 20) as item (item.id)}<article class="panel">
			<div class="section-heading">
				<div>
					<h2>{item.folder_name}</h2>
					<p>{item.drive_id} · {subtitleStatus(item.status)}</p>
				</div>
				<div class="actions">
					<button onclick={() => detail(item.id)}>Details</button><button
						disabled={busy || running}
						onclick={() => {
							open = item.id;
							void action('/' + encodeURIComponent(item.id) + '/run');
						}}>Run</button
					><button
						disabled={busy || running}
						onclick={() => {
							open = item.id;
							void action('/' + encodeURIComponent(item.id) + '/retry');
						}}>Retry with more candidates</button
					><button disabled={busy || running} onclick={() => action('/' + encodeURIComponent(item.id) + '/skip')}>Skip</button>
				</div>
			</div>
			<p class="mono">{item.video_path}</p>
			<p class="muted text-xs">Reference: {reference(item)}</p>
			{#if item.step}<p class="notice">{item.step}… {item.step_detail || ''}</p>{/if}<SubtitleTargets {item} />{#if item.error}<Notice
					error={item.error}
				/>{/if}
		</article>{:else}<p class="empty">
			{resource.loading ? 'Loading movies…' : 'No movies in this filter. Scan the remote movie libraries first.'}
		</p>{/each}
</div>
<Pager bind:page total={filtered.length} />
