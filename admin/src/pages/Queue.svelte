<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { ListChecks, RefreshCw } from 'lucide-svelte';
	import PageHeader from '../components/PageHeader.svelte';
	import Notice from '../components/Notice.svelte';
	import { Resource } from '../lib/resource.svelte';
	import { post } from '../lib/client';
	import { title, status, message } from '../lib/utils';
	import type { RequestRow } from '../lib/types';
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

<PageHeader title="Request queue" description="Review user requests and manage automated downloads." icon={ListChecks}
	><button disabled={resource.loading} onclick={() => resource.load('/requests/queue')}><RefreshCw class="size-4" />Refresh queue</button
	></PageHeader
><Notice error={resource.error} />
<p class="muted">
	{resource.data?.requests?.length || 0} requests · {(resource.data?.requests || []).filter((r) => r.request.status === 'pending').length} awaiting
	approval
</p>
<div class="cards">
	{#each resource.data?.requests || [] as row (row.request.id)}<article class="panel">
			<div class="section-heading">
				<div>
					<h2>{title(row.request.media)}</h2>
					<p>{row.owner} · {row.destination} · {status(row.request.status)}</p>
				</div>
				<div class="actions">
					{#each row.request.status === 'pending' ? ['approve', 'decline'] : !['cancelled', 'declined'].includes(row.request.status) ? ['retry', 'cancel'] : [] as kind}<button
							class:primary={kind === 'approve'}
							class:danger={kind === 'decline' || kind === 'cancel'}
							disabled={busy.includes(row.request.id)}
							onclick={() => action(row.request.id, kind)}>{kind.charAt(0).toUpperCase() + kind.slice(1)}</button
						>{/each}
				</div>
			</div>
			<p class="muted">{row.request.message}</p>
			{#if row.diagnostic}<div class="notice">{row.diagnostic}</div>{/if}
		</article>{:else}<p class="empty">
			{resource.loading ? 'Loading queue…' : 'No requests yet. Requests submitted through the user portal will appear here.'}
		</p>{/each}
</div>
