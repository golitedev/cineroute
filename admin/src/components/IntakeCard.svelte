<script lang="ts">
	import { untrack } from 'svelte';
	import { X, Search } from 'lucide-svelte';
	import { bytes, title, poster } from '../lib/utils';
	import type { Intake } from '../lib/types';
	let {
		group,
		busy,
		action,
		remove,
	}: {
		group: Intake[];
		busy: boolean;
		action: (id: string, kind: string, body?: unknown) => Promise<void>;
		remove: (group: Intake[]) => Promise<void>;
	} = $props();
	const item = $derived(group[0]);
	let query = $state(untrack(() => group[0].title || '')),
		year = $state<number | undefined>(untrack(() => group[0].year || undefined));
	const done = $derived(group.every((i) => i.status === 'submitted'));
	const locked = $derived(busy || group.some((i) => i.status === 'submitting'));
	const destination = $derived(item.dest);
	const mode = $derived(
		(destination?.library || item.library) === 'anime' ? 'anime' : (destination?.remote ?? item.remote) ? 'remote' : 'main',
	);
</script>

<article class="panel intake">
	<div class="section-heading">
		<div>
			<h2>{group.length > 1 ? title(item.match) || item.title : item.torrent_name}</h2>
			<p>{group.length > 1 ? `${group.length} TV parts` : item.filename} · {bytes(group.reduce((n, i) => n + i.size, 0))}</p>
		</div>
		<button class="icon-button ghost" disabled={locked} aria-label="Remove intake" onclick={() => remove(group)}
			><X class="size-4" /></button
		>
	</div>
	{#if group.length > 1}<div class="table-scroll">
			<table>
				<thead><tr><th>Season</th><th>Torrent</th><th>Size</th><th>Status</th></tr></thead><tbody
					>{#each group as member}<tr
							><td>{member.season ? `S${String(member.season).padStart(2, '0')}` : '—'}</td><td>{member.torrent_name}</td><td
								>{bytes(member.size)}</td
							><td><span class="badge" class:good={member.status === 'submitted'}>{member.status}</span></td></tr
						>{/each}</tbody
				>
			</table>
		</div>{:else}<details>
			<summary>Torrent details</summary>
			<dl class="kv">
				<dt>Layout</dt>
				<dd>{item.kind} · {item.files} files</dd>
				<dt>Info hash</dt>
				<dd><code>{item.info_hash_v1 || item.info_hash_v2}</code></dd>
				<dt>Detected type</dt>
				<dd>{item.media_type} · {item.confidence} confidence</dd>
				<dt>Year / season</dt>
				<dd>{item.year || '—'} / {item.season || '—'}</dd>
			</dl>
		</details>{/if}
	{#if !done && !locked}<form
			class="search-row wrap"
			onsubmit={(e) => {
				e.preventDefault();
				void action(item.id, 'search', { query, year: year || 0 });
			}}
		>
			<div class="tabs compact">
				{#each ['movie', 'tv'] as type}<button
						type="button"
						class:active={item.media_type === type}
						onclick={() => action(item.id, 'type', { media_type: type })}>{type === 'movie' ? 'Movie' : 'TV show'}</button
					>{/each}
			</div>
			<input aria-label="Search title" required bind:value={query} /><input
				aria-label="Year"
				type="number"
				min="0"
				max="9999"
				placeholder="Year"
				bind:value={year}
				class="year"
			/><button><Search class="size-4" />Search TMDB</button>
		</form>
		{#if item.tmdb_error}<div class="notice error">{item.tmdb_error}</div>{/if}
		<div class="poster-row">
			{#each (item.tmdb || []).slice(0, 10) as media}<button
					type="button"
					class="poster-option"
					class:selected={media.id === item.match?.id}
					onclick={() => action(item.id, 'match', { tmdb_id: media.id })}
					title={title(media)}
					>{#if poster(media.poster_path)}<img src={poster(media.poster_path)} alt="" loading="lazy" />{:else}<span
							class="poster-placeholder">No poster</span
						>{/if}<span>{title(media)}</span><small>{(media.release_date || media.first_air_date || '').slice(0, 4)}</small></button
				>{/each}
		</div>
	{/if}
	{#if item.match}<section class="intake-destination">
			<div class="section-heading">
				<div>
					<h3>Destination</h3>
					<p>{title(item.match)}</p>
				</div>
				<div class="tabs compact" role="group" aria-label="Destination library">
					{#each ['main', 'remote', 'anime'] as option}<button
							type="button"
							class:active={mode === option}
							aria-pressed={mode === option}
							disabled={locked || done}
							onclick={() =>
								action(item.id, 'destination', { remote: option === 'remote', library: option === 'anime' ? 'anime' : item.media_type })}
							>{option.charAt(0).toUpperCase() + option.slice(1)}</button
						>{/each}
				</div>
			</div>
			{#if destination}<dl class="kv">
					<dt>Folder</dt>
					<dd><code>{destination.folder_name}</code></dd>
					<dt>Drive</dt>
					<dd>{destination.drive_id || '—'} · {bytes(destination.usable_space)} free</dd>
					<dt>Existing library copy</dt>
					<dd>{destination.existing ? `Yes · ${destination.existing_at || 'main'}` : 'No'}</dd>
					<dt>Save path</dt>
					<dd><code>{destination.save_path}</code></dd>
					<dt>Content path</dt>
					<dd><code>{destination.content_path}</code></dd>
					<dt>Torrent size</dt>
					<dd>{bytes(destination.needed_bytes)}</dd>
					<dt>Root folder</dt>
					<dd>{String(destination.root_folder)}</dd>
				</dl>
				{#each destination.warnings || [] as warning}<div class="notice warning">
						{warning}
					</div>{/each}{#if destination.existing_conflict}<div class="notice error">
						Title exists in multiple folders. Resolve the duplicates first.
						<pre>{destination.existing_paths?.join('\n')}</pre>
					</div>{/if}{/if}
			{#each group as member}{#if member.error}<div class="notice error">
						{member.torrent_name}: {member.error}
					</div>{/if}{#if member.result}<div class="notice success">
						<strong>Torrent added and started</strong>
						<p class="mono">{member.result.save_path} · {member.result.hash}</p>
					</div>{/if}{/each}
			{#if locked}<div class="notice">Adding stopped, verifying, starting…</div>{:else if !done}<button
					class="primary"
					disabled={!destination || destination.existing_conflict}
					onclick={() => action(item.id, 'submit')}
					>{group.length > 1 ? `Add all ${group.length} to qBittorrent` : 'Add to qBittorrent'} (stopped, verify, start)</button
				>{/if}
		</section>{/if}
</article>
