package subtitles

import (
	"html"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"cineroute/internal/subtitles/opensubtitles"
)

// This file ports the title and token heuristics from the proven
// `~/Projects/subs/download-swedish-subs.py` pipeline. Keep the behavior and the
// numeric thresholds aligned with that script: they were tuned on a 671-movie
// remote library.

var (
	languageSuffixRe = regexp.MustCompile(`(?i)\.(en|es|pt|sv|fi|da|no|und)\.srt$`)
	yearCandidateRe  = regexp.MustCompile(`(?:19|20)[0-9]{2}`)
	setupMarkerRe    = regexp.MustCompile(`(?i)(?:^|[. _-])(?:480|576|720|1008|1080|2160)p(?:[. _-]|$)|(?:WEB[. _-]?DL|BluRay|BDRip|REMUX|DVDRip)`)
	resolutionSplit  = regexp.MustCompile(`(?i)[. _-](?:480|576|720|1008|1080|2160)p(?:[. _-]|$)`)
	underscoreRunRe  = regexp.MustCompile(`[._]+`)
	wordRe           = regexp.MustCompile(`[a-z0-9]+`)
	forcedRe         = regexp.MustCompile(`(?i)(?:^|[^a-z])(forced|forzados?|forzadas?|foreign.only)(?:[^a-z]|$)`)
	multiReleaseRe   = regexp.MustCompile(`(?i)(trilogy|collection|\bcd[1-9]\b|\bdisc[1-9]\b)`)
	noSubsRe         = regexp.MustCompile(`(?i)\bno[ ._-]*subs?\b`)
	leadingYearRe    = regexp.MustCompile(`^\s*(?:19|20)\d{2}\s*[-:]\s*`)
)

// sourceGroups ports SOURCE_GROUPS.
var sourceGroups = []struct {
	family  string
	aliases map[string]bool
}{
	{"web", tokenSet("web", "webdl", "webrip")},
	{"bluray", tokenSet("bluray", "bdrip", "brrip", "remux")},
	{"dvd", tokenSet("dvd", "dvdrip")},
	{"hdtv", tokenSet("hdtv")},
}

var serviceTokens = tokenSet("amzn", "amazon", "nf", "netflix", "dsnp", "disney", "max", "hmax", "ma", "itunes", "it", "tv4")

var editionTokens = tokenSet("extended", "directors", "director", "theatrical", "remastered", "unrated", "imax", "proper", "repack")

func tokenSet(values ...string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}

// Tokens splits text into lowercase alphanumeric tokens, normalizing WEB-DL and
// WEB.DL spellings so the source family matches either way.
func Tokens(text string) map[string]bool {
	lowered := strings.ToLower(text)
	lowered = strings.ReplaceAll(lowered, "web-dl", "webdl")
	lowered = strings.ReplaceAll(lowered, "web.dl", "webdl")
	out := map[string]bool{}
	for _, token := range wordRe.FindAllString(lowered, -1) {
		out[token] = true
	}
	return out
}

// SourceFamily returns "web", "bluray", "dvd" or "hdtv" when the tokens contain
// an alias for one of those families.
func SourceFamily(tokens map[string]bool) string {
	for _, group := range sourceGroups {
		for alias := range group.aliases {
			if tokens[alias] {
				return group.family
			}
		}
	}
	return ""
}

// EditionFamily groups edition tokens into the family names used by the audit.
func EditionFamily(tokens map[string]bool) map[string]bool {
	families := map[string]bool{}
	if containsAny(tokens, "extended", "unrated") {
		families["extended"] = true
	}
	if containsAny(tokens, "directors", "director", "dc", "dircut") {
		families["directors-cut"] = true
	}
	if tokens["theatrical"] {
		families["theatrical"] = true
	}
	if tokens["remastered"] {
		families["remastered"] = true
	}
	if tokens["imax"] {
		families["imax"] = true
	}
	if tokens["uncut"] {
		families["uncut"] = true
	}
	if tokens["colorized"] {
		families["colorized"] = true
	}
	if containsAny(tokens, "restored", "restoration") {
		families["restored"] = true
	}
	if tokens["criterion"] {
		families["criterion"] = true
	}
	if tokens["anniversary"] {
		families["anniversary"] = true
	}
	if tokens["special"] && tokens["edition"] {
		families["special-edition"] = true
	}
	if tokens["diamond"] && tokens["edition"] {
		families["diamond-edition"] = true
	}
	return families
}

func containsAny(tokens map[string]bool, values ...string) bool {
	for _, value := range values {
		if tokens[value] {
			return true
		}
	}
	return false
}

// FindYears returns year-like four-digit runs that are not part of a longer
// number, mirroring Python's `(?<!\d)((?:19|20)\d{2})(?!\d)`.
func FindYears(text string) []string {
	var out []string
	for _, loc := range yearCandidateRe.FindAllStringIndex(text, -1) {
		start, end := loc[0], loc[1]
		if start > 0 && isDigit(text[start-1]) {
			continue
		}
		if end < len(text) && isDigit(text[end]) {
			continue
		}
		out = append(out, text[start:end])
	}
	return out
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// ParseTitleYear splits a release basename at the last plausible year before
// technical metadata, handling duplicated years such as
// "12.Angry.Men.1957.1957.1008p.AMZN.WEB-DL...".
func ParseTitleYear(basename string) (string, int) {
	cutoff := len(basename)
	if marker := setupMarkerRe.FindStringIndex(basename); marker != nil {
		cutoff = marker[0]
	}
	years := make([][2]int, 0, 2)
	for _, loc := range yearCandidateRe.FindAllStringIndex(basename, -1) {
		start, end := loc[0], loc[1]
		if start > 0 && isDigit(basename[start-1]) {
			continue
		}
		if end < len(basename) && isDigit(basename[end]) {
			continue
		}
		years = append(years, [2]int{start, end})
	}
	var beforeCutoff [][2]int
	for _, loc := range years {
		if loc[0] < cutoff {
			beforeCutoff = append(beforeCutoff, loc)
		}
	}
	if len(beforeCutoff) == 0 {
		title := basename
		if split := resolutionSplit.FindStringIndex(basename); split != nil {
			title = basename[:split[0]]
		}
		return strings.Trim(underscoreRunRe.ReplaceAllString(title, " "), " -"), 0
	}
	yearLoc := beforeCutoff[len(beforeCutoff)-1]
	year, err := strconv.Atoi(basename[yearLoc[0]:yearLoc[1]])
	if err != nil {
		year = 0
	}
	title := strings.Trim(underscoreRunRe.ReplaceAllString(basename[:yearLoc[0]], " "), " -")
	if trimmed := trimDuplicateYearSuffix(title, year); trimmed != "" {
		title = trimmed
	}
	return title, year
}

func trimDuplicateYearSuffix(title string, year int) string {
	if year == 0 {
		return strings.Trim(title, " -")
	}
	suffix := strconv.Itoa(year)
	if !strings.HasSuffix(title, suffix) {
		return strings.Trim(title, " -")
	}
	prefix := title[:len(title)-len(suffix)]
	if prefix == "" {
		return strings.Trim(title, " -")
	}
	last := prefix[len(prefix)-1]
	if last != ' ' && last != '_' && last != '.' && last != '-' {
		return strings.Trim(title, " -")
	}
	return strings.Trim(strings.TrimRight(prefix, " _.-"), " -")
}

// CandidateMovieTitle extracts the movie title reported by OpenSubtitles,
// stripping a leading year prefix and HTML entities.
func CandidateMovieTitle(details opensubtitles.FeatureDetails) string {
	title := strings.TrimSpace(details.MovieName)
	if title == "" {
		title = strings.TrimSpace(details.Title)
	}
	return strings.TrimSpace(leadingYearRe.ReplaceAllString(html.UnescapeString(title), ""))
}

// NormalizedTitle lowercases, expands "&" and removes apostrophes between word
// characters, matching the Python helper.
func NormalizedTitle(title string) string {
	value := strings.ToLower(html.UnescapeString(title))
	value = strings.ReplaceAll(value, "&", " and ")
	var b strings.Builder
	runes := []rune(value)
	for i, r := range runes {
		if r == '\'' || r == '\u2019' {
			prevWord := i > 0 && isWordRune(runes[i-1])
			nextWord := i+1 < len(runes) && isWordRune(runes[i+1])
			if prevWord && nextWord {
				continue
			}
		}
		b.WriteRune(r)
	}
	return strings.Join(wordRe.FindAllString(b.String(), -1), " ")
}

// NormalizedIdentity is the stronger ASCII-normalized form used by the second
// pass: NFKD ASCII folding, case folding, "&" expansion.
func NormalizedIdentity(value string) string {
	folded := asciiFold(value)
	folded = strings.ToLower(folded)
	folded = strings.ReplaceAll(folded, "&", " and ")
	return strings.Join(wordRe.FindAllString(folded, -1), " ")
}

func isWordRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

// asciiFold approximates Python's unicodedata NFKD + ascii-ignore for the Latin
// characters that appear in movie release names.
func asciiFold(value string) string {
	var b strings.Builder
	for _, r := range value {
		if mapped, ok := foldRune(r); ok {
			b.WriteString(mapped)
			continue
		}
		if r < 128 {
			b.WriteRune(r)
		}
	}
	return b.String()
}

var foldTable = map[rune]string{
	'À': "A", 'Á': "A", 'Â': "A", 'Ã': "A", 'Ä': "A", 'Å': "A", 'Æ': "AE",
	'Ç': "C", 'È': "E", 'É': "E", 'Ê': "E", 'Ë': "E",
	'Ì': "I", 'Í': "I", 'Î': "I", 'Ï': "I",
	'Ñ': "N", 'Ò': "O", 'Ó': "O", 'Ô': "O", 'Õ': "O", 'Ö': "O", 'Ø': "O", 'Œ': "OE",
	'Ù': "U", 'Ú': "U", 'Û': "U", 'Ü': "U", 'Ý': "Y", 'Ÿ': "Y",
	'à': "a", 'á': "a", 'â': "a", 'ã': "a", 'ä': "a", 'å': "a", 'æ': "ae",
	'ç': "c", 'è': "e", 'é': "e", 'ê': "e", 'ë': "e",
	'ì': "i", 'í': "i", 'î': "i", 'ï': "i",
	'ñ': "n", 'ò': "o", 'ó': "o", 'ô': "o", 'õ': "o", 'ö': "o", 'ø': "o", 'œ': "oe",
	'ù': "u", 'ú': "u", 'û': "u", 'ü': "u", 'ý': "y", 'ÿ': "y",
	'ß': "ss", 'Ð': "D", 'Þ': "Th", 'ð': "d", 'þ': "th",
	'’': "'", '‘': "'", '“': `"`, '”': `"`, '–': "-", '—': "-",
}

func foldRune(r rune) (string, bool) {
	mapped, ok := foldTable[r]
	return mapped, ok
}

// SortedEditionList returns a deterministic family list for log messages.
func SortedEditionList(families map[string]bool) []string {
	out := make([]string, 0, len(families))
	for family := range families {
		out = append(out, family)
	}
	sort.Strings(out)
	return out
}
