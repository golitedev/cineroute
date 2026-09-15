package companion

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCopyInspectionDistinguishesMissingFromEmpty covers the states the
// companion list depends on: a folder that does not exist is "absent", while a
// folder that exists without video is "none". They must not collapse into one
// value, because a remote-only title is recognized by its main folder being
// absent rather than merely empty.
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
	if got, want := remote.Quality, "none"; got != want {
		t.Errorf("empty remote quality: got %q want %q", got, want)
	}

	// A folder that is not there at all is absent, not empty.
	missingRemote := filepath.Join(remoteRoot, "Not There (1999)")
	if got, want := inspectRemoteMovieFolder(missingRemote, "Not There (1999)").Quality, copyQualityAbsent; got != want {
		t.Errorf("missing remote quality: got %q want %q", got, want)
	}
	missingMain := filepath.Join(mainRoot, "Not There (1999)")
	if got, want := inspectMovieFolder(missingMain, "Not There (1999)").Quality, copyQualityAbsent; got != want {
		t.Errorf("missing main quality: got %q want %q", got, want)
	}
	missingTV := filepath.Join(remoteRoot, "Not There (1999)")
	if got, want := inspectRemoteTVFolder(missingTV, "Not There (1999)").Quality, copyQualityAbsent; got != want {
		t.Errorf("missing remote TV quality: got %q want %q", got, want)
	}
}

// TestUpdateInspectionRecordsRemoteOnlyCopy verifies the persisted state behind
// the companion list for a title that only exists in the remote root, which is
// the case displayed as "main · remote" with remote highlighted.
func TestUpdateInspectionRecordsRemoteOnlyCopy(t *testing.T) {
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

	movie := &Movie{ID: "c_test", FolderName: folderName}
	updateMovieInspection(movie, filepath.Join(mainRoot, folderName), remoteFolder, folderName)

	if movie.ExistingCopy != copyQualityAbsent {
		t.Errorf("ExistingCopy: got %q want %q", movie.ExistingCopy, copyQualityAbsent)
	}
	if movie.RemoteCopy != "1080p" {
		t.Errorf("RemoteCopy: got %q want %q", movie.RemoteCopy, "1080p")
	}
	if len(movie.RemoteFiles) != 1 {
		t.Errorf("RemoteFiles: got %d want 1", len(movie.RemoteFiles))
	}
	// No main video files is what keeps the main half of "main · remote" grey.
	if len(movie.ExistingFiles) != 0 {
		t.Errorf("ExistingFiles: got %d want 0", len(movie.ExistingFiles))
	}
}
