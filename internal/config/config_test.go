package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The defaults and example config must use paths visible to qBittorrent,
// backed by the NAS mounts in the Compose example.
func TestDebianNASLayout(t *testing.T) {
	data, err := os.ReadFile("../../compose.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var compose struct {
		Services map[string]struct {
			User    string
			Volumes []string
		}
	}
	if err := yaml.Unmarshal(data, &compose); err != nil {
		t.Fatal(err)
	}
	service := compose.Services["cineroute"]
	if service.User != "1000:1000" {
		t.Fatalf("container user = %q, want 1000:1000", service.User)
	}
	mounts := make(map[string]string)
	for _, volume := range service.Volumes {
		parts := strings.Split(volume, ":")
		mounts[parts[1]] = parts[0]
	}
	for _, configPath := range []string{"../../config.example.yaml", filepath.Join(t.TempDir(), "missing.yaml")} {
		t.Run(filepath.Base(configPath), func(t *testing.T) {
			cfg, err := Load(configPath)
			if err != nil {
				t.Fatal(err)
			}
			if len(cfg.Drives) != 4 {
				t.Fatalf("got %d drives, want 4", len(cfg.Drives))
			}
			for n := 1; n <= 4; n++ {
				id := fmt.Sprintf("disk%d", n)
				drive, ok := cfg.DriveByID(id)
				if !ok {
					t.Fatalf("missing drive %s", id)
				}
				mount := "/" + id
				for directory, root := range map[string]string{
					"movies": drive.MovieRoot, "movies-remote": drive.MovieRemoteRoot,
					"tv": drive.TVRoot, "tv-remote": drive.TVRemoteRoot, "anime": drive.AnimeRoot,
				} {
					want := filepath.Join("/mnt", id, "data", "media", directory)
					if !strings.HasPrefix(root, mount+"/") {
						t.Fatalf("root %q is outside mount %q", root, mount)
					}
					got := filepath.Join(mounts[mount], strings.TrimPrefix(root, mount+"/"))
					if got != want {
						t.Errorf("%s: %s maps to %s, want %s", id, root, got, want)
					}
				}
			}
		})
	}
}
