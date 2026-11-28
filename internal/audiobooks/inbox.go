package audiobooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const inboxDir = ".telegram-inbox"

type queuedMessage struct {
	Message  *message `json:"message"`
	StatusID int64    `json:"status_id,omitempty"`
}

// Store updates before confirming them to Telegram. Polling can acknowledge
// additional submissions while a single worker handles long downloads in order.
func (w *worker) enqueue(ctx context.Context, u update) error {
	msg := u.Message
	if !msg.privateChat() {
		return nil
	}
	command, _, _ := strings.Cut(strings.TrimSpace(msg.Text), " ")
	if command == "/id" {
		return w.api.send(ctx, msg.Chat.ID, fmt.Sprintf("Your Telegram user ID is %d.", msg.From.ID))
	}
	if !w.allowed[msg.From.ID] {
		return nil
	}
	item := msg.attachment()
	switch command {
	case "/start", "/help", "/details", "/cancel":
	default:
		if item == nil {
			return nil
		}
	}

	path := filepath.Join(inboxDir, fmt.Sprintf("%020d.json", u.ID))
	if _, err := w.staging.Stat(path); !errors.Is(err, os.ErrNotExist) {
		return err
	}
	entry := queuedMessage{Message: msg}
	if w.canImport(msg, item) {
		statusID, err := w.api.sendStatus(ctx, msg, attachmentLabel(item)+"\nQueued for import.")
		if err != nil && ctx.Err() == nil {
			slog.Debug("send queued audiobook status", "message_id", msg.ID, "err", err)
		}
		entry.StatusID = statusID
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if err := w.staging.WriteFile(path+".tmp", data, 0o600); err != nil {
		return err
	}
	return w.staging.Rename(path+".tmp", path)
}

func (w *worker) consumeInbox(ctx context.Context, notify <-chan struct{}) {
	for ctx.Err() == nil {
		err := w.drainInbox(ctx)
		if err != nil && ctx.Err() == nil {
			slog.Warn("audiobook importer will retry queued update", "err", err)
			waitToRetry(ctx, err)
			continue
		}
		select {
		case <-ctx.Done():
		case <-notify:
		}
	}
}

func (w *worker) drainInbox(ctx context.Context) error {
	entries, err := fs.ReadDir(w.staging.FS(), inboxDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.Type().IsRegular() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(inboxDir, entry.Name())
		data, err := w.staging.ReadFile(path)
		if err != nil {
			return err
		}
		var job queuedMessage
		if err := json.Unmarshal(data, &job); err != nil {
			return err
		}
		if err := w.handle(ctx, job.Message, job.StatusID); err != nil {
			return err
		}
		if err := w.staging.Remove(path); err != nil {
			return err
		}
	}
	return nil
}

func waitToRetry(ctx context.Context, err error) {
	delay := 5 * time.Second
	var response *telegramError
	if errors.As(err, &response) {
		delay = max(delay, response.retry)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}
