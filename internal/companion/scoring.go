package companion

import (
	"crypto/sha256"
	"encoding/hex"
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
	sourceGuid       string
}

const MaxCandidateResults = 5

const (
	sweetSpotBytes = 8 << 30
	underTenGiB    = 10 << 30
)

var resolutionRe = regexp.MustCompile(`(?i)\b(?:480|576|720|1080|2160)p\b`)
var webDLRe = regexp.MustCompile(`(?i)\bweb[. _-]*dl\b`)
var webRipRe = regexp.MustCompile(`(?i)\bweb[. _-]*rip\b`)
var hevcRe = regexp.MustCompile(`(?i)\b(?:hevc|h[. _-]*265|x265)\b`)
var avcRe = regexp.MustCompile(`(?i)\b(?:avc|h[. _-]*264|x264)\b`)

// FilterAndRank applies the intentionally small companion quality policy and
// returns candidates ordered for manual review. Prowlarr has already scoped
// the response to the configured indexer and search query, so stale release
// metadata is evidence for ranking rather than a reason to hide a candidate.
func FilterAndRank(releases []prowlarr.Release, title string, year, tmdbID int, policy Policy) []Candidate {
	guidCounts := make(map[string]int, len(releases))
	for _, release := range releases {
		if guid := strings.TrimSpace(release.Guid); guid != "" {
			guidCounts[guid]++
		}
	}
	accepted := make([]Candidate, 0, len(releases))
	for _, release := range releases {
		guid := strings.TrimSpace(release.Guid)
		candidate, ok := scoreRelease(release, title, year, tmdbID, policy, guid == "" || guidCounts[guid] > 1)
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
	if len(accepted) > MaxCandidateResults {
		return accepted[:MaxCandidateResults]
	}
	return accepted
}

func scoreRelease(release prowlarr.Release, title string, year, tmdbID int, policy Policy, useFingerprint bool) (Candidate, bool) {
	if strings.TrimSpace(release.Title) == "" {
		return Candidate{}, false
	}
	indexerMismatch := policy.TargetIndexerID > 0 && release.IndexerID > 0 && release.IndexerID != policy.TargetIndexerID
	exactTMDB := tmdbID > 0 && release.TmdbID > 0 && release.TmdbID == tmdbID
	tmdbMismatch := tmdbID > 0 && release.TmdbID > 0 && release.TmdbID != tmdbID
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

	c := Candidate{
		Guid:        releaseCandidateID(release, useFingerprint),
		Title:       release.Title,
		Size:        release.Size,
		Seeders:     release.Seeders,
		Indexer:     release.Indexer,
		IndexerID:   release.IndexerID,
		TmdbID:      release.TmdbID,
		InfoURL:     release.InfoURL,
		PublishDate: release.PublishDate,
		downloadURL: release.DownloadURL,
		sourceGuid:  strings.TrimSpace(release.Guid),
	}
	if exactTMDB {
		c.Score += 100
		c.Reasons = append(c.Reasons, "TMDB match")
	}
	if indexerMismatch {
		c.Score -= 30
		c.Reasons = append(c.Reasons, "Prowlarr indexer metadata differs — inspect tracker")
	}
	if tmdbMismatch {
		c.Score -= 30
		c.Reasons = append(c.Reasons, "TMDB metadata differs — inspect tracker")
	}
	if titleMatch && yearMatch && year > 0 && releaseYear == year {
		c.Score += 40
		c.Reasons = append(c.Reasons, "title and year match")
	} else if titleMatch {
		c.Score += 22
		c.Reasons = append(c.Reasons, "title match")
	} else {
		c.Score -= 20
		c.Reasons = append(c.Reasons, "alternate tracker title — inspect tracker")
	}
	if !yearMatch {
		c.Score -= 15
		c.Reasons = append(c.Reasons, "year differs — inspect tracker")
	}

	switch {
	case webDLRe.MatchString(release.Title):
		c.Source = "WEB-DL"
		c.Score += 120
		c.Reasons = append(c.Reasons, "WEB-DL")
	case webRipRe.MatchString(release.Title):
		c.Source = "WEBRip"
		c.Score += 40
		c.Reasons = append(c.Reasons, "WEBRip")
	case hasAny(release.Title, "bluray", "brrip", "bdrip"):
		c.Source = "BluRay encode"
		c.Score += 45
		c.Reasons = append(c.Reasons, "BluRay encode")
	default:
		c.Source = "Unknown source"
	}

	if hevcRe.MatchString(release.Title) || hasAny(release.Title, "h265", "x265", "hevc") {
		c.Codec = "HEVC"
		if c.Source == "BluRay encode" {
			c.Score += 0
		} else {
			c.Score += 3
		}
		c.Reasons = append(c.Reasons, "HEVC")
	} else if avcRe.MatchString(release.Title) || hasAny(release.Title, "h264", "x264", "avc") {
		c.Codec = "AVC"
		if c.Source == "BluRay encode" {
			// Prefer the broadly compatible x264 BluRay tier over a much
			// smaller x265 BluRay release.
			c.Score += 25
		} else {
			c.Score += 6
		}
		c.Reasons = append(c.Reasons, "AVC")
	} else {
		c.Codec = "Unknown codec"
	}

	switch {
	case release.Size < sweetSpotBytes:
		c.Score += 20
		c.Reasons = append(c.Reasons, "under 8 GiB sweet spot")
	case release.Size <= underTenGiB:
		c.Score += 10
		c.Reasons = append(c.Reasons, "under 10 GiB")
	default:
		c.Score += 2
	}

	tokens := releaseTokens(release.Title)
	dual := containsToken(tokens, "dual") || containsToken(tokens, "dualaudio")
	spanish := containsAnyToken(tokens, "latino", "spanish", "castellano", "spa", "esp")
	switch {
	case dual && spanish:
		c.LanguageEvidence = "Spanish + dual evidence"
		c.Reasons = append(c.Reasons, "DUAL", "Spanish/Latino")
	case dual:
		c.LanguageEvidence = "Likely dual audio"
		c.Reasons = append(c.Reasons, "DUAL")
	case spanish:
		c.LanguageEvidence = "Spanish evidence"
		c.Reasons = append(c.Reasons, "Spanish/Latino")
	default:
		c.LanguageEvidence = "Unknown — inspect tracker"
		c.Reasons = append(c.Reasons, "language unknown")
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

// releaseCandidateID keeps valid Prowlarr results reviewable when an indexer
// omits or reuses guid. The opaque fallback is stable across the approval-time
// fresh search without exposing or persisting the Prowlarr download URL.
func releaseCandidateID(release prowlarr.Release, useFingerprint bool) string {
	if guid := strings.TrimSpace(release.Guid); guid != "" && !useFingerprint {
		return guid
	}
	return "release-" + releaseFingerprint(release)
}

func releaseFingerprint(release prowlarr.Release) string {
	value := strings.TrimSpace(release.Title) + "\x00" +
		strconv.FormatInt(release.Size, 10) + "\x00" +
		strconv.Itoa(release.IndexerID) + "\x00" +
		strings.TrimSpace(release.Indexer)
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:16])
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
