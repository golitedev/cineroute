package companion

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"cineroute/internal/prowlarr"
)

type Policy struct {
	MaxBytes        int64
	MinSeeders      int
	TargetIndexerID int
}

type Candidate struct {
	Guid             string    `json:"guid"`
	Title            string    `json:"title"`
	Size             int64     `json:"size"`
	Seeders          *int      `json:"seeders"`
	Indexer          string    `json:"indexer"`
	IndexerID        int       `json:"indexer_id"`
	TmdbID           int       `json:"tmdb_id,omitempty"`
	InfoURL          string    `json:"info_url,omitempty"`
	PublishDate      time.Time `json:"publish_date,omitempty"`
	Source           string    `json:"source"`
	Codec            string    `json:"codec"`
	LanguageEvidence string    `json:"language_evidence"`
	Score            int       `json:"score"`
	Reasons          []string  `json:"reasons"`
	downloadURL      string
}

var resolutionRe = regexp.MustCompile(`(?i)\b(?:480|576|720|1080|2160)p\b`)
var webDLRe = regexp.MustCompile(`(?i)\bweb[. _-]*dl\b`)
var webRipRe = regexp.MustCompile(`(?i)\bweb[. _-]*rip\b`)
var hevcRe = regexp.MustCompile(`(?i)\b(?:hevc|h[. _-]*265|x265)\b`)
var avcRe = regexp.MustCompile(`(?i)\b(?:avc|h[. _-]*264|x264)\b`)

// FilterAndRank applies the intentionally small companion quality policy and
// returns candidates ordered for manual review.
func FilterAndRank(releases []prowlarr.Release, title string, year, tmdbID int, policy Policy) []Candidate {
	accepted := make([]Candidate, 0, len(releases))
	for _, release := range releases {
		candidate, ok := scoreRelease(release, title, year, tmdbID, policy)
		if ok {
			accepted = append(accepted, candidate)
		}
	}
	sort.SliceStable(accepted, func(i, j int) bool {
		if accepted[i].Score != accepted[j].Score {
			return accepted[i].Score > accepted[j].Score
		}
		if accepted[i].Size != accepted[j].Size {
			return accepted[i].Size < accepted[j].Size
		}
		leftSeeders := seedCount(accepted[i].Seeders)
		rightSeeders := seedCount(accepted[j].Seeders)
		if leftSeeders != rightSeeders {
			return leftSeeders > rightSeeders
		}
		return accepted[i].PublishDate.After(accepted[j].PublishDate)
	})
	return accepted
}

func scoreRelease(release prowlarr.Release, title string, year, tmdbID int, policy Policy) (Candidate, bool) {
	if strings.TrimSpace(release.Guid) == "" || strings.TrimSpace(release.Title) == "" {
		return Candidate{}, false
	}
	if policy.TargetIndexerID > 0 && release.IndexerID != 0 && release.IndexerID != policy.TargetIndexerID {
		return Candidate{}, false
	}
	exactTMDB := tmdbID > 0 && release.TmdbID > 0 && release.TmdbID == tmdbID
	if release.TmdbID != 0 && tmdbID != 0 && release.TmdbID != tmdbID {
		return Candidate{}, false
	}
	if release.Size <= 0 || (policy.MaxBytes > 0 && release.Size > policy.MaxBytes) {
		return Candidate{}, false
	}
	if policy.MinSeeders > 0 && release.Seeders != nil && *release.Seeders < policy.MinSeeders {
		return Candidate{}, false
	}
	if !hasResolution(release.Title, "1080p") || hasAny(release.Title, "2160p", "4k", "uhd", "720p", "480p") {
		return Candidate{}, false
	}
	if hasAny(release.Title, "remux", "cam", "camrip", "telesync", "telecine", "ts", "tc") {
		return Candidate{}, false
	}

	releaseTitle, releaseYear := releaseTitleAndYear(release.Title)
	wantTitle := normalizedWords(title)
	titleMatch := wordSequenceMatch(releaseTitle, wantTitle)
	yearMatch := year == 0 || releaseYear == 0 || releaseYear == year
	if !exactTMDB && (!titleMatch || !yearMatch) {
		return Candidate{}, false
	}

	c := Candidate{
		Guid:        release.Guid,
		Title:       release.Title,
		Size:        release.Size,
		Seeders:     release.Seeders,
		Indexer:     release.Indexer,
		IndexerID:   release.IndexerID,
		TmdbID:      release.TmdbID,
		InfoURL:     release.InfoURL,
		PublishDate: release.PublishDate,
		downloadURL: release.DownloadURL,
	}
	if exactTMDB {
		c.Score += 100
		c.Reasons = append(c.Reasons, "TMDB match")
	}
	if titleMatch {
		if yearMatch && year > 0 && releaseYear == year {
			c.Score += 40
			c.Reasons = append(c.Reasons, "title and year match")
		} else {
			c.Score += 22
			c.Reasons = append(c.Reasons, "title match")
		}
	}

	switch {
	case webDLRe.MatchString(release.Title):
		c.Source = "WEB-DL"
		c.Score += 35
		c.Reasons = append(c.Reasons, "WEB-DL")
	case webRipRe.MatchString(release.Title):
		c.Source = "WEBRip"
		c.Score += 20
		c.Reasons = append(c.Reasons, "WEBRip")
	case hasAny(release.Title, "bluray", "brrip", "bdrip"):
		c.Source = "BluRay encode"
		c.Score += 10
		c.Reasons = append(c.Reasons, "BluRay encode")
	default:
		c.Source = "Unknown source"
	}

	if hevcRe.MatchString(release.Title) || hasAny(release.Title, "h265", "x265", "hevc") {
		c.Codec = "HEVC"
		c.Score += 8
		c.Reasons = append(c.Reasons, "HEVC")
	} else if avcRe.MatchString(release.Title) || hasAny(release.Title, "h264", "x264", "avc") {
		c.Codec = "AVC"
		c.Reasons = append(c.Reasons, "AVC")
	} else {
		c.Codec = "Unknown codec"
	}

	tokens := releaseTokens(release.Title)
	dual := containsToken(tokens, "dual") || containsToken(tokens, "dualaudio")
	spanish := containsAnyToken(tokens, "latino", "spanish", "castellano", "spa", "esp")
	english := containsAnyToken(tokens, "eng", "english")
	switch {
	case dual && spanish:
		c.LanguageEvidence = "Strong Spanish + dual evidence"
		c.Score += 40
		c.Reasons = append(c.Reasons, "DUAL", "Spanish/Latino")
	case dual:
		c.LanguageEvidence = "Likely dual audio"
		c.Score += 25
		c.Reasons = append(c.Reasons, "DUAL")
	case spanish:
		c.LanguageEvidence = "Strong Spanish evidence"
		c.Score += 18
		c.Reasons = append(c.Reasons, "Spanish/Latino")
	default:
		c.LanguageEvidence = "Unknown — inspect tracker"
		c.Score -= 5
		c.Reasons = append(c.Reasons, "language unknown")
	}
	if english {
		c.Score += 10
		c.Reasons = append(c.Reasons, "English/original")
	}
	if release.Seeders != nil {
		switch {
		case *release.Seeders >= 10:
			c.Score += 5
			c.Reasons = append(c.Reasons, formatSeeders(*release.Seeders)+" seeders")
		case *release.Seeders >= 1:
			c.Score += 2
			c.Reasons = append(c.Reasons, formatSeeders(*release.Seeders)+" seeders")
		}
	} else {
		c.Reasons = append(c.Reasons, "seeders unknown")
	}
	return c, true
}

func hasResolution(title, wanted string) bool {
	for _, match := range resolutionRe.FindAllString(strings.ToLower(title), -1) {
		if strings.EqualFold(match, wanted) {
			return true
		}
	}
	return false
}

func hasAny(title string, values ...string) bool {
	tokens := releaseTokens(title)
	for _, value := range values {
		if containsToken(tokens, strings.ToLower(value)) {
			return true
		}
	}
	return false
}

func releaseTokens(value string) []string {
	value = strings.ToLower(value)
	return strings.FieldsFunc(value, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

func containsToken(tokens []string, want string) bool {
	want = strings.ToLower(want)
	for _, token := range tokens {
		if token == want {
			return true
		}
	}
	return false
}

func containsAnyToken(tokens []string, wants ...string) bool {
	for _, want := range wants {
		if containsToken(tokens, want) {
			return true
		}
	}
	return false
}

func normalizedWords(value string) []string {
	words := releaseTokens(value)
	return words
}

func releaseTitleAndYear(value string) ([]string, int) {
	words := releaseTokens(value)
	year := 0
	yearIndex := len(words)
	for i, word := range words {
		if len(word) == 4 && (strings.HasPrefix(word, "19") || strings.HasPrefix(word, "20")) {
			yearIndex = i
			year, _ = strconv.Atoi(word)
		}
	}
	if yearIndex == len(words) {
		for i, word := range words {
			if word == "1080p" || word == "2160p" || word == "720p" || word == "4k" || word == "uhd" || word == "web" {
				yearIndex = i
				break
			}
		}
	}
	return words[:yearIndex], year
}

func wordSequenceMatch(have, want []string) bool {
	if len(want) == 0 || len(have) < len(want) {
		return false
	}
	for i := 0; i <= len(have)-len(want); i++ {
		match := true
		for j := range want {
			if have[i+j] != want[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func seedCount(seeders *int) int {
	if seeders == nil {
		return -1
	}
	return *seeders
}

func formatSeeders(n int) string {
	return strconv.Itoa(n)
}
