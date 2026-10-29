// Package requests stores request policy, durable jobs and download receipts,
// and manages conservative automated fulfillment.
package requests

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"

	"cineroute/internal/medialanguages"
)

type Option struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

var QualityOptions = []Option{
	{"webdl-1080p", "WEB-DL 1080p"}, {"webrip-1080p", "WEBRip 1080p"}, {"bluray-1080p", "BluRay 1080p"}, {"hdtv-1080p", "HDTV 1080p"},
	{"webdl-720p", "WEB-DL 720p"}, {"webrip-720p", "WEBRip 720p"}, {"bluray-720p", "BluRay 720p"}, {"hdtv-720p", "HDTV 720p"},
	{"webdl-2160p", "WEB-DL 2160p"}, {"webrip-2160p", "WEBRip 2160p"}, {"bluray-2160p", "BluRay 2160p"},
	{"remux-1080p", "REMUX 1080p"}, {"remux-2160p", "REMUX 2160p"}, {"dvd", "DVD"}, {"cam", "CAM"}, {"telesync", "Telesync"},
}

var CodecOptions = []Option{{"h264", "H.264 / x264"}, {"hevc", "HEVC / x265"}, {"av1", "AV1"}, {"mpeg2", "MPEG-2"}}

// SelectionOptions lists the release-ranking criteria in their default order.
// The order itself is configurable in request settings and applies to every
// user and both movie and TV requests.
var SelectionOptions = []Option{
	{"quality", "Quality rank"},
	{"language", "Language matches"},
	{"text_subtitles", "Text subtitle format"},
	{"keywords", "Keyword score"},
	{"pack", "Pack preference"},
	{"priority", "Tracker priority"},
	{"seeders", "Seeders"},
}

// DefaultSelectionOrder returns the historical ranking order.
func DefaultSelectionOrder() []string {
	out := make([]string, 0, len(SelectionOptions))
	for _, option := range SelectionOptions {
		out = append(out, option.ID)
	}
	return out
}

func validSelectionCriterion(id string) bool {
	for _, option := range SelectionOptions {
		if option.ID == id {
			return true
		}
	}
	return false
}

var LanguageOptions = medialanguages.Options

var languageCodePattern = regexp.MustCompile(`^(original|[a-z]{2,3}(-[A-Za-z]{2,4})?)$`)

type Tracker struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Priority int    `json:"priority"`
}

type Keyword struct {
	Pattern string `json:"pattern"`
	Match   string `json:"match"` // contains (case insensitive) or regex
	Mode    string `json:"mode"`  // preferred, required or excluded
	Score   int    `json:"score"`
}

type Profile struct {
	RequireSpanishAudio        bool      `json:"-"`
	Destination                string    `json:"destination"`
	NewTitleDrive              string    `json:"new_title_drive"` // empty means most free space
	Qualities                  []string  `json:"qualities"`       // most preferred first
	Codecs                     []string  `json:"codecs"`
	AudioLanguages             []string  `json:"audio_languages"` // empty means any language
	PreferredAudioLanguages    []string  `json:"preferred_audio_languages"`
	PreferredSubtitleLanguages []string  `json:"preferred_subtitle_languages"`
	PreferTextSubtitles        bool      `json:"prefer_text_subtitles"`
	AllowUnknownLanguage       bool      `json:"allow_unknown_language"`
	MinSizeGiB                 float64   `json:"min_size_gib"`
	MaxSizeGiB                 float64   `json:"max_size_gib"` // zero means unlimited
	MaxPackSizeGiB             float64   `json:"max_pack_size_gib"`
	UpgradeAllowed             bool      `json:"upgrade_allowed"`
	UpgradeUntil               string    `json:"upgrade_until"`
	MinimumScore               int       `json:"minimum_score"`
	UpgradeUntilScore          int       `json:"upgrade_until_score"`
	Keywords                   []Keyword `json:"keywords"`
}

type Settings struct {
	Version                int       `json:"version"`
	AllowMovies            bool      `json:"allow_movies"`
	AllowTV                bool      `json:"allow_tv"`
	AutoApprove            bool      `json:"auto_approve"`
	MovieLimit             int       `json:"movie_limit"`
	TVLimit                int       `json:"tv_limit"`
	LimitDays              int       `json:"limit_days"`
	SearchOnApproval       bool      `json:"search_on_approval"`
	SearchIntervalSeconds  int       `json:"search_interval_seconds"`
	RetryIntervalHours     int       `json:"retry_interval_hours"`
	RetryFailedDownloads   bool      `json:"retry_failed_downloads"`
	StalledMinutes         int       `json:"stalled_minutes"`
	MaxConcurrentDownloads int       `json:"max_concurrent_downloads"`
	ReleaseDelayMinutes    int       `json:"release_delay_minutes"`
	MinSeeders             int       `json:"min_seeders"`
	MovieAvailability      string    `json:"movie_availability"`
	MovieAutoSubtitles     bool      `json:"movie_auto_subtitles"`
	IncludeSpecials        bool      `json:"include_specials"`
	PreferSeasonPacks      bool      `json:"prefer_season_packs"`
	SelectionOrder         []string  `json:"selection_order"`
	SpanishTrackers        []Tracker `json:"spanish_trackers"`
	Trackers               []Tracker `json:"trackers"`
	Movie                  Profile   `json:"movie"`
	TV                     Profile   `json:"tv"`
}

func Defaults() Settings {
	profile := func(max, pack float64) Profile {
		return Profile{Destination: "remote", Qualities: []string{"webdl-1080p", "bluray-1080p", "webrip-1080p"}, Codecs: []string{"h264", "hevc"}, AudioLanguages: []string{}, PreferredAudioLanguages: []string{}, PreferredSubtitleLanguages: []string{}, MaxSizeGiB: max, MaxPackSizeGiB: pack, UpgradeUntil: "webdl-1080p", Keywords: []Keyword{}}
	}
	return Settings{
		Version: 1, AllowMovies: true, AllowTV: true, AutoApprove: true,
		LimitDays: 7, SearchOnApproval: true, SearchIntervalSeconds: 10,
		RetryIntervalHours: 6, RetryFailedDownloads: true, StalledMinutes: 120,
		MaxConcurrentDownloads: 3, MinSeeders: 1, MovieAvailability: "released",
		PreferSeasonPacks: true, SelectionOrder: DefaultSelectionOrder(), Trackers: []Tracker{},
		Movie: profile(20, 0), TV: profile(4, 80),
	}
}

func (s Settings) Validate() error {
	if s.Trackers == nil {
		return fmt.Errorf("trackers must be a list; use an empty list when none are selected")
	}
	if s.Version != 1 {
		return fmt.Errorf("unsupported request settings version")
	}
	for _, field := range []struct {
		name            string
		value, min, max int
	}{
		{"movie_limit", s.MovieLimit, 0, 10000}, {"tv_limit", s.TVLimit, 0, 10000},
		{"limit_days", s.LimitDays, 1, 365}, {"search_interval_seconds", s.SearchIntervalSeconds, 1, 3600},
		{"retry_interval_hours", s.RetryIntervalHours, 1, 720}, {"stalled_minutes", s.StalledMinutes, 0, 10080},
		{"max_concurrent_downloads", s.MaxConcurrentDownloads, 1, 100}, {"release_delay_minutes", s.ReleaseDelayMinutes, 0, 10080},
		{"min_seeders", s.MinSeeders, 0, 100000},
	} {
		if field.value < field.min || field.value > field.max {
			return fmt.Errorf("%s must be between %d and %d", field.name, field.min, field.max)
		}
	}
	if !slices.Contains([]string{"announced", "in_cinemas", "released"}, s.MovieAvailability) {
		return fmt.Errorf("invalid movie_availability")
	}
	if len(s.SelectionOrder) > 0 {
		seen := map[string]bool{}
		for _, criterion := range s.SelectionOrder {
			if !validSelectionCriterion(criterion) {
				return fmt.Errorf("unknown release ranking criterion %q", criterion)
			}
			if seen[criterion] {
				return fmt.Errorf("duplicate release ranking criterion %q", criterion)
			}
			seen[criterion] = true
		}
		if len(seen) != len(SelectionOptions) {
			return fmt.Errorf("selection_order must list every release ranking criterion once")
		}
	}
	if len(s.Trackers) > 100 {
		return fmt.Errorf("select at most 100 trackers")
	}
	seen := map[int]bool{}
	for _, tracker := range s.Trackers {
		if tracker.ID <= 0 || seen[tracker.ID] || tracker.Priority < 1 || tracker.Priority > 100 || len(tracker.Name) > 200 {
			return fmt.Errorf("trackers require unique positive IDs and priorities between 1 and 100")
		}
		seen[tracker.ID] = true
	}
	if len(s.SpanishTrackers) > 100 {
		return fmt.Errorf("select at most 100 Spanish trackers")
	}
	spanishSeen := map[int]bool{}
	for _, tracker := range s.SpanishTrackers {
		if tracker.ID <= 0 || spanishSeen[tracker.ID] || len(tracker.Name) > 200 {
			return fmt.Errorf("Spanish trackers require unique positive IDs")
		}
		spanishSeen[tracker.ID] = true
	}
	if err := s.Movie.validate(); err != nil {
		return fmt.Errorf("movie: %w", err)
	}
	if err := s.TV.validate(); err != nil {
		return fmt.Errorf("tv: %w", err)
	}
	return nil
}

func (p Profile) validate() error {
	for name, languages := range map[string][]string{"preferred_audio_languages": p.PreferredAudioLanguages, "preferred_subtitle_languages": p.PreferredSubtitleLanguages} {
		if len(languages) > 30 {
			return fmt.Errorf("select at most 30 %s", name)
		}
		seen := map[string]bool{}
		for _, code := range languages {
			if seen[code] || (!medialanguages.Supported(code) && !(name == "preferred_audio_languages" && code == "original")) {
				return fmt.Errorf("%s must contain unique supported language codes", name)
			}
			seen[code] = true
		}
	}
	if p.AudioLanguages == nil || p.Keywords == nil {
		return fmt.Errorf("audio_languages and keywords must be lists")
	}
	if p.Destination != "main" && p.Destination != "remote" {
		return fmt.Errorf("destination must be main or remote")
	}
	if len(p.NewTitleDrive) > 100 {
		return fmt.Errorf("invalid new_title_drive")
	}
	for _, selection := range []struct {
		name    string
		values  []string
		options []Option
	}{
		{"qualities", p.Qualities, QualityOptions}, {"codecs", p.Codecs, CodecOptions},
	} {
		if len(selection.values) == 0 {
			return fmt.Errorf("select at least one %s option", selection.name)
		}
		seen := map[string]bool{}
		for _, value := range selection.values {
			if seen[value] || !slices.ContainsFunc(selection.options, func(o Option) bool { return o.ID == value }) {
				return fmt.Errorf("invalid or duplicate %s option %q", selection.name, value)
			}
			seen[value] = true
		}
	}
	if !slices.Contains(p.Qualities, p.UpgradeUntil) {
		return fmt.Errorf("upgrade_until must be an allowed quality")
	}
	if p.MinimumScore < -100000 || p.MinimumScore > 100000 || p.UpgradeUntilScore < p.MinimumScore || p.UpgradeUntilScore > 100000 {
		return fmt.Errorf("score cutoff must be at least the minimum score; scores must be between -100000 and 100000")
	}
	for _, size := range []float64{p.MinSizeGiB, p.MaxSizeGiB, p.MaxPackSizeGiB} {
		if math.IsNaN(size) || math.IsInf(size, 0) || size < 0 || size > 100000 {
			return fmt.Errorf("sizes must be between 0 and 100000 GiB")
		}
	}
	if p.MaxSizeGiB != 0 && p.MaxSizeGiB < p.MinSizeGiB {
		return fmt.Errorf("maximum size must be at least the minimum size")
	}
	if len(p.AudioLanguages) > 30 {
		return fmt.Errorf("select at most 30 audio languages")
	}
	seenLanguages := map[string]bool{}
	for _, language := range p.AudioLanguages {
		if !languageCodePattern.MatchString(language) || seenLanguages[language] {
			return fmt.Errorf("audio_languages must contain unique language codes, such as en, es or sv, or original")
		}
		seenLanguages[language] = true
	}
	if len(p.Keywords) > 100 {
		return fmt.Errorf("use at most 100 keyword rules")
	}
	for i, rule := range p.Keywords {
		if strings.TrimSpace(rule.Pattern) == "" || len(rule.Pattern) > 500 {
			return fmt.Errorf("keyword %d needs a pattern between 1 and 500 characters", i+1)
		}
		if rule.Mode != "preferred" && rule.Mode != "required" && rule.Mode != "excluded" {
			return fmt.Errorf("keyword %d has an invalid mode", i+1)
		}
		if rule.Match != "contains" && rule.Match != "regex" {
			return fmt.Errorf("keyword %d has an invalid match type", i+1)
		}
		if rule.Score < -10000 || rule.Score > 10000 || (rule.Mode != "preferred" && rule.Score != 0) {
			return fmt.Errorf("keyword %d: only preferred rules have scores, between -10000 and 10000", i+1)
		}
		if rule.Match == "regex" {
			if _, err := regexp.Compile(rule.Pattern); err != nil {
				return fmt.Errorf("keyword %d has an invalid regular expression: %w", i+1, err)
			}
		}
	}
	return nil
}

// ProfileForJob applies per-request requirements without changing library defaults.
func ProfileForJob(s Settings, job Job) Profile {
	p := s.Movie
	if job.Media.MediaType == "tv" {
		p = s.TV
	}
	if job.SpanishAudio {
		p.RequireSpanishAudio = true
		p.AudioLanguages = []string{"es"}
		p.AllowUnknownLanguage = false
	}
	return p
}

func (s Settings) TrackersForJob(job Job) []Tracker {
	trackers := append([]Tracker{}, s.Trackers...)
	if job.SpanishAudio {
		for _, tracker := range s.SpanishTrackers {
			if !slices.ContainsFunc(trackers, func(t Tracker) bool { return t.ID == tracker.ID }) {
				tracker.Priority = 1
				trackers = append(trackers, tracker)
			}
		}
		slices.SortStableFunc(trackers, func(a, b Tracker) int {
			if s.IsSpanishTracker(a.ID) == s.IsSpanishTracker(b.ID) {
				return 0
			}
			if s.IsSpanishTracker(a.ID) {
				return -1
			}
			return 1
		})
	}
	return trackers
}

func (s Settings) IsSpanishTracker(id int) bool {
	return slices.ContainsFunc(s.SpanishTrackers, func(t Tracker) bool { return t.ID == id })
}
