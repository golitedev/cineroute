<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { RefreshCw } from 'lucide-svelte';
	import PageHeader from '../components/PageHeader.svelte';
	import IntakeCard from '../components/IntakeCard.svelte';
	import Notice from '../components/Notice.svelte';
	import { Resource } from '../lib/resource.svelte';
	import { api, post } from '../lib/client';
	import { message, title, date } from '../lib/utils';
	import type { Intake } from '../lib/types';
	const resource = new Resource<{ intakes: Intake[] }>();
	const history = new Resource<{ history: Intake[] }>();
	let fileInput: HTMLInputElement;
	let busy = $state<Record<string, string>>({}),
		uploading = $state(false),
		dragging = $state(false);
	const groups = $derived.by(() => {
		const map = new Map<string, Intake[]>();
		for (const item of resource.data?.intakes || []) {
			const key = item.group || item.id;
			if (!map.has(key)) map.set(key, []);
			map.get(key)!.push(item);
		}
		return [...map.values()].map((items) => items.sort((a, b) => a.season - b.season));
	});
	onMount(() => {
		void resource.load('/intakes');
		void history.load('/history');
		const timer = setInterval(() => {
			if (!resource.loading && Object.values(busy).every((kind) => kind === 'submit')) void resource.load('/intakes');
		}, 2500);
		return () => clearInterval(timer);
	});
	onDestroy(() => {
		resource.destroy();
		history.destroy();
	});
	async function upload(files: FileList | null) {
		if (!files?.length || uploading) return;
		uploading = true;
		resource.error = '';
		const body = new FormData();
		for (const file of files) body.append('torrents', file);
		try {
			await api('/intakes', { method: 'POST', body, signal: resource.signal });
			await resource.load('/intakes');
		} catch (e) {
			resource.error = message(e);
		} finally {
			uploading = false;
			if (fileInput) fileInput.value = '';
		}
	}
	async function action(id: string, kind: string, body: unknown = {}) {
		const group = resource.data?.intakes.find((i) => i.id === id)?.group || id;
		if (busy[group]) return;
		resource.cancelLoad();
		busy[group] = kind;
		resource.error = '';
		try {
			await post(`/intakes/${encodeURIComponent(id)}/${kind}`, body);
		} catch (e) {
			resource.error = message(e);
		} finally {
			const error = resource.error;
			await resource.load('/intakes');
			if (error) resource.error = error;
			void history.load('/history');
			delete busy[group];
		}
	}
	async function remove(group: Intake[]) {
		const key = group[0].group || group[0].id;
		if (busy[key]) return;
		resource.cancelLoad();
		busy[key] = 'remove';
		const results = await Promise.allSettled(
			group.map((i) => api(`/intakes/${encodeURIComponent(i.id)}`, { method: 'DELETE', signal: resource.signal })),
		);
		await resource.load('/intakes');
		const failed = results.find((r) => r.status === 'rejected');
		if (failed?.status === 'rejected') resource.error = message(failed.reason);
		delete busy[key];
	}
</script>

<PageHeader title="Route torrents" description="Match torrents and route them to the right library."
	><button disabled={resource.loading || Object.keys(busy).length > 0} onclick={() => resource.load('/intakes')}
		><RefreshCw class="size-4" />Refresh</button
	></PageHeader
>
<Notice error={resource.error} />
<input bind:this={fileInput} type="file" accept=".torrent" multiple hidden onchange={() => upload(fileInput.files)} />
<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<section
	class="upload-zone panel"
	class:dragging
	ondragover={(e) => {
		e.preventDefault();
		dragging = true;
	}}
	ondragleave={() => (dragging = false)}
	ondrop={(e) => {
		e.preventDefault();
		dragging = false;
		void upload(e.dataTransfer?.files || null);
	}}
	aria-label="Torrent upload"
>
	<h2>{uploading ? 'Uploading torrents…' : 'Drop your torrent files here'}</h2>
	<p>Add one or several .torrent files to get started.</p>
	<button class="primary" disabled={uploading} onclick={() => fileInput.click()}>Browse files</button>
</section>
<div class="cards">
	{#each groups as group (group[0].group || group[0].id)}<IntakeCard
			{group}
			busy={busy[group[0].group || group[0].id] || ''}
			{action}
			{remove}
		/>{/each}
</div>
{#if history.data?.history?.length}<section class="panel">
		<h2>Recent submissions</h2>
		<div class="table-scroll">
			<table>
				<thead><tr><th>Time</th><th>Title</th><th>Save path</th><th>Status</th></tr></thead><tbody
					>{#each [...history.data.history].reverse() as entry}<tr
							><td>{date(entry.result?.submitted_at || entry.created_at)}</td><td>{title(entry.match || undefined) || entry.title}</td><td
								class="mono">{entry.result?.save_path || entry.dest?.save_path}</td
							><td><span class="badge" class:good={entry.status === 'submitted'}>{entry.status}</span></td></tr
						>{/each}</tbody
				>
			</table>
		</div>
	</section>{/if}<Notice error={history.error} />
