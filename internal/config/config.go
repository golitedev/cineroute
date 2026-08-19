package config

import (
	"errors"
	"fmt"
	"os"

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
	Companion      Companion   `yaml:"companion"`
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

const (
	DefaultCompanionSearchIntervalSeconds = 10
	MinCompanionSearchIntervalSeconds     = 5
	MaxCompanionSearchIntervalSeconds     = 300
	DefaultCompanionSearchBatchSize       = 20
	MinCompanionSearchBatchSize           = 1
	MaxCompanionSearchBatchSize           = 1000
)

type Prowlarr struct {
	URL         string `yaml:"url"`
	APIKey      string `yaml:"api_key"`
	IndexerName string `yaml:"indexer_name"`
}

type Companion struct {
	Enabled               bool   `yaml:"enabled"`
	StatePath             string `yaml:"state_path"`
	MaxSizeGiB            int64  `yaml:"max_size_gib"`
	MinSeeders            int    `yaml:"min_seeders"`
	SearchLimit           int    `yaml:"search_limit"`
	SearchIntervalSeconds int    `yaml:"search_interval_seconds"`
}

type Library struct {
	FolderFormat string `yaml:"folder_format"`
}

type Drive struct {
	ID        string `yaml:"id"`
	MovieRoot string `yaml:"movie_root"`
	TVRoot    string `yaml:"tv_root"`
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
		Prowlarr: Prowlarr{
			URL:         "http://localhost:9696",
			IndexerName: "LAT-Team",
		},
		Companion: Companion{
			Enabled:               true,
			StatePath:             "/data/companions.db",
			MaxSizeGiB:            20,
			MinSeeders:            1,
			SearchLimit:           50,
			SearchIntervalSeconds: DefaultCompanionSearchIntervalSeconds,
		},
		Library: Library{FolderFormat: "{title} ({year})"},
		Drives: []Drive{
			{ID: "hdd1", MovieRoot: "/m1", TVRoot: "/t1"},
			{ID: "hdd2", MovieRoot: "/m2", TVRoot: "/t2"},
			{ID: "hdd3", MovieRoot: "/m3", TVRoot: "/t3"},
			{ID: "hdd4", MovieRoot: "/m4", TVRoot: "/t4"},
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
	if v := os.Getenv("CINEROUTE_PROWLARR_INDEXER"); v != "" {
		cfg.Prowlarr.IndexerName = v
	}
}

func (c *Config) validate() error {
	if c.Listen == "" {
		return errors.New("listen address must not be empty")
	}
	if c.QBittorrent.URL == "" {
		return errors.New("qbittorrent.url must not be empty")
	}
	if c.Companion.StatePath == "" {
		return errors.New("companion.state_path must not be empty")
	}
	if c.Companion.MaxSizeGiB <= 0 {
		return errors.New("companion.max_size_gib must be greater than zero")
	}
	if c.Companion.MinSeeders < 0 {
		return errors.New("companion.min_seeders must not be negative")
	}
	if c.Companion.SearchLimit <= 0 {
		return errors.New("companion.search_limit must be greater than zero")
	}
	if c.Companion.SearchIntervalSeconds < MinCompanionSearchIntervalSeconds || c.Companion.SearchIntervalSeconds > MaxCompanionSearchIntervalSeconds {
		return fmt.Errorf("companion.search_interval_seconds must be between %d and %d", MinCompanionSearchIntervalSeconds, MaxCompanionSearchIntervalSeconds)
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
	return nil
}

func (c *Config) DriveByID(id string) (Drive, bool) {
	for _, d := range c.Drives {
		if d.ID == id {
			return d, true
		}
	}
	return Drive{}, false
}
