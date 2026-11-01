import { test, expect, intake } from './fixtures';

test('route edits keep the card stable during slow requests and reserve submission feedback for submit', async ({ page, requests }) => {
	let loads = 0;
	let current = structuredClone(intake);
	let release: (() => void) | undefined;
	let pending = '';
	let fail = false;
	await page.clock.install();
	await page.route('http://admin.test/api/intakes', (route) => {
		if (route.request().method() !== 'GET') return route.fallback();
		loads++;
		return route.fulfill({ json: { intakes: [current] } });
	});
	await page.route('http://admin.test/api/intakes/intake1/*', async (route) => {
		pending = new URL(route.request().url()).pathname.split('/').at(-1)!;
		const body = route.request().postDataJSON();
		await new Promise<void>((resolve) => (release = resolve));
		release = undefined;
		if (fail) return route.fulfill({ status: 409, json: { error: 'Destination is unavailable' } });
		if (pending === 'destination') {
			current = {
				...current,
				remote: body.remote,
				library: body.library,
				dest: { ...current.dest, remote: body.remote, library: body.library },
			};
		}
		return route.fulfill({ json: {} });
	});
	await page.goto('http://admin.test/');
	const card = page.locator('.intake');
	await expect(card).toBeVisible();
	await page.evaluate(() => document.fonts.ready);
	const input = card.getByRole('textbox', { name: 'Search title', exact: true });
	await input.fill('Keep my title');
	const originalInput = await input.elementHandle();
	const originalPoster = await card.locator('.poster-option').elementHandle();
	const adding = card.getByText('Adding stopped, verifying, starting…', { exact: true });
	const submit = card.getByRole('button', { name: /Add to qBittorrent/ });
	for (const operation of [
		{ kind: 'destination', button: 'Remote' },
		{ kind: 'destination', button: 'Anime' },
		{ kind: 'destination', button: 'Main' },
		{ kind: 'search', button: 'Search TMDB' },
		{ kind: 'type', button: 'TV show' },
		{ kind: 'match', button: null },
	]) {
		pending = '';
		const before = await card.boundingBox();
		if (operation.button) await card.getByRole('button', { name: operation.button, exact: true }).click();
		else await card.locator('.poster-option').click();
		await expect.poll(() => pending).toBe(operation.kind);
		await expect(card).toHaveAttribute('aria-busy', 'true');
		await expect(input).toBeVisible();
		await expect(input).toBeDisabled();
		await expect(submit).toBeDisabled();
		await expect(card.locator('.poster-option')).toBeVisible();
		await expect(card.locator('.poster-option')).toBeDisabled();
		await expect(adding).toHaveCount(0);
		expect(await originalInput!.evaluate((node) => node.isConnected)).toBe(true);
		expect(await originalPoster!.evaluate((node) => node.isConnected)).toBe(true);
		expect(Math.abs((await card.boundingBox())!.height - before!.height)).toBeLessThan(1);
		const reads = loads;
		await page.clock.runFor(3000);
		expect(loads).toBe(reads);
		release!();
		await expect(card).toHaveAttribute('aria-busy', 'false');
		await expect(input).toBeEnabled();
		await expect(input).toHaveValue('Keep my title');
		if (operation.kind === 'destination')
			await expect(card.getByRole('button', { name: operation.button!, exact: true })).toHaveAttribute('aria-pressed', 'true');
	}
	fail = true;
	pending = '';
	await card.getByRole('button', { name: 'Remote', exact: true }).click();
	await expect.poll(() => pending).toBe('destination');
	release!();
	await expect(page.getByRole('alert')).toContainText('Destination is unavailable');
	await expect(input).toBeEnabled();
	await expect(input).toHaveValue('Keep my title');
	await expect(adding).toHaveCount(0);
	fail = false;
	pending = '';
	await submit.click();
	await expect.poll(() => pending).toBe('submit');
	await expect(adding).toBeVisible();
	await expect(input).toBeVisible();
	await expect(input).toBeDisabled();
	const reads = loads;
	await page.clock.runFor(3000);
	await expect.poll(() => loads).toBeGreaterThan(reads);
	release!();
	await expect(card).toHaveAttribute('aria-busy', 'false');
	await expect(adding).toHaveCount(0);
	await expect(input).toBeEnabled();
});

test('destination edits ignore an older background refresh with an intermediate destination', async ({ page, requests }) => {
	let delayRead = false,
		readReturned = false;
	let releaseRead: (() => void) | undefined, releaseEdit: (() => void) | undefined;
	await page.clock.install();
	await page.route('http://admin.test/api/intakes', async (route) => {
		if (route.request().method() !== 'GET' || !delayRead) return route.fallback();
		await new Promise<void>((resolve) => (releaseRead = resolve));
		await route.fulfill({ json: { intakes: [{ ...intake, dest: null }] } });
		readReturned = true;
	});
	await page.route('http://admin.test/api/intakes/intake1/destination', async (route) => {
		await new Promise<void>((resolve) => (releaseEdit = resolve));
		await route.fulfill({ json: {} });
	});
	await page.goto('http://admin.test/');
	const card = page.locator('.intake');
	await expect(card).toBeVisible();
	delayRead = true;
	await page.clock.runFor(3000);
	await expect.poll(() => !!releaseRead).toBeTruthy();
	await card.getByRole('button', { name: 'Remote', exact: true }).click();
	await expect.poll(() => !!releaseEdit).toBeTruthy();
	releaseRead!();
	await expect.poll(() => readReturned).toBeTruthy();
	await page.clock.runFor(100);
	await expect(card.locator('.intake-destination .kv')).toBeVisible();
	await expect(card.getByRole('textbox', { name: 'Search title', exact: true })).toBeVisible();
	await expect(card.getByText('Adding stopped, verifying, starting…', { exact: true })).toHaveCount(0);
	delayRead = false;
	releaseEdit!();
	await expect(card).toHaveAttribute('aria-busy', 'false');
	await expect(card.getByRole('button', { name: 'Remote', exact: true })).toBeEnabled();
});
