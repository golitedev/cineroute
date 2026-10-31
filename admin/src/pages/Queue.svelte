<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { RefreshCw, History } from 'lucide-svelte';
	import PageHeader from '../components/PageHeader.svelte';
	import Notice from '../components/Notice.svelte';
	import { Resource } from '../lib/resource.svelte';
	import { post } from '../lib/client';
	import { title, status, message } from '../lib/utils';
	import type { RequestRow } from '../lib/types';
	let { onhistory }: { onhistory: () => void } = $props();
	const resource = new Resource<{ requests: RequestRow[] }>();
	let busy = $state<string[]>([]);
	onMount(() => {
		void resource.load('/requests/queue');
		const timer = setInterval(() => {
			if (!resource.loading && !busy.length) void resource.load('/requests/queue');
		}, 15000);
		return () => clearInterval(timer);
	});
	onDestroy(() => resource.destroy());
	async function action(id: string, kind: string) {
		busy.push(id);
		resource.error = '';
		try {
			await post(`/requests/queue/${encodeURIComponent(id)}/${kind}`);
			await resource.load('/requests/queue');
		} catch (e) {
			resource.error = message(e);
		} finally {
			busy = busy.filter((i) => i !== id);
		}
	}
</script>

<PageHeader title="Request queue" description="Review user requests and manage automated downloads."
	><button onclick={onhistory}><History class="size-4" />Request history</button><button
		disabled={resource.loading}
		onclick={() => resource.load('/requests/queue')}><RefreshCw class="size-4" />Refresh queue</button
	></PageHeader
><Notice error={resource.error} />
<p class="muted">
	{resource.data?.requests?.length || 0} requests · {(resource.data?.requests || []).filter((r) => r.request.status === 'pending').length} awaiting
	approval
</p>
<div class="resource-table queue-table">
	<div class="queue-heading"><span>Title</span><span>Requested by</span><span>Status</span><span></span></div>
	{#each resource.data?.requests || [] as row (row.request.id)}<article class="queue-row">
			<div class="queue-title">
				<h2>{title(row.request.media)}</h2>
				<p title={row.request.message}>{row.request.message}</p>
			</div>
			<div class="queue-owner"><strong>{row.owner}</strong><span>{row.destination}</span></div>
			<span class="badge" class:good={row.request.status === 'complete'}>{status(row.request.status)}</span>
			<div class="actions">
				{#each row.request.status === 'pending' ? ['approve', 'decline'] : !['cancelled', 'declined'].includes(row.request.status) ? ['retry', 'cancel'] : [] as kind}<button
						class:primary={kind === 'approve'}
						class:danger={kind === 'decline' || kind === 'cancel'}
						disabled={busy.includes(row.request.id)}
						onclick={() => action(row.request.id, kind)}>{kind.charAt(0).toUpperCase() + kind.slice(1)}</button
					>{/each}
			</div>
			{#if row.diagnostic}<div class="queue-diagnostic notice">{row.diagnostic}</div>{/if}
		</article>{:else}<p class="empty">
			{resource.loading ? 'Loading queue…' : 'No requests yet. Requests submitted through the user portal will appear here.'}
		</p>{/each}
</div>
