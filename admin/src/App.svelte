<script lang="ts">
	import { onMount } from 'svelte';
	import Sidebar from './components/Sidebar.svelte';
	import Login from './components/Login.svelte';
	import Route from './pages/Route.svelte';
	import Library from './pages/Library.svelte';
	import Hardlinks from './pages/Hardlinks.svelte';
	import Subtitles from './pages/Subtitles.svelte';
	import Music from './pages/Music.svelte';
	import MusicImports from './pages/MusicImports.svelte';
	import Settings from './pages/Settings.svelte';
	import Queue from './pages/Queue.svelte';
	import History from './pages/History.svelte';
	import { api, post, configureSession, clearSession } from './lib/client';
	import type { Account, Session, View } from './lib/types';
	const views: View[] = [
		'route',
		'movies',
		'tv',
		'anime',
		'music',
		'music/imports',
		'hardlinks',
		'subtitles',
		'settings',
		'queue',
		'history',
	];
	function currentView(): View {
		const path = window.location.pathname.replace(/\/$/, '');
		return views.find((view) => path === `/admin/${view}`) ?? 'route';
	}
	let account = $state<Account | null>(null),
		checking = $state(true),
		view = $state<View>('route'),
		settingsVisited = $state(false),
		settingsDirty = $state(false),
		error = $state('');
	function reset() {
		clearSession();
		account = null;
		settingsVisited = false;
		settingsDirty = false;
	}
	function applySession(session: Session) {
		if (session.account.role !== 'admin') throw Error('An administrator account is required.');
		configureSession(session.csrf_token, reset);
		showView(currentView());
		account = session.account;
	}
	async function session() {
		try {
			applySession(await api<Session>('/session'));
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
		applySession(await api<Session>('/session'));
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
	function showView(next: View) {
		view = next;
		if (next === 'settings') settingsVisited = true;
	}
	function navigate(next: View) {
		if (next !== view) window.history.pushState(null, '', `/admin/${next}`);
		showView(next);
		window.scrollTo({ top: 0 });
	}
</script>

<svelte:window onpopstate={() => account && showView(currentView())} />
<svelte:head><title>CineRoute · Administration</title></svelte:head>
{#if checking}<div class="empty">Loading CineRoute…</div>{:else if !account}<Login {login} />{:else}<Sidebar
		{account}
		{view}
		{navigate}
		{logout}
		><main class="main-content">
			{#if error}<div class="notice error" role="alert">{error}</div>{/if}{#if view === 'route'}<Route
				/>{:else if view === 'movies' || view === 'tv' || view === 'anime'}{#key view}<Library
						kind={view === 'tv' ? 'tv' : view === 'anime' ? 'anime' : 'movie'}
					/>{/key}{:else if view === 'music'}<Music
					onhistory={() => navigate('music/imports')}
				/>{:else if view === 'music/imports'}<MusicImports />{:else if view === 'hardlinks'}<Hardlinks
				/>{:else if view === 'subtitles'}<Subtitles />{:else if view === 'queue'}<Queue
					onhistory={() => navigate('history')}
				/>{:else if view === 'history'}<History />{/if}{#if settingsVisited}<div hidden={view !== 'settings'}>
					<Settings bind:dirty={settingsDirty} />
				</div>{/if}
		</main></Sidebar
	>{/if}
