package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen         string      `yaml:"listen"`
	AuthUsername   string      `yaml:"auth_username"`
	AuthPassword   string      `yaml:"auth_password"`
	MaxUploadBytes int64       `yaml:"max_upload_bytes"`
	TMDB           TMDB        `yaml:"tmdb"`
	QBittorrent    QBittorrent `yaml:"qbittorrent"`
	Prowlarr       Prowlarr    `yaml:"prowlarr"`
	Requests       Requests    `yaml:"requests"`
	Subtitles      Subtitles   `yaml:"subtitles"`
	Library        Library     `yaml:"library"`
	Drives         []Drive     `yaml:"drives"`
}

type TMDB struct {
	APIKey   string `yaml:"api_key"`
	Language string `yaml:"language"`
}

type QBittorrent struct {
	URL      string `yaml:"url"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type Prowlarr struct {
	URL    string `yaml:"url"`
	APIKey string `yaml:"api_key"`
}

type Requests struct {
	StatePath string `yaml:"state_path"`
}

type Library struct {
	FolderFormat string `yaml:"folder_format"`
}

// Subtitles configures the Swedish external-subtitle workflow for the remote
// movie libraries.
type Subtitles struct {
	Enabled           bool   `yaml:"enabled"`
	StatePath         string `yaml:"state_path"`
	WorkDir           string `yaml:"work_dir"`
	WorkRetentionDays int    `yaml:"work_retention_days"`
	// TargetLanguages are the subtitle languages to add, in order. A movie is
	// only complete once every one of them has a subtitle; a language
	// OpenSubtitles has nothing for is reported per language and does not stop
	// the others.
	TargetLanguages []string `yaml:"target_languages"`
	// TargetLanguage is the legacy single-language setting. It is used as the
	// whole list when target_languages is absent.
	TargetLanguage         string            `yaml:"target_language"`
	ReferenceLanguages     []string          `yaml:"reference_languages"`
	FFmpegPath             string            `yaml:"ffmpeg_path"`
	FFprobePath            string            `yaml:"ffprobe_path"`
	AlassPath              string            `yaml:"alass_path"`
	ScanBatchSize          int               `yaml:"scan_batch_size"`
	RunBatchSize           int               `yaml:"run_batch_size"`
	RequestIntervalMS      int               `yaml:"request_interval_ms"`
	QuotaReserve           int               `yaml:"quota_reserve"`
	MaxCandidates          int               `yaml:"max_candidates"`
	UseMoviehash           bool              `yaml:"use_moviehash"`
	RemovePromoCues        bool              `yaml:"remove_promo_cues"`
	AllowUnalignedFallback bool              `yaml:"allow_unaligned_fallback"`
	MinReferenceCues       int               `yaml:"min_reference_cues"`
	MinVideoBytes          int64             `yaml:"min_video_bytes"`
	SkipSampleFiles        bool              `yaml:"skip_sample_files"`
	ProbeTimeoutSeconds    int               `yaml:"probe_timeout_seconds"`
	ExtractTimeoutSeconds  int               `yaml:"extract_timeout_seconds"`
	SyncTimeoutSeconds     int               `yaml:"sync_timeout_seconds"`
	AlassNoSplit           bool              `yaml:"alass_no_split"`
	AlassSplitPenalty      int               `yaml:"alass_split_penalty"`
	OverwriteExisting      bool              `yaml:"overwrite_existing"`
	Accept                 SubtitleAccept    `yaml:"accept"`
	TitleOverrides         map[string]string `yaml:"title_overrides"`
	OpenSubtitles          OpenSubtitles     `yaml:"opensubtitles"`
}

// SubtitleAccept is the timing gate a synchronized subtitle must pass before it
// is written next to a movie. The defaults are the thresholds from the proven
// manual pipeline.
type SubtitleAccept struct {
	MinWithin2        float64 `yaml:"min_within_2s"`
	MaxP90Seconds     float64 `yaml:"max_p90_nearest_seconds"`
	MaxStartGapMin    float64 `yaml:"max_start_gap_minutes"`
	MaxEndGapMin      float64 `yaml:"max_end_gap_minutes"`
	MaxZeroStartCues  int     `yaml:"max_zero_start_cues"`
	MaxCueMinutes     float64 `yaml:"max_cue_minutes"`
	MaxOverrunMinutes float64 `yaml:"max_overrun_minutes"`
}

type OpenSubtitles struct {
	BaseURL  string `yaml:"base_url"`
	APIKey   string `yaml:"api_key"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type Drive struct {
	ID              string `yaml:"id"`
	MovieRoot       string `yaml:"movie_root"`
	MovieRemoteRoot string `yaml:"movie_remote_root"`
	TVRoot          string `yaml:"tv_root"`
	TVRemoteRoot    string `yaml:"tv_remote_root"`
	AnimeRoot       string `yaml:"anime_root"`
}

func Default() *Config {
	return &Config{
		Listen:         "127.0.0.1:8787",
		AuthUsername:   "cineroute",
		MaxUploadBytes: 64 << 20,
		TMDB: TMDB{
			Language: "en-US",
		},
		QBittorrent: QBittorrent{
			URL:      "http://localhost:8080",
			Username: "admin",
		},
		Prowlarr: Prowlarr{URL: "http://localhost:9696"},
		Requests: Requests{StatePath: "/data/requests.db"},
		Library:  Library{FolderFormat: "{title} ({year})"},
		Subtitles: Subtitles{
			Enabled:               true,
			StatePath:             "/data/subtitles.db",
			WorkDir:               "/tmp/cineroute-subtitles",
			WorkRetentionDays:     7,
			ReferenceLanguages:    []string{"en", "es"},
			FFmpegPath:            "ffmpeg",
			FFprobePath:           "ffprobe",
			AlassPath:             "alass",
			ScanBatchSize:         0,
			RunBatchSize:          20,
			RequestIntervalMS:     400,
			QuotaReserve:          5,
			MaxCandidates:         5,
			UseMoviehash:          true,
			RemovePromoCues:       true,
			MinReferenceCues:      20,
			MinVideoBytes:         20 << 20,
			SkipSampleFiles:       true,
			ProbeTimeoutSeconds:   60,
			ExtractTimeoutSeconds: 900,
			SyncTimeoutSeconds:    300,
			AlassSplitPenalty:     7,
			Accept: SubtitleAccept{
				MinWithin2:        0.90,
				MaxP90Seconds:     2.5,
				MaxStartGapMin:    10,
				MaxEndGapMin:      10,
				MaxZeroStartCues:  3,
				MaxCueMinutes:     10,
				MaxOverrunMinutes: 15,
			},
			OpenSubtitles: OpenSubtitles{
				BaseURL: "https://api.opensubtitles.com/api/v1",
			},
		},
		Drives: []Drive{
			{ID: "disk1", MovieRoot: "/disk1/media/movies", MovieRemoteRoot: "/disk1/media/movies-remote", TVRoot: "/disk1/media/tv", TVRemoteRoot: "/disk1/media/tv-remote", AnimeRoot: "/disk1/media/anime"},
			{ID: "disk2", MovieRoot: "/disk2/media/movies", MovieRemoteRoot: "/disk2/media/movies-remote", TVRoot: "/disk2/media/tv", TVRemoteRoot: "/disk2/media/tv-remote", AnimeRoot: "/disk2/media/anime"},
			{ID: "disk3", MovieRoot: "/disk3/media/movies", MovieRemoteRoot: "/disk3/media/movies-remote", TVRoot: "/disk3/media/tv", TVRemoteRoot: "/disk3/media/tv-remote", AnimeRoot: "/disk3/media/anime"},
			{ID: "disk4", MovieRoot: "/disk4/media/movies", MovieRemoteRoot: "/disk4/media/movies-remote", TVRoot: "/disk4/media/tv", TVRemoteRoot: "/disk4/media/tv-remote", AnimeRoot: "/disk4/media/anime"},
		},
	}
}

// Load reads the config file (if it exists) and applies environment overrides.
// A missing file is not an error: defaults are used so the tool can run with
// only environment variables configured.
func Load(path string) (*Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if err == nil {
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("config %s: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}

	applyEnv(cfg)

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func applyEnv(cfg *Config) {
	if v := os.Getenv("CINEROUTE_REQUESTS_STATE_PATH"); v != "" {
		cfg.Requests.StatePath = v
	}
	if v := os.Getenv("CINEROUTE_LISTEN"); v != "" {
		cfg.Listen = v
	}
	if v := os.Getenv("CINEROUTE_AUTH_USERNAME"); v != "" {
		cfg.AuthUsername = v
	}
	if v := os.Getenv("CINEROUTE_AUTH_PASSWORD"); v != "" {
		cfg.AuthPassword = v
	}
	if v := os.Getenv("CINEROUTE_TMDB_API_KEY"); v != "" {
		cfg.TMDB.APIKey = v
	}
	if v := os.Getenv("CINEROUTE_QBIT_URL"); v != "" {
		cfg.QBittorrent.URL = v
	}
	if v := os.Getenv("CINEROUTE_QBIT_USERNAME"); v != "" {
		cfg.QBittorrent.Username = v
	}
	if v := os.Getenv("CINEROUTE_QBIT_PASSWORD"); v != "" {
		cfg.QBittorrent.Password = v
	}
	if v := os.Getenv("CINEROUTE_PROWLARR_URL"); v != "" {
		cfg.Prowlarr.URL = v
	}
	if v := os.Getenv("CINEROUTE_PROWLARR_API_KEY"); v != "" {
		cfg.Prowlarr.APIKey = v
	}
	if v := os.Getenv("CINEROUTE_OS_API_KEY"); v != "" {
		cfg.Subtitles.OpenSubtitles.APIKey = v
	}
	if v := os.Getenv("CINEROUTE_OS_USERNAME"); v != "" {
		cfg.Subtitles.OpenSubtitles.Username = v
	}
	if v := os.Getenv("CINEROUTE_OS_PASSWORD"); v != "" {
		cfg.Subtitles.OpenSubtitles.Password = v
	}
	if v := os.Getenv("CINEROUTE_SUBTITLES_ENABLED"); v != "" {
		cfg.Subtitles.Enabled = v == "1" || v == "true" || v == "yes" || v == "on"
	}
}

func (c *Config) validate() error {
	if strings.TrimSpace(c.Requests.StatePath) == "" {
		return errors.New("requests.state_path must not be empty")
	}
	if c.Listen == "" {
		return errors.New("listen address must not be empty")
	}
	if c.QBittorrent.URL == "" {
		return errors.New("qbittorrent.url must not be empty")
	}
	seen := map[string]bool{}
	for _, d := range c.Drives {
		if d.ID == "" {
			return errors.New("drive id must not be empty")
		}
		if seen[d.ID] {
			return fmt.Errorf("duplicate drive id %q", d.ID)
		}
		seen[d.ID] = true
		if d.MovieRoot == "" || d.TVRoot == "" {
			return fmt.Errorf("drive %s: movie_root and tv_root are required", d.ID)
		}
	}
	if err := c.validateSubtitles(); err != nil {
		return err
	}
	return nil
}

func (c *Config) validateSubtitles() error {
	s := &c.Subtitles
	if !s.Enabled {
		return nil
	}
	if s.StatePath == "" {
		return errors.New("subtitles.state_path must not be empty")
	}
	if s.WorkDir == "" {
		return errors.New("subtitles.work_dir must not be empty")
	}
	if len(s.TargetLanguages) > 0 {
		for _, language := range s.TargetLanguages {
			if strings.TrimSpace(language) == "" {
				return errors.New("subtitles.target_languages must not contain empty entries")
			}
		}
	}
	if len(s.ReferenceLanguages) == 0 {
		return errors.New("subtitles.reference_languages must not be empty")
	}
	if s.WorkRetentionDays < 0 {
		return errors.New("subtitles.work_retention_days must not be negative")
	}
	if s.ScanBatchSize < 0 {
		return errors.New("subtitles.scan_batch_size must not be negative")
	}
	if s.RunBatchSize < 1 || s.RunBatchSize > 1000 {
		return errors.New("subtitles.run_batch_size must be between 1 and 1000")
	}
	if s.RequestIntervalMS < 0 || s.RequestIntervalMS > 60000 {
		return errors.New("subtitles.request_interval_ms must be between 0 and 60000")
	}
	if s.QuotaReserve < 0 {
		return errors.New("subtitles.quota_reserve must not be negative")
	}
	if s.MaxCandidates < 1 || s.MaxCandidates > 20 {
		return errors.New("subtitles.max_candidates must be between 1 and 20")
	}
	if s.MinReferenceCues < 1 {
		return errors.New("subtitles.min_reference_cues must be at least 1")
	}
	if s.MinVideoBytes < 0 {
		return errors.New("subtitles.min_video_bytes must not be negative")
	}
	if s.ProbeTimeoutSeconds <= 0 || s.ExtractTimeoutSeconds <= 0 || s.SyncTimeoutSeconds <= 0 {
		return errors.New("subtitles timeouts must be greater than zero")
	}
	if s.AlassSplitPenalty < 0 || s.AlassSplitPenalty > 1000 {
		return errors.New("subtitles.alass_split_penalty must be between 0 and 1000")
	}
	a := s.Accept
	if a.MinWithin2 < 0 || a.MinWithin2 > 1 {
		return errors.New("subtitles.accept.min_within_2s must be between 0 and 1")
	}
	if a.MaxP90Seconds <= 0 || a.MaxStartGapMin <= 0 || a.MaxEndGapMin <= 0 || a.MaxCueMinutes <= 0 || a.MaxOverrunMinutes <= 0 {
		return errors.New("subtitles.accept thresholds must be greater than zero")
	}
	if a.MaxZeroStartCues < 0 {
		return errors.New("subtitles.accept.max_zero_start_cues must not be negative")
	}
	return nil
}

// ResolveTargetLanguages returns the subtitle languages to work on, honouring
// the legacy single-language setting and falling back to the built-in default.
func (s Subtitles) ResolveTargetLanguages() []string {
	if len(s.TargetLanguages) > 0 {
		return append([]string(nil), s.TargetLanguages...)
	}
	if strings.TrimSpace(s.TargetLanguage) != "" {
		return []string{s.TargetLanguage}
	}
	return []string{"sv", "es", "en"}
}

func (c *Config) DriveByID(id string) (Drive, bool) {
	for _, d := range c.Drives {
		if d.ID == id {
			return d, true
		}
	}
	return Drive{}, false
}
