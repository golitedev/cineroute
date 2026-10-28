// Package config defines the integrated music engine settings.
package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"cineroute/internal/music/naming"
)

type Config struct {
	Qobuz     Qobuz          `yaml:"qobuz"`
	Paths     Paths          `yaml:"paths"`
	Naming    Naming         `yaml:"naming"`
	Download  Download       `yaml:"download"`
	Artwork   Artwork        `yaml:"artwork"`
	Metadata  Metadata       `yaml:"metadata"`
	Navidrome Navidrome      `yaml:"navidrome"`
	Streamrip map[string]any `yaml:"streamrip"`
}

type Qobuz struct {
	UserID  string `yaml:"user_id"`
	Token   string `yaml:"token"`
	Quality int    `yaml:"quality"`
	AppID   string `yaml:"app_id"`
}

type Paths struct {
	Library string `yaml:"library"`
	Staging string `yaml:"staging"`
	Data    string `yaml:"data"`
}

type Naming struct {
	Folder             string `yaml:"folder"`
	Track              string `yaml:"track"`
	DiscSubfolders     bool   `yaml:"disc_subfolders"`
	RestrictCharacters bool   `yaml:"restrict_characters"`
	IfExists           string `yaml:"if_exists"`
}

type Download struct {
	Attempts          int    `yaml:"attempts"`
	RetryDelay        int    `yaml:"retry_delay"`
	TimeoutMinutes    int    `yaml:"timeout_minutes"`
	MaxConnections    int    `yaml:"max_connections"`
	RequestsPerMinute int    `yaml:"requests_per_minute"`
	Booklets          bool   `yaml:"booklets"`
	VerifyFLAC        bool   `yaml:"verify_flac"`
	FixMD5            bool   `yaml:"fix_md5"`
	KeepFailed        bool   `yaml:"keep_failed"`
	HistoryLimit      int    `yaml:"history_limit"`
	RipCommand        string `yaml:"rip_command"`
}

type Artwork struct {
	Embed         bool   `yaml:"embed"`
	EmbedSize     string `yaml:"embed_size"`
	EmbedMaxWidth int    `yaml:"embed_max_width"`
	SaveCover     bool   `yaml:"save_cover"`
	SavedMaxWidth int    `yaml:"saved_max_width"`
}

type Metadata struct {
	Exclude           []string          `yaml:"exclude"`
	TranslateGenres   bool              `yaml:"translate_genres"`
	CleanDescriptions bool              `yaml:"clean_descriptions"`
	GenreTranslations map[string]string `yaml:"genre_translations"`
}

type Navidrome struct {
	URL      string `yaml:"url"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// Default returns the defaults overlaid by config.yaml music settings.
func Default() Config {
	return Config{
		Qobuz: Qobuz{Quality: 3},
		Paths: Paths{Library: "/music", Staging: "/staging", Data: "/data/music"},
		Naming: Naming{
			Folder:         "{albumartist}/{title} ({year})",
			Track:          "{tracknumber:02}. {title}",
			DiscSubfolders: true,
			IfExists:       IfExistsSkip,
		},
		Download: Download{
			Attempts:          2,
			RetryDelay:        30,
			TimeoutMinutes:    60,
			MaxConnections:    12,
			RequestsPerMinute: 120,
			VerifyFLAC:        true,
			FixMD5:            true,
			HistoryLimit:      200,
			RipCommand:        "rip",
		},
		Metadata: Metadata{TranslateGenres: true, CleanDescriptions: true},
		Artwork: Artwork{
			Embed:         true,
			EmbedSize:     "large",
			EmbedMaxWidth: -1,
			SaveCover:     true,
			SavedMaxWidth: -1,
		},
	}
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
	if c.Paths.Library != "" && c.Paths.Staging != "" {
		if within(c.Paths.Staging, c.Paths.Library) {
			add("paths.staging must not be inside paths.library (your music server would pick up unfinished albums)")
		}
		if within(c.Paths.Library, c.Paths.Staging) {
			add("paths.library must not be inside paths.staging")
		}
	}

	if strings.TrimSpace(c.Naming.Folder) == "" {
		add("naming.folder must be set")
	}
	if err := naming.Check(c.Naming.Folder); err != nil {
		add("naming.folder: %v", err)
	}
	if strings.TrimSpace(c.Naming.Track) == "" {
		add("naming.track must be set")
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
	if d.MaxConnections == 0 || d.MaxConnections < -1 {
		add("download.max_connections must be at least 1 (or -1 for no limit)")
	}
	if d.RequestsPerMinute == 0 || d.RequestsPerMinute < -1 {
		add("download.requests_per_minute must be at least 1 (or -1 for no limit)")
	}
	if d.HistoryLimit < 0 {
		add("download.history_limit must not be negative")
	}
	if strings.TrimSpace(d.RipCommand) == "" {
		add("download.rip_command must be set")
	}

	switch c.Artwork.EmbedSize {
	case "thumbnail", "small", "large", "original":
	default:
		add("artwork.embed_size must be thumbnail, small, large or original")
	}

	c.Navidrome.URL = strings.TrimRight(strings.TrimSpace(c.Navidrome.URL), "/")

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
