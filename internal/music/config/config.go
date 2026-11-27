// Package config defines the integrated music engine settings.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"cineroute/internal/music/naming"
	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	StreamripPath string         `yaml:"streamrip_config"`
	Qobuz         Qobuz          `yaml:"-"`
	Paths         Paths          `yaml:"paths"`
	Naming        Naming         `yaml:"naming"`
	Download      Download       `yaml:"download"`
	Metadata      Metadata       `yaml:"metadata"`
	Lyrics        Lyrics         `yaml:"lyrics"`
	Navidrome     Navidrome      `yaml:"navidrome"`
	Streamrip     map[string]any `yaml:"-"`
}

type Qobuz struct {
	UserID     string `toml:"email_or_userid"`
	Token      string `toml:"password_or_token"`
	Quality    int    `toml:"quality"`
	AppID      string `toml:"app_id"`
	VerifyFLAC bool   `toml:"verify_flac"`
}

type Paths struct {
	Library     string `yaml:"library"`
	HostLibrary string `yaml:"host_library"` // optional host path for import history
	Staging     string `yaml:"staging"`
	Data        string `yaml:"data"`
}

type Naming struct {
	Folder             string `yaml:"-"`
	RestrictCharacters bool   `yaml:"-"`
	IfExists           string `yaml:"if_exists"`
}

type Download struct {
	Attempts       int    `yaml:"attempts"`
	RetryDelay     int    `yaml:"retry_delay"`
	TimeoutMinutes int    `yaml:"timeout_minutes"`
	KeepFailed     bool   `yaml:"keep_failed"`
	HistoryLimit   int    `yaml:"history_limit"`
	RipCommand     string `yaml:"rip_command"`
}

type Metadata struct {
	TranslateGenres   bool              `yaml:"translate_genres"`
	GenreTranslations map[string]string `yaml:"genre_translations"`
}

// Lyrics controls automatic sidecars for newly imported releases.
type Lyrics struct {
	Enabled           bool   `yaml:"enabled"`
	Script            string `yaml:"script"`
	Engine            string `yaml:"engine"`
	VerifiedOnly      bool   `yaml:"verified_only"`
	SyncedOnly        bool   `yaml:"synced_only"`
	RequireAlbumMatch bool   `yaml:"require_album_match"`
	MaxAttempts       int    `yaml:"max_attempts"`
	RetryMinutes      int    `yaml:"retry_minutes"`
	TimeoutMinutes    int    `yaml:"timeout_minutes"`
}

type Navidrome struct {
	URL           string `yaml:"url"`
	Username      string `yaml:"username"`
	Password      string `yaml:"password"`
	MusicFolderID string `yaml:"music_folder_id"`
}

// Default returns the defaults overlaid by config.yaml and streamrip.toml.
func Default() Config {
	return Config{
		Qobuz: Qobuz{Quality: 3, VerifyFLAC: true},
		Paths: Paths{Library: "/music", Staging: "/staging", Data: "/data/music"},
		Naming: Naming{
			Folder:   "{albumartist} — {title} ({year}) [WEB] [{format}] [{bit_depth}B-{sampling_rate}kHz]",
			IfExists: IfExistsSkip,
		},
		Download: Download{
			Attempts:       2,
			RetryDelay:     30,
			TimeoutMinutes: 60,
			HistoryLimit:   200,
			RipCommand:     "rip",
		},
		Lyrics:   Lyrics{Enabled: false, Script: "/opt/lrcscript/lrcscript.py", Engine: "/usr/local/bin/lyrics-fetch", VerifiedOnly: true, MaxAttempts: 4, RetryMinutes: 15, TimeoutMinutes: 60},
		Metadata: Metadata{TranslateGenres: true},
	}
}

// LoadStreamrip reads the native Streamrip settings and the subset shared with
// CineRoute's catalog, duplicate detection, and verification code.
func (c *Config) LoadStreamrip(configDir string) error {
	path := strings.TrimSpace(c.StreamripPath)
	if path == "" {
		path = "streamrip.toml"
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(configDir, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading required Streamrip config %q: %w", path, err)
	}
	var options map[string]any
	if err := toml.Unmarshal(data, &options); err != nil {
		return fmt.Errorf("Streamrip config %q: %w", path, err)
	}
	settings := struct {
		Qobuz     Qobuz `toml:"qobuz"`
		Filepaths struct {
			Folder             string  `toml:"folder_format"`
			Track              *string `toml:"track_format"`
			RestrictCharacters bool    `toml:"restrict_characters"`
		} `toml:"filepaths"`
		Downloads struct {
			MaxConnections    *int `toml:"max_connections"`
			RequestsPerMinute *int `toml:"requests_per_minute"`
		} `toml:"downloads"`
		Artwork struct {
			EmbedSize *string `toml:"embed_size"`
		} `toml:"artwork"`
	}{Qobuz: c.Qobuz}
	settings.Filepaths.Folder = c.Naming.Folder
	settings.Filepaths.RestrictCharacters = c.Naming.RestrictCharacters
	if err := toml.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("Streamrip config %q: %w", path, err)
	}
	if track := settings.Filepaths.Track; track != nil && strings.TrimSpace(*track) == "" {
		return fmt.Errorf("Streamrip config %q: filepaths.track_format must be set", path)
	}
	for _, limit := range []struct {
		name  string
		value *int
	}{
		{"max_connections", settings.Downloads.MaxConnections},
		{"requests_per_minute", settings.Downloads.RequestsPerMinute},
	} {
		if limit.value != nil && (*limit.value == 0 || *limit.value < -1) {
			return fmt.Errorf("Streamrip config %q: downloads.%s must be at least 1 (or -1 for no limit)", path, limit.name)
		}
	}
	if size := settings.Artwork.EmbedSize; size != nil {
		switch *size {
		case "thumbnail", "small", "large", "original":
		default:
			return fmt.Errorf("Streamrip config %q: artwork.embed_size must be thumbnail, small, large or original", path)
		}
	}
	c.StreamripPath, c.Streamrip = path, options
	c.Qobuz = settings.Qobuz
	c.Naming.Folder = settings.Filepaths.Folder
	c.Naming.RestrictCharacters = settings.Filepaths.RestrictCharacters
	return nil
}

const (
	IfExistsSkip    = "skip"
	IfExistsReplace = "replace"
)

// QualityLabels describes the quality setting values.
var QualityLabels = map[int]string{
	1: "MP3 320 kbps",
	2: "CD quality, 16-bit / 44.1 kHz",
	3: "Hi-Res, up to 24-bit / 96 kHz",
	4: "Hi-Res, up to 24-bit / 192 kHz",
}

// Validate checks values and normalises paths.
func (c *Config) Validate() error {
	var errs []string
	add := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }

	c.Qobuz.UserID = strings.TrimSpace(c.Qobuz.UserID)
	c.Qobuz.Token = strings.TrimSpace(c.Qobuz.Token)
	c.Qobuz.AppID = strings.TrimSpace(c.Qobuz.AppID)

	if _, ok := QualityLabels[c.Qobuz.Quality]; !ok {
		add("qobuz.quality must be 1, 2, 3 or 4 (got %d)", c.Qobuz.Quality)
	}

	for name, p := range map[string]*string{
		"paths.library": &c.Paths.Library,
		"paths.staging": &c.Paths.Staging,
		"paths.data":    &c.Paths.Data,
	} {
		if strings.TrimSpace(*p) == "" {
			add("%s must be set", name)
			continue
		}
		abs, err := filepath.Abs(*p)
		if err != nil {
			add("%s: %v", name, err)
			continue
		}
		*p = filepath.Clean(abs)
	}
	if c.Paths.HostLibrary != "" {
		if filepath.IsAbs(c.Paths.HostLibrary) {
			c.Paths.HostLibrary = filepath.Clean(c.Paths.HostLibrary)
		} else {
			add("paths.host_library must be an absolute path")
		}
	}
	if c.Paths.Library != "" && c.Paths.Staging != "" {
		if within(c.Paths.Staging, c.Paths.Library) {
			add("paths.staging must not be inside paths.library (your music server would pick up unfinished albums)")
		}
		if within(c.Paths.Library, c.Paths.Staging) {
			add("paths.library must not be inside paths.staging")
		}
	}

	if strings.TrimSpace(c.Naming.Folder) == "" {
		add("streamrip filepaths.folder_format must be set")
	} else if err := naming.Check(c.Naming.Folder); err != nil {
		add("streamrip filepaths.folder_format: %v", err)
	}
	switch c.Naming.IfExists {
	case IfExistsSkip, IfExistsReplace:
	default:
		add("naming.if_exists must be %q or %q", IfExistsSkip, IfExistsReplace)
	}

	d := &c.Download
	if d.Attempts < 1 || d.Attempts > 10 {
		add("download.attempts must be between 1 and 10")
	}
	if d.RetryDelay < 0 {
		add("download.retry_delay must not be negative")
	}
	if d.TimeoutMinutes < 1 {
		add("download.timeout_minutes must be at least 1")
	}
	if d.HistoryLimit < 0 {
		add("download.history_limit must not be negative")
	}
	if strings.TrimSpace(d.RipCommand) == "" {
		add("download.rip_command must be set")
	}

	if c.Lyrics.Enabled {
		if strings.TrimSpace(c.Lyrics.Script) == "" || strings.TrimSpace(c.Lyrics.Engine) == "" {
			add("lyrics.script and lyrics.engine must be set")
		}
		if c.Lyrics.MaxAttempts < 1 || c.Lyrics.MaxAttempts > 10 {
			add("lyrics.max_attempts must be between 1 and 10")
		}
		if c.Lyrics.RetryMinutes < 1 || c.Lyrics.RetryMinutes > 1440 {
			add("lyrics.retry_minutes must be between 1 and 1440")
		}
		if c.Lyrics.TimeoutMinutes < 1 || c.Lyrics.TimeoutMinutes > 1440 {
			add("lyrics.timeout_minutes must be between 1 and 1440")
		}
	}
	c.Navidrome.URL = strings.TrimRight(strings.TrimSpace(c.Navidrome.URL), "/")
	c.Navidrome.MusicFolderID = strings.TrimSpace(c.Navidrome.MusicFolderID)

	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// QobuzConfigured reports whether the Qobuz credentials are filled in.
func (c *Config) QobuzConfigured() bool {
	return c.Qobuz.UserID != "" && c.Qobuz.Token != ""
}

// QualityLabel describes the configured quality.
func (c *Config) QualityLabel() string { return QualityLabels[c.Qobuz.Quality] }

func within(child, parent string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
