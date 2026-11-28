package audiobooks

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cineroute/internal/config"
)

func TestTelegramImportPublishesOnceAfterLostConfirmation(t *testing.T) {
	w, telegramRoot, library, bot := testWorker(t)
	audio := makeAudio(t, telegramRoot, "book.m4b", "440", "An Author", "A Book")
	bot.paths["first"] = filepath.Join(telegramRoot, "book.m4b")
	msg := documentMessage(55, 10, "first", "forwarded.M4B")
	bot.failNextReply = true
	if err := w.handle(t.Context(), msg, 0); err == nil {
		t.Fatal("expected the first confirmation to fail")
	}
	path := filepath.Join(library, "An Author", "A Book", "A Book.m4b")
	assertAudio(t, path, audio)
	if err := w.handle(t.Context(), msg, 0); err != nil {
		t.Fatal(err)
	}
	assertAudio(t, path, audio)
	if got := bot.reads; got != 1 {
		t.Errorf("expected one download for the replayed message, got %d", got)
	}
	if !strings.Contains(bot.lastReply, "Imported A Book") {
		t.Errorf("unexpected reply %q", bot.lastReply)
	}

	makeAudio(t, telegramRoot, "another.m4b", "650", "An Author", "A Book")
	bot.paths["second"] = filepath.Join(telegramRoot, "another.m4b")
	if err := w.handle(t.Context(), documentMessage(55, 11, "second", "different.m4b"), 0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bot.lastReply, "already uses that folder") {
		t.Errorf("expected a collision reply, got %q", bot.lastReply)
	}
	assertAudio(t, path, audio)
	if err := w.handle(t.Context(), textMessage(55, 12, "/details 11 An Author | Another Edition"), 0); err != nil {
		t.Fatal(err)
	}
	otherAudio, err := os.ReadFile(bot.paths["second"])
	if err != nil {
		t.Fatal(err)
	}
	assertAudio(t, filepath.Join(library, "An Author", "Another Edition", "Another Edition.m4b"), otherAudio)
	assertAudio(t, path, audio)
}

func TestTelegramImportStagesMissingMetadataAndRestrictsAccess(t *testing.T) {
	w, telegramRoot, library, bot := testWorker(t)
	audio := makeAudio(t, telegramRoot, "untagged.m4b", "440", "", "")
	bot.paths["book"] = filepath.Join(telegramRoot, "untagged.m4b")
	if err := w.handle(t.Context(), documentMessage(99, 10, "book", "unknown.m4b"), 0); err != nil {
		t.Fatal(err)
	}
	if bot.reads != 0 || bot.lastReply != "" {
		t.Fatal("unauthorized attachment triggered a download or reply")
	}
	group := documentMessage(55, 11, "book", "unknown.m4b")
	group.Chat.Type = "group"
	if err := w.handle(t.Context(), group, 0); err != nil {
		t.Fatal(err)
	}
	if bot.reads != 0 {
		t.Fatal("a group attachment triggered a download")
	}

	if err := w.handle(t.Context(), documentMessage(55, 12, "book", "unknown.m4b"), 0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bot.lastReply, "/details 12") {
		t.Errorf("expected a metadata prompt, got %q", bot.lastReply)
	}
	if dirs, err := os.ReadDir(library); err != nil || len(dirs) != 0 {
		t.Fatalf("library should be empty while metadata is pending, got %v, %v", dirs, err)
	}

	// A fresh worker has no in-memory import history.
	restarted := &worker{
		api: w.api, allowed: w.allowed, staging: w.staging, library: w.library, maxFileBytes: w.maxFileBytes,
	}
	if err := restarted.handle(t.Context(), textMessage(56, 13, "/details 12 Someone Else | Stolen Book"), 0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bot.lastReply, "no staged audiobook") {
		t.Errorf("expected the import to be scoped to its originating chat, got %q", bot.lastReply)
	}
	if err := restarted.handle(t.Context(), textMessage(55, 14, "/details 12 Book Author | Book Title"), 0); err != nil {
		t.Fatal(err)
	}
	assertAudio(t, filepath.Join(library, "Book Author", "Book Title", "Book Title.m4b"), audio)

	outside := filepath.Join(t.TempDir(), "outside.m4b")
	if err := os.WriteFile(outside, audio, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(telegramRoot, "link.m4b")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	bot.paths["outside"] = outside
	bot.paths["symlink"] = link
	for i, id := range []string{"outside", "symlink"} {
		if err := w.handle(t.Context(), documentMessage(55, int64(20+i), id, "unsafe.m4b"), 0); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(bot.lastReply, "couldn't stage") {
			t.Errorf("expected an external path to be rejected, got %q", bot.lastReply)
		}
	}
}

func TestTelegramServicePollsAndAcknowledgesUpdates(t *testing.T) {
	ffprobe(t)
	telegramRoot, staging, library := t.TempDir(), t.TempDir(), t.TempDir()
	received := make(chan string, 1)
	confirmed := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bot123:fake-token/getUpdates":
			var req struct{ Offset int64 }
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
				rw.WriteHeader(http.StatusBadRequest)
				return
			}
			if req.Offset == 0 {
				writeResult(t, rw, []update{{ID: 42, Message: textMessage(55, 1, "/id")}})
				return
			}
			if req.Offset != 43 {
				t.Errorf("polled offset %d, want 43", req.Offset)
			}
			select {
			case confirmed <- struct{}{}:
			default:
			}
			<-r.Context().Done()
		case "/bot123:fake-token/sendMessage":
			var req struct{ Text string }
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			received <- req.Text
			writeResult(t, rw, true)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			rw.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	service, err := Start(ctx, config.Audiobooks{
		Enabled: true, BotAPIURL: server.URL, BotToken: "123:fake-token",
		TelegramRoot: telegramRoot, StagingRoot: staging, LibraryRoot: library,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	select {
	case <-confirmed:
		if got := <-received; got != "Your Telegram user ID is 55." {
			t.Errorf("reply was %q", got)
		}
	case <-ctx.Done():
		t.Fatal("worker failed to acknowledge the Telegram update")
	}
}

type fakeBot struct {
	mu            sync.Mutex
	paths         map[string]string
	reads         int
	lastReply     string
	failNextReply bool
}

func testWorker(t *testing.T) (*worker, string, string, *fakeBot) {
	t.Helper()
	probe := ffprobe(t)
	a, b, c := t.TempDir(), t.TempDir(), t.TempDir()
	open := func(path string) *os.Root {
		root, err := os.OpenRoot(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { root.Close() })
		return root
	}
	bot := &fakeBot{paths: make(map[string]string)}
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		bot.mu.Lock()
		defer bot.mu.Unlock()
		switch r.URL.Path {
		case "/bot123:fake/getFile":
			var req struct {
				FileID string `json:"file_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			bot.reads++
			writeResult(t, rw, map[string]string{"file_path": bot.paths[req.FileID]})
		case "/bot123:fake/sendMessage":
			var req struct{ Text string }
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			if bot.failNextReply {
				bot.failNextReply = false
				rw.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			bot.lastReply = req.Text
			writeResult(t, rw, true)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			rw.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return &worker{
		api: newTelegram(server.URL, "123:fake"), allowed: map[int64]bool{55: true, 56: true}, ffprobe: probe,
		telegramPath: a, telegramRoot: open(a), staging: open(b), library: open(c), maxFileBytes: 1 << 20,
	}, a, c, bot
}

func writeResult(t *testing.T, w http.ResponseWriter, result any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result}); err != nil {
		t.Error(err)
	}
}

func textMessage(user, id int64, text string) *message {
	m := &message{ID: id, Text: text}
	m.From.ID, m.Chat.ID, m.Chat.Type = user, user, "private"
	return m
}

func documentMessage(user, id int64, fileID, filename string) *message {
	m := textMessage(user, id, "")
	m.Document = &attachment{ID: fileID, Name: filename}
	return m
}

func assertAudio(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(want, got) {
		t.Fatalf("published audio does not match the source at %s: %v", path, err)
	}
}

func ffprobe(t *testing.T) string {
	t.Helper()
	binary, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is not installed")
	}
	return binary
}

func makeAudio(t *testing.T, dir, name, frequency, author, title string) []byte {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	path := filepath.Join(dir, name)
	cmd := exec.CommandContext(t.Context(), "ffmpeg", "-v", "error", "-f", "lavfi", "-i", "sine=frequency="+frequency,
		"-t", "0.2", "-c:a", "aac", "-metadata", "artist="+author, "-metadata", "album="+title, "-f", "mp4", path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make sample audiobook: %v: %s", err, output)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestTelegramQueuesSecondBookWhileFirstIsDownloading(t *testing.T) {
	ffprobe(t)
	telegramRoot, staging, library := t.TempDir(), t.TempDir(), t.TempDir()
	makeAudio(t, telegramRoot, "first.m4b", "440", "An Author", "First Book")
	makeAudio(t, telegramRoot, "second.m4b", "650", "An Author", "Second Book")

	type notice struct {
		id   int64
		text string
	}
	notices := make(chan notice, 20)
	firstDownloading := make(chan struct{})
	releaseFirst := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bot123:fake-token/getUpdates":
			var req struct{ Offset int64 }
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
				return
			}
			switch req.Offset {
			case 0:
				writeResult(t, rw, []update{{ID: 41, Message: documentMessage(55, 11, "first", "first.m4b")}})
			case 42:
				select {
				case <-firstDownloading:
					writeResult(t, rw, []update{{ID: 42, Message: documentMessage(55, 12, "second", "second.m4b")}})
				case <-r.Context().Done():
				}
			default:
				<-r.Context().Done()
			}
		case "/bot123:fake-token/getFile":
			var req struct {
				ID string `json:"file_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
				return
			}
			if req.ID == "first" {
				close(firstDownloading)
				select {
				case <-releaseFirst:
				case <-r.Context().Done():
					return
				}
			}
			writeResult(t, rw, map[string]string{"file_path": filepath.Join(telegramRoot, req.ID+".m4b")})
		case "/bot123:fake-token/sendMessage", "/bot123:fake-token/editMessageText":
			var req struct {
				Text  string
				ID    int64 `json:"message_id"`
				Reply struct {
					ID int64 `json:"message_id"`
				} `json:"reply_parameters"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
				return
			}
			id := req.ID
			if strings.HasSuffix(r.URL.Path, "/sendMessage") {
				if req.Reply.ID == 0 {
					t.Errorf("unexpected standalone message: %q", req.Text)
				}
				id = req.Reply.ID + 100
			}
			notices <- notice{id: id, text: req.Text}
			writeResult(t, rw, map[string]int64{"message_id": id})
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			rw.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	service, err := Start(ctx, config.Audiobooks{
		Enabled: true, BotAPIURL: server.URL, BotToken: "123:fake-token", AllowedUserIDs: []int64{55},
		TelegramRoot: telegramRoot, StagingRoot: staging, LibraryRoot: library,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	expect := func(id int64, text string) {
		t.Helper()
		select {
		case got := <-notices:
			if got.id != id || !strings.Contains(got.text, text) {
				t.Fatalf("status was (%d, %q), want (%d, %q)", got.id, got.text, id, text)
			}
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %q", text)
		}
	}
	expect(111, "Queued")
	expect(111, "Downloading")
	expect(112, "Queued")
	close(releaseFirst)
	expect(111, "Preparing")
	expect(111, "Imported First Book")
	expect(112, "Downloading")
	expect(112, "Preparing")
	expect(112, "Imported Second Book")
}
