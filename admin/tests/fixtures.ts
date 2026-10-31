import { test as base, expect } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';
const profile = () => ({
	destination: 'remote',
	new_title_drive: '',
	qualities: ['webdl-1080p', 'bluray-1080p', 'webrip-1080p'],
	codecs: ['h264', 'hevc'],
	audio_languages: [],
	preferred_audio_languages: [],
	preferred_subtitle_languages: [],
	prefer_text_subtitles: false,
	allow_unknown_language: false,
	min_size_gib: 0,
	max_size_gib: 20,
	max_pack_size_gib: 80,
	upgrade_allowed: false,
	upgrade_until: 'webdl-1080p',
	minimum_score: 0,
	upgrade_until_score: 0,
	keywords: [],
});
export const settings = {
	version: 1,
	allow_movies: true,
	allow_tv: true,
	auto_approve: true,
	movie_limit: 0,
	tv_limit: 0,
	limit_days: 7,
	search_on_approval: true,
	search_interval_seconds: 10,
	retry_interval_hours: 6,
	retry_failed_downloads: true,
	stalled_minutes: 120,
	max_concurrent_downloads: 3,
	release_delay_minutes: 0,
	min_seeders: 1,
	movie_availability: 'released',
	movie_auto_subtitles: false,
	include_specials: false,
	prefer_season_packs: true,
	selection_order: ['quality', 'language', 'text_subtitles', 'keywords', 'pack', 'priority', 'video_bitrate', 'seeders'],
	spanish_trackers: [],
	trackers: [],
	movie: profile(),
	tv: profile(),
};
export const settingsView = {
	settings,
	qualities: [
		{ id: 'webdl-1080p', label: 'WEB-DL 1080p' },
		{ id: 'bluray-1080p', label: 'BluRay 1080p' },
		{ id: 'webrip-1080p', label: 'WEBRip 1080p' },
		{ id: 'remux-2160p', label: 'REMUX 2160p' },
	],
	codecs: [
		{ id: 'h264', label: 'H.264 / x264' },
		{ id: 'hevc', label: 'HEVC / x265' },
	],
	languages: [
		{ id: 'en', label: 'English' },
		{ id: 'es', label: 'Spanish' },
		{ id: 'sv', label: 'Swedish' },
	],
	selection_criteria: settings.selection_order.map((id, i) => ({
		id,
		label: [
			'Quality rank',
			'Language matches',
			'Text subtitle format',
			'Keyword score',
			'Pack preference',
			'Tracker priority',
			'Video bitrate',
			'Seeders',
		][i],
	})),
	drives: [
		{
			ID: 'disk1',
			MovieRoot: '/disk1/media/movies',
			MovieRemoteRoot: '/disk1/media/movies-remote',
			TVRoot: '/disk1/media/tv',
			TVRemoteRoot: '/disk1/media/tv-remote',
		},
	],
	prowlarr_configured: true,
	engine_available: true,
};
export const movie = {
	id: 'copy1',
	drive_id: 'disk1',
	folder_name: '1917 (2019)',
	path: '/disk1/media/movies/1917 (2019)',
	remote_path: '/disk1/media/movies-remote/1917 (2019)',
	status: 'both',
	existing_files: ['movie.mkv'],
	existing_file_sizes: { 'movie.mkv': 68.93 * 1024 ** 3 },
	remote_files: ['remote.mkv'],
	remote_file_sizes: { 'remote.mkv': 6.07 * 1024 ** 3 },
};
export const show = {
	...movie,
	id: 'show1',
	folder_name: 'Example Show (2024)',
	existing_files: ['episode1.mkv', 'episode2.mkv'],
	existing_file_sizes: { 'episode1.mkv': 1024 ** 3, 'episode2.mkv': 2 * 1024 ** 3 },
};
export const subtitle = {
	id: 'sub1',
	drive_id: 'disk1',
	folder_name: 'Hugo (2011)',
	video_path: '/disk1/media/movies-remote/Hugo (2011)/Hugo.mkv',
	title: 'Hugo',
	year: 2011,
	status: 'pending',
	probed: true,
	has_external_subtitle: false,
	reference_kind: 'embedded',
	reference_lang: 'en',
	reference_stream: 2,
	step_detail: '42% · 0:50:00 of 2:00:00',
	targets: [
		{ language: 'sv', status: 'pending' },
		{ language: 'es', status: 'present', present: true, sources: ['embedded stream #2'] },
		{ language: 'en', status: 'no_match', error: 'no safe candidate was found' },
	],
};
export const subtitleView = {
	enabled: true,
	work_dir: '/data/work',
	target_languages: ['sv', 'es', 'en'],
	reference_languages: ['en'],
	settings: { run_batch_size: 20, request_interval_ms: 400, max_candidates: 5, alass_split_penalty: 7, quota_reserve: 5 },
	stats: { total: 729, pending: 94, added: 1 },
	items: [subtitle],
	batch: { running: false, kind: 'run', total: 0, done: 0 },
	quota: { known: false, remaining: 0 },
	open_item: subtitle,
	attempts: [
		{
			at: '2026-01-01T00:00:00Z',
			status: 'rejected_timing',
			file_id: 7,
			release: 'Hugo.WEB-DL',
			metrics: { within_2s: 0.5, p90: 4.2, score: 10 },
		},
	],
	candidates: [
		{ rank: 1, language: 'sv', score: 420, category: 'strong', safe: true, release: 'Hugo.2011.WEB-DL', reasons: ['year match'] },
	],
};
const media = { id: 101, title: 'Example <Movie>', media_type: 'movie', release_date: '2024-01-01' };
export const job = {
	id: 'request1',
	media,
	status: 'pending',
	message: 'Waiting for approval',
	progress: 0.4,
	created_at: '2026-01-01T12:00:00Z',
};
export const history = {
	entries: [
		{
			request: { ...job, status: 'downloading' },
			owner: 'alice <admin>',
			destination: 'remote',
			status: 'stalled',
			diagnostic: 'No peers <yet>',
			downloads: [
				{
					title: 'Release <unsafe>',
					tracker: 'Tracker',
					tracker_url: 'https://tracker.example/torrents/42',
					requested_by: 'alice',
					status: 'stalled',
					progress: 0.4,
					drive: 'disk2',
					size: 1024 ** 3,
					torrent_state: 'stalledDL',
					languages: { status: 'reported', audio: ['es'], subtitles: ['sv'], text_subtitles: ['sv'], video_bitrate_bps: 9096000 },
					language_score: 2,
					subtitle_format_score: 1,
				},
			],
		},
	],
	page: 1,
	total_pages: 2,
	total: 51,
	counts: { stalled: 1, complete: 50 },
	as_of: '2026-01-01T14:00:00Z',
};
export const intake = {
	id: 'intake1',
	group: 'movie1',
	filename: 'example.torrent',
	torrent_name: 'Example.Movie.2024',
	created_at: '2026-01-01T00:00:00Z',
	size: 1024 ** 3,
	kind: 'single',
	root_folder: false,
	files: 1,
	info_hash_v1: 'abc123',
	media_type: 'movie',
	library: 'movie',
	remote: false,
	title: 'Example Movie',
	year: 2024,
	season: 0,
	confidence: 'high',
	tmdb: [{ ...media, title: 'Example Movie' }],
	match: { ...media, title: 'Example Movie' },
	dest: {
		library: 'movie',
		remote: false,
		folder_name: 'Example Movie (2024)',
		drive_id: 'disk1',
		existing: false,
		save_path: '/disk1/media/movies/Example Movie (2024)',
		content_path: '/disk1/media/movies/Example Movie (2024)/movie.mkv',
		root_folder: false,
		needed_bytes: 1024 ** 3,
		usable_space: 1024 ** 4,
		existing_conflict: false,
		existing_paths: [],
	},
	status: 'matched',
	error: '',
};
export const hardlink = {
	id: 'link1',
	media_type: 'movie',
	drive_id: 'disk1',
	source_path: '/main/Movie',
	remote_path: '/remote/Movie',
	source_folder: 'Movie',
	remote_folder: 'Movie',
	status: 'needs_relink',
	status_reason: 'Name changed',
	can_relink: true,
	can_remove: true,
	source_exists: true,
	linked_files: 1,
	source_file_count: 1,
	remote_file_count: 1,
	extra_file_count: 0,
	linked_bytes: 1024 ** 3,
	files: [{ relative_path: 'movie.mkv', source_path: '/main/Movie/movie.mkv', size: 1024 ** 3, linked: true }],
};
export type RequestRecord = { path: string; method: string; body: unknown; csrf: string | undefined };
export const test = base.extend<{ requests: RequestRecord[] }>({
	requests: async ({ page }, use) => {
		const records: RequestRecord[] = [];
		await page.route('http://admin.test/**', async (route) => {
			const req = route.request(),
				url = new URL(req.url());
			if (url.pathname.startsWith('/api/')) {
				const name = url.pathname.slice(4);
				let body: unknown;
				try {
					body = req.postDataJSON();
				} catch {
					body = req.postData();
				}
				records.push({ path: name, method: req.method(), body, csrf: req.headers()['x-cineroute-csrf'] });
				let data: unknown = {};
				switch (name) {
					case '/session':
						data = { account: { name: 'sueco', role: 'admin' }, csrf_token: 'test-csrf' };
						break;
					case '/status':
						data = { qbittorrent: 'ok', tmdb: 'configured', prowlarr: 'configured', storage: { healthy: true, free: 9 * 1024 ** 4 } };
						break;
					case '/intakes':
						data = { intakes: [intake] };
						break;
					case '/history':
						data = { history: [] };
						break;
					case '/copies':
					case '/copies/scan':
						data = { items: url.searchParams.get('type') === 'tv' ? [show] : [movie], scanned_at: '2026-01-01T00:00:00Z' };
						break;
					case '/hardlinks':
					case '/hardlinks/scan':
						data = {
							items: [hardlink],
							stats: { total: 1, healthy: 0, needs_relink: 1, linked_files: 1, linked_bytes: 1024 ** 3 },
							warnings: [],
							scanned_at: '2026-01-01T00:00:00Z',
						};
						break;
					case '/requests/settings':
						data = req.method() === 'PUT' ? { ...settingsView, settings: body } : settingsView;
						break;
					case '/requests/indexers':
						data = {
							indexers: [
								{ id: 3, name: 'Tracker', enable: true, protocol: 'torrent' },
								{ id: 4, name: 'Usenet', enable: true, protocol: 'usenet' },
							],
						};
						break;
					case '/requests/queue':
						data = { requests: [{ request: job, owner: 'alice', destination: 'remote' }] };
						break;
					case '/requests/history':
						data = { ...history, page: Number(url.searchParams.get('page') || 1) };
						break;
					case '/subtitles':
					case '/subtitles/scan':
					case '/subtitles/settings':
						data = { ...subtitleView, open_item: url.searchParams.has('open') ? subtitle : undefined };
						break;
					default:
						if (name.includes('/relink') || name.startsWith('/hardlinks/'))
							data = { view: { items: [{ ...hardlink, status: 'healthy' }], stats: { total: 1, healthy: 1 }, warnings: [] } };
				}
				return route.fulfill({ json: data });
			}
			const root = path.resolve('../internal/adminui/dist');
			const name = url.pathname === '/' ? 'index.html' : url.pathname.replace(/^\/admin\//, '');
			const file = path.resolve(root, name);
			if (!file.startsWith(root + path.sep) || !fs.existsSync(file) || !fs.statSync(file).isFile()) return route.fulfill({ status: 404 });
			return route.fulfill({
				body: fs.readFileSync(file),
				contentType: file.endsWith('.js')
					? 'text/javascript'
					: file.endsWith('.css')
						? 'text/css'
						: file.endsWith('.woff2')
							? 'font/woff2'
							: 'text/html',
				headers: {
					'Content-Security-Policy':
						"default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; font-src 'self'; img-src 'self' https://image.tmdb.org; connect-src 'self'",
				},
			});
		});
		await use(records);
	},
});
export { expect };
export async function navigate(page: import('@playwright/test').Page, name: string) {
	const mobile = await page.getByRole('button', { name: 'Expand sidebar', exact: true }).isVisible();
	if (mobile && !(await page.getByRole('button', { name, exact: true }).isVisible()))
		await page.getByRole('button', { name: 'Expand sidebar', exact: true }).click();
	await page.getByRole('button', { name, exact: true }).click();
}
