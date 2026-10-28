// Package genres translates Qobuz genre tags using complete phrases.
package genres

import (
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// Translator turns genre names into English. Safe for concurrent use.
// Construct a Translator with New; its table is immutable after construction.
type Translator struct {
	table map[string]string
}

// New returns a translator with the built-in table plus extra mappings.
// Keys and lookups use the same normalization. Extra keys override built-ins;
// an empty extra value means keep the original phrase exactly as written.
// If extra keys normalize identically, the lexicographically last key wins.
// New copies extra, so callers may modify it after New returns.
func New(extra map[string]string) *Translator {
	t := &Translator{table: make(map[string]string, len(builtins)*2+len(extra))}
	for key, value := range builtins {
		key = normalize(key)
		if key == normalize(value) {
			t.table[key] = "" // Already English: preserve its spelling and formatting.
		} else {
			t.table[key] = value
		}
	}
	// English targets are known phrases too, even when they contain accents.
	for _, value := range builtins {
		key := normalize(value)
		if _, exists := t.table[key]; !exists {
			t.table[key] = ""
		}
	}
	keys := make([]string, 0, len(extra))
	for key := range extra {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		t.table[normalize(key)] = extra[key]
	}
	return t
}

// Spaces adjacent to a delimiter belong to that delimiter. This lets unknown
// segments and every retained delimiter survive byte-for-byte. A hyphen is a
// delimiter only when whitespace appears on both sides.
var separator = regexp.MustCompile(`[\s\p{Z}\x{0085}]*[,;/→][\s\p{Z}\x{0085}]*|[\s\p{Z}\x{0085}]+-[\s\p{Z}\x{0085}]+`)

type segment struct{ text, before string }

func split(value string) []segment {
	matches := separator.FindAllStringIndex(value, -1)
	parts := make([]segment, 0, len(matches)+1)
	start, before := 0, ""
	for _, match := range matches {
		parts = append(parts, segment{value[start:match[0]], before})
		before, start = value[match[0]:match[1]], match[1]
	}
	return append(parts, segment{value[start:], before})
}

func (t *Translator) lookup(value string) (string, bool) {
	mapped, ok := t.table[normalize(value)]
	if mapped == "" {
		return value, ok
	}
	return mapped, ok
}

// Translate translates one tag value, trying a whole phrase before splitting
// on commas, semicolons, slashes, arrows, or whitespace-surrounded hyphens.
// It preserves unknown segments and retained separators exactly, and removes
// duplicate translated segments using normalized comparison, keeping the first.
// When a duplicate is removed, the next retained segment keeps its own preceding
// separator. A whole-phrase mapping is returned verbatim, without splitting it.
func (t *Translator) Translate(value string) string {
	if mapped, ok := t.lookup(value); ok {
		return mapped
	}
	parts := split(value)
	seen := make(map[string]bool, len(parts))
	var result strings.Builder
	changed, kept := false, false
	for _, part := range parts {
		translated, _ := t.lookup(part.text)
		key := normalize(translated)
		// Empty pieces carry formatting (e.g. consecutive or trailing separators),
		// rather than a genre, and must not be deduplicated.
		if key != "" && seen[key] {
			changed = true
			continue
		}
		if key != "" {
			seen[key] = true
		}
		if kept {
			result.WriteString(part.before)
		}
		result.WriteString(translated)
		kept = true
		changed = changed || translated != part.text
	}
	if !changed {
		return value
	}
	return result.String()
}

// TranslateAll translates every value and drops normalized duplicate values,
// keeping order. It does not modify the input slice and preserves a nil input.
func (t *Translator) TranslateAll(values []string) []string {
	if values == nil {
		return nil
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		translated := t.Translate(value)
		key := normalize(translated)
		if !seen[key] {
			result = append(result, translated)
			seen[key] = true
		}
	}
	return result
}

// Changed reports whether TranslateAll would change values.
func (t *Translator) Changed(values []string) bool {
	return !slices.Equal(values, t.TranslateAll(values))
}

// Unknown returns unknown segments that contain French accents or function
// words. Results are trimmed, deduplicated by normalization, and kept in order.
// Recognized whole phrases (including explicit keep-as-is overrides) are skipped.
// This is a logging heuristic, not language identification.
func (t *Translator) Unknown(values []string) []string {
	var result []string
	seen := make(map[string]bool)
	for _, value := range values {
		if _, known := t.lookup(value); known {
			continue
		}
		for _, part := range split(value) {
			key := normalize(part.text)
			if _, known := t.lookup(part.text); known || seen[key] || !looksFrench(part.text) {
				continue
			}
			seen[key] = true
			result = append(result, strings.TrimSpace(part.text))
		}
	}
	return result
}

func looksFrench(value string) bool {
	value = composeFrench(strings.ToLower(value))
	if strings.ContainsAny(value, "àâçéèêëîïôûùüÿœæ") {
		return true
	}
	for _, word := range strings.FieldsFunc(value, func(r rune) bool { return !unicode.IsLetter(r) }) {
		switch word {
		case "de", "du", "des", "pour", "et", "le", "la", "les", "aux", "au", "une", "avec", "sans", "dans":
			return true
		}
	}
	return false
}

func normalize(value string) string {
	value = composeFrench(strings.ToLower(value))
	value = strings.NewReplacer("’", "'", "‘", "'", "ʼ", "'", "œ", "oe", "æ", "ae").Replace(value)
	return strings.Join(strings.Fields(value), " ")
}

// Lowercasing first also handles the uppercase forms of these French letters.
// Other combining marks remain significant; no accent stripping is performed.
func composeFrench(value string) string {
	return frenchCompositions.Replace(value)
}

var frenchCompositions = strings.NewReplacer(
	"e\u0301", "é", "e\u0300", "è", "e\u0302", "ê", "e\u0308", "ë",
	"a\u0300", "à", "a\u0302", "â", "c\u0327", "ç",
	"i\u0302", "î", "i\u0308", "ï", "o\u0302", "ô",
	"u\u0302", "û", "u\u0300", "ù", "u\u0308", "ü", "y\u0308", "ÿ",
)
