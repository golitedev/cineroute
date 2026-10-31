<script lang="ts">
	// Arcane sidebar-menu-button default variant, with native Svelte callbacks.
	import { cn } from '../../lib/utils';
	import { useSidebar } from './context.svelte';
	import type { Snippet } from 'svelte';
	let {
		active = false,
		large = false,
		label,
		onclick,
		children,
	}: { active?: boolean; large?: boolean; label: string; onclick?: () => void; children: Snippet } = $props();
	const sidebar = useSidebar();
</script>

<button
	type="button"
	data-sidebar="menu-button"
	data-active={active}
	aria-current={active ? 'page' : undefined}
	title={sidebar.isExpanded ? undefined : label}
	{onclick}
	class={cn(
		'peer/menu-button ring-sidebar-ring hover:bg-sidebar-accent hover:text-sidebar-accent-foreground active:bg-sidebar-accent active:text-sidebar-accent-foreground data-[active=true]:bg-sidebar-accent data-[active=true]:text-sidebar-accent-foreground data-[active=true]:font-medium flex w-full items-center gap-2 overflow-hidden rounded-md p-2 text-left text-sm outline-hidden focus-visible:ring-2 disabled:pointer-events-none disabled:opacity-50 [&>svg]:size-4 [&>svg]:shrink-0',
		large ? 'h-12' : 'h-8',
		!sidebar.isExpanded && '!size-8 !justify-center !p-0 [&>span]:hidden [&>div.grid]:hidden [&>svg.ml-auto]:hidden',
	)}
>
	{@render children()}
</button>
