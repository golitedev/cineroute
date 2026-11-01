<script lang="ts">
	import { onMount } from 'svelte';
	import { DropdownMenu } from 'bits-ui';
	import {
		Pin,
		PinOff,
		ChevronsUpDown,
		LogOut,
		Clapperboard,
		Upload,
		Film,
		Tv,
		Link,
		Subtitles,
		Settings,
		ListChecks,
		PanelLeft,
		X,
	} from 'lucide-svelte';
	import { SidebarState, setSidebar } from './sidebar/context.svelte';
	import Group from './sidebar/group.svelte';
	import GroupLabel from './sidebar/group-label.svelte';
	import Menu from './sidebar/menu.svelte';
	import MenuItem from './sidebar/menu-item.svelte';
	import Footer from './sidebar/footer.svelte';
	import Content from './sidebar/content.svelte';
	import Header from './sidebar/header.svelte';
	import Button from './sidebar/Button.svelte';
	import Services from './Services.svelte';
	import type { View, Account } from '../lib/types';
	let {
		view,
		account,
		navigate,
		logout,
		children,
	}: { view: View; account: Account; navigate: (view: View) => void; logout: () => void; children: import('svelte').Snippet } = $props();
	const sidebar = setSidebar(new SidebarState());
	const groups: { label: string; items: { id: View; label: string; icon: typeof Upload }[] }[] = [
		{
			label: 'Management',
			items: [
				{ id: 'route', label: 'Route torrents', icon: Upload },
				{ id: 'queue', label: 'Request queue', icon: ListChecks },
			],
		},
		{
			label: 'Resources',
			items: [
				{ id: 'movies', label: 'Movies', icon: Film },
				{ id: 'tv', label: 'TV shows', icon: Tv },
				{ id: 'hardlinks', label: 'Hardlinks', icon: Link },
				{ id: 'subtitles', label: 'Subtitles', icon: Subtitles },
			],
		},
		{
			label: 'Administration',
			items: [{ id: 'settings', label: 'Request settings', icon: Settings }],
		},
	];
	let toggleButton = $state<HTMLButtonElement>();
	function close() {
		sidebar.close();
		toggleButton?.focus();
	}
	function go(next: View) {
		navigate(next);
		if (sidebar.mobile) close();
	}
	onMount(() => {
		try {
			sidebar.pinned = localStorage.getItem('cineroute-sidebar-pinned') !== 'false';
		} catch {}
		const mobile = matchMedia('(max-width:767px)'),
			tablet = matchMedia('(max-width:1023px)');
		const update = () => {
			sidebar.mobile = mobile.matches;
			sidebar.tablet = tablet.matches;
			sidebar.close();
		};
		update();
		mobile.addEventListener('change', update);
		tablet.addEventListener('change', update);
		return () => {
			mobile.removeEventListener('change', update);
			tablet.removeEventListener('change', update);
			sidebar.destroy();
		};
	});
	function keydown(e: KeyboardEvent) {
		if (e.key === 'Escape' && sidebar.mobile && sidebar.drawer) close();
		if ((e.metaKey || e.ctrlKey) && e.key === 'b') {
			e.preventDefault();
			sidebar.toggle();
		}
	}
</script>

<svelte:window onkeydown={keydown} />
<div class="admin-layout" style:--sidebar-gap-width={sidebar.gap}>
	{#if sidebar.mobile && sidebar.drawer}<button class="drawer-backdrop" aria-label="Close navigation" onclick={close}></button>{/if}
	<aside
		aria-label="Administration sidebar"
		class="sidebar-container group"
		data-state={sidebar.state}
		data-collapsible={sidebar.isExpanded ? undefined : 'icon'}
		data-variant="floating"
		data-side="left"
		data-hovered={sidebar.isHovered}
		class:mobile={sidebar.mobile}
		class:drawer={sidebar.drawer}
		inert={sidebar.mobile && !sidebar.drawer}
		onmouseenter={() => {
			if (!sidebar.pinned || sidebar.tablet) sidebar.setHovered(true);
		}}
		onmouseleave={() => sidebar.setHovered(false, 150)}
	>
		<div
			data-sidebar="sidebar"
			class="sidebar-inner flex h-full w-full flex-col overflow-x-hidden rounded-lg border border-sidebar-border shadow-sm backdrop-blur-md"
		>
			<Header collapsed={!sidebar.isExpanded}>
				<div class="relative">
					<a
						href="#route"
						onclick={(e) => {
							e.preventDefault();
							go('route');
						}}
						aria-label="CineRoute administration"
						class="brand-row flex items-center justify-center px-4"
						class:collapsed={!sidebar.isExpanded}
						><Clapperboard class="size-8 shrink-0" />{#if sidebar.isExpanded}<span>CINEROUTE</span>{/if}</a
					>
					<button
						class="sidebar-pin"
						aria-label={sidebar.pinned ? 'Unpin sidebar' : 'Pin sidebar'}
						title={sidebar.pinned ? 'Unpin sidebar' : 'Pin sidebar'}
						disabled={sidebar.tablet}
						onclick={() => sidebar.toggle()}
						>{#if sidebar.pinned}<Pin class="size-3.5 fill-current" />{:else}<PinOff class="size-3.5" />{/if}</button
					>
					{#if sidebar.mobile}<button class="sidebar-mobile-close" aria-label="Close navigation" onclick={close}
							><X class="size-4" /></button
						>{/if}
				</div>
				<div class="sidebar-services"><Services /></div>
			</Header>
			<Content class={sidebar.isExpanded ? '-mt-2' : ''}>
				{#each groups as group}<Group
						><GroupLabel>{group.label}</GroupLabel><Menu
							>{#each group.items as item}<MenuItem
									><Button label={item.label} active={view === item.id} onclick={() => go(item.id)}
										><item.icon /><span>{item.label}</span></Button
									></MenuItem
								>{/each}</Menu
						></Group
					>{/each}
			</Content>
			<Footer
				><div class={sidebar.isExpanded ? 'px-3 pb-2' : 'px-0 pb-2'}>
					<DropdownMenu.Root
						><DropdownMenu.Trigger class="account-trigger" aria-label="Account menu"
							><span class="avatar">{account.name.charAt(0).toUpperCase()}</span>{#if sidebar.isExpanded}<span
									class="grid flex-1 text-left text-sm leading-tight"
									><span class="truncate font-medium">{account.name}</span><span class="truncate text-xs">Administrator</span></span
								><ChevronsUpDown class="ml-auto size-4 shrink-0 text-muted-foreground" />{/if}</DropdownMenu.Trigger
						><DropdownMenu.Portal
							><DropdownMenu.Content side="right" align="end" sideOffset={12} class="account-menu"
								><div class="px-2 py-2 text-xs text-muted-foreground">{account.name} · Administrator</div>
								<DropdownMenu.Separator class="my-1 h-px bg-border" /><DropdownMenu.Item class="account-item" onSelect={logout}
									><LogOut class="size-4" />Sign out</DropdownMenu.Item
								></DropdownMenu.Content
							></DropdownMenu.Portal
						></DropdownMenu.Root
					>
				</div>
			</Footer>
		</div>
	</aside>
	<div class="workspace" inert={sidebar.mobile && sidebar.drawer}>
		{#if sidebar.mobile}<button
				bind:this={toggleButton}
				class="mobile-navigation icon-button ghost"
				onclick={() => sidebar.toggle()}
				aria-label={sidebar.isExpanded ? 'Collapse sidebar' : 'Expand sidebar'}
				aria-expanded={sidebar.isExpanded}><PanelLeft class="size-4" /></button
			>{/if}
		{@render children()}
	</div>
</div>
