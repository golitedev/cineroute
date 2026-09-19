package web

import (
	"os"
	"strings"
	"testing"
)

// TestSubtitleScriptIsTopLevel guards a whole class of silent front-end
// breakage: if the Subtitles JavaScript block is ever nested inside another
// function (for example because a text replacement matched a call site inside
// login()), every Go test still passes, but the page renders empty and every
// button silently throws "function is not defined". The check walks the script
// tracking brace depth and requires the page entry points to be declared at the
// top level.
func TestSubtitleScriptIsTopLevel(t *testing.T) {
	data, err := os.ReadFile("templates/index.html")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	script := extractInlineScript(t, string(data))
	if script == "" {
		t.Fatal("no inline script found in the template")
	}

	// Entry points the page markup calls directly (onclick/showView) plus the
	// functions they depend on. Every one of these must be global.
	wanted := []string{
		"showView",
		"loadSubtitles",
		"renderSubtitles",
		"renderSubtitleDetail",
		"subtitleRow",
		"subtitleStatusLabel",
		"subtitleStatusClass",
		"subtitleExternalText",
		"subtitleReferenceText",
		"subtitleMetricsText",
		"scanSubtitles",
		"runSubtitles",
		"cancelSubtitleJob",
		"clearSubtitleWork",
		"saveSubtitleSettings",
		"runSubtitleItem",
		"retrySubtitleItem",
		"skipSubtitleItem",
		"openSubtitleItem",
		"closeSubtitleItem",
		"setSubtitleFilter",
		"setSubtitleSearch",
		"clearSubtitleSearch",
		"loadHardlinks",
		"loadCompanions",
		"loadStatus",
	}
	for _, name := range wanted {
		pos := strings.Index(script, "function "+name+"(")
		if pos < 0 {
			t.Errorf("function %s is missing from the page script", name)
			continue
		}
		if depth := jsBraceDepth(script[:pos]); depth != 0 {
			t.Errorf("function %s is nested at brace depth %d; the subtitles script block must stay at the top level of the page script", name, depth)
		}
	}
}

func extractInlineScript(t *testing.T, html string) string {
	t.Helper()
	start := strings.Index(html, "<script>")
	if start < 0 {
		return ""
	}
	end := strings.Index(html[start:], "</script>")
	if end < 0 {
		return ""
	}
	return html[start+len("<script>") : start+end]
}

// jsBraceDepth returns the brace nesting depth at the end of src. It skips
// comments, string literals and regex literals well enough for this template,
// which is all the check needs.
func jsBraceDepth(src string) int {
	depth := 0
	var previous rune
	for i := 0; i < len(src); i++ {
		switch c := rune(src[i]); {
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			if next := strings.IndexByte(src[i:], '\n'); next >= 0 {
				i += next
			} else {
				i = len(src)
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			if next := strings.Index(src[i+2:], "*/"); next >= 0 {
				i += next + 3
			} else {
				i = len(src)
			}
		case c == '"' || c == '\'' || c == '`':
			quote := byte(c)
			i++
			for i < len(src) {
				if src[i] == '\\' {
					i += 2
					continue
				}
				if src[i] == quote {
					break
				}
				i++
			}
			previous = 'x'
		case c == '/' && strings.ContainsRune("(,=:[!&|?{};+*%<>~", previous):
			i++
			for i < len(src) {
				if src[i] == '\\' {
					i += 2
					continue
				}
				if src[i] == '/' {
					break
				}
				i++
			}
			previous = 'x'
		default:
			switch c {
			case '{':
				depth++
			case '}':
				depth--
			}
			if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
				previous = c
			}
		}
	}
	return depth
}
