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
	if err := roots[1].MkdirAll(inboxDir, 0o700); err != nil {
		lock.Close()
		closeRoots()
		return nil, fmt.Errorf("create Telegram audiobook inbox: %w", err)
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
	notify := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.consumeInbox(ctx, notify)
	}()
	defer func() { <-done }()

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
			if err = w.enqueue(ctx, update); err != nil {
				break
			}
			offset = update.ID + 1
			select {
			case notify <- struct{}{}:
			default:
			}
		}
		if err != nil && ctx.Err() == nil {
			slog.Warn("audiobook importer will retry Telegram polling", "err", err)
			waitToRetry(ctx, err)
		}
	}
}

func jobName(chatID, messageID int64) string {
	return fmt.Sprintf("import-%d-%d", chatID, messageID)
}

func (m *message) privateChat() bool {
	return m != nil && m.ID > 0 && m.From.ID > 0 && m.Chat.Type == "private" && m.Chat.ID == m.From.ID
}

func (m *message) attachment() *attachment {
	if m.Audio != nil {
		return m.Audio
	}
	return m.Document
}

func (w *worker) canImport(msg *message, item *attachment) bool {
	return item != nil && item.ID != "" && formats[attachmentExtension(item)] != "" &&
		msg.MediaGroup == "" && item.Size <= w.maxFileBytes
}

func (w *worker) handle(ctx context.Context, msg *message, statusID int64) error {
	if !msg.privateChat() || !w.allowed[msg.From.ID] {
		return nil
	}
	reply := func(text string) error { return w.api.send(ctx, msg.Chat.ID, text) }
	command, args, _ := strings.Cut(strings.TrimSpace(msg.Text), " ")
	switch command {
	case "/start", "/help":
		return reply("Send one complete M4B, M4A or MP3 per message. You can send several separately and I'll queue them. You can set the details in the caption as Book: Author | Title.")
	case "/details", "/cancel":
		return w.handleCommand(ctx, msg.Chat.ID, command, strings.TrimSpace(args))
	}

	item := msg.attachment()
	if item == nil {
		return nil
	}
	ext := attachmentExtension(item)
	if !w.canImport(msg, item) {
		if item.Size > w.maxFileBytes {
			return reply("That audiobook is larger than the configured file size limit.")
		}
		return reply("Send one complete M4B, M4A or MP3 per message. Please send grouped files separately.")
	}
	p := progress{api: w.api, chatID: msg.Chat.ID, statusID: statusID, label: attachmentLabel(item)}
	job := jobName(msg.Chat.ID, msg.ID)
	b, err := w.loadBook(job)
	if errors.Is(err, os.ErrNotExist) {
		b, err = w.stage(ctx, job, msg, item, ext, p)
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		slog.Warn("stage audiobook", "message_id", msg.ID, "err", err)
		switch {
		case errors.Is(err, errSize):
			return p.finish(ctx, "The audiobook is empty or exceeds the file size limit.")
		case errors.Is(err, errAudio):
			return p.finish(ctx, "I couldn't read that file as audio. Please send an M4B, M4A or MP3 audiobook.")
		default:
			return p.finish(ctx, "I couldn't stage that file. Please try sending it again.")
		}
	}
	return w.finish(ctx, msg.ID, job, b, p)
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
	p := progress{api: w.api, chatID: chatID, statusID: b.StatusID, label: attachmentLabel(&attachment{Name: b.Filename})}
	if command == "/cancel" {
		if err := w.staging.RemoveAll(job); err != nil {
			slog.Warn("remove staged audiobook", "message_id", id, "err", err)
			return reply("I couldn't remove the staged audiobook. Please try again.")
		}
		return p.finish(ctx, "Cancelled the staged audiobook.")
	}

	b.Author, b.Title = details(metadata)
	if !b.ready() {
		return reply(fmt.Sprintf("Use /details %d Author | Title with a non-empty author and title.", id))
	}
	if err := w.saveBook(job, b); err != nil {
		slog.Warn("save staged audiobook details", "message_id", id, "err", err)
		return reply("I couldn't save those details. Please try again.")
	}
	return w.finish(ctx, id, job, b, p)
}

func (w *worker) finish(ctx context.Context, id int64, job string, b book, p progress) error {
	next := fmt.Sprintf("Send /details %d Author | Title, or /cancel %d to discard the staged file.", id, id)
	if !b.ready() {
		return p.finish(ctx, fmt.Sprintf("I staged %q and need its author and title. %s", b.Filename, next))
	}
	if err := w.publish(ctx, job, b); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, errCollision) {
			return p.finish(ctx, "A different audiobook already uses that folder. "+next)
		}
		slog.Warn("publish audiobook", "message_id", id, "err", err)
		return p.finish(ctx, "I couldn't move the audiobook into the library. It is still staged. "+next)
	}
	if err := p.finish(ctx, fmt.Sprintf("Imported %s by %s. It will appear in Audiobookshelf after the next scan.", b.Title, b.Author)); err != nil {
		return err
	}
	if err := w.staging.RemoveAll(job); err != nil {
		slog.Warn("clean up audiobook staging", "message_id", id, "err", err)
	}
	return nil
}
