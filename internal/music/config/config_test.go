package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestMusicDefaults(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.Qobuz.Quality != 3 || !cfg.Download.FixMD5 || !cfg.Download.VerifyFLAC || cfg.Download.Attempts != 2 {
		t.Fatal("incomplete verification defaults")
	}
	cfg.Qobuz.UserID = " test "
	cfg.Qobuz.Token = " token "
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if !cfg.QobuzConfigured() || cfg.Qobuz.UserID != "test" || cfg.Qobuz.Token != "token" {
		t.Fatal("credentials not normalized")
	}
}
func TestMusicConfigRejectsUnsafePathsAndInvalidSettings(t *testing.T) {
	tests := map[string]func(*Config){
		"staging inside library": func(c *Config) { c.Paths.Staging = filepath.Join(c.Paths.Library, "staging") },
		"library inside staging": func(c *Config) { c.Paths.Library = filepath.Join(c.Paths.Staging, "library") },
		"invalid quality":        func(c *Config) { c.Qobuz.Quality = 5 },
		"invalid naming":         func(c *Config) { c.Naming.Folder = "{unknown}" },
		"invalid replacement":    func(c *Config) { c.Naming.IfExists = "overwrite" },
		"zero attempts":          func(c *Config) { c.Download.Attempts = 0 },
		"empty downloader":       func(c *Config) { c.Download.RipCommand = "" },
	}
	for name, modify := range tests {
		t.Run(name, func(t *testing.T) {
			c := Default()
			modify(&c)
			if err := c.Validate(); err == nil || strings.TrimSpace(err.Error()) == "" {
				t.Fatal("invalid music settings accepted")
			}
		})
	}
}
