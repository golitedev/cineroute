<script lang="ts">
	import type { Snippet } from 'svelte';
	import type { Icon } from 'lucide-svelte';
	let {
		title,
		description = '',
		icon: IconComponent,
		children,
	}: { title: string; description?: string; icon?: typeof Icon; children?: Snippet } = $props();
</script>

<div class="page-header">
	<div class="flex min-w-0 items-start gap-3 sm:gap-4">
		{#if IconComponent}<span class="page-icon"><IconComponent class="size-5" /></span>{/if}
		<div>
			<h1>{title}</h1>
			{#if description}<p>{description}</p>{/if}
		</div>
	</div>
	{#if children}<div class="actions">{@render children()}</div>{/if}
</div>
