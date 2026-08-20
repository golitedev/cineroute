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
	// These values are ranking preferences only. They must never make a
	// Prowlarr release disappear from manual review.
	MaxBytes        int64
	MinSeeders      int
	TargetIndexerID int
}

type Candidate struct {
	Guid             string    `json:"guid"`
	Title            string    `json:"title"`
	Size             int64     `json:"size"`
	Seeders          *int      `json:"seeders"`
	AgeHours         float64   `json:"age_hours"`
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
	TVPackEligible   bool      `json:"tv_pack_eligible"`
	TVPackReason     string    `json:"tv_pack_reason,omitempty"`
	downloadURL      string
	sourceGuid       string
}

const MaxCandidateResults = 50

const (
	sweetSpotBytes      = 8 << 30
	underTenGiB         = 10 << 30
	maxReleaseYearDelta = 1
)

var resolutionRe = regexp.MustCompile(`(?i)\b(?:480|576|720|1008|1080|2160|4320)p\b`)
var webDLRe = regexp.MustCompile(`(?i)\bweb[. _-]*dl\b`)
var webRipRe = regexp.MustCompile(`(?i)\bweb[. _-]*rip\b`)
var hevcRe = regexp.MustCompile(`(?i)\b(?:hevc|h[. _-]*265|x265)\b`)
var avcRe = regexp.MustCompile(`(?i)\b(?:avc|h[. _-]*264|x264)\b`)
var tvEpisodeRe = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:s[0-9]{1,3}[ ._-]*e[0-9]{1,3}|season[ ._-]*[0-9]{1,3}[ ._-]*(?:episode|ep)[ ._-]*[0-9]{1,3}|[0-9]{1,3}[ ._-]*x[ ._-]*[0-9]{1,3}|(?:episode|ep)[ ._-]*[0-9]{1,3})(?:[^a-z0-9]|$)`)
var tvSeasonRe = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:s[0-9]{1,3}|season[ ._-]*[0-9]{1,3})(?:[^a-z0-9]|$)`)
var tvCollectionRe = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:complete|collection|full[ ._-]*series|all[ ._-]*seasons)(?:[^a-z0-9]|$)`)

// FilterAndRank keeps every release returned by Prowlarr, orders it for manual
// review, and retains at most MaxCandidateResults rows. The name is kept for
// compatibility with the companion workflow; this function no longer filters
// releases by title, year, metadata, size, seeders, resolution, or source.
func FilterAndRank(releases []prowlarr.Release, title string, year, tmdbID int, policy Policy) []Candidate {
	guidCounts := make(map[string]int, len(releases))
	for _, release := range releases {
		if guid := strings.TrimSpace(release.Guid); guid != "" {
			guidCounts[guid]++
		}
	}
	ranked := make([]Candidate, 0, len(releases))
	for _, release := range releases {
		guid := strings.TrimSpace(release.Guid)
		ranked = append(ranked, scoreRelease(release, title, year, tmdbID, policy, guid == "" || guidCounts[guid] > 1))
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Score != ranked[j].Score {
			return ranked[i].Score > ranked[j].Score
		}
		if ranked[i].Size != ranked[j].Size {
			return ranked[i].Size < ranked[j].Size
		}
		leftSeeders := seedCount(ranked[i].Seeders)
		rightSeeders := seedCount(ranked[j].Seeders)
		if leftSeeders != rightSeeders {
			return leftSeeders > rightSeeders
		}
		return ranked[i].PublishDate.After(ranked[j].PublishDate)
	})
	if len(ranked) > MaxCandidateResults {
		return ranked[:MaxCandidateResults]
	}
	return ranked
}

// MarkTVPackCandidates keeps every release visible while identifying which
// releases are safe to approve for a TV companion. Individual episodes remain
// reviewable for transparency but are never eligible for download.
func MarkTVPackCandidates(candidates []Candidate) {
	for i := range candidates {
		candidates[i].TVPackEligible, candidates[i].TVPackReason = TVPackEligibility(candidates[i].Title)
	}
}

func TVPackEligibility(title string) (bool, string) {
	if tvEpisodeRe.MatchString(title) {
		return false, "individual episode release — not eligible for TV companion"
	}
	if tvSeasonRe.MatchString(title) || tvCollectionRe.MatchString(title) {
		return true, "season or series pack"
	}
	return false, "not identified as a season or series pack"
}

func scoreRelease(release prowlarr.Release, title string, year, tmdbID int, policy Policy, useFingerprint bool) Candidate {
	exactTMDB := tmdbID > 0 && release.TmdbID > 0 && release.TmdbID == tmdbID

	releaseTitle, releaseYear := releaseTitleAndYear(release.Title)
	wantTitle := normalizedWords(title)
	titleMatch := wordSequenceMatch(releaseTitle, wantTitle)
	yearMatch := year == 0 || releaseYear == 0 || yearDistance(year, releaseYear) <= maxReleaseYearDelta

	c := Candidate{
		Guid:        releaseCandidateID(release, useFingerprint),
		Title:       release.Title,
		Size:        release.Size,
		Seeders:     release.Seeders,
		AgeHours:    release.AgeHours,
		Indexer:     release.Indexer,
		IndexerID:   release.IndexerID,
		TmdbID:      release.TmdbID,
		InfoURL:     release.InfoURL,
		PublishDate: release.PublishDate,
		downloadURL: release.DownloadURL,
		sourceGuid:  strings.TrimSpace(release.Guid),
	}

	if strings.TrimSpace(release.Title) == "" {
		c.Score -= 40
		c.Reasons = append(c.Reasons, "release title missing")
	}
	if policy.TargetIndexerID > 0 && release.IndexerID > 0 && release.IndexerID != policy.TargetIndexerID {
		c.Score -= 30
		c.Reasons = append(c.Reasons, "Prowlarr indexer metadata differs — inspect tracker")
	}
	if exactTMDB {
		c.Score += 100
		c.Reasons = append(c.Reasons, "TMDB match")
	} else if tmdbID > 0 && release.TmdbID > 0 && release.TmdbID != tmdbID {
		c.Score -= 30
		c.Reasons = append(c.Reasons, "TMDB metadata differs — inspect tracker")
	} else if tmdbID > 0 && release.TmdbID == 0 {
		c.Reasons = append(c.Reasons, "TMDB metadata unavailable")
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
	if year > 0 && releaseYear > 0 && releaseYear != year {
		c.Reasons = append(c.Reasons, "year differs by "+strconv.Itoa(yearDistance(year, releaseYear))+" — inspect tracker")
	}
	if !yearMatch {
		c.Score -= 15
		c.Reasons = append(c.Reasons, "year differs — inspect tracker")
	}

	addSourceEvidence(&c, release.Title)
	addResolutionEvidence(&c, release.Title)

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
	case release.Size <= 0:
		c.Score -= 5
		c.Reasons = append(c.Reasons, "size unknown")
	case release.Size < sweetSpotBytes:
		c.Score += 20
		c.Reasons = append(c.Reasons, "under 8 GiB sweet spot")
	case release.Size <= underTenGiB:
		c.Score += 10
		c.Reasons = append(c.Reasons, "under 10 GiB")
	default:
		c.Score += 2
	}
	if policy.MaxBytes > 0 && release.Size > policy.MaxBytes {
		c.Score -= 8
		c.Reasons = append(c.Reasons, "over configured size preference — inspect tracker")
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
		default:
			c.Reasons = append(c.Reasons, formatSeeders(*release.Seeders)+" seeders")
		}
	} else {
		c.Reasons = append(c.Reasons, "seeders unknown")
	}
	if policy.MinSeeders > 0 && release.Seeders != nil && *release.Seeders < policy.MinSeeders {
		c.Score -= 4
		c.Reasons = append(c.Reasons, "below configured seeder preference — inspect tracker")
	}
	return c
}

func addSourceEvidence(c *Candidate, title string) {
	switch {
	case hasAny(title, "cam", "camrip", "telesync", "telecine", "ts", "tc"):
		c.Source = "CAM/TS"
		c.Score -= 80
		c.Reasons = append(c.Reasons, "CAM/TS source")
	case webDLRe.MatchString(title):
		c.Source = "WEB-DL"
		c.Score += 130
		c.Reasons = append(c.Reasons, "WEB-DL")
	case hasAny(title, "remux"):
		c.Source = "BluRay REMUX"
		c.Score += 55
		c.Reasons = append(c.Reasons, "BluRay REMUX")
	case hasAny(title, "bluray", "brrip", "bdrip"):
		c.Source = "BluRay encode"
		c.Score += 45
		c.Reasons = append(c.Reasons, "BluRay encode")
	case webRipRe.MatchString(title):
		c.Source = "WEBRip"
		c.Score += 40
		c.Reasons = append(c.Reasons, "WEBRip")
	default:
		c.Source = "Unknown source"
		c.Reasons = append(c.Reasons, "source unknown")
	}
}

func addResolutionEvidence(c *Candidate, title string) {
	resolution := 0
	for _, match := range resolutionRe.FindAllString(strings.ToLower(title), -1) {
		value, err := strconv.Atoi(strings.TrimSuffix(strings.ToLower(match), "p"))
		if err == nil && value > resolution {
			resolution = value
		}
	}
	if hasAny(title, "4k", "uhd") && resolution < 2160 {
		resolution = 2160
	}

	switch {
	case resolution == 1008:
		c.Score += 35
		c.Reasons = append(c.Reasons, "tracker labels 1008p — treated as 1080p")
	case resolution >= 1080 && resolution < 2160:
		c.Score += 35
		c.Reasons = append(c.Reasons, "1080p")
	case resolution >= 2160:
		c.Score += 20
		c.Reasons = append(c.Reasons, "4K/2160p")
	case resolution >= 720:
		c.Score += 10
		c.Reasons = append(c.Reasons, "720p")
	case resolution > 0:
		c.Score += 4
		c.Reasons = append(c.Reasons, strconv.Itoa(resolution)+"p")
	default:
		c.Reasons = append(c.Reasons, "resolution unknown")
	}
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
	return titleTokens(value)
}

func releaseTitleAndYear(value string) ([]string, int) {
	words := titleTokens(value)
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

// titleTokens treats apostrophes as spelling punctuation inside a word.
// Trackers commonly write possessives as either "Schindler's" or
// "Schindlers", and both should match the same library title.
func titleTokens(value string) []string {
	value = strings.NewReplacer("'", "", "’", "", "‘", "").Replace(value)
	return releaseTokens(value)
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

func yearDistance(left, right int) int {
	if left > right {
		return left - right
	}
	return right - left
}
