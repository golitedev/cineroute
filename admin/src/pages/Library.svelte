<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { Film, Tv, RefreshCw } from 'lucide-svelte';
	import PageHeader from '../components/PageHeader.svelte';
	import Notice from '../components/Notice.svelte';
	import Pager from '../components/Pager.svelte';
	import { Resource } from '../lib/resource.svelte';
	import { post } from '../lib/client';
	import { bytes, message } from '../lib/utils';
	import type { CopyView, Copy } from '../lib/types';
	let { kind }: { kind: 'movie' | 'tv' } = $props();
	const resource = new Resource<CopyView>();
	let query = $state(''),
		filter = $state('all'),
		page = $state(0),
		open = $state<Copy | null>(null),
		busy = $state(false),
		success = $state('');
	const items = $derived(resource.data?.items || []);
	const filtered = $derived(
		items.filter(
			(i) =>
				(filter === 'all' || i.status === filter) &&
				`${i.folder_name} ${i.drive_id} ${i.path} ${i.remote_path} ${i.existing_files?.join(' ')} ${i.remote_files?.join(' ')}`
					.toLowerCase()
					.includes(query.trim().toLowerCase()),
		),
	);
	const totals = (files: string[] = [], sizes: Record<string, number> = {}) =>
		files.length ? bytes(files.reduce((n, f) => n + (sizes[f] || 0), 0)) : '';
	const url = () => `/copies?type=${kind}`;
	onMount(() => {
		void resource.load(url());
	});
	onDestroy(() => resource.destroy());
	async function scan() {
		busy = true;
		try {
			resource.data = await post<CopyView>(`/copies/scan?type=${kind}`);
			page = 0;
			open = null;
		} catch (e) {
			resource.error = message(e);
		} finally {
			busy = false;
		}
	}
	async function hardlink(item: Copy) {
		busy = true;
		resource.error = '';
		try {
			const r = await post<{ view: CopyView }>(`/copies/${encodeURIComponent(item.id)}/hardlink?type=${kind}`);
			resource.data = r.view;
			open = r.view.items.find((i) => i.id === item.id) || null;
			success = 'Hardlink created.';
		} catch (e) {
			resource.error = message(e);
		} finally {
			busy = false;
		}
	}
</script>

<PageHeader
	title={kind === 'tv' ? 'TV shows' : 'Movies'}
	description="Browse the main and remote libraries, and link missing copies."
	icon={kind === 'tv' ? Tv : Film}
	><button disabled={busy || resource.loading} onclick={scan}><RefreshCw class="size-4" />{busy ? 'Scanning…' : 'Scan library'}</button
	></PageHeader
>
<Notice error={resource.error || resource.data?.state_error} {success} />
<div class="stats">
	{#each [[items.length, kind === 'tv' ? 'Shows' : 'Movies'], [items.filter((i) => i.status === 'main_only').length, 'Main only'], [items.filter((i) => i.status === 'remote_only').length, 'Remote only'], [items.filter((i) => i.status === 'both').length, 'Both copies']] as [n, label]}<div
		>
			<strong>{n}</strong><span>{label}</span>
		</div>{/each}
</div>
<div class="search-row">
	<input
		aria-label="Filter library titles"
		type="search"
		placeholder="Filter library titles…"
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
<div class="tabs" aria-label="Copy filters">
	{#each [['all', 'All'], ['main_only', 'Main only'], ['remote_only', 'Remote only'], ['both', 'Both copies']] as [value, label]}<button
			class:active={filter === value}
			aria-pressed={filter === value}
			onclick={() => {
				filter = value;
				page = 0;
			}}>{label}</button
		>{/each}
</div>
<div class="resource-table">
	<div class="library-heading"><span>Title</span><span>Copies</span><span></span></div>
	{#each filtered.slice(page * 20, (page + 1) * 20) as item (item.id)}<div class="library-row">
			<div>
				<strong>{item.folder_name}</strong>
				<div class="muted text-xs">
					{item.drive_id} ·
					<span class:present={item.existing_files?.length}>main {totals(item.existing_files, item.existing_file_sizes)}</span>
					· <span class="remote" class:present={item.remote_files?.length}>remote {totals(item.remote_files, item.remote_file_sizes)}</span>
				</div>
			</div>
			<span class="badge" class:good={item.status === 'both'}
				>{{ both: 'both copies', main_only: 'main only', remote_only: 'remote only' }[item.status] || item.status}</span
			><button onclick={() => (open = item)}>Open</button>
		</div>{:else}<div class="empty">
			{resource.loading ? 'Loading library…' : items.length ? 'No titles match this filter.' : 'Scan the library to browse titles.'}
		</div>{/each}
</div>
<Pager bind:page total={filtered.length} />
{#if open}<section class="panel detail">
		<div class="section-heading">
			<div>
				<h2>{open.folder_name}</h2>
				<p>{open.drive_id}</p>
			</div>
			<button onclick={() => (open = null)}>Close</button>
		</div>
		{#each [{ label: 'Main', files: open.existing_files, sizes: open.existing_file_sizes }, { label: 'Remote', files: open.remote_files, sizes: open.remote_file_sizes }] as side}<h3
			>
				{side.label}
			</h3>
			{#each side.files || [] as file}<p class="mono">{file} · {bytes(side.sizes?.[file] || 0)}</p>{:else}<p class="muted">
					No video files
				</p>{/each}{/each}{#if open.status === 'both'}<p class="muted">
				Both sides contain video. Use Hardlinks to inspect and maintain existing links.
			</p>{:else if open.remote_path}<button class="primary" disabled={busy} onclick={() => open && hardlink(open)}
				>{busy ? 'Linking…' : open.status === 'remote_only' ? 'Hardlink to main' : 'Hardlink to remote'}</button
			>{:else}<p class="muted">No remote root is configured for this drive.</p>{/if}
	</section>{/if}
