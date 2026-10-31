<script lang="ts">
	import type { Tracker, Indexer } from '../lib/types';
	let { selected = $bindable(), indexers, spanish = false }: { selected: Tracker[]; indexers: Indexer[]; spanish?: boolean } = $props();
	const rows = $derived.by(() => {
		const map = new Map<number, Indexer>();
		for (const i of indexers) map.set(i.id, i);
		for (const t of selected) if (!map.has(t.id)) map.set(t.id, { id: t.id, name: t.name, protocol: '', enable: false });
		return [...map.values()].sort((a, b) => a.name.localeCompare(b.name));
	});
	function toggle(row: Indexer, checked: boolean) {
		if (checked) {
			if (!row.enable || row.protocol !== 'torrent' || selected.some((t) => t.id === row.id)) return;
			selected.push({ id: row.id, name: row.name, priority: spanish ? 1 : 25 });
		} else selected = selected.filter((t) => t.id !== row.id);
	}
</script>

{#each rows as row}{@const tracker = selected.find((t) => t.id === row.id)}
	<div class="tracker-row">
		<label class="check"
			><input
				type="checkbox"
				checked={!!tracker}
				disabled={!tracker && (!row.enable || row.protocol !== 'torrent')}
				onchange={(e) => toggle(row, e.currentTarget.checked)}
			/><span
				>{row.name}<small
					>{!row.enable || row.protocol !== 'torrent' ? 'Saved selection; unavailable or disabled' : 'Torrent tracker'}</small
				></span
			></label
		>{#if tracker && !spanish}<label>Priority<input type="number" required min="1" max="100" bind:value={tracker.priority} /></label>{/if}
	</div>{:else}<p class="muted">No trackers loaded. Refresh to choose from your Prowlarr configuration.</p>{/each}
<p class="muted text-xs">{selected.length} trackers selected.</p>
