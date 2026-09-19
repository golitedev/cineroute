// Package opensubtitles is the small slice of the OpenSubtitles.com REST API
// needed by the Swedish subtitle workflow. The request shapes, headers, pacing
// and safety checks mirror the proven `~/Projects/subs/download-swedish-subs.py`
// client.
package opensubtitles

import (
	"encoding/json"
	"strconv"
	"strings"
)

// FlexID accepts an API identifier that may arrive as a JSON number or string
// and normalizes it to a decimal string.
type FlexID string

func (v *FlexID) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	if raw == "" || raw == "null" {
		*v = ""
		return nil
	}
	if strings.HasPrefix(raw, `"`) {
		var text string
		if err := json.Unmarshal(data, &text); err == nil {
			*v = FlexID(strings.TrimSpace(text))
			return nil
		}
	}
	if number, err := strconv.ParseFloat(raw, 64); err == nil {
		*v = FlexID(strconv.FormatInt(int64(number), 10))
		return nil
	}
	*v = FlexID(strings.Trim(raw, `"`))
	return nil
}

func (v FlexID) String() string { return string(v) }

// Intish accepts a JSON number or numeric string and normalizes it to int. An
// unexpected value becomes zero rather than failing the whole response.
type Intish int

func (v *Intish) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	if raw == "" || raw == "null" {
		*v = 0
		return nil
	}
	raw = strings.Trim(raw, `"`)
	if raw == "" {
		*v = 0
		return nil
	}
	if number, err := strconv.ParseFloat(raw, 64); err == nil {
		*v = Intish(int(number))
		return nil
	}
	*v = 0
	return nil
}

func (v Intish) Int() int { return int(v) }

// Number accepts a JSON number or numeric string.
type Number float64

func (v *Number) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	if raw == "" || raw == "null" {
		*v = 0
		return nil
	}
	raw = strings.Trim(raw, `"`)
	if raw == "" {
		*v = 0
		return nil
	}
	if number, err := strconv.ParseFloat(raw, 64); err == nil {
		*v = Number(number)
		return nil
	}
	*v = 0
	return nil
}

func (v Number) Float64() float64 { return float64(v) }

// File is one downloadable file attached to a subtitle.
type File struct {
	FileID   Intish `json:"file_id"`
	FileName string `json:"file_name"`
}

// FeatureDetails describes the movie a subtitle belongs to.
type FeatureDetails struct {
	FeatureID   FlexID `json:"feature_id"`
	FeatureType string `json:"feature_type"`
	MovieName   string `json:"movie_name"`
	Title       string `json:"title"`
	Year        Intish `json:"year"`
	ImdbID      FlexID `json:"imdb_id"`
	TmdbID      FlexID `json:"tmdb_id"`
}

// ItemAttributes is the subtitle attribute block.
type ItemAttributes struct {
	SubtitleID        FlexID         `json:"subtitle_id"`
	Language          string         `json:"language"`
	DownloadCount     Intish         `json:"download_count"`
	HearingImpaired   bool           `json:"hearing_impaired"`
	ForeignPartsOnly  bool           `json:"foreign_parts_only"`
	FromTrusted       bool           `json:"from_trusted"`
	MachineTranslated bool           `json:"machine_translated"`
	AITranslated      bool           `json:"ai_translated"`
	Ratings           Number         `json:"ratings"`
	Votes             Intish         `json:"votes"`
	FPS               Number         `json:"fps"`
	HD                bool           `json:"hd"`
	MoviehashMatch    bool           `json:"moviehash_match"`
	Release           string         `json:"release"`
	UploadDate        string         `json:"upload_date"`
	URL               string         `json:"url"`
	FeatureDetails    FeatureDetails `json:"feature_details"`
	Files             []File         `json:"files"`
}

// Item is one subtitle search result. Raw keeps the verbatim JSON so the UI and
// the audit trail can show exactly what the API returned.
type Item struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	Attributes ItemAttributes  `json:"attributes"`
	Raw        json.RawMessage `json:"-"`
}

// SearchResponse is the /subtitles and /features envelope.
type SearchResponse struct {
	TotalPages int    `json:"total_pages"`
	TotalCount int    `json:"total_count"`
	Page       int    `json:"page"`
	Data       []Item `json:"data"`
}

// FeatureAttributes describes a canonical movie feature.
type FeatureAttributes struct {
	FeatureID     FlexID   `json:"feature_id"`
	FeatureType   string   `json:"feature_type"`
	Title         string   `json:"title"`
	OriginalTitle string   `json:"original_title"`
	TitleAka      []string `json:"title_aka"`
	Year          Intish   `json:"year"`
	ImdbID        FlexID   `json:"imdb_id"`
	TmdbID        FlexID   `json:"tmdb_id"`
}

// Feature is one /features result.
type Feature struct {
	ID         string            `json:"id"`
	Type       string            `json:"type"`
	Attributes FeatureAttributes `json:"attributes"`
	Raw        json.RawMessage   `json:"-"`
}

// FeatureResponse is the /features envelope.
type FeatureResponse struct {
	TotalPages int       `json:"total_pages"`
	TotalCount int       `json:"total_count"`
	Page       int       `json:"page"`
	Data       []Feature `json:"data"`
}

// LoginResponse is the /login envelope.
type LoginResponse struct {
	Token   string `json:"token"`
	Status  int    `json:"status"`
	BaseURL string `json:"base_url"`
	User    struct {
		AllowedDownloads Intish `json:"allowed_downloads"`
		UserID           FlexID `json:"user_id"`
		Level            string `json:"level"`
		VIP              bool   `json:"vip"`
	} `json:"user"`
}

// DownloadResponse is the /download envelope.
type DownloadResponse struct {
	Link      string `json:"link"`
	FileName  string `json:"file_name"`
	Requests  Intish `json:"requests"`
	Remaining Intish `json:"remaining"`
	Message   string `json:"message"`
	ResetTime string `json:"reset_time"`
}

// UserInfoResponse is the /infos/user envelope.
type UserInfoResponse struct {
	Data struct {
		AllowedDownloads   Intish `json:"allowed_downloads"`
		RemainingDownloads Intish `json:"remaining_downloads"`
		UserID             FlexID `json:"user_id"`
		Level              string `json:"level"`
		VIP                bool   `json:"vip"`
	} `json:"data"`
	AllowedDownloads   Intish `json:"allowed_downloads"`
	RemainingDownloads Intish `json:"remaining_downloads"`
	ResetTimeUtc       string `json:"reset_time_utc"`
	UserID             FlexID `json:"user_id"`
	Level              string `json:"level"`
	VIP                bool   `json:"vip"`
}
