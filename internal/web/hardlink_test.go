package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"cineroute/internal/companion"
	"cineroute/internal/config"
)

func TestHardlinkManagementAPIListsAndRemovesLinks(t *testing.T) {
	base := t.TempDir()
	primary := filepath.Join(base, "movies")
	remote := filepath.Join(base, "movies-remote")
	tvPrimary := filepath.Join(base, "tv")
	tvRemote := filepath.Join(base, "tv-remote")
	for _, dir := range []string{primary, remote, tvPrimary, tvRemote} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sourceFolder := filepath.Join(primary, "API Movie (2025)")
	remoteFolder := filepath.Join(remote, "API Movie (2025)")
	if err := os.MkdirAll(sourceFolder, 0o755); err != nil {
		t.Fatal(err)
	}
	sourceFile := filepath.Join(sourceFolder, "movie.mkv")
	remoteFile := filepath.Join(remoteFolder, "movie.mkv")
	if err := os.WriteFile(sourceFile, []byte("movie"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(remoteFolder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(sourceFile, remoteFile); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Companion.StatePath = filepath.Join(base, "state.db")
	cfg.Drives = []config.Drive{{
		ID:              "hdd-api",
		MovieRoot:       primary,
		MovieRemoteRoot: remote,
		TVRoot:          tvPrimary,
		TVRemoteRoot:    tvRemote,
	}}
	srv := New(cfg, nil, nil)
	handler := srv.Handler()

	list := httptest.NewRecorder()
	handler.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/hardlinks", nil))
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d, body %s", list.Code, list.Body.String())
	}
	var view companion.HardlinkView
	if err := json.NewDecoder(list.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	if view.Stats.Total != 1 || len(view.Items) != 1 || view.Items[0].Status != "healthy" {
		t.Fatalf("hardlink view = %+v, want one healthy item", view)
	}

	remove := httptest.NewRecorder()
	handler.ServeHTTP(remove, httptest.NewRequest(http.MethodDelete, "/api/hardlinks/"+view.Items[0].ID, nil))
	if remove.Code != http.StatusOK {
		t.Fatalf("remove status = %d, body %s", remove.Code, remove.Body.String())
	}
	if _, err := os.Stat(sourceFile); err != nil {
		t.Fatalf("primary file was removed: %v", err)
	}
	if _, err := os.Stat(remoteFile); !os.IsNotExist(err) {
		t.Fatalf("remote hardlink still exists: %v", err)
	}
}
