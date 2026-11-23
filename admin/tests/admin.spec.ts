import { test, expect, navigate, settingsView, movie, history, subtitleView } from './fixtures';

test('Svelte shell, library totals, filters, details, and account menu', async ({ page, requests }, info) => {
	const errors: string[] = [];
	page.on('pageerror', (e) => errors.push(e.message));
	await page.goto('http://admin.test/');
	await expect(page.getByRole('heading', { name: 'Route torrents', exact: true })).toBeVisible();
	await expect(page.getByRole('button', { name: 'Search & request' })).toHaveCount(0);
	await navigate(page, 'Movies');
	await expect(page.locator('.library-row')).toContainText('main 68.93 GiB');
	await expect(page.locator('.library-row')).toContainText('remote 6.07 GiB');
	await expect(page.getByRole('button', { name: 'Open', exact: true })).toHaveCount(0);
	const row = page.getByRole('button', { name: `${movie.folder_name} details`, exact: true });
	await row.click();
	await expect(page.locator('.library-entry[open] .library-details')).toContainText('movie.mkv');
	await row.focus();
	await page.keyboard.press('Space');
	await expect(page.locator('.library-details')).toHaveCount(0);
	await page.keyboard.press('Enter');
	await expect(page.locator('.library-entry[open] .library-details')).toBeVisible();
	await page.getByRole('searchbox', { name: 'Filter library titles' }).fill('absent');
	await expect(page.locator('.library-row')).toHaveCount(0);
	await page.getByRole('button', { name: 'Clear', exact: true }).click();
	await page.getByRole('button', { name: 'Scan library', exact: true }).click();
	await expect.poll(() => requests.some((r) => r.path === '/copies/scan' && r.csrf === 'test-csrf')).toBeTruthy();
	await navigate(page, 'TV shows');
	await expect(page.locator('.library-row')).toContainText('main 5.00 GiB');
	await page.getByRole('button', { name: 'Example Show (2024) details', exact: true }).click();
	const main = page.getByRole('region', { name: 'Main copy', exact: true });
	await expect(main.locator('.copy-file')).toHaveCount(2);
	await expect(main.locator('.copy-file').first()).toContainText('Example.Show.S01.Pack');
	await expect(main.locator('.copy-file').first()).toContainText('3.00 GiB');
	await expect(main.locator('.copy-file').last()).toContainText('Example.Show.S02.Pack');
	await expect(main.locator('.copy-file').last()).toContainText('2.00 GiB');
	await expect(page.getByRole('region', { name: 'Remote copy', exact: true })).toContainText('512.00 MiB');
	await expect(page.locator('.library-details')).not.toContainText('episode1.mkv');
	await expect(page.locator('.library-details')).not.toContainText('Disc1');
	await navigate(page, 'Anime');
	await expect(page.getByRole('heading', { name: 'Anime', exact: true })).toBeVisible();
	await expect(page.locator('.stats')).toContainText('Titles');
	await expect(page.locator('.library-row')).toContainText('disk1 · 4.00 GiB');
	await expect(page.locator('.library-row')).toContainText('2 files');
	await page.getByRole('button', { name: 'Frieren (2023) details', exact: true }).click();
	const animeLibrary = page.getByRole('region', { name: 'Anime library', exact: true });
	await expect(animeLibrary.locator('.copy-file')).toHaveCount(1);
	await expect(animeLibrary.locator('.copy-file')).toContainText('Frieren.S01.Pack');
	await expect(animeLibrary.locator('.copy-file')).toContainText('4.00 GiB');
	await expect(page.getByRole('region', { name: 'Remote copy', exact: true })).toHaveCount(0);
	await expect(page.getByRole('button', { name: /Hardlink to/ })).toHaveCount(0);
	const toggle = page.getByRole('button', { name: 'Expand sidebar', exact: true });
	if (await toggle.isVisible()) await toggle.click();
	await page.getByRole('button', { name: 'Account menu', exact: true }).click();
	await expect(page.getByRole('menuitem', { name: 'Sign out' })).toBeVisible();
	await page.keyboard.press('Escape');
	expect(errors).toEqual([]);
	await page.evaluate(() => Promise.all(document.getAnimations().map((a) => a.finished)));
	await page.screenshot({ path: info.outputPath('library.png'), fullPage: true });
});

test('request policy edits persist across tabs and navigation, and save exact profile state', async ({ page, requests }) => {
	await page.goto('http://admin.test/');
	await navigate(page, 'Request settings');
	await page.getByRole('spinbutton', { name: 'Movie limit per user', exact: true }).fill('4');
	await expect(page.getByRole('status')).toContainText(['Unsaved changes']);
	await page.getByRole('button', { name: 'Movies', exact: true }).last().click();
	await page.getByLabel('Add requested movies to').selectOption('main');
	await page.getByLabel('Disk for new titles').selectOption('disk1');
	await page.getByRole('group', { name: 'Prioritize audio', exact: true }).getByLabel('Spanish', { exact: true }).check();
	await page.getByLabel('Prefer text-based subtitles', { exact: true }).check();
	await page.getByRole('button', { name: 'Add keyword rule' }).click();
	await page.getByLabel('Keyword or pattern').fill('DUAL <tag>');
	await navigate(page, 'Hardlinks');
	await navigate(page, 'Request settings');
	await expect(page.getByLabel('Add requested movies to')).toHaveValue('main');
	await page.getByRole('button', { name: 'Save settings', exact: true }).click();
	await expect(page.getByText('Request settings saved.', { exact: true })).toBeVisible();
	const body = requests.findLast((r) => r.path === '/requests/settings' && r.method === 'PUT')!.body as typeof settingsView.settings;
	expect(body.movie_limit).toBe(4);
	expect(body.movie.destination).toBe('main');
	expect(body.tv.destination).toBe('remote');
	expect(body.movie.preferred_audio_languages).toEqual(['es']);
	expect(body.movie.prefer_text_subtitles).toBe(true);
	expect(body.movie.keywords[0].pattern).toBe('DUAL <tag>');
});

test('failed and in-flight saves retain edits', async ({ page, requests }) => {
	await page.goto('http://admin.test/');
	await navigate(page, 'Request settings');
	await page.getByRole('spinbutton', { name: 'Movie limit per user', exact: true }).fill('5');
	await page.route('**/api/requests/settings', async (route) => {
		if (route.request().method() === 'PUT') return route.fulfill({ status: 400, json: { error: 'Invalid settings' } });
		return route.fallback();
	});
	await page.getByRole('button', { name: 'Save settings', exact: true }).click();
	await expect(page.getByRole('alert')).toHaveText('Invalid settings');
	await expect(page.getByRole('spinbutton', { name: 'Movie limit per user', exact: true })).toHaveValue('5');
	await expect(page.getByRole('button', { name: 'Save settings', exact: true })).toBeEnabled();
	let complete: (() => void) | undefined;
	await page.route('**/api/requests/settings', async (route) => {
		if (route.request().method() !== 'PUT') return route.fallback();
		await new Promise<void>((resolve) => (complete = resolve));
		await route.fulfill({ json: { ...settingsView, settings: route.request().postDataJSON() } });
	});
	await page.getByRole('button', { name: 'Save settings', exact: true }).click();
	await expect.poll(() => !!complete).toBeTruthy();
	await page.getByRole('spinbutton', { name: 'Movie limit per user', exact: true }).fill('6');
	complete!();
	await expect(page.getByRole('button', { name: 'Save settings', exact: true })).toBeEnabled();
	await expect(page.getByRole('spinbutton', { name: 'Movie limit per user', exact: true })).toHaveValue('6');
});

test('queue actions and safe download evidence render without HTML injection', async ({ page, requests }) => {
	await page.goto('http://admin.test/');
	await navigate(page, 'Request queue');
	await expect(page.getByRole('heading', { name: 'Example <Movie>', exact: true })).toBeVisible();
	await page.getByRole('button', { name: 'Approve', exact: true }).click();
	await expect.poll(() => requests.some((r) => r.path === '/requests/queue/request1/approve' && r.csrf === 'test-csrf')).toBeTruthy();
	await navigate(page, 'Request history');
	await expect(page.locator('.download-detail')).toContainText('Reported audio: Spanish');
	await expect(page.locator('.download-detail')).toContainText('Reported full subtitles: Swedish');
	await expect(page.locator('.download-detail')).toContainText('Reported video bitrate: 9.10 Mbps');
	await expect(page.getByRole('link', { name: 'Open torrent on tracker' })).toHaveAttribute('href', 'https://tracker.example/torrents/42');
	await expect(page.locator('.notice').filter({ hasText: 'No peers <yet>' })).toBeVisible();
	await page.getByRole('button', { name: 'Next', exact: true }).click();
	await expect(page.locator('.pager')).toContainText('Page 2 of 2');
	await page.route('**/api/requests/history?*', (route) =>
		route.fulfill({
			json: {
				...history,
				entries: [{ ...history.entries[0], downloads: [{ ...history.entries[0].downloads[0], tracker_url: 'javascript:alert(1)' }] }],
			},
		}),
	);
	await page.getByRole('button', { name: 'Refresh history' }).click();
	await expect(page.getByRole('link', { name: 'Open torrent on tracker' })).toHaveCount(0);
});

test('subtitle progress, language outcomes, details and locked batch controls', async ({ page, requests }) => {
	await page.goto('http://admin.test/');
	await navigate(page, 'Subtitles');
	await expect(page.getByRole('button', { name: 'Add missing subtitles — next 20' })).toBeEnabled();
	await expect(page.locator('.target-line').filter({ hasText: 'embedded stream #2' }).first()).toBeVisible();
	await expect(page.locator('.target-line').filter({ hasText: 'no safe candidate was found' }).first()).toBeVisible();
	await page.getByRole('button', { name: 'Details', exact: true }).click();
	await expect(page.getByRole('heading', { name: 'Attempts', exact: true })).toBeVisible();
	await page.getByRole('button', { name: 'Add missing subtitles — next 20' }).click();
	await expect.poll(() => requests.some((r) => r.path === '/subtitles/run')).toBeTruthy();
	await page.getByText('Processing settings', { exact: true }).click();
	await page.getByRole('spinbutton', { name: 'Batch size', exact: true }).fill('4');
	await page.getByRole('button', { name: 'Save settings', exact: true }).click();
	await expect
		.poll(() => requests.some((r) => r.path === '/subtitles/settings' && r.method === 'PATCH' && r.csrf === 'test-csrf'))
		.toBeTruthy();
	await page.route('**/api/subtitles*', (route) =>
		route.request().method() === 'GET'
			? route.fulfill({
					json: {
						...subtitleView,
						batch: { running: true, kind: 'run', total: 20, done: 3, stage: 'reference', current: subtitleView.items[0].video_path },
						items: [{ ...subtitleView.items[0], step: 'reference' }],
					},
				})
			: route.fallback(),
	);
	await page.getByRole('button', { name: 'Scan remote movies', exact: true }).click();
	await expect(page.getByRole('button', { name: 'Running…', exact: true })).toBeDisabled();
	await expect(page.getByRole('button', { name: 'Run', exact: true })).toBeDisabled();
	await expect(page.getByText('42% · 0:50:00 of 2:00:00', { exact: false })).toBeVisible();
	await page.getByRole('button', { name: 'Cancel', exact: true }).click();
	await expect.poll(() => requests.some((r) => r.path === '/subtitles/run/cancel')).toBeTruthy();
});

test('torrent routing and hardlink operations retain API semantics', async ({ page, requests }) => {
	page.on('dialog', (dialog) => dialog.accept());
	await page.goto('http://admin.test/');
	await expect(page.getByRole('heading', { name: 'Example.Movie.2024', exact: true })).toBeVisible();
	await page.getByRole('button', { name: 'Remote', exact: true }).click();
	await expect
		.poll(() =>
			requests.some((r) => r.path === '/intakes/intake1/destination' && JSON.stringify(r.body) === '{"remote":true,"library":"movie"}'),
		)
		.toBeTruthy();
	await expect(page.getByLabel('Automatically fetch missing subtitles')).toBeChecked();
	await page.getByRole('button', { name: /Add to qBittorrent/ }).click();
	await expect
		.poll(() => requests.some((r) => r.path === '/intakes/intake1/submit' && JSON.stringify(r.body) === '{"auto_subtitles":true}'))
		.toBeTruthy();
	await navigate(page, 'Hardlinks');
	await page.getByRole('combobox').selectOption('remote_to_main');
	await page.getByRole('button', { name: 'Relink', exact: true }).click();
	await expect
		.poll(() => requests.some((r) => r.path === '/hardlinks/link1/relink' && JSON.stringify(r.body) === '{"direction":"remote_to_main"}'))
		.toBeTruthy();
	await page.getByRole('button', { name: 'Remove', exact: true }).click();
	await expect.poll(() => requests.some((r) => r.path === '/hardlinks/link1' && r.method === 'DELETE')).toBeTruthy();
});

test('torrent routing can opt out of automatic subtitles', async ({ page, requests }) => {
	await page.goto('http://admin.test/');
	await expect(page.getByLabel('Automatically fetch missing subtitles')).toBeChecked();
	await page.getByLabel('Automatically fetch missing subtitles').uncheck();
	await page.getByRole('button', { name: /Add to qBittorrent/ }).click();
	await expect
		.poll(() => requests.some((r) => r.path === '/intakes/intake1/submit' && JSON.stringify(r.body) === '{"auto_subtitles":false}'))
		.toBeTruthy();
});

test('login, logout, session expiry, and mobile drawer keep admin data isolated', async ({ page, requests }) => {
	let loggedIn = false;
	await page.route('**/api/session', (route) =>
		route.fulfill({
			status: loggedIn ? 200 : 401,
			json: loggedIn ? { account: { name: 'sueco', role: 'admin' }, csrf_token: 'test-csrf' } : { error: 'unauthorized' },
		}),
	);
	await page.route('**/api/login', (route) => {
		loggedIn = true;
		return route.fulfill({ json: {} });
	});
	await page.goto('http://admin.test/');
	await page.getByLabel('Username').fill('sueco');
	await page.getByLabel('Password', { exact: true }).fill('secret');
	await page.getByRole('button', { name: 'Sign in', exact: true }).click();
	await expect(page.getByRole('heading', { name: 'Route torrents', exact: true })).toBeVisible();
	await navigate(page, 'Movies');
	await expect(page.locator('.library-row')).toBeVisible();
	await page.route('**/api/copies?*', (route) => route.fulfill({ status: 401, json: { error: 'session expired' } }));
	await page.getByRole('button', { name: 'Scan library' }).click(); // scan still succeeds; the following navigation refresh expires.
	await navigate(page, 'TV shows');
	await expect(page.getByRole('button', { name: 'Sign in', exact: true })).toBeVisible();
	await expect(page.locator('.library-row')).toHaveCount(0);
});

test('responsive sidebar, keyboard pinning and every admin page fit the viewport', async ({ page, requests }, info) => {
	const errors: string[] = [];
	page.on('pageerror', (e) => errors.push(e.message));
	await page.goto('http://admin.test/');
	for (const name of ['Movies', 'TV shows', 'Anime', 'Hardlinks', 'Subtitles', 'Request settings', 'Request queue', 'Request history']) {
		await navigate(page, name);
		await expect(page.getByRole('heading', { name, exact: true })).toBeVisible();
		await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBeTruthy();
	}
	if (info.project.name === 'desktop') {
		await page.keyboard.press('Control+b');
		await expect(page.locator('.sidebar-container')).toHaveAttribute('data-collapsible', 'icon');
		await page.locator('.main-content').hover();
		await page.keyboard.press('Control+b');
		await expect(page.locator('.sidebar-container')).not.toHaveAttribute('data-collapsible', 'icon');
	} else {
		await page.getByRole('button', { name: 'Expand sidebar', exact: true }).click();
		await expect(page.locator('.workspace')).toHaveAttribute('inert', '');
		await page.keyboard.press('Escape');
		await expect(page.locator('.workspace')).not.toHaveAttribute('inert', '');
	}
	expect(errors).toEqual([]);
	await page.evaluate(() => Promise.all(document.getAnimations().map((a) => a.finished)));
	await page.screenshot({ path: info.outputPath('admin.png'), fullPage: true });
});

test('Arcane sidebar auto-collapses on tablets, expands on hover, and persists its pin', async ({ page, requests }, info) => {
	test.skip(info.project.name === 'mobile', 'Desktop pointer behavior');
	await page.setViewportSize({ width: 900, height: 1000 });
	await page.mouse.move(700, 50);
	await page.goto('http://admin.test/');
	await expect(page.getByRole('heading', { name: 'Route torrents', exact: true })).toBeVisible();
	await page.mouse.move(700, 50);
	const sidebar = page.locator('.sidebar-container');
	await expect(sidebar).toHaveAttribute('data-collapsible', 'icon');
	await sidebar.hover();
	await expect(sidebar).not.toHaveAttribute('data-collapsible', 'icon');
	await expect(page.getByRole('button', { name: 'Unpin sidebar', exact: true })).toBeDisabled();
	await page.getByRole('button', { name: 'Movies', exact: true }).click();
	await expect(page.getByRole('heading', { name: 'Movies', exact: true })).toBeVisible();
	await page.locator('.main-content').hover();
	await expect(sidebar).toHaveAttribute('data-collapsible', 'icon');
	await page.setViewportSize({ width: 1440, height: 1000 });
	await expect(sidebar).not.toHaveAttribute('data-collapsible', 'icon');
	await page.getByRole('button', { name: 'Unpin sidebar', exact: true }).click();
	await page.locator('.main-content').hover();
	await expect(sidebar).toHaveAttribute('data-collapsible', 'icon');
	await page.reload();
	await expect(sidebar).toHaveAttribute('data-collapsible', 'icon');
	await sidebar.hover();
	await expect(sidebar).not.toHaveAttribute('data-collapsible', 'icon');
	await page.getByRole('button', { name: 'Pin sidebar', exact: true }).click();
	await page.locator('.main-content').hover();
	await expect(sidebar).not.toHaveAttribute('data-collapsible', 'icon');
});

test('content fills an ultrawide workspace and services live beneath the sidebar brand', async ({ page, requests }, info) => {
	await page.setViewportSize({ width: 2560, height: 1440 });
	await page.mouse.move(1200, 500);
	await page.goto('http://admin.test/');
	await expect(page.getByRole('heading', { name: 'Route torrents', exact: true })).toBeVisible();
	await expect(page.getByRole('button', { name: 'Activity Center', exact: true })).toHaveCount(0);
	await expect(page.locator('.page-icon')).toHaveCount(0);
	await navigate(page, 'Request queue');
	await expect(page.locator('.queue-row')).toHaveCount(1);
	const bounds = await page.locator('.queue-table').boundingBox();
	expect(bounds!.width).toBeGreaterThan(2250);
	expect(bounds!.x + bounds!.width).toBeGreaterThan(2500);
	const row = await page.locator('.queue-row').boundingBox();
	expect(row!.height).toBeLessThan(85);
	const radius = await page.locator('[data-sidebar="menu-button"][data-active="true"]').evaluate((el) => getComputedStyle(el).borderRadius);
	expect(parseFloat(radius)).toBeCloseTo(9.8, 1);
	const status = page.getByLabel('Service status', { exact: true });
	await expect(status).toBeVisible();
	await expect(page.locator('.workspace-bar')).toHaveCount(0);
	await expect(page.locator('.sidebar-services').getByLabel('Service status', { exact: true })).toBeVisible();
	const position = await status.boundingBox();
	const brand = await page.locator('.brand-row').boundingBox();
	expect(position!.y).toBeGreaterThanOrEqual(brand!.y + brand!.height);
	expect(position!.x + position!.width).toBeLessThan(272);
	await status.click();
	await expect(page.locator('.service-popover')).toContainText('qBittorrent');
	await expect(page.locator('.service-popover')).toContainText('TMDB');
	await page.keyboard.press('Escape');
	await expect(page.locator('.service-popover')).not.toBeVisible();
	await page.screenshot({ path: info.outputPath('queue-ultrawide.png'), fullPage: true });
	await page.setViewportSize({ width: 390, height: 844 });
	await page.getByRole('button', { name: 'Expand sidebar', exact: true }).click();
	await expect(status).toContainText('Services');
	await expect(status).toContainText('free');
	await status.click();
	const details = page.locator('.service-popover');
	await expect(details).toContainText('qBittorrent');
	const detailsBounds = await details.boundingBox();
	expect(detailsBounds!.x).toBeGreaterThan(0);
	expect(detailsBounds!.x + detailsBounds!.width).toBeLessThan(272);
	await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBeTruthy();
});

test('movie and TV codec preferences can be ranked independently and saved', async ({ page, requests }) => {
	await page.goto('http://admin.test/');
	await navigate(page, 'Request settings');
	await page.locator('.tabs').getByRole('button', { name: 'Movies', exact: true }).click();
	await page.getByRole('button', { name: 'Move HEVC / x265 up', exact: true }).click();
	const codecs = page.getByRole('group', { name: 'Video codec ranking' });
	await expect(codecs.locator('.ordered-list strong')).toHaveText(['HEVC / x265', 'H.264 / x264']);
	await page.locator('.tabs').getByRole('button', { name: 'TV', exact: true }).click();
	await expect(codecs.locator('.ordered-list strong')).toHaveText(['H.264 / x264', 'HEVC / x265']);
	await codecs.getByRole('button', { name: 'Remove', exact: true }).last().click();
	await expect(codecs.getByRole('button', { name: 'Remove', exact: true })).toBeDisabled();
	await codecs.getByLabel('Codec to add').selectOption('hevc');
	await codecs.getByRole('button', { name: 'Add codec', exact: true }).click();
	await page.getByRole('button', { name: 'Save settings', exact: true }).click();
	await expect(page.getByText('Request settings saved.', { exact: true })).toBeVisible();
	const body = requests.findLast((r) => r.path === '/requests/settings' && r.method === 'PUT')!.body as typeof settingsView.settings;
	expect(body.movie.codecs).toEqual(['hevc', 'h264']);
	expect(body.tv.codecs).toEqual(['h264', 'hevc']);
	await page.locator('.tabs').getByRole('button', { name: 'Movies', exact: true }).click();
	await expect(codecs.locator('.ordered-list strong')).toHaveText(['HEVC / x265', 'H.264 / x264']);
});

test('admin routes survive refresh and browser history', async ({ page, requests }) => {
	await page.goto('http://admin.test/admin/music');
	await expect(page.getByRole('heading', { name: 'Music', exact: true })).toBeVisible();
	expect(requests.some((request) => request.path === '/session')).toBeTruthy();
	await navigate(page, 'TV shows');
	await expect(page).toHaveURL(/\/admin\/tv$/);
	await page.reload();
	await expect(page.getByRole('heading', { name: 'TV shows', exact: true })).toBeVisible();
	await navigate(page, 'Request settings');
	await expect(page).toHaveURL(/\/admin\/settings$/);
	await page.reload();
	await expect(page.getByRole('heading', { name: 'Request settings', exact: true })).toBeVisible();
	await page.goBack();
	await expect(page.getByRole('heading', { name: 'TV shows', exact: true })).toBeVisible();
	await page.goForward();
	await expect(page.getByRole('heading', { name: 'Request settings', exact: true })).toBeVisible();
});

test('music library indexing starts only when an admin clicks scan', async ({ page, requests }) => {
	let running = false;
	let scans = 0;
	await page.route('**/api/music/library/scan', async (route) => {
		if (route.request().method() === 'POST') {
			expect(route.request().headers()['x-cineroute-csrf']).toBe('test-csrf');
			scans++;
			running = true;
		}
		await route.fulfill({ json: { enabled: true, running } });
	});
	await page.goto('http://admin.test/admin/music');
	const scan = page.getByRole('button', { name: 'Scan music library', exact: true });
	await expect(scan).toBeEnabled();
	expect(requests.some((request) => request.path === '/session')).toBeTruthy();
	expect(scans).toBe(0);
	await scan.click();
	await expect(page.getByRole('button', { name: 'Scanning music library…' })).toBeDisabled();
	running = false;
	await expect(scan).toBeEnabled({ timeout: 7_000 });
	expect(scans).toBe(1);
});

test('admin chart playlists show sync progress and automatic import history', async ({ page, requests }) => {
	let created = false;
	let reviewing = false;
	const qobuzTracks = [
		{ id: '10', title: 'Studio version', artist: 'Example artist', album: 'Example release', disc: 1, number: 1, duration: 165 },
		{ id: '20', title: 'Live version', artist: 'Example artist', album: 'Example release', disc: 1, number: 2, duration: 202 },
	];
	const trackLink = 'https://play.qobuz.com/track/20';
	let confirmedTrack: (typeof qobuzTracks)[number] | undefined;
	const folder = 'Example artist — Example release (2026) [FLAC]';
	const folderPath = '/mnt/music/' + folder;
	await page.route('**/api/music/charts**', async (route) => {
		const request = route.request();
		const { pathname } = new URL(request.url());
		if (pathname.endsWith('/imports.csv')) {
			return route.fulfill({
				contentType: 'text/csv',
				headers: { 'Content-Disposition': 'attachment; filename="cineroute-automatic-imports.csv"' },
				body: 'Folder path\n' + folderPath,
			});
		}
		if (pathname === '/api/music/charts/colombia/tracks/2/qobuz') {
			expect(request.headers()['x-cineroute-csrf']).toBe('test-csrf');
			const reference = request.method() === 'DELETE' ? '' : request.postDataJSON().reference;
			if (reference) expect(['album123', trackLink]).toContain(reference);
			if (request.method() === 'POST') {
				return route.fulfill({ json: { tracks: reference === trackLink ? [qobuzTracks[1]] : qobuzTracks } });
			}
			if (request.method() === 'PUT') {
				expect(request.postDataJSON()).toEqual({ reference, track_id: '20' });
				confirmedTrack = qobuzTracks[1];
			} else {
				expect(request.method()).toBe('DELETE');
				confirmedTrack = undefined;
			}
			return route.fulfill({ status: 202, json: { saved: true } });
		}
		if (pathname.endsWith('/imports')) {
			return route.fulfill({
				json: {
					more: false,
					offset: 0,
					imports: [
						{
							job_id: 'import-1',
							album: 'Example release',
							artist: 'Example artist',
							imported_at: '2026-11-18T00:00:00Z',
							partial: false,
							folder_path: folderPath,
							sources: [{ playlist: 'Top 5 Colombia', track: 'Chart song', chart_date: '2026-11-16' }],
						},
					],
				},
			});
		}
		if (request.method() === 'POST') {
			expect(request.headers()['x-cineroute-csrf']).toBe('test-csrf');
			expect(request.postDataJSON()).toEqual({ country: 'co', limit: 5, name: '', enabled: true });
			created = true;
			return route.fulfill({ status: 201, json: {} });
		}
		return route.fulfill({
			json: {
				available: true,
				markets: [
					{ code: 'global', label: 'Global' },
					{ code: 'co', label: 'Colombia' },
				],
				playlists: created
					? [
							{
								id: 'colombia',
								settings: { country: 'co', limit: 5, enabled: true },
								display_name: 'Top 5 Colombia',
								chart_date: '2026-11-16',
								published_date: '2026-11-16',
								published: 1,
								ready: 1,
								tracks: [
									{ rank: 1, spotify_id: '1', title: 'Chart song', artists: ['Example artist'], state: 'ready' },
									{
										rank: 2,
										spotify_id: '2',
										title: 'Live version',
										artists: ['Example artist'],
										state: reviewing ? 'needs_review' : 'indexing',
										reason: reviewing ? 'No matching Qobuz track' : '',
										manual_match: !!confirmedTrack,
										qobuz_match: confirmedTrack,
										candidates:
											reviewing && !confirmedTrack
												? [
														{
															track_id: '20',
															title: 'Live version',
															artist: 'Example artist',
															album: 'Example release',
															duration_seconds: 190,
															isrc: 'USAT21704212',
															decision: 'duration differs from chart recording',
														},
													]
												: undefined,
									},
								],
							},
						]
					: [],
			},
		});
	});
	await page.goto('http://admin.test/');
	await navigate(page, 'Music');
	expect(requests.some((request) => request.path === '/session')).toBeTruthy();
	await page.getByRole('button', { name: 'Add chart playlist' }).click();
	await page.getByLabel('Country or Global').selectOption('co');
	await page.getByLabel('Track count').fill('5');
	await page.getByRole('button', { name: 'Save playlist' }).click();
	await expect(page.getByRole('heading', { name: 'Top 5 Colombia', exact: true })).toBeVisible();
	await expect(page.getByText('1 of 5 published')).toBeVisible();
	const status = page.getByRole('region', { name: 'Chart playlists' }).getByRole('status');
	await expect(status).toContainText('Adding tracks');
	await expect(status).toContainText('1 waiting for Navidrome');
	reviewing = true;
	await expect(status).toContainText('Sync complete with tracks needing review', { timeout: 7_000 });
	await expect(page.getByRole('progressbar', { name: 'Tracks processed' })).toHaveAttribute('value', '2');
	await page.getByText('Track status', { exact: true }).click();
	await expect(page.getByText('Needs review · No matching Qobuz track')).toBeVisible();
	await page.getByText('Qobuz candidates (1)', { exact: true }).click();
	await expect(page.getByText('190 sec · USAT21704212 · duration differs from chart recording', { exact: false })).toBeVisible();
	await expect(page.getByRole('link', { name: 'Example release' })).toHaveAttribute('href', trackLink);
	await page.getByRole('button', { name: 'Use Qobuz link', exact: true }).click();
	await page.getByLabel('Qobuz link or album ID').fill('album123');
	await page.getByRole('button', { name: 'Find tracks', exact: true }).click();
	await expect(page.getByRole('button', { name: 'Use this track', exact: true })).toBeDisabled();
	await page.getByRole('combobox', { name: 'Qobuz track', exact: true }).selectOption('20');
	await page.getByRole('button', { name: 'Use this track', exact: true }).click();
	await expect(page.getByText('Qobuz match: Live version', { exact: false })).toBeVisible();
	await page.getByRole('button', { name: 'Edit Qobuz match', exact: true }).click();
	await expect(page.getByLabel('Qobuz link or album ID')).toHaveValue(trackLink);
	await page.getByRole('button', { name: 'Use automatic match', exact: true }).click();
	await page.getByRole('button', { name: 'Use Qobuz link', exact: true }).click();
	await page.getByLabel('Qobuz link or album ID').fill(trackLink);
	await page.getByRole('button', { name: 'Find tracks', exact: true }).click();
	await expect(page.getByRole('combobox', { name: 'Qobuz track' })).toHaveCount(0);
	await page.getByRole('button', { name: 'Use this track', exact: true }).click();
	await expect(page.getByText('Qobuz match: Live version', { exact: false })).toBeVisible();
	await page.getByRole('main').getByRole('button', { name: 'Import history', exact: true }).click();
	await expect(page).toHaveURL(/\/admin\/music\/imports$/);
	await page.reload();
	await expect(page.getByRole('heading', { name: 'Automatic imports' })).toBeVisible();
	const history = page.getByRole('region', { name: 'Import history' });
	await expect(history.getByRole('heading', { name: 'Example release' })).toBeVisible();
	await expect(history.getByText(folder, { exact: false })).toBeVisible();
	await expect(history.getByLabel('Folder path')).toHaveValue(folderPath);
	await expect(history.getByText('Top 5 Colombia · Chart song · 2026-11-16')).toBeVisible();
	const downloaded = page.waitForEvent('download');
	await page.getByRole('link', { name: 'Download CSV' }).click();
	expect((await downloaded).suggestedFilename()).toBe('cineroute-automatic-imports.csv');
});
