package torrentmeta

import (
	"crypto/sha1"
	"strings"
	"testing"
)

func TestParseSingleFile(t *testing.T) {
	raw := makeTorrent("Toy.Story.1995.mkv", nil)
	mi, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if mi.Kind != KindSingleFile || mi.RootFolder {
		t.Fatalf("want single file, root_folder=false; got %s/%v", mi.Kind, mi.RootFolder)
	}
	if mi.Name != "Toy.Story.1995.mkv" || mi.Size != 1234 {
		t.Fatalf("unexpected name/size: %q %d", mi.Name, mi.Size)
	}
	if mi.InfoHashV1 == "" {
		t.Fatal("missing v1 hash")
	}
	// The v1 hash must be sha1 of the raw info span.
	infoStart := strings.Index(string(raw), "d4:name") + len("d4:name") - 1
	_ = infoStart
	start := strings.Index(string(raw), "4:info") + len("4:info")
	// find matching 'e' of the info dict: it is the last byte for our fixture
	end := len(raw) - 1
	sum := sha1.Sum(raw[start:end])
	want := strings.ToLower(hexEncode(sum[:]))
	if mi.InfoHashV1 != want {
		t.Fatalf("hash mismatch:\n got %s\nwant %s", mi.InfoHashV1, want)
	}
	if got := mi.ContentPath("/m4/Toy Story (1995)"); got != "/m4/Toy Story (1995)/Toy.Story.1995.mkv" {
		t.Fatalf("content path: %s", got)
	}
}

func hexEncode(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hexdigits[v>>4]
		out[i*2+1] = hexdigits[v&0xf]
	}
	return string(out)
}

func TestParseMultiFile(t *testing.T) {
	raw := makeTorrent("Movie.2026.GROUP", map[string]int64{
		"Movie.2026.GROUP.mkv": 1000,
		"Subtitles/en.srt":     42,
	})
	mi, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if mi.Kind != KindRootedMulti || !mi.RootFolder {
		t.Fatalf("want rooted multi, root_folder=true; got %s/%v", mi.Kind, mi.RootFolder)
	}
	if mi.RootName != "Movie.2026.GROUP" || mi.Size != 1042 {
		t.Fatalf("unexpected root/size: %q %d", mi.RootName, mi.Size)
	}
	got := mi.RelPaths()
	want := []string{"Movie.2026.GROUP.mkv", "Subtitles/en.srt"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("rel paths: %v", got)
	}
	if got := mi.FullPaths(); len(got) != 2 || got[0] != "Movie.2026.GROUP/Movie.2026.GROUP.mkv" || got[1] != "Movie.2026.GROUP/Subtitles/en.srt" {
		t.Fatalf("full paths: %v", got)
	}
	if got := mi.ContentPath("/m2/Movie Name (2026)"); got != "/m2/Movie Name (2026)/Movie.2026.GROUP" {
		t.Fatalf("content path: %s", got)
	}
}

func TestParseV2RootedAndRootless(t *testing.T) {
	rooted, err := Parse(makeV2Torrent("Subs", "Subs", "Episode 1.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	if !rooted.RootFolder || rooted.RootName != "Subs" || rooted.Size != 1000 {
		t.Fatalf("rooted: %+v", rooted)
	}
	if got := rooted.RelPaths(); len(got) != 1 || got[0] != "Episode 1.mkv" {
		t.Fatalf("rooted rel paths: %v", got)
	}
	if got := rooted.FullPaths(); len(got) != 1 || got[0] != "Subs/Episode 1.mkv" {
		t.Fatalf("rooted full paths: %v", got)
	}

	rootless, err := Parse(makeV2Torrent("My.Show", "Subs", "Episode 1.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	if rootless.RootFolder {
		t.Fatal("expected rootless")
	}
	if got := rootless.RelPaths(); len(got) != 1 || got[0] != "Subs/Episode 1.mkv" {
		t.Fatalf("rootless rel paths: %v", got)
	}
	if got := rootless.FullPaths(); len(got) != 1 || got[0] != "Subs/Episode 1.mkv" {
		t.Fatalf("rootless full paths: %v", got)
	}
	if got := rootless.ContentPath("/t1/My Show (2020)"); got != "/t1/My Show (2020)" {
		t.Fatalf("rootless content path: %s", got)
	}
}

func TestPureV2Hashes(t *testing.T) {
	mi, err := Parse(makeV2Torrent("Subs", "Subs", "Episode 1.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	if mi.HasV1 {
		t.Fatal("pure v2 must not have a v1 component")
	}
	if !mi.HasV2 || mi.InfoHashV2 == "" || len(mi.InfoHashV2) != 64 {
		t.Fatalf("v2 hash: %q", mi.InfoHashV2)
	}
	if mi.InfoHashV1 != "" {
		t.Fatalf("pure v2 must not carry a v1 hash, got %q", mi.InfoHashV1)
	}
	if mi.PrimaryHash() != mi.InfoHashV2 {
		t.Fatalf("primary hash should be v2: %q", mi.PrimaryHash())
	}
	if mi.QueryHashes() != mi.InfoHashV2 {
		t.Fatalf("query hashes: %q", mi.QueryHashes())
	}
}

func TestHybridHashes(t *testing.T) {
	// Hybrid = pieces (v1) + file tree (v2) in the same info dict.
	tree := "d" + beStr("Ep1.mkv") + "d" + "0:" + "d" + beStr("length") + beInt(1000) + "e" + "e" + "e"
	info := "d" + beStr("file tree") + tree +
		beStr("meta version") + beInt(2) +
		beStr("name") + beStr("Hybrid.Show.S01") +
		beStr("piece length") + beInt(16384) +
		beStr("pieces") + beStr(string(make([]byte, 20))) + "e"
	raw := []byte("d" + beStr("info") + info + "e")
	mi, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !mi.HasV1 || !mi.HasV2 {
		t.Fatalf("hybrid: HasV1=%v HasV2=%v", mi.HasV1, mi.HasV2)
	}
	if mi.InfoHashV1 == "" || mi.InfoHashV2 == "" {
		t.Fatal("hybrid must carry both hashes")
	}
	// qBittorrent reports the v1 hash for hybrid torrents.
	if mi.PrimaryHash() != mi.InfoHashV1 {
		t.Fatalf("hybrid primary hash should be v1: %q", mi.PrimaryHash())
	}
}

func TestRejectTraversal(t *testing.T) {
	raw := makeTorrent("Evil", map[string]int64{
		"../escape.txt": 5,
	})
	if _, err := Parse(raw); err == nil {
		t.Fatal("expected traversal rejection")
	}
}

func TestRejectDuplicateKeys(t *testing.T) {
	raw := "d4:info" + "d" + "4:name" + "1:x" + "4:name" + "1:y" + "e" + "e"
	if _, err := Parse([]byte(raw)); err != ErrDuplicateKey {
		t.Fatalf("expected duplicate key error, got %v", err)
	}
}

func TestRejectTrailingGarbage(t *testing.T) {
	raw := append(makeTorrent("A", nil), 'x')
	if _, err := Parse(raw); err == nil {
		t.Fatal("expected trailing garbage rejection")
	}
}
