package subtitles

import (
	"regexp"
	"strings"
)

// Spanish variant labels. OpenSubtitles reports a plain "es" for both Castilian
// and Latin American Spanish, so the release and file names carry the signal:
// scene releases tag Latin American dubs and translations as LATINO/LATAM and
// European ones as CASTELLANO (or simply "Spanish"). Latin American is preferred
// for the "es" target because a Castilian subtitle matched against a Latin
// American dub is a different translation.
const (
	VariantLatin     = "latin"
	VariantCastilian = "castilian"
)

var (
	latinSpanishRe     = regexp.MustCompile(`\b(latino|latina|latin|latam|lat|espanol\s*latino|spanish\s*lat(in)?(\s*american)?)\b`)
	castilianSpanishRe = regexp.MustCompile(`\b(castellano|castilian|castella|spain|espana|spanish\s*spain|esp)\b`)
)

// latinSpanishRegions are the ISO 3166/UN M49 region codes used for Latin
// American Spanish. OpenSubtitles mainly reports "es", but a regional tag is
// honoured whenever it appears (in the API or in a filename).
var latinSpanishRegions = map[string]bool{
	"419": true, "latam": true, "latin": true,
	"mx": true, "ar": true, "co": true, "cl": true, "pe": true, "ve": true,
	"uy": true, "py": true, "bo": true, "ec": true, "gt": true, "cr": true,
	"pa": true, "do": true, "pr": true, "cu": true, "hn": true, "sv": true,
	"ni": true, "us": true,
}

// languageMatches reports whether a subtitle language tag satisfies a target
// language, including regional variants: the target "es" accepts "es" and
// "es-419", while "en" does not accept "en-GB" being confused with anything
// else because the base always has to match.
func languageMatches(target, candidate string) bool {
	target = normalizeLanguage(target)
	candidate = normalizeLanguage(candidate)
	if target == "" || candidate == "" {
		return false
	}
	if target == candidate {
		return true
	}
	// A target without a region accepts every regional variant of that language.
	return strings.HasPrefix(candidate, target+"-")
}

// spanishVariant classifies a Spanish subtitle as Latin American or Castilian,
// returning "" for a non-Spanish subtitle or when nothing indicates the variant.
func spanishVariant(language, release string) string {
	tag := strings.ToLower(strings.TrimSpace(language))
	base, region, hasRegion := strings.Cut(tag, "-")
	if base != "es" && base != "spa" {
		return ""
	}
	if hasRegion && region != "" {
		if latinSpanishRegions[region] {
			return VariantLatin
		}
		if region == "es" {
			return VariantCastilian
		}
	}
	text := strings.ToLower(release)
	switch {
	case latinSpanishRe.MatchString(text):
		return VariantLatin
	case castilianSpanishRe.MatchString(text):
		return VariantCastilian
	default:
		return ""
	}
}

// variantRank orders the variants of a target language: an explicit Latin
// American match first, then a release with no variant signal, then Castilian.
func variantRank(variant string) int {
	switch variant {
	case VariantLatin:
		return 0
	case VariantCastilian:
		return 2
	default:
		return 1
	}
}

// variantLabel renders a variant for the reasons list and the page.
func variantLabel(variant string) string {
	switch variant {
	case VariantLatin:
		return "Latin American Spanish"
	case VariantCastilian:
		return "Castilian Spanish"
	default:
		return ""
	}
}

// searchLanguages expands the target languages into the OpenSubtitles
// `languages` CSV for one search request.
//
// Spanish needs special care: the API reports "es" (Spanish), "ea" (Spanish
// (LA)) and "sp" (Spanish (EU)) as separate codes, so a search that only asked
// for "es" would never see a Latin American subtitle to prefer. Portuguese has
// the same shape ("pt-pt" vs "pt-br") but no preference is configured for it.
func searchLanguages(targets []string) string {
	var codes []string
	seen := map[string]bool{}
	for _, target := range targets {
		for _, code := range searchLanguageCodes(target) {
			if seen[code] {
				continue
			}
			seen[code] = true
			codes = append(codes, code)
		}
	}
	return strings.Join(codes, ",")
}

// searchLanguageCodes lists the API codes that can satisfy one target language.
func searchLanguageCodes(target string) []string {
	if normalizeLanguage(target) == "es" {
		return []string{"es", "ea", "sp"}
	}
	return []string{target}
}

// NormalizeTargetLanguages cleans the configured target languages: language
// codes are normalized, duplicates are dropped and the configured order is kept.
func NormalizeTargetLanguages(values []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, value := range values {
		language := normalizeLanguage(value)
		if language == "" || seen[language] {
			continue
		}
		seen[language] = true
		out = append(out, language)
	}
	return out
}

// describeTargets renders the target languages for logs and messages.
func describeTargets(languages []string) string {
	if len(languages) == 0 {
		return "none"
	}
	return strings.Join(languages, ", ")
}

// targetLabel renders a per-language status for humans.
func targetLabel(status string) string {
	switch status {
	case TargetPresent:
		return "present"
	case StatusAdded:
		return "added"
	case StatusAddedReview:
		return "added (review)"
	case StatusNoMatch:
		return "no match"
	case StatusNeedsReview:
		return "needs review"
	case StatusNoReference:
		return "no reference"
	case StatusFailed:
		return "failed"
	case StatusPending:
		return "pending"
	default:
		return status
	}
}
