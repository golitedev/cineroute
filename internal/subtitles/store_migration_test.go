package subtitles

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// TestStoreMigrationAddsLanguageColumns covers the upgrade path of a deployment
// that already has a subtitles.db: the multi-language columns are added to the
// existing tables instead of the store failing to open or losing its cached
// candidates.
func TestStoreMigrationAddsLanguageColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subtitles.db")

	// A database as written before multi-language support: candidate and attempt
	// rows exist, but without language or variant.
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	if _, err := legacy.Exec(`
CREATE TABLE subtitle_candidates (
  item_id TEXT NOT NULL,
  file_id INTEGER NOT NULL,
  rank INTEGER NOT NULL,
  score REAL NOT NULL,
  category TEXT NOT NULL,
  safe INTEGER NOT NULL DEFAULT 0,
  release TEXT NOT NULL DEFAULT '',
  reasons_json TEXT NOT NULL DEFAULT '[]',
  raw_json TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (item_id, file_id)
);
CREATE TABLE subtitle_attempts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  item_id TEXT NOT NULL,
  file_id INTEGER NOT NULL,
  status TEXT NOT NULL,
  category TEXT NOT NULL DEFAULT '',
  release TEXT NOT NULL DEFAULT '',
  metrics_json TEXT NOT NULL DEFAULT '',
  detail TEXT NOT NULL DEFAULT '',
  at TEXT NOT NULL
);
INSERT INTO subtitle_candidates (item_id, file_id, rank, score, category, safe, release)
  VALUES ('s_old', 42, 1, 120, 'strong', 1, 'Old.Release.1080p');
INSERT INTO subtitle_attempts (item_id, file_id, status, category, release, at)
  VALUES ('s_old', 42, 'accepted', 'strong', 'Old.Release.1080p', '2026-01-01T00:00:00Z');
`); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}

	st, err := openStore(path)
	if err != nil {
		t.Fatalf("openStore on a legacy database: %v", err)
	}
	t.Cleanup(func() { _ = st.close() })

	// The old rows survive and simply have no language.
	existing, err := st.loadCandidates("s_old")
	if err != nil {
		t.Fatalf("load legacy candidates: %v", err)
	}
	if len(existing) != 1 || existing[0].FileID != 42 || existing[0].Language != "" {
		t.Fatalf("legacy candidates = %+v, want one row without a language", existing)
	}
	attempts, err := st.loadAttempts("s_old")
	if err != nil {
		t.Fatalf("load legacy attempts: %v", err)
	}
	if len(attempts) != 1 || attempts[0].Status != "accepted" {
		t.Fatalf("legacy attempts = %+v", attempts)
	}

	// New rows round-trip the language and the Spanish variant.
	candidates := []Candidate{{
		FileID: 43, Rank: 1, Score: 150, Category: CategoryStrong, Safe: true,
		Release: "Movie.2019.1080p.LATINO", Language: "es-419", Variant: VariantLatin,
		Reasons: []string{"Latin American Spanish"}, Raw: json.RawMessage(`{"id":"43"}`),
	}}
	if err := st.replaceCandidates("s_new", candidates); err != nil {
		t.Fatalf("replaceCandidates: %v", err)
	}
	loaded, err := st.loadCandidates("s_new")
	if err != nil {
		t.Fatalf("loadCandidates: %v", err)
	}
	if len(loaded) != 1 || loaded[0].Language != "es-419" || loaded[0].Variant != VariantLatin {
		t.Fatalf("loaded candidates = %+v, want the language and variant kept", loaded)
	}

	attempt := Attempt{ItemID: "s_new", FileID: 43, Language: "es-419", Status: "accepted", At: time.Now()}
	if err := st.addAttempt(attempt); err != nil {
		t.Fatalf("addAttempt: %v", err)
	}
	stored, err := st.loadAttempts("s_new")
	if err != nil {
		t.Fatalf("loadAttempts: %v", err)
	}
	if len(stored) != 1 || stored[0].Language != "es-419" {
		t.Fatalf("stored attempts = %+v, want the language kept", stored)
	}
}
