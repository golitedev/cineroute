<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { RefreshCw, ChevronRight } from 'lucide-svelte';
	import PageHeader from '../components/PageHeader.svelte';
	import Notice from '../components/Notice.svelte';
	import Pager from '../components/Pager.svelte';
	import { Resource } from '../lib/resource.svelte';
	import { post } from '../lib/client';
	import { bytes, message } from '../lib/utils';
	import { libraryEntries } from '../lib/library';
	import type { CopyView, Copy } from '../lib/types';
	let { kind }: { kind: 'movie' | 'tv' } = $props();
	const resource = new Resource<CopyView>();
	let query = $state(''),
		filter = $state('all'),
		page = $state(0),
		openId = $state(''),
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
			openId = '';
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
			openId = r.view.items.some((i) => i.id === item.id) ? item.id : '';
			success = 'Hardlink created.';
		} catch (e) {
			resource.error = message(e);
		} finally {
			busy = false;
		}
	}
</script>

<PageHeader title={kind === 'tv' ? 'TV shows' : 'Movies'} description="Browse the main and remote libraries, and link missing copies."
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
<div class="resource-table library-table">
	<div class="library-heading"><span>Title</span><span>Copies</span><span></span></div>
	{#each filtered.slice(page * 20, (page + 1) * 20) as item (item.id)}
		<details
			class="library-entry"
			open={openId === item.id}
			ontoggle={(e) => {
				if (e.currentTarget.open) openId = item.id;
				else if (openId === item.id) openId = '';
			}}
		>
			<!-- svelte-ignore a11y_no_redundant_roles (Keep grid summaries exposed as disclosure buttons across browsers.) -->
			<summary
				class="library-row"
				role="button"
				aria-label={`${item.folder_name} details`}
				aria-expanded={openId === item.id}
				aria-controls={`copy-detail-${item.id}`}
			>
				<div>
					<strong>{item.folder_name}</strong>
					<div class="muted text-xs">
						{item.drive_id} ·
						<span class:present={item.existing_files?.length}>main {totals(item.existing_files, item.existing_file_sizes)}</span>
						·
						<span class="remote" class:present={item.remote_files?.length}>remote {totals(item.remote_files, item.remote_file_sizes)}</span>
					</div>
				</div>
				<span class="badge" class:good={item.status === 'both'}
					>{{ both: 'both copies', main_only: 'main only', remote_only: 'remote only' }[item.status] || item.status}</span
				><ChevronRight class="row-chevron size-4" />
			</summary>
			{#if openId === item.id}<div class="library-details" id={`copy-detail-${item.id}`}>
					<div class="copy-sides">
						{#each [{ label: 'Main', files: item.existing_files, sizes: item.existing_file_sizes, root: item.path }, { label: 'Remote', files: item.remote_files, sizes: item.remote_file_sizes, root: item.remote_path }] as side}
							<section class="copy-side" aria-label={`${side.label} copy`}>
								<div class="copy-side-heading">
									<h3>{side.label}</h3>
									<span>{totals(side.files, side.sizes)}</span>
								</div>
								{#each libraryEntries(kind, side.files || [], side.sizes || {}, side.root, item.folder_name) as entry}<div
										class="copy-file"
									>
										<code>{entry.name}</code><span>{bytes(entry.size)}</span>
									</div>{:else}<p class="muted text-xs">No video files</p>{/each}
							</section>
						{/each}
					</div>
					<div class="copy-detail-footer">
						{#if item.status === 'both'}<p class="muted">
								Both sides contain video. Use Hardlinks to inspect and maintain existing links.
							</p>{:else if item.remote_path}<button class="primary" disabled={busy} onclick={() => hardlink(item)}
								>{busy ? 'Linking…' : item.status === 'remote_only' ? 'Hardlink to main' : 'Hardlink to remote'}</button
							>{:else}<p class="muted">No remote root is configured for this drive.</p>{/if}
					</div>
				</div>{/if}
		</details>
	{:else}<div class="empty">
			{resource.loading ? 'Loading library…' : items.length ? 'No titles match this filter.' : 'Scan the library to browse titles.'}
		</div>{/each}
</div>
<Pager bind:page total={filtered.length} />
