<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { Settings, Save, RefreshCw } from 'lucide-svelte';
	import PageHeader from '../components/PageHeader.svelte';
	import Notice from '../components/Notice.svelte';
	import ProfileEditor from '../components/ProfileEditor.svelte';
	import Trackers from '../components/Trackers.svelte';
	import { Resource } from '../lib/resource.svelte';
	import { api } from '../lib/client';
	import { message, move } from '../lib/utils';
	import type { Settings as Policy, SettingsView, Indexer } from '../lib/types';
	let { dirty = $bindable(false) }: { dirty?: boolean } = $props();
	const resource = new Resource<SettingsView>();
	let draft = $state<Policy | null>(null),
		saved = $state(''),
		saving = $state(false),
		tab = $state('general'),
		success = $state(''),
		indexers = $state<Indexer[]>([]),
		indexerError = $state(''),
		indexersLoading = $state(false);
	const isDirty = $derived(!!draft && JSON.stringify(draft) !== saved);
	$effect(() => {
		dirty = isDirty;
	});
	const checks = [
		['allow_movies', 'Allow movie requests'],
		['allow_tv', 'Allow TV requests'],
		['auto_approve', 'Automatically approve requests'],
		['search_on_approval', 'Search when a request is approved'],
		['retry_failed_downloads', 'Try another release after a failed download'],
	] as const;
	const numbers = [
		['movie_limit', 'Movie limit per user', 0, 10000],
		['tv_limit', 'Series limit per user', 0, 10000],
		['limit_days', 'Limit window (days)', 1, 365],
		['search_interval_seconds', 'Delay between tracker searches (seconds)', 1, 3600],
		['retry_interval_hours', 'Retry missing releases after (hours)', 1, 720],
		['release_delay_minutes', 'Wait for a preferred release (minutes)', 0, 10080],
		['min_seeders', 'Minimum seeders', 0, 100000],
		['max_concurrent_downloads', 'Maximum simultaneous request downloads', 1, 100],
		['stalled_minutes', 'Consider stalled after (minutes)', 0, 10080],
	] as const;
	function normalize(data: SettingsView) {
		const value = structuredClone(data.settings);
		value.spanish_trackers ||= [];
		value.selection_order ||= (data.selection_criteria || []).map((c) => c.id);
		for (const kind of ['movie', 'tv'] as const) {
			value[kind].audio_languages ||= [];
			value[kind].keywords ||= [];
			value[kind].preferred_audio_languages ||= [];
			value[kind].preferred_subtitle_languages ||= [];
		}
		return value;
	}
	onMount(() => {
		void load();
	});
	onDestroy(() => resource.destroy());
	async function load(force = false) {
		if (force && isDirty && !confirm('Discard unsaved request settings and reload?')) return;
		await resource.load('/requests/settings');
		if (resource.data && !resource.error) {
			draft = normalize($state.snapshot(resource.data));
			saved = JSON.stringify(draft);
			if (resource.data.prowlarr_configured && !indexers.length) void refreshIndexers();
		}
	}
	async function refreshIndexers() {
		indexersLoading = true;
		try {
			const data = await api<{ indexers: Indexer[] }>('/requests/indexers', { signal: resource.signal });
			indexers = data.indexers || [];
			indexerError = '';
		} catch (e) {
			indexerError = message(e);
		} finally {
			indexersLoading = false;
		}
	}
	async function save(e: SubmitEvent) {
		e.preventDefault();
		if (!draft || saving) return;
		let snapshot: string;
		try {
			snapshot = JSON.stringify(draft, (_k, v) => {
				if (typeof v === 'number' && !Number.isFinite(v)) throw Error('Fill in all numeric settings before saving.');
				return v;
			});
		} catch (e) {
			resource.error = message(e);
			return;
		}
		saving = true;
		resource.error = '';
		try {
			const data = await api<SettingsView>('/requests/settings', {
				method: 'PUT',
				headers: { 'Content-Type': 'application/json' },
				body: snapshot,
				signal: resource.signal,
			});
			resource.data = data;
			saved = JSON.stringify(normalize(data));
			if (JSON.stringify(draft) === snapshot) draft = normalize(data);
			success = 'Request settings saved.';
		} catch (e) {
			resource.error = message(e);
		} finally {
			saving = false;
		}
	}
</script>

<svelte:window
	onbeforeunload={(event) => {
		if (isDirty) {
			event.preventDefault();
			event.returnValue = '';
		}
	}}
/>
<form onsubmit={save}>
	<PageHeader title="Request settings" description="Configure approval, destinations, trackers and release preferences." icon={Settings}
		><span class="muted text-xs" role="status">{saving ? 'Saving…' : isDirty ? 'Unsaved changes' : 'All changes saved'}</span><button
			type="button"
			disabled={saving || resource.loading}
			onclick={() => load(true)}><RefreshCw class="size-4" />Reload saved</button
		><button class="primary" disabled={!isDirty || saving || resource.loading}><Save class="size-4" />Save settings</button></PageHeader
	>
	<Notice error={resource.error} {success} />
	{#if draft && resource.data}{@const data = resource.data}
		<div class="tabs">
			{#each [['general', 'General'], ['trackers', 'Trackers'], ['movie', 'Movies'], ['tv', 'TV']] as [key, label]}<button
					type="button"
					class:active={tab === key}
					onclick={() => (tab = key)}>{label}</button
				>{/each}
		</div>
		<div class="panel settings-panel">
			{#if tab === 'general'}<section class="form-section">
					<h2>Permissions and approval</h2>
					<p class="muted">Defaults for every user. A limit of 0 means unlimited; TV limits count series.</p>
					<div class="form-grid">
						{#each checks as [key, label]}<label class="check"><input type="checkbox" bind:checked={draft[key]} />{label}</label>{/each}
					</div>
				</section>
				<section class="form-section">
					<h2>Search and download policy</h2>
					<div class="form-grid">
						{#each numbers as [key, label, min, max]}<label
								>{label}<input type="number" required {min} {max} bind:value={draft[key]} /></label
							>{/each}
					</div>
					<p class="muted text-xs">
						A release delay of 0 disables waiting. A stalled time of 0 disables automatic stalled-download recovery.
					</p>
				</section>
				<section class="form-section">
					<div class="section-heading">
						<div>
							<h2>Spanish trackers</h2>
							<p>Prioritized when Spanish audio is required. Metadata must still confirm Spanish audio.</p>
						</div>
						<button type="button" disabled={!data.prowlarr_configured || indexersLoading} onclick={refreshIndexers}>Refresh trackers</button
						>
					</div>
					<Notice error={indexerError} /><Trackers bind:selected={draft.spanish_trackers} {indexers} spanish />
				</section>
				<section class="form-section">
					<h2>Release ranking</h2>
					<p class="muted">First rule wins. Each following rule breaks ties. Applies to movies and TV.</p>
					<div class="ordered-list">
						{#each draft.selection_order as id, index}{@const label = data.selection_criteria.find((c) => c.id === id)?.label || id}
							<div>
								<span class="muted">{index + 1}</span><strong>{label}</strong><button
									type="button"
									aria-label={`Move ${label} up`}
									disabled={index === 0}
									onclick={() => draft && move(draft.selection_order, index, -1)}>↑</button
								><button
									type="button"
									aria-label={`Move ${label} down`}
									disabled={index === draft.selection_order.length - 1}
									onclick={() => draft && move(draft.selection_order, index, 1)}>↓</button
								>
							</div>{/each}
					</div>
					<button
						type="button"
						onclick={() => {
							if (draft) draft.selection_order = data.selection_criteria.map((c) => c.id);
						}}>Restore default order</button
					>
				</section>
			{:else if tab === 'trackers'}<section class="form-section">
					<div class="section-heading">
						<div>
							<h2>Choose Prowlarr trackers</h2>
							<p>Only selected, enabled torrent trackers are used. Lower priority numbers win ties.</p>
						</div>
						<button type="button" disabled={!data.prowlarr_configured || indexersLoading} onclick={refreshIndexers}>Refresh trackers</button
						>
					</div>
					<Notice error={indexerError} /><Trackers bind:selected={draft.trackers} {indexers} />
				</section>
			{:else}{@const kind = tab === 'tv' ? 'tv' : 'movie'}<ProfileEditor bind:profile={draft[kind]} {kind} {data} />
				<section class="form-section">
					{#if kind === 'tv'}<h2>TV options</h2>
						<label class="check"><input type="checkbox" bind:checked={draft.include_specials} />Include specials (season 0)</label><label
							class="check"
							><input type="checkbox" bind:checked={draft.prefer_season_packs} />Prefer season packs over individual episodes</label
						>{:else}<h2>Subtitles after download</h2>
						<label class="check"
							><input type="checkbox" bind:checked={draft.movie_auto_subtitles} />Automatically run the alass subtitle flow</label
						>
						<p class="muted text-xs">
							After download, add missing text subtitles using the configured languages, alignment and quota settings.
						</p>
						<label
							>Start looking for a movie when<select bind:value={draft.movie_availability}
								><option value="released">Released for home viewing</option><option value="in_cinemas">In cinemas</option><option
									value="announced">Announced</option
								></select
							></label
						>{/if}
				</section>{/if}
		</div>
	{:else}<p class="empty">{resource.loading ? 'Loading request settings…' : 'Request settings could not be loaded.'}</p>{/if}
</form>
