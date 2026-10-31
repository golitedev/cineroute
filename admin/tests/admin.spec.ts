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
	await page.getByRole('button', { name: 'Open', exact: true }).click();
	await expect(page.locator('.detail')).toContainText(movie.folder_name);
	await page.getByRole('searchbox', { name: 'Filter library titles' }).fill('absent');
	await expect(page.locator('.library-row')).toHaveCount(0);
	await page.getByRole('button', { name: 'Clear', exact: true }).click();
	await page.getByRole('button', { name: 'Scan library', exact: true }).click();
	await expect.poll(() => requests.some((r) => r.path === '/copies/scan' && r.csrf === 'test-csrf')).toBeTruthy();
	await navigate(page, 'TV shows');
	await expect(page.locator('.library-row')).toContainText('main 3.00 GiB');
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
	await navigate(page, 'Activity Center');
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
	await page.getByRole('button', { name: /Add to qBittorrent/ }).click();
	await expect.poll(() => requests.some((r) => r.path === '/intakes/intake1/submit')).toBeTruthy();
	await navigate(page, 'Hardlinks');
	await page.getByRole('combobox').selectOption('remote_to_main');
	await page.getByRole('button', { name: 'Relink', exact: true }).click();
	await expect
		.poll(() => requests.some((r) => r.path === '/hardlinks/link1/relink' && JSON.stringify(r.body) === '{"direction":"remote_to_main"}'))
		.toBeTruthy();
	await page.getByRole('button', { name: 'Remove', exact: true }).click();
	await expect.poll(() => requests.some((r) => r.path === '/hardlinks/link1' && r.method === 'DELETE')).toBeTruthy();
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
	for (const name of ['Movies', 'TV shows', 'Hardlinks', 'Subtitles', 'Request settings', 'Request queue', 'Activity Center']) {
		await navigate(page, name);
		await expect(page.getByRole('heading', { name, exact: true })).toBeVisible();
		await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBeTruthy();
	}
	if (info.project.name === 'desktop') {
		await page.keyboard.press('Control+b');
		await expect(page.locator('.sidebar-container')).toHaveAttribute('data-collapsible', 'icon');
		await page.locator('.workspace-bar').hover();
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
	await page.locator('.workspace-bar').hover();
	await expect(sidebar).toHaveAttribute('data-collapsible', 'icon');
	await page.setViewportSize({ width: 1440, height: 1000 });
	await expect(sidebar).not.toHaveAttribute('data-collapsible', 'icon');
	await page.getByRole('button', { name: 'Unpin sidebar', exact: true }).click();
	await page.locator('.workspace-bar').hover();
	await expect(sidebar).toHaveAttribute('data-collapsible', 'icon');
	await page.reload();
	await expect(sidebar).toHaveAttribute('data-collapsible', 'icon');
	await sidebar.hover();
	await expect(sidebar).not.toHaveAttribute('data-collapsible', 'icon');
	await page.getByRole('button', { name: 'Pin sidebar', exact: true }).click();
	await page.locator('.workspace-bar').hover();
	await expect(sidebar).not.toHaveAttribute('data-collapsible', 'icon');
});
