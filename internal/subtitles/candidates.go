package subtitles

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"cineroute/internal/subtitles/opensubtitles"
)

// Candidate categories from the proven audit step: only "strong" and "fallback"
// are ever downloaded; "review" is reported and left for a human.
const (
	CategoryStrong   = "strong"
	CategoryFallback = "fallback"
	CategoryReview   = "review"
)

// Candidate is one audited OpenSubtitles result for one reference.
//
// The JSON tags matter: this struct is what the Subtitles page renders in its
// candidate list, and the page reads lower-case keys. Without them encoding/json
// emits "Rank"/"Score"/"Safe" and every row renders as "#undefined · score 0 ·
// unsafe". Attributes and Raw stay internal: they are the unprocessed
// OpenSubtitles payload, they are never rendered, and they used to be repeated
// for every candidate on every poll.
type Candidate struct {
	FileID      int     `json:"file_id"`
	Rank        int     `json:"rank"`
	Score       float64 `json:"score"`
	PassScore   float64 `json:"pass_score"`
	Category    string  `json:"category"`
	Safe        bool    `json:"safe"`
	Release     string  `json:"release"`
	Language    string  `json:"language,omitempty"`
	MovieTitle  string  `json:"movie_title,omitempty"`
	FeatureYear int     `json:"feature_year,omitempty"`
	// Variant marks a regional variant of the target language, for example
	// "latin" or "castilian" for Spanish.
	Variant    string                       `json:"variant,omitempty"`
	Reasons    []string                     `json:"reasons,omitempty"`
	Attributes opensubtitles.ItemAttributes `json:"-"`
	Raw        json.RawMessage              `json:"-"`
}

const rejectedScore = -10000.0

// yearTolerance is how many years a candidate may differ from the reference
// year and still be the same movie. Film years are release dates, not birthdays:
// a film that opened in one country in 2017 legitimately appears as 2018 in
// another (Revenge (2017) shipped as "Revenge.2018.1080p.AMZN..."), so a
// one-year difference with a matching title is the same film, not a mismatch.
const yearTolerance = 1

// trustedYearTolerance applies when the user configured a title override for the
// movie, which is an explicit statement about which film is meant.
const trustedYearTolerance = 3

// yearWithinTolerance reports whether a candidate year is close enough to the
// reference year. A missing year on either side is not a mismatch: there is
// nothing to compare, and the title checks still have to pass.
func yearWithinTolerance(referenceYear, candidateYear, tolerance int) bool {
	if referenceYear == 0 || candidateYear == 0 {
		return true
	}
	return absInt(candidateYear-referenceYear) <= tolerance
}

// bestFile picks the file inside a result whose name is most similar to the
// reference basename, like `score_candidate`.
func bestFile(reference Reference, files []opensubtitles.File) (opensubtitles.File, bool) {
	if len(files) == 0 {
		return opensubtitles.File{}, false
	}
	best := files[0]
	bestScore := -1.0
	for _, file := range files {
		score := Ratio(reference.Basename, file.FileName)
		if score > bestScore {
			best = file
			bestScore = score
		}
	}
	return best, true
}

// ScoreCandidate ports `download-swedish-subs.py::score_candidate`.
func ScoreCandidate(reference Reference, item opensubtitles.Item) (float64, []string, int, bool) {
	attributes := item.Attributes
	file, ok := bestFile(reference, attributes.Files)
	if !ok {
		return rejectedScore, []string{"no downloadable file"}, 0, false
	}
	fileID := file.FileID.Int()
	if fileID <= 0 {
		return rejectedScore, []string{"missing file_id"}, 0, false
	}

	release := strings.Join([]string{attributes.Release, file.FileName, attributes.FeatureDetails.MovieName}, " ")
	localTokens := Tokens(reference.Basename)
	candidateTokens := Tokens(release)
	score := Ratio(reference.Basename, release) * 100.0
	reasons := []string{fmt.Sprintf("filename similarity %.0f", score)}

	movieTitle := CandidateMovieTitle(attributes.FeatureDetails)
	titleSimilarity := 0.0
	if movieTitle != "" {
		titleSimilarity = Ratio(reference.Title, movieTitle)
	}
	normalizedLocal := strings.Join(wordRe.FindAllString(strings.ToLower(reference.Title), -1), " ")
	normalizedCandidate := strings.Join(wordRe.FindAllString(strings.ToLower(movieTitle), -1), " ")
	switch {
	case normalizedLocal != "" && normalizedLocal == normalizedCandidate:
		score += 300
		reasons = append(reasons, "exact movie-title match")
	case titleSimilarity >= 0.78:
		score += 180 * titleSimilarity
		reasons = append(reasons, fmt.Sprintf("movie-title similarity %.2f", titleSimilarity))
	default:
		score -= 1000
		reasons = append(reasons, fmt.Sprintf("MOVIE-TITLE MISMATCH (%s)", orUnknown(movieTitle)))
	}

	candidateYear := attributes.FeatureDetails.Year.Int()
	if reference.Year != 0 && candidateYear != 0 {
		switch {
		case reference.Year == candidateYear:
			score += 100
			reasons = append(reasons, "year match")
		case yearWithinTolerance(reference.Year, candidateYear, yearTolerance):
			// Slightly below an exact match, so a same-year release still wins,
			// but no longer the -500 of a wrong film.
			score += 90
			reasons = append(reasons, fmt.Sprintf("year within %d (%d, reference %d)", yearTolerance, candidateYear, reference.Year))
		default:
			score -= 500
			reasons = append(reasons, fmt.Sprintf("YEAR MISMATCH %d", candidateYear))
		}
	}

	localSource := SourceFamily(localTokens)
	candidateSource := SourceFamily(candidateTokens)
	if localSource != "" && candidateSource != "" {
		if localSource == candidateSource {
			score += 35
			reasons = append(reasons, localSource+" match")
		} else {
			score -= 20
			reasons = append(reasons, fmt.Sprintf("source differs (%s)", candidateSource))
		}
	}

	serviceMatches := intersectTokens(localTokens, candidateTokens, serviceTokens)
	editionMatches := intersectTokens(localTokens, candidateTokens, editionTokens)
	score += 15 * float64(len(serviceMatches))
	score += 20 * float64(len(editionMatches))
	if len(serviceMatches) > 0 {
		reasons = append(reasons, "service match")
	}
	if len(editionMatches) > 0 {
		reasons = append(reasons, "edition match")
	}
	if attributes.FromTrusted {
		score += 10
		reasons = append(reasons, "trusted")
	}
	if attributes.HearingImpaired {
		score -= 8
		reasons = append(reasons, "hearing impaired")
	}
	if attributes.MachineTranslated || attributes.AITranslated {
		score -= 80
		reasons = append(reasons, "machine translated")
	}
	ratings := attributes.Ratings.Float64()
	if ratings > 10 {
		ratings = 10
	}
	if ratings > 0 {
		score += ratings
	}
	return score, reasons, fileID, true
}

func intersectTokens(left, right, filter map[string]bool) []string {
	var out []string
	for token := range filter {
		if left[token] && right[token] {
			out = append(out, token)
		}
	}
	sort.Strings(out)
	return out
}

func orUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}

// AuditCandidate ports `download-swedish-subs.py::audit_candidate`, returning
// CategoryStrong, CategoryFallback or CategoryReview.
func AuditCandidate(reference Reference, item opensubtitles.Item, release string) (string, []string) {
	attributes := item.Attributes
	fileNames := make([]string, 0, len(attributes.Files))
	for _, file := range attributes.Files {
		fileNames = append(fileNames, file.FileName)
	}
	evidence := strings.Join([]string{release, strings.Join(fileNames, " ")}, " ")
	var reasons []string

	movieTitle := CandidateMovieTitle(attributes.FeatureDetails)
	if NormalizedTitle(reference.Title) != NormalizedTitle(movieTitle) {
		reasons = append(reasons, fmt.Sprintf("approximate title: %s", orUnknown(movieTitle)))
	}

	// A year within the tolerance is the same movie as far as the audit is
	// concerned; a missing feature year is still worth reporting because nothing
	// can confirm the film.
	featureYear := attributes.FeatureDetails.Year.Int()
	if reference.Year != 0 && (featureYear == 0 || !yearWithinTolerance(reference.Year, featureYear, yearTolerance)) {
		reasons = append(reasons, fmt.Sprintf("feature year %s", orMissing(featureYear)))
	}

	var conflicting []string
	seen := map[int]bool{}
	for _, yearText := range FindYears(evidence) {
		year, err := strconv.Atoi(yearText)
		if err != nil || seen[year] {
			continue
		}
		seen[year] = true
		if reference.Year != 0 && !yearWithinTolerance(reference.Year, year, yearTolerance) {
			conflicting = append(conflicting, yearText)
		}
	}
	sort.Strings(conflicting)
	if len(conflicting) > 0 {
		reasons = append(reasons, "release year conflict: "+strings.Join(conflicting, ","))
	}
	if forcedRe.MatchString(evidence) {
		reasons = append(reasons, "forced subtitle")
	}
	if noSubsRe.MatchString(evidence) {
		reasons = append(reasons, "release says no subs")
	}
	if attributes.MachineTranslated || attributes.AITranslated {
		reasons = append(reasons, "machine translated")
	}
	if multiReleaseRe.MatchString(evidence) {
		reasons = append(reasons, "multi-part/collection release")
	}

	localParts := Tokens(reference.Basename)
	candidateParts := Tokens(evidence)
	localEditions := EditionFamily(localParts)
	candidateEditions := EditionFamily(candidateParts)
	if !sameFamilies(localEditions, candidateEditions) && (len(localEditions) > 0 || len(candidateEditions) > 0) {
		reasons = append(reasons, "edition mismatch: target="+familyLabel(localEditions)+" candidate="+familyLabel(candidateEditions))
	}

	if len(reasons) > 0 {
		return CategoryReview, reasons
	}
	var warnings []string
	if attributes.HearingImpaired {
		warnings = append(warnings, "hearing impaired")
	}
	localSource := SourceFamily(localParts)
	candidateSource := SourceFamily(candidateParts)
	if localSource != "" && candidateSource != "" && localSource == candidateSource {
		if len(warnings) == 0 {
			warnings = append(warnings, "same source: "+localSource)
		}
		return CategoryStrong, warnings
	}
	if localSource != "" && candidateSource != "" {
		warnings = append(warnings, fmt.Sprintf("source fallback: %s->%s", localSource, candidateSource))
	} else {
		warnings = append(warnings, "source not established")
	}
	return CategoryFallback, warnings
}

func orMissing(year int) string {
	if year == 0 {
		return "missing"
	}
	return strconv.Itoa(year)
}

func familyLabel(families map[string]bool) string {
	if len(families) == 0 {
		return "standard"
	}
	return strings.Join(SortedEditionList(families), ",")
}

func sameFamilies(left, right map[string]bool) bool {
	if len(left) != len(right) {
		return false
	}
	for family := range left {
		if !right[family] {
			return false
		}
	}
	return true
}

// CandidateSafety ports `second-pass-swedish.py::candidate_safety`. It returns
// whether the candidate is safe to download, its identity score, the reasons it
// is unsafe, the reported movie title and the feature year.
func CandidateSafety(reference Reference, item opensubtitles.Item, release string, acceptedTitles map[string]bool, titleOverrides map[string]string, featureID string) (bool, float64, []string, string, int) {
	attributes := item.Attributes
	feature := attributes.FeatureDetails
	movieTitle := CandidateMovieTitle(attributes.FeatureDetails)
	targetTitle := NormalizedTitle(reference.Title)
	foundTitle := NormalizedTitle(movieTitle)
	titleSimilarity := 0.0
	if foundTitle != "" {
		titleSimilarity = Ratio(targetTitle, foundTitle)
	}
	featureYear := feature.Year.Int()
	fileNames := make([]string, 0, len(attributes.Files))
	for _, file := range attributes.Files {
		fileNames = append(fileNames, file.FileName)
	}
	evidence := release + " " + strings.Join(fileNames, " ")
	var reasons []string

	if acceptedTitles == nil {
		acceptedTitles = map[string]bool{targetTitle: true}
	}
	exactFeature := featureID != "" && feature.FeatureID.String() == featureID
	trustedOverride := false
	if foundTitle != "" {
		if override, ok := titleOverrides[targetTitle]; ok {
			trustedOverride = NormalizedTitle(override) == foundTitle
		}
	}
	if !exactFeature {
		if !acceptedTitles[foundTitle] && titleSimilarity < 0.90 {
			reasons = append(reasons, fmt.Sprintf("wrong/uncertain title: %s (%.2f)", orUnknown(movieTitle), titleSimilarity))
		}
		allowedYearDelta := yearTolerance
		if trustedOverride {
			allowedYearDelta = trustedYearTolerance
		}
		if reference.Year != 0 && (featureYear == 0 || !yearWithinTolerance(reference.Year, featureYear, allowedYearDelta)) {
			reasons = append(reasons, fmt.Sprintf("feature year %s", orMissing(featureYear)))
		}
	}
	if forcedRe.MatchString(evidence) {
		reasons = append(reasons, "forced")
	}
	if noSubsRe.MatchString(evidence) {
		reasons = append(reasons, "release says no subs")
	}
	localEditions := EditionFamily(Tokens(reference.Basename))
	candidateEditions := EditionFamily(Tokens(evidence))
	if !exactFeature && !sameFamilies(localEditions, candidateEditions) && (len(localEditions) > 0 || len(candidateEditions) > 0) {
		reasons = append(reasons, "edition mismatch")
	}

	safe := len(reasons) == 0
	sourceBonus := 0.0
	localSource := SourceFamily(Tokens(reference.Basename))
	candidateSource := SourceFamily(Tokens(evidence))
	if localSource != "" && candidateSource != "" && localSource == candidateSource {
		sourceBonus = 30
	}
	identityScore := titleSimilarity*100 + sourceBonus
	if exactFeature {
		identityScore += 500
		if sameFamilies(localEditions, candidateEditions) {
			identityScore += 20
		}
		if !(attributes.MachineTranslated || attributes.AITranslated) {
			identityScore += 15
		}
	}
	ratings := attributes.Ratings.Float64()
	if ratings > 10 {
		ratings = 10
	}
	if ratings > 0 {
		identityScore += ratings
	}
	return safe, identityScore, reasons, movieTitle, featureYear
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
