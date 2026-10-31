<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { HardDrive } from 'lucide-svelte';
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
	onMount(() => {
		void resource.load('/status');
		const timer = setInterval(() => {
			void resource.load('/status');
		}, 30000);
		return () => clearInterval(timer);
	});
	onDestroy(() => resource.destroy());
</script>

<div class="services" aria-label="Service status" role="status">
	{#if resource.error}<span class="text-destructive">{resource.error}</span>{:else if resource.data}{@const data = resource.data}<span
			class:healthy={data.qbittorrent === 'ok'}>qBittorrent: {data.qbittorrent}</span
		><span class:healthy={data.tmdb === 'configured'}>TMDB: {data.tmdb === 'configured' ? 'connected' : data.tmdb}</span
		>{#if data.prowlarr === 'configured'}<span class="healthy">Prowlarr: connected</span
			>{/if}{#if data.preallocate?.includes('enabled')}<span>Preallocation on</span>{/if}{#if data.temp_path?.includes('enabled')}<span
				>Temporary path on</span
			>{/if}<span class:healthy={data.storage?.healthy}
			><HardDrive class="size-3" />{data.storage?.healthy
				? `${bytes(data.storage.free)} free`
				: data.storage?.err || 'Storage unavailable'}</span
		>{:else}<span>Checking services…</span>{/if}
</div>
