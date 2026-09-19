package subtitles

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestProberWithRealBinaries exercises the actual ffmpeg/ffprobe command lines.
// The unit tests use a fake prober, so a wrong flag (for example `-nostdin`,
// which ffprobe rejects) would otherwise silently disable embedded-subtitle
// detection on every real deployment. The test is skipped when the tools are not
// installed, so it still runs anywhere they are available.
func TestProberWithRealBinaries(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe is not installed")
	}

	dir := t.TempDir()
	subtitlePath := filepath.Join(dir, "ref.srt")
	if err := os.WriteFile(subtitlePath, []byte(referenceSRT), 0o644); err != nil {
		t.Fatalf("write subtitle fixture: %v", err)
	}
	videoPath := filepath.Join(dir, "Sample.2019.1080p.WEB-DL.mkv")

	// A tiny real video with one embedded SubRip stream tagged "eng".
	build := exec.Command(ffmpeg,
		"-v", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=duration=2:size=128x72:rate=5",
		"-f", "srt", "-i", subtitlePath,
		"-map", "0:v", "-map", "1:0",
		"-c:v", "mpeg4", "-c:s", "srt",
		"-metadata:s:s:0", "language=eng",
		"-shortest", videoPath)
	var buildErr bytes.Buffer
	build.Stderr = &buildErr
	if err := build.Run(); err != nil {
		t.Skipf("cannot build a fixture video (%v): %s", err, strings.TrimSpace(buildErr.String()))
	}

	prober := ExecProber{FFmpegPath: ffmpeg, FFprobePath: "ffprobe", ProbeTimeout: 30 * time.Second, ExtractTimeout: 60 * time.Second}
	ctx := context.Background()

	info, err := prober.Probe(ctx, videoPath)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if len(info.Streams) != 1 {
		t.Fatalf("Probe found %d subtitle streams, want 1: %+v", len(info.Streams), info.Streams)
	}
	stream := info.Streams[0]
	if !stream.Usable {
		t.Errorf("stream should be usable: %+v", stream)
	}
	if stream.Language != "en" {
		t.Errorf("language = %q, want en (normalized from eng)", stream.Language)
	}
	if info.DurationMS <= 0 {
		t.Errorf("duration was not detected: %d", info.DurationMS)
	}

	// Extraction must produce a parseable SRT reference.
	extracted := filepath.Join(dir, "extracted.srt")
	if err := prober.ExtractSubtitle(ctx, videoPath, stream.Index, extracted); err != nil {
		t.Fatalf("ExtractSubtitle: %v", err)
	}
	cues, err := CueTimesFile(extracted)
	if err != nil {
		t.Fatalf("extracted reference is not usable: %v", err)
	}
	if len(cues) == 0 {
		t.Fatal("extracted reference has no cues")
	}

	// Normalization of an external subtitle must work too.
	normalized := filepath.Join(dir, "normalized.srt")
	if err := prober.ConvertToSRT(ctx, subtitlePath, normalized); err != nil {
		t.Fatalf("ConvertToSRT: %v", err)
	}
	if !IsValidSRTFile(normalized) {
		t.Fatal("normalized reference is not a valid SRT")
	}
}
