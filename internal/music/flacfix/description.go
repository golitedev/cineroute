package flacfix

import (
	"html"
	"regexp"
	"strings"
)

var (
	brRe        = regexp.MustCompile(`(?i)<br\s*/?>`)
	paragraphRe = regexp.MustCompile(`(?i)</?(p|div)(\s[^<>]*)?>`)
	listItemRe  = regexp.MustCompile(`(?i)</li\s*>`)
	tagRe       = regexp.MustCompile(`</?[A-Za-z][A-Za-z0-9]*(\s[^<>]*)?/?>`)
	spacesRe    = regexp.MustCompile(`[ \t]+`)
	blankRe     = regexp.MustCompile(`\n{3,}`)
)

// CleanDescription turns the HTML Qobuz uses in album descriptions into
// plain text: <br> becomes a line break, paragraphs become blank lines,
// other tags are removed (their text is kept) and entities such as &amp;
// are decoded. Text without markup is returned unchanged.
func CleanDescription(s string) string {
	if !strings.ContainsAny(s, "<&") {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = brRe.ReplaceAllString(s, "\n")
	s = paragraphRe.ReplaceAllString(s, "\n\n")
	s = listItemRe.ReplaceAllString(s, "\n")
	s = tagRe.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = strings.ReplaceAll(s, " ", " ")

	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(spacesRe.ReplaceAllString(l, " "))
	}
	s = strings.Join(lines, "\n")
	s = blankRe.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}
