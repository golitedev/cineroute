package config

import (
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

const DefaultAudiobookFileLimit int64 = 5 << 30

type Audiobooks struct {
	Enabled        bool    `yaml:"enabled"`
	BotAPIURL      string  `yaml:"bot_api_url"`
	BotToken       string  `yaml:"-"`
	AllowedUserIDs []int64 `yaml:"allowed_user_ids"`
	TelegramRoot   string  `yaml:"telegram_root"`
	StagingRoot    string  `yaml:"staging_root"`
	LibraryRoot    string  `yaml:"library_root"`
	MaxFileBytes   int64   `yaml:"max_file_bytes"`
}

var botTokenFormat = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`)

func (a Audiobooks) Validate() error {
	if !a.Enabled {
		return nil
	}
	if !botTokenFormat.MatchString(a.BotToken) {
		return fmt.Errorf("audiobooks requires a valid CINEROUTE_AUDIOBOOKS_BOT_TOKEN")
	}
	u, err := url.Parse(a.BotAPIURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		strings.Trim(u.Path, "/") != "" || strings.EqualFold(u.Hostname(), "api.telegram.org") {
		return fmt.Errorf("audiobooks.bot_api_url must be the origin of a local Bot API server")
	}
	for _, setting := range []struct{ name, path string }{
		{"telegram_root", a.TelegramRoot}, {"staging_root", a.StagingRoot}, {"library_root", a.LibraryRoot},
	} {
		if !filepath.IsAbs(setting.path) || filepath.Clean(setting.path) == string(filepath.Separator) {
			return fmt.Errorf("audiobooks.%s must be an absolute directory path below the filesystem root", setting.name)
		}
	}
	for _, id := range a.AllowedUserIDs {
		if id <= 0 {
			return fmt.Errorf("audiobooks.allowed_user_ids must contain positive Telegram user IDs")
		}
	}
	if a.MaxFileBytes < 0 || a.MaxFileBytes > DefaultAudiobookFileLimit {
		return fmt.Errorf("audiobooks.max_file_bytes must be between 0 and %d", DefaultAudiobookFileLimit)
	}
	return nil
}
