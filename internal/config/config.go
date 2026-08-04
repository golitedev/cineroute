package config

import (
	"errors"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen         string      `yaml:"listen"`
	AuthPassword   string      `yaml:"auth_password"`
	MaxUploadBytes int64       `yaml:"max_upload_bytes"`
	TMDB           TMDB        `yaml:"tmdb"`
	QBittorrent    QBittorrent `yaml:"qbittorrent"`
	Library        Library     `yaml:"library"`
	Drives         []Drive     `yaml:"drives"`
}

type TMDB struct {
	APIKey   string `yaml:"api_key"`
	Language string `yaml:"language"`
}

type QBittorrent struct {
	URL           string `yaml:"url"`
	Username      string `yaml:"username"`
	Password      string `yaml:"password"`
	MovieCategory string `yaml:"movie_category"`
	TVCategory    string `yaml:"tv_category"`
}

type Library struct {
	FolderFormat string `yaml:"folder_format"`
}

type Drive struct {
	ID           string `yaml:"id"`
	MovieRoot    string `yaml:"movie_root"`
	TVRoot       string `yaml:"tv_root"`
	ReserveBytes int64  `yaml:"reserve_bytes"`
}

func Default() *Config {
	return &Config{
		Listen:         "127.0.0.1:8787",
		MaxUploadBytes: 64 << 20,
		TMDB: TMDB{
			Language: "en-US",
		},
		QBittorrent: QBittorrent{
			URL:           "http://localhost:8080",
			Username:      "admin",
			MovieCategory: "cineroute-movie",
			TVCategory:    "cineroute-tv",
		},
		Library: Library{FolderFormat: "{title} ({year})"},
		Drives: []Drive{
			{ID: "hdd1", MovieRoot: "/m1", TVRoot: "/t1", ReserveBytes: 100 << 30},
			{ID: "hdd2", MovieRoot: "/m2", TVRoot: "/t2", ReserveBytes: 100 << 30},
			{ID: "hdd3", MovieRoot: "/m3", TVRoot: "/t3", ReserveBytes: 100 << 30},
			{ID: "hdd4", MovieRoot: "/m4", TVRoot: "/t4", ReserveBytes: 100 << 30},
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
}

func (c *Config) validate() error {
	if c.Listen == "" {
		return errors.New("listen address must not be empty")
	}
	if c.QBittorrent.URL == "" {
		return errors.New("qbittorrent.url must not be empty")
	}
	if c.QBittorrent.MovieCategory == "" || c.QBittorrent.TVCategory == "" {
		return errors.New("qbittorrent categories must not be empty")
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
		if d.ReserveBytes < 0 {
			return fmt.Errorf("drive %s: reserve_bytes must not be negative", d.ID)
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

func (c *Config) CategoryFor(mediaType string) string {
	if mediaType == "tv" {
		return c.QBittorrent.TVCategory
	}
	return c.QBittorrent.MovieCategory
}
