package config

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

// Jellyfin notifications are optional and use paths as seen by the server.
type Jellyfin struct {
	Enabled      bool                  `yaml:"enabled"`
	URL          string                `yaml:"url"`
	APIKey       string                `yaml:"api_key"`
	StatePath    string                `yaml:"state_path"`
	PathMappings []JellyfinPathMapping `yaml:"path_mappings"`
}

type JellyfinPathMapping struct {
	Local  string `yaml:"local"`
	Remote string `yaml:"remote"`
}

func (j Jellyfin) Validate() error {
	if !j.Enabled {
		return nil
	}
	u, err := url.Parse(j.URL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("jellyfin.url must be an HTTP(S) server URL without credentials, query or fragment")
	}
	if strings.TrimSpace(j.APIKey) == "" || strings.ContainsAny(j.APIKey, "\r\n") {
		return fmt.Errorf("jellyfin.api_key is required and must not contain newlines")
	}
	if strings.TrimSpace(j.StatePath) == "" {
		return fmt.Errorf("jellyfin.state_path is required")
	}
	seen := map[string]bool{}
	for _, m := range j.PathMappings {
		root := filepath.Clean(m.Local)
		if !filepath.IsAbs(m.Local) || !filepath.IsAbs(m.Remote) || seen[root] {
			return fmt.Errorf("jellyfin.path_mappings require unique absolute local roots and absolute remote roots")
		}
		seen[root] = true
	}
	return nil
}
