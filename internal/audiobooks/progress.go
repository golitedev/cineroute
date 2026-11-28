package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type progress struct {
	api      telegram
	chatID   int64
	statusID int64
	label    string
}

func attachmentLabel(item *attachment) string {
	name := strings.TrimSpace(item.Name)
	if name == "" {
		return "Audiobook"
	}
	runes := []rune(name)
	if len(runes) > 150 {
		return string(runes[:147]) + "…"
	}
	return name
}

func (p progress) phase(ctx context.Context, text string) {
	if p.statusID > 0 {
		_ = p.api.editStatus(ctx, p.chatID, p.statusID, p.label+"\n"+text)
	}
}

func (p progress) finish(ctx context.Context, text string) error {
	if p.statusID > 0 {
		err := p.api.editStatus(ctx, p.chatID, p.statusID, p.label+"\n"+text)
		if err == nil {
			return nil
		}
		var response *telegramError
		if !errors.As(err, &response) {
			return err
		}
		if response.unchanged || response.code == http.StatusForbidden {
			return nil
		}
		if response.code != http.StatusBadRequest && response.code != http.StatusNotFound {
			return err
		}
		// The progress message may have been deleted. Send the outcome normally.
	}
	return p.api.send(ctx, p.chatID, text)
}

func (p progress) download(ctx context.Context, fileID string) (string, error) {
	if p.statusID <= 0 {
		return p.api.getFile(ctx, fileID)
	}
	p.phase(ctx, "Downloading from Telegram…")
	updates, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		start := time.Now()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-updates.Done():
				return
			case now := <-ticker.C:
				p.phase(updates, fmt.Sprintf("Still downloading from Telegram… %s elapsed.", now.Sub(start).Round(time.Second)))
			}
		}
	}()
	defer func() {
		cancel()
		<-done
	}()
	return p.api.getFile(ctx, fileID)
}
