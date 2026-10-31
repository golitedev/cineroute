export interface LibraryEntry {
	name: string;
	size: number;
}
// TV inventory paths are grouped at the first folder under the show's root.
export function libraryEntries(
	kind: 'movie' | 'tv',
	files: string[],
	sizes: Record<string, number>,
	root: string,
	folderName: string,
): LibraryEntry[] {
	if (kind === 'movie') return files.map((name) => ({ name, size: sizes[name] || 0 }));
	const prefix = root.replace(/\/+$/, '') + '/';
	const packs = new Map<string, number>();
	for (const file of files) {
		const relative = file.startsWith(prefix) ? file.slice(prefix.length) : file.replace(/^\.\//, '');
		const parts = relative.split('/').filter(Boolean);
		const name = parts.length > 1 ? parts[0] : folderName;
		packs.set(name, (packs.get(name) || 0) + (sizes[file] || 0));
	}
	return [...packs].map(([name, size]) => ({ name, size })).sort((a, b) => a.name.localeCompare(b.name));
}
