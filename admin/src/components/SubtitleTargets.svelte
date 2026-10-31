<script lang="ts">
	import type { Subtitle } from '../lib/types';
	import { metrics } from '../lib/subtitles';
	let { item }: { item: Subtitle } = $props();
</script>

{#each item.targets || [] as target}<div class="target-line">
		<strong>{target.language}</strong><span class="badge" class:good={target.present || target.status === 'added'}
			>{target.status.replaceAll('_', ' ')}</span
		>{#if target.sources?.length}<span>{target.sources.join(', ')}</span>{/if}{#if target.error}<span class="text-destructive"
				>{target.error}</span
			>{/if}{#if target.variant}<span>{target.variant.replaceAll('_', ' ')}</span>{/if}{#if target.installed_path}<code
				>{target.installed_path}</code
			>{/if}{#if target.metrics}<span>{metrics(target.metrics)}</span>{/if}
	</div>{/each}
