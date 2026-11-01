export type View = 'route' | 'movies' | 'tv' | 'anime' | 'hardlinks' | 'subtitles' | 'settings' | 'queue' | 'history';
export interface Account {
	name: string;
	role: string;
}
export interface Session {
	account: Account;
	csrf_token: string;
}
export interface Media {
	id: number;
	title?: string;
	name?: string;
	poster_path?: string;
	release_date?: string;
	first_air_date?: string;
	media_type?: string;
}
export interface Copy {
	id: string;
	drive_id: string;
	folder_name: string;
	path: string;
	remote_path: string;
	status: string;
	existing_files: string[];
	existing_file_sizes: Record<string, number>;
	remote_files: string[];
	remote_file_sizes: Record<string, number>;
}
export interface CopyView {
	items: Copy[];
	scanned_at: string;
	state_error?: string;
}
export interface Hardlink {
	id: string;
	media_type: string;
	drive_id: string;
	source_path: string;
	remote_path: string;
	source_folder: string;
	remote_folder: string;
	status: string;
	status_reason: string;
	can_relink: boolean;
	can_remove: boolean;
	source_exists: boolean;
	source_root: string;
	remote_root: string;
	linked_files: number;
	source_file_count: number;
	remote_file_count: number;
	extra_file_count: number;
	linked_bytes: number;
	files?: { relative_path: string; source_path: string; size: number; linked: boolean }[];
}
export interface HardlinkView {
	items: Hardlink[];
	stats: Record<string, number>;
	scanned_at: string;
	warnings: string[];
}
export interface Option {
	id: string;
	label: string;
}
export interface Tracker {
	id: number;
	name: string;
	priority: number;
}
export interface Indexer {
	id: number;
	name: string;
	protocol: string;
	enable: boolean;
}
export interface Profile {
	destination: string;
	new_title_drive: string;
	qualities: string[];
	codecs: string[];
	audio_languages: string[];
	preferred_audio_languages: string[];
	preferred_subtitle_languages: string[];
	prefer_text_subtitles: boolean;
	allow_unknown_language: boolean;
	min_size_gib: number;
	max_size_gib: number;
	max_pack_size_gib: number;
	upgrade_allowed: boolean;
	upgrade_until: string;
	minimum_score: number;
	upgrade_until_score: number;
	keywords: { pattern: string; match: string; mode: string; score: number }[];
}
export interface Settings {
	version: number;
	allow_movies: boolean;
	allow_tv: boolean;
	auto_approve: boolean;
	movie_limit: number;
	tv_limit: number;
	limit_days: number;
	search_on_approval: boolean;
	search_interval_seconds: number;
	retry_interval_hours: number;
	retry_failed_downloads: boolean;
	stalled_minutes: number;
	max_concurrent_downloads: number;
	release_delay_minutes: number;
	min_seeders: number;
	movie_availability: string;
	movie_auto_subtitles: boolean;
	include_specials: boolean;
	prefer_season_packs: boolean;
	selection_order: string[];
	spanish_trackers: Tracker[];
	trackers: Tracker[];
	movie: Profile;
	tv: Profile;
}
export interface SettingsView {
	settings: Settings;
	qualities: Option[];
	codecs: Option[];
	languages: Option[];
	selection_criteria: Option[];
	drives: { ID: string; MovieRoot: string; MovieRemoteRoot: string; TVRoot: string; TVRemoteRoot: string }[];
	prowlarr_configured: boolean;
	engine_available: boolean;
}
export interface Job {
	id: string;
	media: Media;
	status: string;
	message: string;
	progress: number;
	created_at: string;
	seasons?: number[];
}
export interface RequestRow {
	request: Job;
	owner: string;
	destination: string;
	status?: string;
	diagnostic?: string;
	next_search?: string;
	downloads?: Download[];
}
export interface Download {
	title: string;
	tracker: string;
	tracker_url: string;
	requested_by: string;
	status: string;
	progress: number;
	drive: string;
	quality: string;
	size: number;
	episodes?: string[];
	added_at?: string;
	completed_at?: string;
	failed_at?: string;
	last_progress?: string;
	observed_at?: string;
	torrent_state?: string;
	error?: string;
	language_score: number;
	subtitle_format_score: number;
	languages?: {
		status: string;
		audio?: string[];
		subtitles?: string[];
		forced_subtitles?: string[];
		text_subtitles?: string[];
		image_subtitles?: string[];
		has_text_subtitles?: boolean;
		has_image_subtitles?: boolean;
		video_bitrate_bps?: number;
	};
}
export interface RequestHistory {
	entries: RequestRow[];
	page: number;
	total_pages: number;
	total: number;
	counts: Record<string, number>;
	as_of: string;
}
export interface Destination {
	library: string;
	remote: boolean;
	folder_name: string;
	drive_id: string;
	existing: boolean;
	existing_at: string;
	save_path: string;
	content_path: string;
	root_folder: boolean;
	needed_bytes: number;
	usable_space: number;
	warnings?: string[];
	existing_conflict: boolean;
	existing_paths: string[];
}
export interface Intake {
	id: string;
	group: string;
	created_at: string;
	filename: string;
	torrent_name: string;
	size: number;
	kind: string;
	root_folder: boolean;
	files: number;
	info_hash_v1: string;
	info_hash_v2: string;
	media_type: string;
	library: string;
	remote: boolean;
	title: string;
	year: number;
	season: number;
	confidence: string;
	tmdb: Media[];
	tmdb_error: string;
	match: Media | null;
	dest: Destination | null;
	status: string;
	error: string;
	result?: { torrent_name: string; save_path: string; content_path: string; drive_id: string; hash: string; submitted_at: string };
}
export interface Metrics {
	within_2s?: number;
	p90?: number;
	score?: number;
	start_gap_minutes?: number;
	end_gap_minutes?: number;
	overrun_minutes?: number;
}
export interface Subtitle {
	id: string;
	drive_id: string;
	folder_name: string;
	video_path: string;
	video_name: string;
	title: string;
	year: number;
	status: string;
	probed: boolean;
	has_external_subtitle: boolean;
	reference_kind?: string;
	reference_lang?: string;
	reference_stream?: number;
	step?: string;
	step_detail?: string;
	updated_at?: string;
	error?: string;
	work_dir?: string;
	targets?: {
		language: string;
		status: string;
		present?: boolean;
		sources?: string[];
		error?: string;
		installed_path?: string;
		variant?: string;
		metrics?: Metrics;
	}[];
	external_subtitles?: { file_name: string; language: string; usable: boolean }[];
	embedded_sub_streams?: { index: number; codec: string; language: string; usable: boolean }[];
}
export interface SubtitleView {
	enabled: boolean;
	work_dir: string;
	target_languages: string[];
	reference_languages: string[];
	settings: {
		run_batch_size: number;
		request_interval_ms: number;
		max_candidates: number;
		alass_split_penalty: number;
		quota_reserve: number;
	};
	stats: Record<string, number>;
	items: Subtitle[];
	batch: {
		running: boolean;
		kind: string;
		total: number;
		done: number;
		stage?: string;
		current?: string;
		error?: string;
		canceled?: boolean;
	};
	quota: { known: boolean; remaining: number; reset_at?: string };
	state_error?: string;
	work_dir_error?: string;
	feature_error?: string;
	open_item?: Subtitle;
	candidates?: {
		rank: number;
		language?: string;
		variant?: string;
		score: number;
		category: string;
		safe: boolean;
		release: string;
		reasons?: string[];
	}[];
	attempts?: { at: string; status: string; language?: string; file_id: number; release?: string; detail?: string; metrics?: Metrics }[];
}
