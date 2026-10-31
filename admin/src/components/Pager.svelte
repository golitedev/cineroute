<script lang="ts">
	let { page = $bindable(0), total, size = 20 }: { page?: number; total: number; size?: number } = $props();
	const pages = $derived(Math.max(1, Math.ceil(total / size)));
	$effect(() => {
		if (page >= pages) page = pages - 1;
	});
</script>

{#if total}<div class="pager">
		<button disabled={page === 0} onclick={() => page--}>Previous</button><span>Page {page + 1} of {pages} · {total} items</span><button
			disabled={page >= pages - 1}
			onclick={() => page++}>Next</button
		>
	</div>{/if}
