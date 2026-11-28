// Package audiobooks imports Telegram attachments into an Audiobookshelf library.
package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"cineroute/internal/config"
	"golang.org/x/sys/unix"
)

type Service struct {
	cancel context.CancelFunc
	done   chan struct{}
}

type worker struct {
	api          telegram
	allowed      map[int64]bool
	telegramPath string
	telegramRoot *os.Root
	staging      *os.Root
	library      *os.Root
	ffprobe      string
	maxFileBytes int64
}

func Start(ctx context.Context, cfg config.Audiobooks) (*Service, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		return nil, fmt.Errorf("find ffprobe: %w", err)
	}

	paths := []string{cfg.TelegramRoot, cfg.StagingRoot, cfg.LibraryRoot}
	roots := make([]*os.Root, 0, len(paths))
	closeRoots := func() {
		for _, root := range roots {
			root.Close()
		}
	}
	for _, path := range paths {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			closeRoots()
			return nil, fmt.Errorf("resolve audiobook directory %q: %w", path, err)
		}
		for _, root := range roots {
			if inside(root.Name(), resolved) || inside(resolved, root.Name()) {
				closeRoots()
				return nil, errors.New("Telegram, staging and audiobook library directories must be disjoint")
			}
		}
		root, err := os.OpenRoot(resolved)
		if err != nil {
			closeRoots()
			return nil, fmt.Errorf("open audiobook directory %q: %w", path, err)
		}
		roots = append(roots, root)
	}

	lock, err := roots[1].OpenFile(".cineroute-audiobooks.lock", os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		closeRoots()
		return nil, fmt.Errorf("open audiobook importer lock: %w", err)
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		closeRoots()
		return nil, fmt.Errorf("lock audiobook staging directory: %w", err)
	}

	w := worker{
		api: newTelegram(cfg.BotAPIURL, cfg.BotToken), allowed: make(map[int64]bool, len(cfg.AllowedUserIDs)),
		telegramPath: filepath.Clean(cfg.TelegramRoot), telegramRoot: roots[0], staging: roots[1], library: roots[2],
		ffprobe: ffprobe, maxFileBytes: cfg.MaxFileBytes,
	}
	if w.maxFileBytes == 0 {
		w.maxFileBytes = config.DefaultAudiobookFileLimit
	}
	for _, id := range cfg.AllowedUserIDs {
		w.allowed[id] = true
	}

	ctx, cancel := context.WithCancel(ctx)
	service := &Service{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(service.done)
		defer closeRoots()
		defer lock.Close()
		defer w.api.client.CloseIdleConnections()
		w.run(ctx)
	}()
	return service, nil
}

func inside(parent, path string) bool {
	rel, err := filepath.Rel(parent, path)
	return err == nil && filepath.IsLocal(rel)
}

func (s *Service) Close() {
	if s != nil {
		s.cancel()
		<-s.done
	}
}

func (w *worker) run(ctx context.Context) {
	var offset int64
	for ctx.Err() == nil {
		updates, err := w.api.poll(ctx, offset)
		if err == nil && len(updates) == 0 {
			// The successful poll confirmed the previous offset. Telegram
			// can assign a new random starting ID after a week of inactivity.
			offset = 0
		}
		for _, update := range updates {
			if update.ID <= 0 || (offset > 0 && update.ID < offset) {
				continue
			}
			if err = w.handle(ctx, update.Message); err != nil {
				break
			}
			offset = update.ID + 1
		}
		if err != nil && ctx.Err() == nil {
			slog.Warn("audiobook importer will retry Telegram polling", "err", err)
			delay := 5 * time.Second
			var response *telegramError
			if errors.As(err, &response) {
				delay = max(delay, response.retry)
			}
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
			}
		}
	}
}

func jobName(chatID, messageID int64) string {
	return fmt.Sprintf("import-%d-%d", chatID, messageID)
}

func (w *worker) handle(ctx context.Context, msg *message) error {
	if msg == nil || msg.ID <= 0 || msg.From.ID <= 0 || msg.Chat.Type != "private" || msg.Chat.ID != msg.From.ID {
		return nil
	}
	reply := func(text string) error { return w.api.send(ctx, msg.Chat.ID, text) }
	command, args, _ := strings.Cut(strings.TrimSpace(msg.Text), " ")
	if command == "/id" {
		return reply(fmt.Sprintf("Your Telegram user ID is %d.", msg.From.ID))
	}
	if !w.allowed[msg.From.ID] {
		return nil
	}
	switch command {
	case "/start", "/help":
		return reply("Send one complete M4B, M4A or MP3 audiobook in this chat. You can set the details in the caption as Book: Author | Title.")
	case "/details", "/cancel":
		return w.handleCommand(ctx, msg.Chat.ID, command, strings.TrimSpace(args))
	}

	item := msg.Audio
	if item == nil {
		item = msg.Document
	}
	if item == nil {
		return nil
	}
	ext := attachmentExtension(item)
	if formats[ext] == "" || item.ID == "" || msg.MediaGroup != "" {
		return reply("Send one complete audiobook as an M4B, M4A or MP3 file.")
	}
	if item.Size > w.maxFileBytes {
		return reply("That audiobook is larger than the configured file size limit.")
	}
	job := jobName(msg.Chat.ID, msg.ID)
	b, err := w.loadBook(job)
	if errors.Is(err, os.ErrNotExist) {
		b, err = w.stage(ctx, job, msg, item, ext)
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		slog.Warn("stage audiobook", "message_id", msg.ID, "err", err)
		switch {
		case errors.Is(err, errSize):
			return reply("The audiobook is empty or exceeds the file size limit.")
		case errors.Is(err, errAudio):
			return reply("I couldn't read that file as audio. Please send an M4B, M4A or MP3 audiobook.")
		default:
			return reply("I couldn't stage that file. Please try sending it again.")
		}
	}
	return w.finish(ctx, msg.Chat.ID, msg.ID, job, b)
}

func (w *worker) handleCommand(ctx context.Context, chatID int64, command, args string) error {
	reply := func(text string) error { return w.api.send(ctx, chatID, text) }
	idPart, metadata, _ := strings.Cut(args, " ")
	id, err := strconv.ParseInt(idPart, 10, 64)
	if err != nil || id <= 0 {
		if command == "/cancel" {
			return reply("Use /cancel ID with the ID from the import message.")
		}
		return reply("Use /details ID Author | Title with the ID from the import message.")
	}
	job := jobName(chatID, id)
	b, err := w.loadBook(job)
	if errors.Is(err, os.ErrNotExist) {
		return reply("There is no staged audiobook with that ID in this chat.")
	}
	if err != nil {
		slog.Warn("read staged audiobook", "message_id", id, "err", err)
		return reply("I couldn't read the staged audiobook.")
	}
	if command == "/cancel" {
		if err := w.staging.RemoveAll(job); err != nil {
			slog.Warn("remove staged audiobook", "message_id", id, "err", err)
			return reply("I couldn't remove the staged audiobook. Please try again.")
		}
		return reply("Cancelled the staged audiobook.")
	}

	b.Author, b.Title = details(metadata)
	if !b.ready() {
		return reply(fmt.Sprintf("Use /details %d Author | Title with a non-empty author and title.", id))
	}
	if err := w.saveBook(job, b); err != nil {
		slog.Warn("save staged audiobook details", "message_id", id, "err", err)
		return reply("I couldn't save those details. Please try again.")
	}
	return w.finish(ctx, chatID, id, job, b)
}

func (w *worker) finish(ctx context.Context, chatID, id int64, job string, b book) error {
	next := fmt.Sprintf("Send /details %d Author | Title, or /cancel %d to discard the staged file.", id, id)
	if !b.ready() {
		return w.api.send(ctx, chatID, fmt.Sprintf("I staged %q and need its author and title. %s", b.Filename, next))
	}
	if err := w.publish(ctx, job, b); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, errCollision) {
			return w.api.send(ctx, chatID, "A different audiobook already uses that folder. "+next)
		}
		slog.Warn("publish audiobook", "message_id", id, "err", err)
		return w.api.send(ctx, chatID, "I couldn't move the audiobook into the library. It is still staged. "+next)
	}
	if err := w.api.send(ctx, chatID, fmt.Sprintf("Imported %s by %s. It will appear in Audiobookshelf after the next scan.", b.Title, b.Author)); err != nil {
		return err
	}
	if err := w.staging.RemoveAll(job); err != nil {
		slog.Warn("clean up audiobook staging", "message_id", id, "err", err)
	}
	return nil
}
