package copies

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSelectedHardlinksKeepOtherSeriesFilesOut(t *testing.T) {
	base := t.TempDir()
	source, target := filepath.Join(base, "main"), filepath.Join(base, "remote")
	if err := os.MkdirAll(filepath.Join(source, "Season 1"), 0o755); err != nil {
		t.Fatal(err)
	}
	episode := filepath.Join("Season 1", "Show.S01E01.mkv")
	for _, path := range []string{episode, filepath.Join("Season 1", "Show.S01E02.mkv")} {
		if err := os.WriteFile(filepath.Join(source, path), []byte("seed"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := LinkFiles(source, target, []string{episode}); err != nil {
		t.Fatal(err)
	}
	a, _ := os.Stat(filepath.Join(source, episode))
	b, _ := os.Stat(filepath.Join(target, episode))
	if b == nil || !os.SameFile(a, b) {
		t.Fatal("selected file was not hardlinked")
	}
	if _, err := os.Stat(filepath.Join(target, "Season 1", "Show.S01E02.mkv")); !os.IsNotExist(err) {
		t.Fatal("unselected episode was linked")
	}
	if _, err := LinkFiles(source, target, []string{episode}); err != nil {
		t.Fatalf("idempotent link: %v", err)
	}
	for _, path := range []string{"../outside", "missing.mkv", "Season 1"} {
		if _, err := LinkFiles(source, target, []string{path}); err == nil {
			t.Errorf("invalid selection accepted: %s", path)
		}
	}
	if err := os.Symlink(filepath.Join(source, episode), filepath.Join(source, "symlink.mkv")); err != nil {
		t.Fatal(err)
	}
	if _, err := LinkFiles(source, target, []string{"symlink.mkv"}); err == nil {
		t.Fatal("source symlink accepted")
	}
	conflicting := filepath.Join(base, "conflict")
	if err := os.MkdirAll(filepath.Join(conflicting, "Season 1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(conflicting, episode), []byte("unrelated"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LinkFiles(source, conflicting, []string{episode}); err == nil {
		t.Fatal("conflicting destination accepted")
	}
}
