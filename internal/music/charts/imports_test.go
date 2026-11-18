package charts

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"cineroute/internal/music/config"
	"cineroute/internal/music/jobs"
)

func TestImportHistoryRecoversExactPathsAndExportsThem(t *testing.T) {
	ctx := t.Context()
	paths := config.Paths{Data: t.TempDir(), Library: "/music", HostLibrary: "/mnt/music"}
	queue := &testQueue{job: jobs.Job{ID: "retained", AlbumID: "qobuz-1", Status: jobs.StatusCompleted,
		LibraryPath: "Ártist/Release, Deluxe [FLAC]", ChartLogged: true}}
	store, err := openStore(paths.Data)
	if err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 11, 18, 11, 0, 0, 0, time.UTC)
	for _, id := range []string{"retained", "gone"} {
		receipt := Import{JobID: id, AlbumID: "qobuz-1", Date: when, Artist: "Ártist", Album: "=Release",
			Sources: []jobs.ChartSource{{Playlist: "Top 10", Track: "Song", ChartDate: "2026-11-16"}}}
		raw, err := json.Marshal(receipt)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.ExecContext(ctx, "INSERT INTO chart_import VALUES (?, ?, ?)", id, when.Unix(), raw); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	manager, err := New(paths, nil, nil, queue)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	queue.job = jobs.Job{}
	manager, err = New(paths, nil, nil, queue)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	const folder = "/mnt/music/Ártist/Release, Deluxe [FLAC]"
	imports, more, err := manager.Imports(ctx, 0)
	if err != nil || more || len(imports) != 2 || imports[0].FolderPath != folder || imports[1].FolderPath != "" {
		t.Fatalf("imports = %+v, more = %t, error = %v", imports, more, err)
	}
	data, err := manager.ImportsCSV(ctx)
	if err != nil {
		t.Fatal(err)
	}
	records, err := csv.NewReader(bytes.NewReader(data)).ReadAll()
	want := []string{"2026-11-18T11:00:00Z", "Ártist", "'=Release", "Release, Deluxe [FLAC]", folder,
		"Complete", "Top 10 · Song · 2026-11-16", "retained"}
	if err != nil || len(records) != 3 || !slices.Equal(records[1], want) || records[2][4] != "" {
		t.Fatalf("CSV = %q, error = %v", records, err)
	}
}
