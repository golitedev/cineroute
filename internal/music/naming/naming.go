// Package naming renders album folder names from a template such as
// "{albumartist}/{title} ({year})".
package naming

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Fields lists the placeholders a folder template may use.
var Fields = []string{
	"albumartist", "title", "album", "version", "year", "date", "label",
	"genre", "format", "bit_depth", "sampling_rate", "id", "upc",
}

var fieldSet = func() map[string]bool {
	m := make(map[string]bool, len(Fields))
	for _, f := range Fields {
		m[f] = true
	}
	return m
}()

type token struct {
	literal string
	field   string // set for placeholders
}

func parse(tmpl string) ([]token, error) {
	var out []token
	var lit strings.Builder
	for i := 0; i < len(tmpl); i++ {
		c := tmpl[i]
		switch {
		case c == '{' && i+1 < len(tmpl) && tmpl[i+1] == '{':
			lit.WriteByte('{')
			i++
		case c == '}' && i+1 < len(tmpl) && tmpl[i+1] == '}':
			lit.WriteByte('}')
			i++
		case c == '{':
			end := strings.IndexByte(tmpl[i:], '}')
			if end < 0 {
				return nil, fmt.Errorf("unclosed { in %q", tmpl)
			}
			name := strings.TrimSpace(tmpl[i+1 : i+end])
			if !fieldSet[name] {
				return nil, fmt.Errorf("unknown placeholder {%s}; available: %s", name, strings.Join(sortedFields(), ", "))
			}
			if lit.Len() > 0 {
				out = append(out, token{literal: lit.String()})
				lit.Reset()
			}
			out = append(out, token{field: name})
			i += end
		case c == '}':
			return nil, fmt.Errorf("unmatched } in %q (use }} for a literal brace)", tmpl)
		default:
			lit.WriteByte(c)
		}
	}
	if lit.Len() > 0 {
		out = append(out, token{literal: lit.String()})
	}
	return out, nil
}

func sortedFields() []string {
	f := append([]string(nil), Fields...)
	sort.Strings(f)
	return f
}

// Check reports template syntax errors and unknown placeholders.
func Check(tmpl string) error {
	_, err := parse(tmpl)
	return err
}

// Uses reports whether the template contains the placeholder.
func Uses(tmpl, field string) bool {
	toks, err := parse(tmpl)
	if err != nil {
		return false
	}
	for _, t := range toks {
		if t.field == field {
			return true
		}
	}
	return false
}

// Render fills the template and returns a relative, slash-separated path that
// is safe to join under the library folder. Values never create sub folders:
// a "/" inside a value (AC/DC) is removed, only "/" in the template splits.
func Render(tmpl string, values map[string]string, restrict bool) (string, error) {
	toks, err := parse(tmpl)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, t := range toks {
		if t.field == "" {
			b.WriteString(t.literal)
			continue
		}
		b.WriteString(cleanValue(values[t.field]))
	}
	var segs []string
	for _, seg := range strings.Split(strings.ReplaceAll(b.String(), "\\", "/"), "/") {
		seg = cleanSegment(seg, restrict)
		if seg == "" {
			continue
		}
		segs = append(segs, seg)
	}
	if len(segs) == 0 {
		return "", fmt.Errorf("folder template %q produced an empty path", tmpl)
	}
	return strings.Join(segs, "/"), nil
}

// cleanValue removes characters a value must not contribute, most
// importantly path separators.
func cleanValue(v string) string {
	v = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || unicode.IsControl(r) {
			return -1
		}
		return r
	}, v)
	return strings.TrimSpace(v)
}

var (
	emptyBrackets = regexp.MustCompile(`\(\s*\)|\[\s*\]|\{\s*\}|<\s*>`)
	spaces        = regexp.MustCompile(`\s+`)
	reserved      = map[string]bool{
		"CON": true, "PRN": true, "AUX": true, "NUL": true,
		"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
		"COM6": true, "COM7": true, "COM8": true, "COM9": true,
		"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
		"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
	}
)

const maxSegmentBytes = 180

// cleanSegment turns one path segment into a portable folder name. It follows
// streamrip's (pathvalidate's) rules so folder and file names look alike:
// characters invalid on Windows/SMB shares are dropped.
func cleanSegment(s string, restrict bool) string {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '<', '>', ':', '"', '|', '?', '*':
			return -1
		}
		if unicode.IsControl(r) {
			return -1
		}
		if restrict && (r > unicode.MaxASCII || !unicode.IsPrint(r)) {
			return -1
		}
		return r
	}, s)
	for {
		next := emptyBrackets.ReplaceAllString(s, "")
		if next == s {
			break
		}
		s = next
	}
	s = spaces.ReplaceAllString(s, " ")
	s = strings.Trim(s, " -_.,;")
	s = truncate(s, maxSegmentBytes)
	s = strings.TrimRight(s, " .")
	if reserved[strings.ToUpper(s)] {
		s += "_"
	}
	return s
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
