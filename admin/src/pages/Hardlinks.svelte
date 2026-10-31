<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { RefreshCw } from 'lucide-svelte';
	import PageHeader from '../components/PageHeader.svelte';
	import Notice from '../components/Notice.svelte';
	import Pager from '../components/Pager.svelte';
	import { Resource } from '../lib/resource.svelte';
	import { api, post } from '../lib/client';
	import { bytes, date, message } from '../lib/utils';
	import type { HardlinkView, Hardlink } from '../lib/types';
	const resource = new Resource<HardlinkView>();
	let query = $state(''),
		filter = $state('all'),
		mirror = $state('main_to_remote'),
		page = $state(0),
		busy = $state(''),
		success = $state('');
	const filtered = $derived(
		(resource.data?.items || []).filter(
			(i) =>
				(filter === 'all' || i.media_type === filter || i.status === filter) &&
				[i.drive_id, i.remote_folder, i.source_folder, i.source_path, i.remote_path].join(' ').toLowerCase().includes(query.toLowerCase()),
		),
	);
	onMount(() => {
		void resource.load('/hardlinks');
	});
	onDestroy(() => resource.destroy());
	async function action(kind: 'scan' | 'relink' | 'remove', item?: Hardlink) {
		if (kind === 'remove' && item && !confirm(`Remove verified remote hardlinks in ${item.remote_path}? Primary library files are kept.`))
			return;
		if (
			kind === 'relink' &&
			item &&
			!confirm(
				mirror === 'main_to_remote'
					? `Mirror ${item.source_path || item.source_folder} into ${item.remote_path}? Extra remote files may be removed.`
					: `Link remote files from ${item.remote_path} into the main library?`,
			)
		)
			return;
		busy = item?.id || 'scan';
		resource.error = '';
		try {
			if (kind === 'scan') resource.data = await post<HardlinkView>('/hardlinks/scan');
			else if (item) {
				const path = '/hardlinks/' + encodeURIComponent(item.id);
				const r =
					kind === 'remove'
						? await api<HardlinkView | { view: HardlinkView }>(path, { method: 'DELETE' })
						: await post<HardlinkView | { view: HardlinkView }>(path + '/relink', { direction: mirror });
				resource.data = 'view' in r ? r.view : r;
			}
			success = kind === 'scan' ? 'Scan completed.' : kind === 'remove' ? 'Remote hardlinks removed.' : 'Folder relinked.';
		} catch (e) {
			resource.error = message(e);
		} finally {
			busy = '';
		}
	}
</script>

<PageHeader title="Hardlinks" description="Inspect existing links, keep names in sync, and maintain remote folders."
	><button disabled={!!busy} onclick={() => action('scan')}
		><RefreshCw class="size-4" />{busy === 'scan' ? 'Scanning…' : 'Scan hardlinks'}</button
	></PageHeader
>
<Notice error={resource.error} {success} />{#each resource.data?.warnings || [] as warning}<Notice error={warning} />{/each}
<div class="stats">
	{#each [['total', 'Folders'], ['healthy', 'Healthy'], ['needs_relink', 'Needs relink'], ['linked_files', 'Linked files']] as [key, label]}<div
		>
			<strong>{resource.data?.stats?.[key] || 0}</strong><span>{label}</span>
		</div>{/each}
	<div><strong>{bytes(resource.data?.stats?.linked_bytes || 0)}</strong><span>Linked data</span></div>
</div>
<p class="muted text-xs">Last scanned {date(resource.data?.scanned_at) || 'not yet'}</p>
<div class="search-row">
	<input
		type="search"
		aria-label="Search hardlinks"
		placeholder="Search folders, drives and paths…"
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
	{#each [['all', 'All links'], ['movie', 'Movies'], ['tv', 'TV'], ['needs_relink', 'Needs relink'], ['healthy', 'Healthy'], ['partial', 'Partial'], ['orphaned', 'Orphaned']] as [key, label]}<button
			class:active={filter === key}
			onclick={() => {
				filter = key;
				page = 0;
			}}>{label}</button
		>{/each}
</div>
<label class="inline-field"
	>Relink mirror<select bind:value={mirror}
		><option value="main_to_remote">Main → remote</option><option value="remote_to_main">Remote → main</option></select
	></label
>
<div class="cards">
	{#each filtered.slice(page * 20, (page + 1) * 20) as item (item.id)}<article class="panel">
			<div class="section-heading">
				<div>
					<h2>{item.remote_folder}</h2>
					<p>{item.drive_id} · {item.media_type} · <span class="badge">{item.status.replaceAll('_', ' ')}</span></p>
				</div>
				<div class="actions">
					<button disabled={!!busy || !item.can_relink} onclick={() => action('relink', item)}
						>{busy === item.id ? 'Working…' : 'Relink'}</button
					><button class="danger" disabled={!!busy || !item.can_remove} onclick={() => action('remove', item)}>Remove</button>
				</div>
			</div>
			<div class="path-grid">
				<div><span>Primary source</span><code>{item.source_path || 'Source not found'}</code></div>
				<div><span>Remote folder</span><code>{item.remote_path}</code></div>
			</div>
			<p class="muted text-xs">{item.linked_files} linked files · {bytes(item.linked_bytes)} · {item.extra_file_count} extra files</p>
			<p class="muted">{item.status_reason}</p>
			{#if item.files?.length}<details>
					<summary>Show file map · {item.files.length} files</summary>{#each item.files as file}<div class="file-row">
							<span class="badge" class:good={file.linked}>{file.linked ? 'linked' : 'extra'}</span><code>{file.relative_path}</code><span
								>{bytes(file.size)}</span
							>{#if file.source_path}<code class="file-source">{file.source_path}</code>{/if}
						</div>{/each}
				</details>{/if}
		</article>{:else}<div class="empty">
			{resource.loading ? 'Loading hardlinks…' : 'No folders match these filters. Scan the library to inspect links.'}
		</div>{/each}
</div>
<Pager bind:page total={filtered.length} />
