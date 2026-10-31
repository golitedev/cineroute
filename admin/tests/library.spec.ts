import { test, expect } from '@playwright/test';
import { libraryEntries } from '../src/lib/library';
test('TV details summarize packs and loose episodes while movie details retain files', () => {
	const root = '/disk1/tv/Show (2024)';
	const files = [`${root}/Show.S01.Pack/Disc1/E01.mkv`, `${root}/Show.S01.Pack/Disc2/E02.mkv`, `${root}/E03.mkv`, `${root}/E04.mkv`];
	const sizes = { [files[0]]: 100, [files[1]]: 200, [files[2]]: 50, [files[3]]: 75 };
	expect(libraryEntries('tv', files, sizes, root, 'Show (2024)')).toEqual([
		{ name: 'Show (2024)', size: 125 },
		{ name: 'Show.S01.Pack', size: 300 },
	]);
	expect(
		libraryEntries(
			'tv',
			['Season 01/E01.mkv', 'Season 01/E02.mkv'],
			{ 'Season 01/E01.mkv': 10, 'Season 01/E02.mkv': 20 },
			root,
			'Show (2024)',
		),
	).toEqual([{ name: 'Season 01', size: 30 }]);
	expect(libraryEntries('movie', files, sizes, root, 'Show (2024)')).toEqual(files.map((name) => ({ name, size: sizes[name] })));
});
