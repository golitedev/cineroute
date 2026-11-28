package audiobooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type telegram struct {
	base   string
	client *http.Client
}

// Telegram credentials are part of the URL. Never include the URL or the
// remote response description in errors that can reach the logs.
func newTelegram(origin, token string) telegram {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return telegram{
		base: strings.TrimRight(origin, "/") + "/bot" + token + "/",
		client: &http.Client{
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

type update struct {
	ID      int64    `json:"update_id"`
	Message *message `json:"message"`
}

type message struct {
	ID         int64  `json:"message_id"`
	MediaGroup string `json:"media_group_id"`
	From       struct {
		ID int64 `json:"id"`
	} `json:"from"`
	Chat struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	} `json:"chat"`
	Text     string      `json:"text"`
	Caption  string      `json:"caption"`
	Audio    *attachment `json:"audio"`
	Document *attachment `json:"document"`
}

type attachment struct {
	ID       string `json:"file_id"`
	Name     string `json:"file_name"`
	MIMEType string `json:"mime_type"`
	Size     int64  `json:"file_size"`
}

type telegramError struct {
	method string
	code   int
	retry  time.Duration
}

func (e *telegramError) Error() string {
	return fmt.Sprintf("Telegram %s returned status %d", e.method, e.code)
}

func (t telegram) call(ctx context.Context, method string, input, output any) error {
	body, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("encode Telegram %s request", method)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.base+method, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("construct Telegram %s request", method)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("Telegram %s connection failed", method)
	}
	defer resp.Body.Close()
	var envelope struct {
		OK         bool            `json:"ok"`
		Code       int             `json:"error_code"`
		Result     json.RawMessage `json:"result"`
		Parameters struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&envelope)
	if resp.StatusCode != http.StatusOK || envelope.Code != 0 {
		code := envelope.Code
		if code == 0 {
			code = resp.StatusCode
		}
		return &telegramError{method: method, code: code, retry: time.Duration(min(max(envelope.Parameters.RetryAfter, 0), 900)) * time.Second}
	}
	if decodeErr != nil || !envelope.OK || (output != nil && json.Unmarshal(envelope.Result, output) != nil) {
		return fmt.Errorf("invalid Telegram %s response", method)
	}
	return nil
}

func (t telegram) poll(ctx context.Context, offset int64) ([]update, error) {
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	var updates []update
	err := t.call(ctx, "getUpdates", map[string]any{
		"offset": offset, "timeout": 25, "limit": 25, "allowed_updates": []string{"message"},
	}, &updates)
	return updates, err
}

func (t telegram) getFile(ctx context.Context, fileID string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Minute)
	defer cancel()
	var result struct {
		Path string `json:"file_path"`
	}
	err := t.call(ctx, "getFile", map[string]string{"file_id": fileID}, &result)
	return result.Path, err
}

func (t telegram) send(ctx context.Context, chatID int64, text string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	err := t.call(ctx, "sendMessage", map[string]any{"chat_id": chatID, "text": text}, nil)
	var response *telegramError
	if errors.As(err, &response) && response.code == http.StatusForbidden {
		// A blocked bot cannot deliver a reply. Acknowledge the update so
		// this chat does not prevent other users' messages from being read.
		return nil
	}
	return err
}
