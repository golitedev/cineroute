<script lang="ts">
	import { onMount } from 'svelte';
	import { Copy, Download } from 'lucide-svelte';
	import Notice from '../components/Notice.svelte';
	import PageHeader from '../components/PageHeader.svelte';
	import { Resource } from '../lib/resource.svelte';
	import { date } from '../lib/utils';

	type History = {
		imports: {
			job_id: string;
			album: string;
			artist: string;
			imported_at: string;
			partial: boolean;
			folder_path?: string;
			sources: { playlist: string; chart_date: string; track: string }[];
		}[];
		more: boolean;
		offset: number;
	};

	const history = new Resource<History>();
	let copyResult = $state<{ id: string; text: string }>();

	function load(offset = 0) {
		return history.load(`/music/charts/imports?offset=${offset}`);
	}

	onMount(() => {
		void load();
		const timer = setInterval(() => {
			if (!history.loading && !history.data?.offset) void load();
		}, 15_000);
		return () => {
			clearInterval(timer);
			history.destroy();
		};
	});

	async function copyPath(input: HTMLInputElement, id: string) {
		try {
			await navigator.clipboard.writeText(input.value);
			copyResult = { id, text: 'Path copied.' };
		} catch {
			// The clipboard API may be unavailable on a local HTTP connection.
			input.focus();
			input.select();
			const copied = document.execCommand('copy');
			copyResult = { id, text: copied ? 'Path copied.' : 'Press Ctrl+C or Command+C to copy the selected path.' };
		}
	}
</script>

<PageHeader title="Automatic imports" description="Albums CineRoute downloaded for chart playlists.">
	<a class="export" href="/api/music/charts/imports.csv" download="cineroute-automatic-imports.csv">
		<Download class="size-4" /> Download CSV
	</a>
</PageHeader>

<Notice error={history.error} />

<section class="cards" aria-label="Import history">
	{#each history.data?.imports || [] as item (item.job_id)}
		<article class="panel">
			<h2>{item.album}</h2>
			<p class="muted">{item.artist} · {date(item.imported_at)}{item.partial ? ' · Partial album' : ''}</p>
			{#if item.folder_path}
				<p class="folder-name"><span class="muted">Folder name</span> {item.folder_path.split('/').pop()}</p>
				<div class="path-row">
					<label>
						Folder path
						<input readonly value={item.folder_path} />
					</label>
					<button
						type="button"
						onclick={(event) => {
							const field = event.currentTarget.parentElement?.querySelector('input');
							if (field) void copyPath(field, item.job_id);
						}}><Copy class="size-4" /> Copy path</button
					>
				</div>
				{#if copyResult?.id === item.job_id}<p class="muted" role="status">{copyResult.text}</p>{/if}
			{:else}
				<p class="muted">Folder path unavailable for this import.</p>
			{/if}
			<ul class="sources muted">
				{#each item.sources as source}
					<li>{source.playlist} · {source.track} · {source.chart_date}</li>
				{/each}
			</ul>
		</article>
	{:else}
		{#if !history.loading && !history.error}<p class="empty">No automatic albums imported yet.</p>{/if}
	{/each}
</section>

{#if history.data && (history.data.offset || history.data.more)}
	<div class="pager">
		<button disabled={history.loading || !history.data.offset} onclick={() => load(Math.max(0, (history.data?.offset || 0) - 25))}>
			Newer
		</button>
		<button disabled={history.loading || !history.data.more} onclick={() => load((history.data?.offset || 0) + 25)}> Older </button>
	</div>
{/if}

<style>
	.export {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		padding: 6px 12px;
		border: 1px solid var(--border);
		border-radius: 6px;
		background: var(--muted);
		color: var(--foreground);
		text-decoration: none;
	}
	.export:hover {
		border-color: var(--muted-foreground);
	}
	.folder-name {
		margin-top: 10px;
		overflow-wrap: anywhere;
	}
	.folder-name span {
		margin-right: 0.45rem;
	}
	.path-row {
		display: flex;
		align-items: end;
		gap: 8px;
		margin-top: 8px;
	}
	.path-row label {
		flex: 1;
		min-width: 0;
	}
	.path-row input {
		width: 100%;
		font-family: monospace;
	}
	.sources {
		margin-top: 8px;
		overflow-wrap: anywhere;
	}
</style>
