<script lang="ts">
	import { move } from '../lib/utils';
	import type { Profile, SettingsView } from '../lib/types';
	let { profile = $bindable(), kind, data }: { profile: Profile; kind: 'movie' | 'tv'; data: SettingsView } = $props();
	let addQuality = $state('');
	const eligible = $derived((data.drives || []).filter((d) => root(d)));
	const available = $derived((data.qualities || []).filter((q) => !profile.qualities.includes(q.id)));
	function root(d: SettingsView['drives'][number]) {
		return kind === 'movie'
			? profile.destination === 'main'
				? d.MovieRoot
				: d.MovieRemoteRoot
			: profile.destination === 'main'
				? d.TVRoot
				: d.TVRemoteRoot;
	}
	const label = (id: string) => data.qualities.find((q) => q.id === id)?.label || id;
	function removeQuality(index: number) {
		profile.qualities.splice(index, 1);
		if (!profile.qualities.includes(profile.upgrade_until)) profile.upgrade_until = profile.qualities[0];
	}
</script>

<section class="form-section">
	<h2>Library destination</h2>
	<div class="form-grid">
		<label
			>Add requested {kind === 'tv' ? 'shows' : 'movies'} to<select bind:value={profile.destination}
				><option value="remote">Remote library</option><option value="main">Main library</option></select
			><small>Only copies in the selected library satisfy a request.</small></label
		><label
			>Disk for new titles<select bind:value={profile.new_title_drive}
				><option value="">Automatic — most free space</option>{#each eligible as drive}<option value={drive.ID}>{drive.ID}</option
					>{/each}{#if profile.new_title_drive && !eligible.some((d) => d.ID === profile.new_title_drive)}<option
						value={profile.new_title_drive}>{profile.new_title_drive} — unavailable for this destination</option
					>{/if}</select
			><small>Existing titles always stay on their current disk.</small></label
		>
	</div>
	<p class="muted text-xs">All seasons and episodes of a show stay together.</p>
	{#each eligible.filter((d) => !profile.new_title_drive || d.ID === profile.new_title_drive) as drive}<p class="mono">
			{drive.ID} · {root(drive)}
		</p>{:else}<p class="muted">No matching library roots are configured for this selection.</p>{/each}
</section>
<section class="form-section">
	<h2>Allowed qualities</h2>
	<p class="muted">Most preferred first. Qualities outside this list are rejected.</p>
	<div class="ordered-list">
		{#each profile.qualities as id, index}<div>
				<span class="muted">{index + 1}</span><strong>{label(id)}</strong><button
					type="button"
					aria-label={`Move ${label(id)} up`}
					disabled={index === 0}
					onclick={() => move(profile.qualities, index, -1)}>↑</button
				><button
					type="button"
					aria-label={`Move ${label(id)} down`}
					disabled={index === profile.qualities.length - 1}
					onclick={() => move(profile.qualities, index, 1)}>↓</button
				><button type="button" disabled={profile.qualities.length === 1} onclick={() => removeQuality(index)}>Remove</button>
			</div>{/each}
	</div>
	<div class="search-row">
		<select aria-label="Quality to add" bind:value={addQuality}
			><option value="">Choose a quality</option>{#each available as option}<option value={option.id}>{option.label}</option>{/each}</select
		><button
			type="button"
			disabled={!addQuality || !available.some((q) => q.id === addQuality)}
			onclick={() => {
				profile.qualities.push(addQuality);
				addQuality = '';
			}}>Add quality</button
		>
	</div>
	<div class="form-grid">
		<label class="check"><input type="checkbox" bind:checked={profile.upgrade_allowed} />Upgrade existing requested copies</label><label
			>Stop upgrading at<select bind:value={profile.upgrade_until}
				>{#each profile.qualities as id}<option value={id}>{label(id)}</option>{/each}</select
			></label
		><label>Minimum keyword score<input type="number" min="-100000" max="100000" required bind:value={profile.minimum_score} /></label
		><label
			>Stop upgrading at keyword score<input
				type="number"
				min="-100000"
				max="100000"
				required
				bind:value={profile.upgrade_until_score}
			/><small>Quality and score targets must both be met.</small></label
		>
	</div>
</section>
<section class="form-section">
	<h2>File size and playback</h2>
	<p class="muted">Sizes are in GiB. A maximum of 0 means unlimited.</p>
	<div class="form-grid">
		<label
			>Minimum {kind === 'tv' ? 'episode' : 'movie'} size<input
				type="number"
				min="0"
				max="100000"
				step="0.1"
				required
				bind:value={profile.min_size_gib}
			/></label
		><label
			>Maximum {kind === 'tv' ? 'episode' : 'movie'} size<input
				type="number"
				min="0"
				max="100000"
				step="0.1"
				required
				bind:value={profile.max_size_gib}
			/></label
		>{#if kind === 'tv'}<label
				>Maximum season / series pack size<input
					type="number"
					min="0"
					max="100000"
					step="0.1"
					required
					bind:value={profile.max_pack_size_gib}
				/></label
			>{/if}
	</div>
	<fieldset>
		<legend>Allowed video codecs</legend>
		<div class="checkbox-options">
			{#each data.codecs || [] as option}<label class="check"
					><input type="checkbox" value={option.id} bind:group={profile.codecs} />{option.label}</label
				>{/each}
		</div>
	</fieldset>
	<div class="form-grid">
		<label
			>Allowed audio languages (optional)<input
				value={profile.audio_languages.join(', ')}
				oninput={(e) =>
					(profile.audio_languages = e.currentTarget.value
						.split(',')
						.map((s) => s.trim())
						.filter(Boolean))}
				placeholder="Any language"
			/><small>A filter: en, es, sv, or original. Leave blank for any audio.</small></label
		><label class="check"
			><input type="checkbox" bind:checked={profile.allow_unknown_language} />Accept releases with unknown audio language</label
		>
	</div>
</section>
<section class="form-section">
	<h2>Language priorities</h2>
	<p class="muted">
		Prefer tracker-reported audio and full subtitle languages. These preferences rank releases; they do not reject missing metadata.
		Forced-only subtitles and commentary do not earn points.
	</p>
	<div class="form-grid">
		{#each [{ field: 'preferred_audio_languages', label: 'Prioritize audio', options: [{ id: 'original', label: 'Original language' }, ...(data.languages || [])] }, { field: 'preferred_subtitle_languages', label: 'Prioritize full subtitles', options: data.languages || [] }] as group}{@const field =
				group.field as 'preferred_audio_languages' | 'preferred_subtitle_languages'}
			<fieldset>
				<legend>{group.label}</legend
				>{#each group.options.filter((o) => ['original', 'en', 'es', 'sv'].includes(o.id) || profile[field].includes(o.id)) as option}<label
						class="check"><input type="checkbox" value={option.id} bind:group={profile[field]} />{option.label}</label
					>{/each}
				<details>
					<summary>More languages</summary>
					<div class="language-options">
						{#each group.options.filter((o) => !['original', 'en', 'es', 'sv'].includes(o.id) && !profile[field].includes(o.id)) as option}<label
								class="check"><input type="checkbox" value={option.id} bind:group={profile[field]} />{option.label}</label
							>{/each}
					</div>
				</details>
			</fieldset>{/each}
	</div>
	<label class="check"><input type="checkbox" bind:checked={profile.prefer_text_subtitles} />Prefer text-based subtitles</label>
	<p class="muted text-xs">Prefer SRT, ASS/SSA and WebVTT over PGS or VobSub. Language matches take priority.</p>
</section>
<section class="form-section">
	<h2>Keyword rules</h2>
	<p class="muted">
		Required rules must match; rejected rules block releases. Preferred rules add scores. Text matching ignores case; use (?i) for
		case-insensitive regex.
	</p>
	{#each profile.keywords as rule, index}<div class="keyword-row">
			<label
				>Action<select
					bind:value={rule.mode}
					onchange={() => {
						if (rule.mode !== 'preferred') rule.score = 0;
					}}
					><option value="preferred">Prefer / score</option><option value="required">Require</option><option value="excluded">Reject</option
					></select
				></label
			><label
				>Match<select bind:value={rule.match}
					><option value="contains">Text contains</option><option value="regex">Regular expression</option></select
				></label
			><label>Keyword or pattern<input required maxlength="500" bind:value={rule.pattern} /></label><label
				>Score<input type="number" required min="-10000" max="10000" disabled={rule.mode !== 'preferred'} bind:value={rule.score} /></label
			><button type="button" onclick={() => profile.keywords.splice(index, 1)}>Remove</button>
		</div>{/each}<button
		type="button"
		onclick={() => profile.keywords.push({ pattern: '', match: 'contains', mode: 'preferred', score: 10 })}>Add keyword rule</button
	>
</section>
