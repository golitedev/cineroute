package jobs

import "testing"

func TestEnglishMessages(t *testing.T) {
	cases := map[string]Msg{
		"Waiting in queue": M("queued"),
		"Not added to the library: 2 of 10 tracks failed. Qobuz is rate limiting requests. Tried 2 times. The files were kept in /s/x.": M("incomplete", "failed", 2, "total", 10).
			With("rate_limited", true).With("tries", 2).With("kept_path", "/s/x"),
		"Added to your library (FLAC 24-bit / 96 kHz)": M("added", "format", "FLAC", "bits", 24, "khz", "96"),
		"Added to your library":                        M("added"),
		"Attempt 1 failed, trying again in 30 s":       M("retry_wait", "attempt", 1, "seconds", 30),
		"Could not run streamrip: boom":                M("error_streamrip", "error", "boom"),
		"The staging disk is full.":                    M("failure_disk_full"),
	}
	for want, m := range cases {
		if got := m.English(); got != want {
			t.Errorf("%s: got %q, want %q", m.Code, got, want)
		}
	}
	// Every code has an English text.
	for code := range english {
		if M(code).English() == code {
			t.Errorf("%s has no English text", code)
		}
	}
	for _, code := range failureCodes {
		if M(code).English() == code {
			t.Errorf("%s has no English text", code)
		}
	}
}
