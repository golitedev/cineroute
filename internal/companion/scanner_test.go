package companion

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCopyInspectionDistinguishesMissingFromEmpty covers the companion list
// display: a folder that does not exist is "absent" and a folder that exists
// without video is "none". They must not collapse into the same state, because
// the list shows which side actually holds the title.
func TestCopyInspectionDistinguishesMissingFromEmpty(t *testing.T) {
	base := t.TempDir()
	folderName := "The Thing (1982)"
	mainRoot := filepath.Join(base, "movies")
	remoteRoot := filepath.Join(base, "movies-remote")
	for _, dir := range []string{mainRoot, remoteRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Main copy exists and holds a 1080p video.
	mainFolder := filepath.Join(mainRoot, folderName)
	if err := os.MkdirAll(mainFolder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mainFolder, folderName+" - 1080p.mkv"), []byte("v"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Remote list entry exists but is still empty (created before its copy).
	remoteFolder := filepath.Join(remoteRoot, folderName)
	if err := os.MkdirAll(remoteFolder, 0o755); err != nil {
		t.Fatal(err)
	}

	main, remote := inspectMovieCopies(mainFolder, remoteFolder, folderName)
	if got, want := main.Quality, "1080p"; got != want {
		t.Errorf("main quality: got %q want %q", got, want)
	}
	if !main.Exists {
		t.Error("main folder exists but Exists is false")
	}
	if got, want := remote.Quality, "none"; got != want {
		t.Errorf("empty remote quality: got %q want %q", got, want)
	}
	if !remote.Exists {
		t.Error("empty remote folder exists but Exists is false")
	}

	// Missing remote folder: "absent", because the display distinguishes a
	// folder that is not there from one that is there and empty.
	missingRemote := filepath.Join(remoteRoot, "Not There (1999)")
	absent := inspectRemoteMovieFolder(missingRemote, "Not There (1999)")
	if got, want := absent.Quality, copyQualityAbsent; got != want {
		t.Errorf("missing remote quality: got %q want %q", got, want)
	}
	if absent.Exists {
		t.Error("missing remote folder must not report Exists")
	}

	// Missing main folder behaves the same way.
	missingMain := filepath.Join(mainRoot, "Not There (1999)")
	if inspection := inspectMovieFolder(missingMain, "Not There (1999)"); inspection.Quality != copyQualityAbsent || inspection.Exists {
		t.Errorf("missing main folder: quality=%q exists=%v", inspection.Quality, inspection.Exists)
	}
}

// TestUpdateInspectionRecordsFolderPresence verifies the persisted state that
// the companion list reads: Exists flags and absence sentinels must be stored
// on the movie, not just computed locally.
func TestUpdateInspectionRecordsFolderPresence(t *testing.T) {
	base := t.TempDir()
	folderName := "Allegiant (2016)"
	mainRoot := filepath.Join(base, "movies")
	remoteRoot := filepath.Join(base, "movies-remote")
	for _, dir := range []string{mainRoot, remoteRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	remoteFolder := filepath.Join(remoteRoot, folderName)
	if err := os.MkdirAll(remoteFolder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remoteFolder, folderName+" - 1080p.mkv"), []byte("v"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Main folder is missing and the title only exists in the remote root,
	// which is the case the companion list shows as main · remote.
	movie := &Movie{ID: "c_test", FolderName: folderName}
	updateMovieInspection(movie, filepath.Join(mainRoot, folderName), remoteFolder, folderName)

	if movie.ExistingCopy != copyQualityAbsent {
		t.Errorf("ExistingCopy: got %q want %q", movie.ExistingCopy, copyQualityAbsent)
	}
	if movie.MainExists {
		t.Error("MainExists must be false when the main folder is not present")
	}
	if !movie.RemoteFolderExists {
		t.Error("RemoteFolderExists must be true for an existing remote folder")
	}
	if movie.RemoteCopy != "1080p" {
		t.Errorf("RemoteCopy: got %q want %q", movie.RemoteCopy, "1080p")
	}
	if len(movie.RemoteFiles) != 1 {
		t.Errorf("RemoteFiles: got %d want 1", len(movie.RemoteFiles))
	}
	if len(movie.ExistingFiles) != 0 {
		t.Errorf("ExistingFiles: got %d want 0", len(movie.ExistingFiles))
	}
}
