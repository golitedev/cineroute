<script lang="ts">
	import { Eye, EyeOff, Clapperboard } from 'lucide-svelte';
	import Notice from './Notice.svelte';
	let { login }: { login: (name: string, password: string) => Promise<void> } = $props();
	let name = $state(''),
		password = $state(''),
		visible = $state(false),
		busy = $state(false),
		error = $state('');
	async function submit(e: SubmitEvent) {
		e.preventDefault();
		busy = true;
		error = '';
		try {
			await login(name, password);
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			busy = false;
		}
	}
</script>

<div class="login">
	<form class="panel" onsubmit={submit}>
		<div class="login-brand"><Clapperboard />CINEROUTE</div>
		<h1>Administration</h1>
		<p>Sign in to manage your media library.</p>
		<label>Username<input name="username" autocomplete="username" required bind:value={name} /></label><label
			>Password
			<div class="password-field">
				<input name="password" autocomplete="current-password" type={visible ? 'text' : 'password'} required bind:value={password} /><button
					type="button"
					class="icon-button ghost"
					aria-label={visible ? 'Hide password' : 'Show password'}
					onclick={() => (visible = !visible)}
					>{#if visible}<EyeOff class="size-4" />{:else}<Eye class="size-4" />{/if}</button
				>
			</div></label
		><Notice {error} /><button class="primary" disabled={busy}>{busy ? 'Signing in…' : 'Sign in'}</button>
	</form>
</div>
