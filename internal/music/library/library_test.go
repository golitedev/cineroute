package library

import (
	"testing"

	"cineroute/internal/music/qobuz"
)

func TestFolderForPreservesReleaseVersion(t *testing.T) {
	album := qobuz.Album{Title: "PARADIXE", Artist: "Feid", Version: "REMIX", ReleaseDate: "2026-01-01"}
	for _, tt := range []struct{ template, want string }{
		{"{albumartist} — {album} ({year})", "Feid — PARADIXE (REMIX) (2026)"},
		{"{albumartist}/{album} ({version})", "Feid/PARADIXE (REMIX)"},
		{"{albumartist}/{title}", "Feid/PARADIXE (REMIX)"},
	} {
		t.Run(tt.template, func(t *testing.T) {
			got, err := FolderFor(tt.template, album, Quality{}, 3, false)
			if err != nil || got != tt.want {
				t.Fatalf("FolderFor = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
	album.Version = ""
	got, err := FolderFor("{albumartist} — {album} ({year})", album, Quality{}, 3, false)
	if err != nil || got != "Feid — PARADIXE (2026)" {
		t.Fatalf("original release path changed: %q, %v", got, err)
	}
}
