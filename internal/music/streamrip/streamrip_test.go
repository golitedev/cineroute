package streamrip

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"

	"cineroute/internal/music/config"
)

// script writes an executable shell script and returns its path.
func script(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "rip")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunAuthErrorIsRedactedAndFatal(t *testing.T) {
	sample, err := filepath.Abs("testdata/auth_error.txt")
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "job.log")
	res, err := Run(context.Background(), RunOptions{
		Command: script(t, "cat '"+sample+"'\nexit 1\n"),
		Folder:  t.TempDir(), URL: "https://play.qobuz.com/album/x", Quality: 3,
		LogPath: logPath, Secrets: []string{"SECRETTOKEN123"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Failure != FailureAuth || !res.Failure.Fatal() {
		t.Fatalf("failure = %v, want auth", res.Failure)
	}
	if res.ExitCode != 1 {
		t.Fatalf("exit code %d", res.ExitCode)
	}
	logged, _ := os.ReadFile(logPath)
	for _, s := range []string{res.Tail, string(logged), strings.Join(res.Errors, "\n")} {
		if strings.Contains(s, "SECRETTOKEN123") {
			t.Fatal("token leaked into output")
		}
	}
	if !strings.Contains(string(logged), "***") {
		t.Fatal("log should contain the masked token")
	}
}

func TestRunCollectsTrackErrors(t *testing.T) {
	out := `[10:00:00] INFO     Downloading album                                         main.py:10
[10:00:05] ERROR    Persistent error downloading track 'Rosa Parks', skipping: Server disconnected     track.py:72
[10:00:06] ERROR    Error getting downloadable data for track 7 [123]: Track not available
                    for streaming.                                                  track.py:150
[10:00:07] INFO     done                                                            main.py:20
`
	p := filepath.Join(t.TempDir(), "out.txt")
	os.WriteFile(p, []byte(out), 0o644)
	res, err := Run(context.Background(), RunOptions{Command: script(t, "cat '"+p+"'\n"), Folder: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"Persistent error downloading track 'Rosa Parks', skipping: Server disconnected",
		"Error getting downloadable data for track 7 [123]: Track not available for streaming.",
	}
	if strings.Join(res.Errors, "|") != strings.Join(want, "|") {
		t.Fatalf("errors = %q", res.Errors)
	}
	if res.Failure != FailureNone || res.ExitCode != 0 {
		t.Fatalf("unexpected failure %v exit %d", res.Failure, res.ExitCode)
	}
}

func TestRunCancelKillsChildren(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "child-alive")
	cmd := script(t, "(sleep 3; touch '"+marker+"') &\nsleep 30\n")
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()
	start := time.Now()
	if _, err := Run(ctx, RunOptions{Command: cmd, Folder: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("cancel did not stop rip promptly")
	}
	time.Sleep(3500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a child process survived the cancel")
	}
}

func TestRunTimeout(t *testing.T) {
	res, err := Run(context.Background(), RunOptions{Command: script(t, "sleep 30\n"), Folder: t.TempDir(), Timeout: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut {
		t.Fatal("expected a timeout")
	}
}

func TestRunMissingBinary(t *testing.T) {
	if _, err := Run(context.Background(), RunOptions{Command: filepath.Join(t.TempDir(), "nope")}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestClassify(t *testing.T) {
	cases := map[string]Failure{
		"IneligibleError: Free accounts are not eligible to download tracks.":             FailureIneligible,
		"InvalidAppIdError: Invalid app id from params":                                   FailureAppID,
		"OSError: [Errno 28] No space left on device":                                     FailureDiskFull,
		"aiohttp.client_exceptions.ClientResponseError: 429, message='Too Many Requests'": FailureRateLimited,
		"MissingCredentialsError":                                                         FailureMissingCredentials,
		"INFO Downloading Unauthorized Biography":                                         FailureNone,
	}
	for line, want := range cases {
		if got := classify(strings.ToLower(line)); got != want {
			t.Errorf("classify(%q) = %v, want %v", line, got, want)
		}
	}
}

// ripForTest returns a rip binary: the real one when CINEROUTE_MUSIC_TEST_RIP is set
// or rip is installed, otherwise a fake that writes a small default config.
func ripForTest(t *testing.T) string {
	if p := os.Getenv("CINEROUTE_MUSIC_TEST_RIP"); p != "" {
		return p
	}
	if p, err := exec.LookPath("rip"); err == nil {
		return p
	}
	return script(t, `
# usage: rip --config-path PATH config path
cat > "$2" <<'TOML'
[downloads]
folder = "/home/x/StreamripDownloads"
max_connections = 6
[qobuz]
quality = 3
app_id = ""
secrets = []
[database]
downloads_enabled = true
[conversion]
enabled = false
codec = "ALAC"
[filepaths]
folder_format = "{albumartist} - {title}"
track_format = "{tracknumber}. {title}"
[cli]
progress_bars = true
[misc]
version = "2.2.0"
check_for_updates = true
TOML
`)
}

func TestWriteConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Paths.Staging = filepath.Join(dir, "staging")
	cfg.Qobuz.UserID, cfg.Qobuz.Token, cfg.Qobuz.Quality = "42", "tok", 4
	cfg.Download.RipCommand = ripForTest(t)
	cfg.Metadata.Exclude = []string{"copyright"}
	cfg.Streamrip = map[string]any{
		"conversion": map[string]any{"enabled": true, "codec": "FLAC"},
		"database":   map[string]any{"downloads_enabled": true},
	}
	files := FilesIn(filepath.Join(dir, "data"))

	ignored, err := WriteConfig(context.Background(), &cfg, files)
	if err != nil {
		t.Fatal(err)
	}
	if len(ignored) != 1 || ignored[0] != "database.downloads_enabled" {
		t.Fatalf("ignored = %v", ignored)
	}
	doc, err := readTOML(files.Config)
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) any {
		var cur any = doc
		for _, p := range strings.Split(path, ".") {
			m, _ := cur.(map[string]any)
			cur = m[p]
		}
		return cur
	}
	checks := map[string]any{
		"downloads.folder":           cfg.Paths.Staging,
		"qobuz.quality":              int64(4),
		"qobuz.use_auth_token":       true,
		"qobuz.email_or_userid":      "42",
		"qobuz.password_or_token":    "tok",
		"database.downloads_enabled": false,
		"conversion.enabled":         true,
		"conversion.codec":           "FLAC",
		"filepaths.folder_format":    "{id}",
		"cli.progress_bars":          false,
		"misc.check_for_updates":     false,
	}
	for path, want := range checks {
		if got := get(path); got != want {
			t.Errorf("%s = %#v, want %#v", path, got, want)
		}
	}
	if ex, _ := get("metadata.exclude").([]any); len(ex) != 1 || ex[0] != "copyright" {
		t.Errorf("metadata.exclude = %#v", get("metadata.exclude"))
	}
	if v, _ := get("misc.version").(string); v == "" {
		t.Error("streamrip's config version must be kept")
	}

	// streamrip caches the app id and secrets; a rewrite keeps them.
	setPath(doc, "qobuz.app_id", "999")
	setPath(doc, "qobuz.secrets", []any{"s1"})
	b, _ := toml.Marshal(doc)
	os.WriteFile(files.Config, b, 0o600)
	if _, err := WriteConfig(context.Background(), &cfg, files); err != nil {
		t.Fatal(err)
	}
	if CachedAppID(files) != "999" {
		t.Fatal("cached app id was lost")
	}
	if err := ForgetAppID(files); err != nil || CachedAppID(files) != "" {
		t.Fatalf("ForgetAppID: %v", err)
	}
}
