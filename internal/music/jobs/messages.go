package jobs

import (
	"fmt"
	"path/filepath"
	"strings"

	"cineroute/internal/music/streamrip"
	"cineroute/internal/music/verify"
)

// Msg is a job message as a code plus values, so the web UI can show it in
// the user's language. English renders the English text kept in
// Job.Message for logs and API clients that don't translate.
type Msg struct {
	Code string
	Args map[string]any
}

// M builds a message from a code and key/value pairs.
func M(code string, kv ...any) Msg {
	m := Msg{Code: code, Args: map[string]any{}}
	for i := 0; i+1 < len(kv); i += 2 {
		m.Args[kv[i].(string)] = kv[i+1]
	}
	return m
}

// With returns a copy with one more value.
func (m Msg) With(key string, v any) Msg {
	args := make(map[string]any, len(m.Args)+1)
	for k, val := range m.Args {
		args[k] = val
	}
	args[key] = v
	return Msg{Code: m.Code, Args: args}
}

var english = map[string]string{
	"queued":               "Waiting in queue",
	"starting":             "Starting",
	"downloading":          "Downloading",
	"fixing_flac":          "Fixing FLAC metadata",
	"importing":            "Moving into the library",
	"cancelled":            "Cancelled",
	"cancelling":           "Cancelling…",
	"already_in_library":   "Already in your library",
	"interrupted_restart":  "Interrupted by a restart, queued again",
	"interrupted_shutdown": "Interrupted by shutdown, will resume",
	"retry_wait":           "Attempt {attempt} failed, trying again in {seconds} s",
	"checking":             "Checking {tracks} tracks",
	"added":                "Added to your library",
	"incomplete":           "Not added to the library: {failed} of {total} tracks failed.",
	"has_problems":         "Not added to the library: the download has problems.",
	"no_tracks":            "Not added to the library: streamrip did not download any tracks.",
	"timeout":              "The download took longer than {minutes} minutes and was stopped.",
	"error_album":          "Could not load the album from Qobuz: {error}",
	"error_staging":        "Could not prepare the staging folder: {error}",
	"error_streamrip":      "Could not run streamrip: {error}",
	"error_verify":         "Could not check the download: {error}",
	"error_fix":            "Could not fix the FLAC files: {error}",
	"error_naming":         "Could not name the album folder: {error}",
	"error_move_aside":     "Could not move the existing album aside: {error}",
	"error_import":         "Could not move the album into the library: {error}",
}

// English renders the message in English.
func (m Msg) English() string {
	s, ok := english[m.Code]
	if !ok {
		if f, isFailure := failureByCode[m.Code]; isFailure {
			s = f.Message()
		} else {
			s = m.Code
		}
	}
	for k, v := range m.Args {
		s = strings.ReplaceAll(s, "{"+k+"}", fmt.Sprint(v))
	}
	if m.Code == "added" {
		if f, ok := m.Args["format"]; ok {
			s += fmt.Sprintf(" (%v %v-bit / %v kHz)", f, m.Args["bits"], m.Args["khz"])
		}
	}
	if v, _ := m.Args["rate_limited"].(bool); v {
		s += " Qobuz is rate limiting requests."
	}
	if n, ok := m.Args["tries"]; ok {
		s += fmt.Sprintf(" Tried %v times.", n)
	}
	if p, ok := m.Args["kept_path"]; ok {
		s += fmt.Sprintf(" The files were kept in %v.", p)
	}
	return s
}

// set stores a message on a job.
func (j *Job) set(m Msg) {
	j.Message = m.English()
	j.MessageCode = m.Code
	j.MessageArgs = m.Args
	if j.MessageArgs == nil {
		j.MessageArgs = map[string]any{}
	}
}

var failureCodes = map[streamrip.Failure]string{
	streamrip.FailureAuth:               "failure_auth",
	streamrip.FailureIneligible:         "failure_ineligible",
	streamrip.FailureAppID:              "failure_app_id",
	streamrip.FailureRateLimited:        "failure_rate_limited",
	streamrip.FailureDiskFull:           "failure_disk_full",
	streamrip.FailureMissingCredentials: "failure_missing_credentials",
	streamrip.FailureCrash:              "failure_crash",
}

var failureByCode = func() map[string]streamrip.Failure {
	m := map[string]streamrip.Failure{}
	for f, c := range failureCodes {
		m[c] = f
	}
	return m
}()

func failureMsg(f streamrip.Failure) Msg { return M(failureCodes[f]) }

// ProblemItem is one problem of a failed album, in a form the UI can
// translate. Text is the English line.
type ProblemItem struct {
	Disc        int    `json:"disc"`
	Track       int    `json:"track"`
	Title       string `json:"title"`
	Reason      string `json:"reason"`
	Detail      string `json:"detail"`
	Unavailable bool   `json:"unavailable"`
	Text        string `json:"text"`
}

// reasonCodes maps verify's reasons to stable codes.
func reasonCode(r string) string {
	return strings.ReplaceAll(strings.TrimSpace(strings.ToLower(r)), " ", "_")
}

func verifyProblem(p verify.Problem, unavailable bool) ProblemItem {
	text := p.String()
	if unavailable {
		text += " (Qobuz does not offer this track for streaming)"
	}
	title := p.Title
	if title == "" && p.Path != "" {
		title = filepath.Base(p.Path)
	}
	return ProblemItem{
		Disc: p.Disc, Track: p.Number, Title: title,
		Reason: reasonCode(p.Reason), Detail: p.Detail,
		Unavailable: unavailable, Text: text,
	}
}

func ripProblem(line string) ProblemItem {
	return ProblemItem{Reason: "streamrip", Detail: line, Text: "streamrip: " + line}
}

func fileProblem(rel string, err error) ProblemItem {
	return ProblemItem{Reason: "file_error", Title: rel, Detail: err.Error(), Text: fmt.Sprintf("%s: %v", rel, err)}
}

func moreProblem(n int) ProblemItem {
	return ProblemItem{Reason: "more", Detail: fmt.Sprint(n), Text: fmt.Sprintf("… and %d more", n)}
}

func problemTexts(items []ProblemItem) []string {
	out := make([]string, len(items))
	for i, p := range items {
		out[i] = p.Text
	}
	return out
}
