import { test, expect, type Page } from '@playwright/test';
import { readFileSync } from 'node:fs';

const runtimeErrors = new WeakMap<Page, string[]>();
test.beforeEach(async ({ page }) => {
    const errors: string[] = [];
    runtimeErrors.set(page, errors);
    page.on('pageerror', (error) => errors.push(error.message));
    page.on('console', (message) => {
        if (message.type() === 'error' && /Content Security Policy|violates.*directive/i.test(message.text())) errors.push(message.text());
    });
});
test.afterEach(async ({ page }) => {
    expect(runtimeErrors.get(page)).toEqual([]);
});

const movie = { id: 101, media_type: 'movie', title: 'A good movie', release_date: '2024-01-01', poster_path: '/movie.jpg', overview: 'A movie description.' };
const tv = { id: 202, media_type: 'tv', name: 'A good show', first_air_date: '2023-01-01', poster_path: '/tv.jpg', overview: 'A show description.' };
const album = {
    id: 'album1',
    title: 'A good album',
    artist: { id: 1, name: 'An artist' },
    image: { large: 'https://static.qobuz.com/album.jpg', small: 'https://static.qobuz.com/album.jpg' },
    genre: { name: 'Rock' },
    released_at: 1704067200,
    tracks_count: 10,
    duration: 2400,
    maximum_bit_depth: 16,
    maximum_sampling_rate: 44.1,
    streamable: true
};
const albumJob = {
    id: 'music1',
    album_id: 'album1',
    title: 'A good album',
    artist: 'An artist',
    year: '2024',
    cover: album.image.large,
    requested_by: 'alice',
    status: 'completed',
    message: 'Added to library',
    created_at: '2024-01-01T00:00:00Z',
    tracks_total: 10,
    tracks_done: 10,
    attempt: 1,
    max_attempts: 2,
    problems: []
};

async function fixtures(page: Page, authenticated = true) {
    let loggedIn = authenticated;
    const created: any[] = [];
    const cancelled: string[] = [];
    const queuedAlbums: string[] = [];
    let currentMusicJob = { ...albumJob };
    const jobs: any[] = [
        {
            id: 'old-request',
            media: { ...movie, id: 303, title: 'An earlier movie' },
            status: 'complete',
            message: 'Ready in the library',
            progress: 1,
            seasons: [],
            created_at: '2024-01-02T00:00:00Z'
        }
    ];
    await page.route(/https:\/\/(static\.qobuz\.com|image\.tmdb\.org)\//, (route) =>
        route.fulfill({
            contentType: 'image/svg+xml',
            body: '<svg xmlns="http://www.w3.org/2000/svg" width="200" height="300"><rect width="200" height="300" fill="#333"/></svg>'
        })
    );
    for (const [url, file] of [
        ['logo.svg', 'logo.svg'],
        ['favicon.svg', 'favicon.svg']
    ]) {
        await page.route(`**/${url}`, (route) => route.fulfill({ contentType: 'image/svg+xml', body: readFileSync(`../internal/web/logo/${file}`) }));
    }
    await page.route('**/api/**', async (route) => {
        const req = route.request(),
            url = new URL(req.url());
        const respond = (data: unknown, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(data) });
        if (url.pathname === '/api/login') {
            loggedIn = true;
            return respond({ account: { name: 'alice', role: 'user' } });
        }
        if (!loggedIn) return respond({ error: 'unauthorized' }, 401);
        if (req.method() !== 'GET') expect(req.headers()['x-cineroute-csrf']).toBe('test-csrf');
        if (url.pathname === '/api/session') return respond({ account: { name: 'alice', role: 'user' }, csrf_token: 'test-csrf' });
        if (url.pathname === '/api/music/info') return respond({ success: true, data: { enabled: true, qobuz_ok: true } });
        if (url.pathname === '/api/music/jobs' && req.method() === 'POST') {
            queuedAlbums.push(req.postDataJSON().album_id);
            currentMusicJob = { ...albumJob, status: 'downloading', tracks_done: 3, message: 'Downloading album' };
            return respond({ success: true, data: currentMusicJob });
        }
        if (url.pathname === '/api/music/jobs') return respond({ success: true, data: { jobs: [currentMusicJob] } });
        if (url.pathname === '/api/music/get-music')
            return respond({
                success: true,
                data: {
                    query: url.searchParams.get('q'),
                    switchTo: null,
                    albums: { items: [album], total: 1 },
                    tracks: { items: [], total: 0 },
                    artists: { items: [], total: 0 }
                }
            });
        if (url.pathname === '/api/catalog/search')
            return respond({ results: [url.searchParams.get('type') === 'tv' ? tv : movie], page: 1, total_pages: 1, similar_matches: true });
        if (url.pathname.startsWith('/api/catalog/'))
            return respond({
                media: url.pathname.includes('/tv/') ? tv : movie,
                seasons: url.pathname.includes('/tv/')
                    ? [
                          { season_number: 1, name: 'Season 1', episode_count: 8, air_date: '2023-01-01' },
                          { season_number: 2, name: 'Season 2', episode_count: 8, air_date: '2099-01-01' }
                      ]
                    : null,
                monitor: 'all',
                auto_approve: true,
                digitally_available: true
            });
        if (url.pathname === '/api/user/requests' && req.method() === 'POST') {
            const body = req.postDataJSON();
            created.push(body);
            const job = {
                id: 'new-request',
                media: body.media_type === 'tv' ? tv : movie,
                status: 'downloading',
                message: 'Downloading your request',
                progress: 0.42,
                seasons: body.seasons || [],
                follow_future: body.follow_future || false,
                prefer_spanish_audio: body.prefer_spanish_audio,
                created_at: '2024-01-03T00:00:00Z'
            };
            const existingIndex = jobs.findIndex((item) => item.id === job.id);
            if (existingIndex >= 0) jobs[existingIndex] = job;
            else jobs.push(job);
            return respond({ request: job }, 201);
        }
        if (url.pathname === '/api/user/requests') return respond({ requests: jobs });
        if (url.pathname.endsWith('/cancel')) {
            cancelled.push(url.pathname);
            return respond({});
        }
        if (url.pathname === '/api/logout') {
            loggedIn = false;
            return respond({});
        }
        throw new Error(`Unexpected API ${req.method()} ${url.pathname}`);
    });
    return { created, cancelled, queuedAlbums };
}

test('clean home with centered media controls and one shared history popup', async ({ page }) => {
    await fixtures(page);
    await page.goto('/');
    await expect(page.getByRole('img', { name: 'CineRoute', exact: true })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'My requests', exact: true })).toHaveCount(0);
    const nav = page.getByRole('navigation');
    await expect(nav.getByRole('button')).toHaveText(['Movies', 'TV', 'Music']);
    await expect(nav.getByRole('button', { name: 'Movies', exact: true })).toHaveAttribute('aria-pressed', 'true');
    await expect(nav.getByRole('button', { name: 'Music', exact: true })).toHaveAttribute('aria-pressed', 'false');
    const box = await nav.boundingBox();
    const search = await page.getByRole('search').boundingBox();
    const viewportHeight = await page.evaluate(() => window.innerHeight);
    expect(Math.abs(search!.y + search!.height / 2 - viewportHeight / 2)).toBeLessThan(65);
    expect(Math.abs(box!.x + box!.width / 2 - search!.x - search!.width / 2)).toBeLessThan(3);
    await expect(page.getByRole('button', { name: 'Movies', exact: true })).toBeVisible();
    await expect(page.getByRole('button', { name: 'TV', exact: true })).toBeVisible();
    await page.screenshot({ path: `/tmp/cineroute-${test.info().project.name}-home.png` });
    await page.getByRole('button', { name: 'Open my requests', exact: true }).click();
    await expect(page.getByRole('heading', { name: 'My requests', exact: true })).toBeVisible();
    await expect(page.getByText('An earlier movie', { exact: true })).toBeVisible();
    await expect(page.getByText('A good album', { exact: true })).toBeVisible();
    await page.screenshot({ path: `/tmp/cineroute-${test.info().project.name}-requests.png` });
    await page.getByRole('button', { name: 'Close', exact: true }).click();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBeTruthy();
});

test('movie requests preserve Spanish preference and appear in shared status bar', async ({ page }) => {
    const f = await fixtures(page);
    await page.goto('/');
    await page.getByRole('button', { name: 'Movies', exact: true }).click();
    await page.getByRole('textbox', { name: 'Search', exact: true }).fill('A good');
    await page.getByRole('button', { name: 'Search', exact: true }).click();
    await expect(page.getByText('Showing similar titles. Select the one you meant.')).toBeVisible();
    const card = page.getByRole('button', { name: 'A good movie 2024', exact: true });
    const poster = await card.locator('img').boundingBox();
    expect(poster!.height / poster!.width).toBeCloseTo(1.5, 1);
    await card.click();
    await expect(page.getByRole('dialog').getByRole('alert')).toHaveCount(0);
    await expect(page.getByRole('note', { name: 'Release availability' })).toHaveCount(0);
    await page.getByRole('checkbox', { name: 'Prefer Spanish audio' }).check();
    await page.getByRole('button', { name: 'Add to library', exact: true }).click();
    await expect.poll(() => f.created.length).toBe(1);
    expect(f.created[0]).toEqual({ media_type: 'movie', tmdb_id: 101, prefer_spanish_audio: true });
    await expect(page.getByRole('status')).toContainText(['Downloading your request']);
    await page.getByRole('button', { name: 'Close', exact: true }).click();
    await expect(page.getByText('Downloading your request', { exact: true })).toBeVisible();
    await expect(page.getByRole('progressbar', { name: 'Download progress' })).toHaveAttribute('aria-valuenow', '42');
    await page.locator('.video-card').first().click();
    await expect(page.getByRole('checkbox', { name: 'Prefer Spanish audio' })).toBeChecked();
    await page.getByRole('checkbox', { name: 'Prefer Spanish audio' }).uncheck();
    await page.getByRole('button', { name: 'Update request', exact: true }).click();
    await expect.poll(() => f.created.length).toBe(2);
    expect(f.created[1].prefer_spanish_audio).toBe(false);
    await page.getByRole('button', { name: 'Close', exact: true }).click();
    await page.getByRole('button', { name: 'Open my requests', exact: true }).click();
    page.once('dialog', (dialog) => dialog.accept());
    await page.getByRole('button', { name: 'Cancel A good movie', exact: true }).click();
    await expect.poll(() => f.cancelled.length).toBe(1);
});

test('TV details send selected seasons and future monitoring', async ({ page }) => {
    const f = await fixtures(page);
    await page.goto('/');
    await page.getByRole('button', { name: 'TV', exact: true }).click();
    await page.getByRole('textbox', { name: 'Search', exact: true }).fill('A show');
    await page.getByRole('button', { name: 'Search', exact: true }).click();
    await page.getByRole('button', { name: 'A good show 2023', exact: true }).click();
    await expect(page.getByRole('checkbox', { name: /Season 1/ })).toBeChecked();
    await expect(page.getByRole('checkbox', { name: /Season 2/ })).not.toBeChecked();
    await expect(page.getByRole('checkbox', { name: 'Follow future seasons', exact: true })).toBeChecked();
    await page.getByRole('checkbox', { name: /Season 2/ }).check();
    await page.getByRole('button', { name: 'Add to library', exact: true }).click();
    await expect.poll(() => f.created.length).toBe(1);
    expect(f.created[0]).toEqual({ media_type: 'tv', tmdb_id: 202, prefer_spanish_audio: false, seasons: [1, 2], follow_future: true });
});

test('music keeps square artwork and movie selection does not query Qobuz', async ({ page }) => {
    await fixtures(page);
    await page.goto('/');
    await page.getByRole('navigation').getByRole('button', { name: 'Music', exact: true }).click();
    await page.getByRole('textbox', { name: 'Search', exact: true }).fill('An artist');
    await page.getByRole('button', { name: 'Search', exact: true }).click();
    await expect(page.getByText('A good album', { exact: true }).first()).toBeVisible();
    const cover = await page.locator('.release-card .aspect-square').first().boundingBox();
    expect(cover!.height / cover!.width).toBeCloseTo(1, 1);
    await page.getByRole('button', { name: 'Movies', exact: true }).click();
    await expect(page.getByRole('button', { name: 'A good movie 2024', exact: true })).toBeVisible();
});

test('login uses CineRoute user session and returns to search', async ({ page }) => {
    await fixtures(page, false);
    await page.goto('/login');
    await page.getByLabel('Username', { exact: true }).fill('alice');
    await page.getByLabel('Password', { exact: true }).fill('secret');
    await page.getByRole('button', { name: 'Sign in', exact: true }).click();
    await expect(page).toHaveURL(/\/$/);
    await expect(page.getByRole('button', { name: 'Movies', exact: true })).toBeVisible();
});

test('album requests use the same login, CSRF token, and progress bar', async ({ page }) => {
    const f = await fixtures(page);
    await page.goto('/');
    await page.getByRole('navigation').getByRole('button', { name: 'Music', exact: true }).click();
    await page.getByRole('textbox', { name: 'Search', exact: true }).fill('An artist');
    await page.getByRole('button', { name: 'Search', exact: true }).click();
    const card = page.locator('.release-card').first();
    // Touch devices expose the actions on tap; desktop exposes them on hover.
    if (test.info().project.name === 'mobile') await card.locator('.release-overlay').tap();
    else await card.hover();
    await page.getByRole('button', { name: 'Download full release: A good album', exact: true }).click();
    await expect.poll(() => f.queuedAlbums).toEqual(['album1']);
    await expect(page.getByRole('progressbar', { name: 'Download progress' })).toHaveAttribute('aria-valuenow', '30');
});

test('TV titles without announced seasons can still follow future seasons', async ({ page }) => {
    const f = await fixtures(page);
    await page.route('**/api/catalog/tv/202', (route) =>
        route.fulfill({ contentType: 'application/json', body: JSON.stringify({ media: tv, seasons: null, monitor: 'all', auto_approve: true }) })
    );
    await page.goto('/');
    await page.getByRole('button', { name: 'TV', exact: true }).click();
    await page.getByRole('textbox', { name: 'Search', exact: true }).fill('A show');
    await page.getByRole('button', { name: 'Search', exact: true }).click();
    await page.getByRole('button', { name: 'A good show 2023', exact: true }).click();
    await expect(page.getByText('No seasons announced yet.', { exact: true })).toBeVisible();
    await expect(page.getByRole('dialog').getByRole('alert')).toHaveCount(0);
    await page.getByRole('button', { name: 'Add to library', exact: true }).click();
    await expect.poll(() => f.created.length).toBe(1);
    expect(f.created[0]).toMatchObject({ media_type: 'tv', seasons: [], follow_future: true });
});

test('Music stays a media tab while albums, tracks, and artists use its filter', async ({ page }) => {
    await fixtures(page);
    await page.goto('/');
    const navigation = page.getByRole('navigation');
    const musicTab = navigation.getByRole('button', { name: 'Music', exact: true });
    await expect(musicTab).toHaveAttribute('aria-pressed', 'false');
    await musicTab.click();
    await expect(musicTab).toHaveAttribute('aria-pressed', 'true');
    const musicBox = await musicTab.boundingBox();
    const filterBox = await page.getByRole('button', { name: 'Music · Albums', exact: true }).boundingBox();
    expect(filterBox!.x).toBeGreaterThanOrEqual(musicBox!.x + musicBox!.width);
    expect(Math.abs(filterBox!.y + filterBox!.height / 2 - musicBox!.y - musicBox!.height / 2)).toBeLessThan(2);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBeTruthy();
    await expect(navigation.getByRole('button', { name: 'Albums', exact: true })).toHaveCount(0);
    await page.getByRole('button', { name: 'Music · Albums', exact: true }).click();
    await expect(page.getByRole('menuitemradio', { name: 'Albums', exact: true })).toHaveAttribute('aria-checked', 'true');
    await page.getByRole('menuitemradio', { name: 'Tracks', exact: true }).click();
    await expect(page.getByRole('button', { name: 'Music · Tracks', exact: true })).toBeVisible();
    await page.getByRole('button', { name: 'Music · Tracks', exact: true }).click();
    await page.getByRole('menuitemradio', { name: 'Artists', exact: true }).click();
    await expect(page.getByRole('button', { name: 'Music · Artists', exact: true })).toBeVisible();
    await navigation.getByRole('button', { name: 'Movies', exact: true }).click();
    await expect(page.getByRole('button', { name: 'Music · Artists', exact: true })).toHaveCount(0);
    await musicTab.click();
    await expect(page.getByRole('button', { name: 'Music · Artists', exact: true })).toBeVisible();
    await expect(musicTab).toHaveAttribute('aria-pressed', 'true');
    await page.getByRole('button', { name: 'Music · Artists', exact: true }).click();
    await page.getByRole('menuitemradio', { name: 'Albums', exact: true }).click();
    await page.getByRole('textbox', { name: 'Search', exact: true }).fill('An artist');
    await page.getByRole('button', { name: 'Search', exact: true }).click();
    await expect(page.getByText('A good album', { exact: true }).first()).toBeVisible();
    await page.getByRole('button', { name: 'Music · Albums', exact: true }).click();
    await page.getByRole('menuitemradio', { name: 'Tracks', exact: true }).click();
    await expect(page.locator('.release-card')).toHaveCount(0);
    await expect(page.getByText('No matching titles. Try another name or year.', { exact: true })).toBeVisible();
});

test('media buttons keep their size and position when selection changes', async ({ page }) => {
    await fixtures(page);
    await page.goto('/');
    const navigation = page.getByRole('navigation');
    const names = ['Movies', 'TV', 'Music'];
    const initial = await Promise.all(names.map((name) => navigation.getByRole('button', { name, exact: true }).boundingBox()));
    for (const selected of ['TV', 'Music', 'Movies']) {
        await navigation.getByRole('button', { name: selected, exact: true }).click();
        await expect(navigation.getByRole('button', { name: selected, exact: true })).toHaveAttribute('aria-pressed', 'true');
        for (const [index, name] of names.entries()) {
            const box = await navigation.getByRole('button', { name, exact: true }).boundingBox();
            for (const dimension of ['x', 'y', 'width', 'height'] as const) expect(Math.abs(box![dimension] - initial[index]![dimension])).toBeLessThan(0.5);
        }
    }
});

for (const kind of ['movie', 'tv'] as const) {
    test(`${kind} requests show an advisory warning without blocking submission`, async ({ page }) => {
        const f = await fixtures(page);
        const media = kind === 'movie' ? movie : tv;
        await page.route(`**/api/catalog/${kind}/${media.id}`, (route) =>
            route.fulfill({
                contentType: 'application/json',
                body: JSON.stringify({ media, seasons: null, monitor: 'all', auto_approve: true, digitally_available: false })
            })
        );
        await page.goto('/');
        await page
            .getByRole('navigation')
            .getByRole('button', { name: kind === 'movie' ? 'Movies' : 'TV', exact: true })
            .click();
        await page.getByRole('textbox', { name: 'Search', exact: true }).fill('A good');
        await page.getByRole('button', { name: 'Search', exact: true }).click();
        await page.locator('.video-card').first().click();
        const warning = page.getByRole('note', { name: 'Release availability' });
        await expect(warning).toBeVisible();
        await expect(warning).toContainText('You can still request it.');
        await expect(warning).toContainText(kind === 'movie' ? 'may not have a digital release yet' : 'may not have aired yet');
        const request = page.getByRole('button', { name: 'Add to library', exact: true });
        await expect(request).toBeEnabled();
        await request.click();
        await expect.poll(() => f.created.length).toBe(1);
        expect(f.created[0].media_type).toBe(kind);
    });
}

test('an empty final search page does not claim more released titles can be loaded', async ({ page }) => {
    await fixtures(page);
    await page.route('**/api/catalog/search?**', (route) =>
        route.fulfill({ contentType: 'application/json', body: JSON.stringify({ results: [], page: 1, total_pages: 1 }) })
    );
    await page.goto('/');
    await page.getByRole('textbox', { name: 'Search', exact: true }).fill('No matching movie');
    await page.getByRole('button', { name: 'Search', exact: true }).click();
    await expect(page.getByText('No matching titles. Try another name or year.', { exact: true })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Load more', exact: true })).toHaveCount(0);
    await expect(page.getByText(/Load more to continue/)).toHaveCount(0);
});
