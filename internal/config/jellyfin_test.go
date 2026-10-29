package config

import (
	"path/filepath"
	"testing"
)

func TestJellyfinValidation(t *testing.T) {
	good := Jellyfin{Enabled: true, URL: "https://jellyfin.lan/base", APIKey: "key", StatePath: "/data/jellyfin.db"}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Jellyfin){
		func(j *Jellyfin) { j.URL = "file:///tmp/server" },
		func(j *Jellyfin) { j.URL = "https://user:secret@host" },
		func(j *Jellyfin) { j.URL = "https://host?api_key=secret" },
		func(j *Jellyfin) { j.APIKey = "" },
		func(j *Jellyfin) { j.StatePath = "" },
		func(j *Jellyfin) { j.PathMappings = []JellyfinPathMapping{{Local: "relative", Remote: "/movies"}} },
		func(j *Jellyfin) {
			j.PathMappings = []JellyfinPathMapping{{Local: "/a/", Remote: "/b"}, {Local: "/a", Remote: "/c"}}
		},
	} {
		bad := good
		change(&bad)
		if bad.Validate() == nil {
			t.Errorf("accepted invalid settings: %+v", bad)
		}
	}
}

func TestJellyfinEnvironment(t *testing.T) {
	t.Setenv("CINEROUTE_JELLYFIN_ENABLED", "true")
	t.Setenv("CINEROUTE_JELLYFIN_URL", "http://jellyfin:8096")
	t.Setenv("CINEROUTE_JELLYFIN_API_KEY", "secret")
	t.Setenv("CINEROUTE_JELLYFIN_STATE_PATH", filepath.Join(t.TempDir(), "jellyfin.db"))
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Jellyfin.Enabled || cfg.Jellyfin.URL != "http://jellyfin:8096" || cfg.Jellyfin.APIKey != "secret" {
		t.Fatal("missing environment overrides")
	}
}
