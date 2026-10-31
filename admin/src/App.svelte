<script lang="ts">
	import { onMount } from 'svelte';
	import Sidebar from './components/Sidebar.svelte';
	import Login from './components/Login.svelte';
	import Services from './components/Services.svelte';
	import Route from './pages/Route.svelte';
	import Library from './pages/Library.svelte';
	import Hardlinks from './pages/Hardlinks.svelte';
	import Subtitles from './pages/Subtitles.svelte';
	import Settings from './pages/Settings.svelte';
	import Queue from './pages/Queue.svelte';
	import History from './pages/History.svelte';
	import { api, post, configureSession, clearSession } from './lib/client';
	import type { Account, Session, View } from './lib/types';
	let account = $state<Account | null>(null),
		checking = $state(true),
		view = $state<View>('route'),
		settingsVisited = $state(false),
		settingsDirty = $state(false),
		error = $state('');
	function reset() {
		clearSession();
		account = null;
		view = 'route';
		settingsVisited = false;
		settingsDirty = false;
	}
	async function session() {
		try {
			const session = await api<Session>('/session');
			if (session.account.role !== 'admin') throw Error('An administrator account is required.');
			configureSession(session.csrf_token, reset);
			account = session.account;
		} catch {
			reset();
		} finally {
			checking = false;
		}
	}
	onMount(() => {
		void session();
	});
	async function login(name: string, password: string) {
		await post('/login', { username: name, password });
		const session = await api<Session>('/session');
		if (session.account.role !== 'admin') throw Error('An administrator account is required.');
		configureSession(session.csrf_token, reset);
		account = session.account;
	}
	async function logout() {
		if (settingsDirty && !confirm('Discard unsaved settings and sign out?')) return;
		try {
			await post('/logout');
			reset();
			error = '';
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}
	function navigate(next: View) {
		view = next;
		if (next === 'settings') settingsVisited = true;
		window.scrollTo({ top: 0 });
	}
</script>

<svelte:head><title>CineRoute · Administration</title></svelte:head>
{#if checking}<div class="empty">Loading CineRoute…</div>{:else if !account}<Login {login} />{:else}<Sidebar
		{account}
		{view}
		{navigate}
		{logout}
		><main class="main-content">
			<Services />{#if error}<div class="notice error" role="alert">{error}</div>{/if}{#if view === 'route'}<Route
				/>{:else if view === 'movies' || view === 'tv'}{#key view}<Library
						kind={view === 'tv' ? 'tv' : 'movie'}
					/>{/key}{:else if view === 'hardlinks'}<Hardlinks />{:else if view === 'subtitles'}<Subtitles />{:else if view === 'queue'}<Queue
				/>{:else if view === 'history'}<History />{/if}{#if settingsVisited}<div hidden={view !== 'settings'}>
					<Settings bind:dirty={settingsDirty} />
				</div>{/if}
		</main></Sidebar
	>{/if}
