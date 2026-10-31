<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { Server, HardDrive, Check, TriangleAlert, ChevronDown } from 'lucide-svelte';
	import { Resource } from '../lib/resource.svelte';
	import { bytes } from '../lib/utils';
	const resource = new Resource<{
		qbittorrent: string;
		tmdb: string;
		prowlarr: string;
		storage: { healthy: boolean; free: number; err?: string };
		preallocate?: string;
		temp_path?: string;
	}>();
	let expanded = $state(false),
		element: HTMLDetailsElement;
	const services = $derived(
		resource.data
			? [
					{ name: 'qBittorrent', status: resource.data.qbittorrent, healthy: resource.data.qbittorrent === 'ok' },
					{
						name: 'TMDB',
						status: resource.data.tmdb === 'configured' ? 'connected' : resource.data.tmdb,
						healthy: resource.data.tmdb === 'configured',
					},
					{
						name: 'Prowlarr',
						status: resource.data.prowlarr === 'configured' ? 'connected' : resource.data.prowlarr,
						healthy: resource.data.prowlarr === 'configured',
					},
				]
			: [],
	);
	const attention = $derived(
		!!resource.error || (!!resource.data && (!resource.data.storage?.healthy || services.some((s) => !s.healthy))),
	);
	onMount(() => {
		void resource.load('/status');
		const timer = setInterval(() => void resource.load('/status'), 30000);
		return () => clearInterval(timer);
	});
	onDestroy(() => resource.destroy());
</script>

<svelte:window
	onclick={(e) => {
		if (element && !element.contains(e.target as Node)) {
			element.open = false;
			expanded = false;
		}
	}}
	onkeydown={(e) => {
		if (e.key === 'Escape' && element?.open) {
			element.open = false;
			expanded = false;
			element.querySelector('summary')?.focus();
		}
	}}
/>
<details class="service-status" bind:this={element} bind:open={expanded}>
	<!-- svelte-ignore a11y_no_redundant_roles (Keep flex summaries exposed as disclosure buttons across browsers.) -->
	<summary
		role="button"
		aria-expanded={expanded}
		aria-label="Service status"
		title={attention ? 'Service attention required' : 'Service status'}
		>{#if attention}<TriangleAlert class="size-3.5 text-amber-400" />{:else}<Server class="size-3.5" />{/if}<span
			class="service-summary-label">Services</span
		>{#if resource.data?.storage?.healthy}<span class="service-storage">{bytes(resource.data.storage.free)} free</span>{/if}<ChevronDown
			class="size-3"
		/></summary
	>
	{#if expanded}<div class="service-popover">
			<h2>Service status</h2>
			{#if resource.error}<p class="text-destructive" role="alert">
					{resource.error}
				</p>{:else if resource.data}{#each services as service}<div class="service-line">
						<span>{service.name}</span><span
							>{#if service.healthy}<Check class="size-3" />{:else}<TriangleAlert
									class="size-3 text-amber-400"
								/>{/if}{service.status}</span
						>
					</div>{/each}
				<div class="service-line">
					<span><HardDrive class="size-3" />Storage</span><span
						>{resource.data.storage?.healthy
							? `${bytes(resource.data.storage.free)} free`
							: resource.data.storage?.err || 'Unavailable'}</span
					>
				</div>
				{#if resource.data.preallocate?.includes('enabled')}<p>
						Preallocation enabled
					</p>{/if}{#if resource.data.temp_path?.includes('enabled')}<p>Temporary path enabled</p>{/if}{:else}<p>Checking services…</p>{/if}
		</div>{/if}
</details>
