package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntegratedMusicSplitConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	ripPath := filepath.Join(dir, "streamrip.toml")
	body := `music:
  enabled: true
  qobuz:
    user_id: old-id
    token: old-token
  paths:
    library: /library/music
    staging: /downloads/music
    data: /state/music
`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CINEROUTE_MUSIC_ENABLED", "")
	t.Setenv("CINEROUTE_QOBUZ_USER_ID", "")
	t.Setenv("CINEROUTE_QOBUZ_TOKEN", "")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), ripPath) {
		t.Fatalf("missing Streamrip config accepted: %v", err)
	}
	rip := `[qobuz]
quality = 2
[filepaths]
folder_format = "{albumartist}/{title} ({year})"
restrict_characters = true
`
	if err := os.WriteFile(ripPath, []byte(rip), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "password_or_token") {
		t.Fatalf("legacy YAML credentials accepted: %v", err)
	}
	rip = strings.Replace(rip, "quality = 2", "quality = 2\nemail_or_userid = 'file-id'\npassword_or_token = 'file-token'", 1)
	if err := os.WriteFile(ripPath, []byte(rip), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CINEROUTE_QOBUZ_TOKEN", "test-token")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Music.Qobuz.Quality != 2 || cfg.Music.Qobuz.UserID != "file-id" || cfg.Music.Qobuz.Token != "test-token" || cfg.Music.Paths.Data != "/state/music" {
		t.Fatal("music TOML, YAML or env settings lost")
	}
	if cfg.Music.Naming.Folder != "{albumartist}/{title} ({year})" || !cfg.Music.Naming.RestrictCharacters || cfg.Music.StreamripPath != ripPath {
		t.Fatal("Streamrip file paths were not loaded")
	}
	if !cfg.Music.Qobuz.VerifyFLAC || cfg.Music.Download.Attempts != 2 {
		t.Fatal("music defaults lost while overlaying config")
	}
	body = strings.Replace(body, "/downloads/music", "/library/music/staging", 1)
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "staging") {
		t.Fatalf("partial music would be visible to library scanner: %v", err)
	}
}

func TestStreamripConfigPathAndMusicDisabled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("music:\n  enabled: true\n  streamrip_config: private.toml\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ripPath := filepath.Join(dir, "private.toml")
	if err := os.WriteFile(ripPath, []byte("[qobuz\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CINEROUTE_MUSIC_ENABLED", "true")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), ripPath) {
		t.Fatalf("malformed Streamrip config accepted: %v", err)
	}
	t.Setenv("CINEROUTE_MUSIC_ENABLED", "false")
	if _, err := Load(path); err != nil {
		t.Fatalf("music is disabled: %v", err)
	}
}
