import { test, expect, movie, navigate } from './fixtures';
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

for (const section of ['Movies', 'TV shows', 'Anime']) {
	test(`${section} sorts by date added and filters by title`, async ({ page, requests }) => {
		const items = [
			{ ...movie, id: 'old', folder_name: 'Alpha', added_at: '2025-01-01T12:00:00Z' },
			{ ...movie, id: 'new', folder_name: 'Zulu', added_at: '2026-03-15T12:00:00Z' },
			{ ...movie, id: 'middle', folder_name: 'Beta', added_at: '2026-02-01T12:00:00Z' },
			{ ...movie, id: 'unknown', folder_name: 'Unknown' },
		];
		await page.route('http://admin.test/api/copies?*', (route) => route.fulfill({ json: { items } }));
		await page.goto('http://admin.test/');
		await navigate(page, 'Movies');
		await expect(page.locator('.library-row')).toHaveCount(4);
		if (section !== 'Movies') await navigate(page, section);
		const titles = page.locator('.library-row strong');
		await expect(titles).toHaveText(['Alpha', 'Beta', 'Unknown', 'Zulu']);
		await expect(page.getByLabel(/Added (from|through)/)).toHaveCount(0);
		await page.getByRole('combobox', { name: 'Sort library' }).selectOption('newest');
		await expect(titles).toHaveText(['Zulu', 'Beta', 'Alpha', 'Unknown']);
		await page.getByRole('combobox', { name: 'Sort library' }).selectOption('oldest');
		await expect(titles).toHaveText(['Alpha', 'Beta', 'Zulu', 'Unknown']);
		await page.getByRole('searchbox', { name: 'Filter library titles' }).fill('zulu');
		await expect(titles).toHaveText(['Zulu']);
		await page.getByRole('button', { name: 'Clear', exact: true }).click();
		await expect(titles).toHaveCount(4);
	});
}
