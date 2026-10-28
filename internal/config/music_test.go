package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntegratedMusicYAMLDefaultsAndCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := `music:
  enabled: true
  qobuz:
    quality: 2
  paths:
    library: /library/music
    staging: /downloads/music
    data: /state/music
  naming:
    folder: "{albumartist}/{title} ({year})"
`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "qobuz") {
		t.Fatalf("missing music credentials accepted: %v", err)
	}
	t.Setenv("CINEROUTE_QOBUZ_USER_ID", "test-id")
	t.Setenv("CINEROUTE_QOBUZ_TOKEN", "test-token")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Music.Qobuz.Quality != 2 || cfg.Music.Qobuz.UserID != "test-id" || cfg.Music.Qobuz.Token != "test-token" || cfg.Music.Paths.Data != "/state/music" {
		t.Fatal("music YAML/env settings lost")
	}
	if !cfg.Music.Download.VerifyFLAC || !cfg.Music.Download.FixMD5 || cfg.Music.Download.Attempts != 2 {
		t.Fatal("music defaults lost while overlaying YAML")
	}
	body = strings.Replace(body, "/downloads/music", "/library/music/staging", 1)
	os.WriteFile(path, []byte(body), 0600)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "staging") {
		t.Fatalf("partial music would be visible to library scanner: %v", err)
	}
}
