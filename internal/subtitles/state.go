package subtitles

import "time"

// Workflow statuses. They mirror the vocabulary of the proven
// `~/Projects/subs` pipeline: a candidate is only installed when it passes the
// strict timing gate, otherwise the item stays in review.
//
// An item can carry several target languages, so these statuses describe the
// whole movie: StatusPartial means some languages were installed while others
// ran out of candidates, and StatusHasTargets means every configured language
// already had a subtitle.
const (
	StatusPending     = "pending"
	StatusHasTargets  = "has_targets"
	StatusProcessing  = "processing"
	StatusDownloading = "downloading"
	StatusSyncing     = "syncing"
	StatusAdded       = "added"
	// StatusAddedReview marks an unaligned fallback install: the subtitle was
	// written without a passing alass alignment because the reference was
	// unusable and allow_unaligned_fallback is enabled.
	StatusAddedReview = "added_review"
	// StatusPartial means at least one target language was installed while at
	// least one other is still missing.
	StatusPartial     = "partial"
	StatusNeedsReview = "needs_review"
	StatusNoMatch     = "no_match"
	StatusNoReference = "no_reference"
	StatusFailed      = "failed"
	StatusSkipped     = "skipped"
)

// legacyStatusHasSwedish is the pre-multi-language name of StatusHasTargets. It
// is still recognized so an item stored by an older build is reconciled on the
// next scan instead of looking permanently finished.
const legacyStatusHasSwedish = "has_swedish"

func isHasTargetsStatus(status string) bool {
	return status == StatusHasTargets || status == legacyStatusHasSwedish
}

// transientStatuses are reset to pending when the process restarts, since no
// work can still be in flight after a restart.
func isTransientStatus(status string) bool {
	switch status {
	case StatusProcessing, StatusDownloading, StatusSyncing:
		return true
	default:
		return false
	}
}

// Per-target statuses. They reuse the item vocabulary where it applies and add
// TargetPresent for a language that already had a subtitle before the run.
const (
	// TargetPresent marks a language that already has a usable subtitle, either
	// an external file next to the movie or an embedded stream.
	TargetPresent = "present"
)

// TargetState is the subtitle state of one target language for one movie. The
// page shows one line per configured language, so a movie that got Swedish but
// has no Spanish candidate is visible instead of looking simply "added".
type TargetState struct {
	Language string `json:"language"`
	// Present is true when a usable subtitle in this language already exists:
	// an external file next to the video or an embedded stream.
	Present bool `json:"present"`
	// Sources lists where an already-present subtitle was found.
	Sources []string `json:"sources,omitempty"`
	// Status is the outcome for this language: present, added, added_review,
	// no_match, needs_review, no_reference, failed or pending.
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	// OutputPath is the installed `<video>.<language>.srt`, when there is one.
	OutputPath  string `json:"output_path,omitempty"`
	OutputBytes int64  `json:"output_bytes,omitempty"`

	FileID   int      `json:"file_id,omitempty"`
	Release  string   `json:"release,omitempty"`
	Category string   `json:"category,omitempty"`
	Score    float64  `json:"score,omitempty"`
	Variant  string   `json:"variant,omitempty"`
	Metrics  *Metrics `json:"metrics,omitempty"`
}

// resolved reports whether this language has a subtitle, either because it was
// already there or because one was installed.
func (t TargetState) resolved() bool {
	switch t.Status {
	case TargetPresent, StatusAdded, StatusAddedReview:
		return true
	default:
		return false
	}
}

// EmbeddedSubtitle describes one subtitle stream reported by ffprobe.
type EmbeddedSubtitle struct {
	Index    int    `json:"index"`
	Codec    string `json:"codec"`
	Language string `json:"language,omitempty"`
	Title    string `json:"title,omitempty"`
	Forced   bool   `json:"forced,omitempty"`
	Default  bool   `json:"default,omitempty"`
	// Usable is false for image-based streams (PGS, DVD, DVB, XSUB), which
	// cannot be used as an alass reference.
	Usable bool `json:"usable"`
}

// ExternalSubtitleRef is one subtitle file sitting next to a video file.
type ExternalSubtitleRef struct {
	FileName string `json:"file_name"`
	Language string `json:"language,omitempty"`
	Ext      string `json:"ext,omitempty"`
	// Usable is false for image-based formats (.sub/.idx VobSub) that alass
	// cannot parse.
	Usable bool `json:"usable"`
}

// Reference is the timing reference used for synchronization.
type Reference struct {
	Basename string `json:"basename"`
	Title    string `json:"title"`
	Year     int    `json:"year,omitempty"`
	Language string `json:"language,omitempty"`
}

// Item is the durable workflow record for one remote video file. Subtitles are
// per video file, so a folder with several versions produces several items.
type Item struct {
	ID         string `json:"id"`
	DriveID    string `json:"drive_id"`
	RemoteRoot string `json:"remote_root"`
	FolderName string `json:"folder_name"`
	FolderPath string `json:"folder_path"`
	VideoPath  string `json:"video_path"`
	VideoName  string `json:"video_name"`
	VideoSize  int64  `json:"video_size"`
	VideoMtime int64  `json:"video_mtime"`
	DurationMS int64  `json:"duration_ms,omitempty"`

	Title  string `json:"title"`
	Year   int    `json:"year"`
	Status string `json:"status"`
	Step   string `json:"step,omitempty"`
	// StepDetail is the short progress line for the current stage, for example
	// "42% · 0:50:00 of 2:00:00" while an embedded reference is demuxed. An
	// embedded extraction reads the whole video file, so without it the page can
	// only show a spinner for several minutes and cannot be told apart from a
	// stuck job.
	StepDetail string `json:"step_detail,omitempty"`
	Error      string `json:"error,omitempty"`

	// Probed reports whether the video's subtitle streams have been read. A movie
	// whose scan ran past the probe budget is queued but not analyzed yet, which
	// is different from a movie with no subtitle streams at all.
	Probed               bool                  `json:"probed"`
	ExistingSubLanguages []string              `json:"existing_sub_languages,omitempty"`
	ExternalSubtitles    []ExternalSubtitleRef `json:"external_subtitles,omitempty"`
	EmbeddedSubStreams   []EmbeddedSubtitle    `json:"embedded_sub_streams,omitempty"`
	// HasExternalSubtitle is true when a usable text subtitle file sits next to
	// the video, which is the easy case. Movies without one need their reference
	// extracted from an embedded stream (or report no_reference).
	HasExternalSubtitle bool `json:"has_external_subtitle"`
	// Targets is the per-language state for every configured target language, in
	// configuration order. It is what the page renders as "sv ✓ / es ✗".
	Targets []TargetState `json:"targets,omitempty"`

	ReferenceKind   string `json:"reference_kind,omitempty"`
	ReferenceLang   string `json:"reference_lang,omitempty"`
	ReferenceStream int    `json:"reference_stream,omitempty"`
	ReferencePath   string `json:"reference_path,omitempty"`

	WorkDir string `json:"work_dir,omitempty"`

	Attempts  int       `json:"attempts"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Target returns the state of one target language.
func (i *Item) Target(language string) *TargetState {
	for index := range i.Targets {
		if i.Targets[index].Language == language {
			return &i.Targets[index]
		}
	}
	return nil
}

// MissingTargets lists the languages that still have no subtitle.
func (i *Item) MissingTargets() []string {
	var out []string
	for _, target := range i.Targets {
		if !target.resolved() {
			out = append(out, target.Language)
		}
	}
	return out
}

// HasAllTargets reports whether every configured language has a subtitle.
func (i *Item) HasAllTargets() bool {
	if len(i.Targets) == 0 {
		return isHasTargetsStatus(i.Status)
	}
	for _, target := range i.Targets {
		if !target.resolved() {
			return false
		}
	}
	return true
}

// Attempt records one downloaded candidate and the alass/metrics outcome, so a
// reviewed item can be audited exactly like the Python `attempts` table.
type Attempt struct {
	ItemID   string    `json:"item_id"`
	FileID   int       `json:"file_id"`
	Language string    `json:"language,omitempty"`
	Status   string    `json:"status"`
	Category string    `json:"category,omitempty"`
	Release  string    `json:"release,omitempty"`
	Metrics  *Metrics  `json:"metrics,omitempty"`
	Detail   string    `json:"detail,omitempty"`
	At       time.Time `json:"at"`
}

// BatchStatus is the progress of the current scan or run job.
type BatchStatus struct {
	Running  bool   `json:"running"`
	Canceled bool   `json:"canceled"`
	Kind     string `json:"kind,omitempty"`
	Total    int    `json:"total"`
	Done     int    `json:"done"`
	Current  string `json:"current,omitempty"`
	Stage    string `json:"stage,omitempty"`
	Error    string `json:"error,omitempty"`
}

// QuotaView reports the OpenSubtitles download quota when known.
type QuotaView struct {
	Known     bool   `json:"known"`
	Remaining int    `json:"remaining"`
	Allowed   int    `json:"allowed"`
	ResetAt   string `json:"reset_at,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Stats summarizes the queue for the page header.
type Stats struct {
	Total       int `json:"total"`
	Pending     int `json:"pending"`
	HasTargets  int `json:"has_targets"`
	Added       int `json:"added"`
	Partial     int `json:"partial"`
	NeedsReview int `json:"needs_review"`
	NoMatch     int `json:"no_match"`
	NoReference int `json:"no_reference"`
	Failed      int `json:"failed"`
	Skipped     int `json:"skipped"`
	Processing  int `json:"processing"`
	// NoExternalSubtitle counts movies still needing subtitles that have no
	// usable subtitle file next to the video, so the reference must come from an
	// embedded stream.
	NoExternalSubtitle int `json:"no_external_subtitle"`
	// WithExternalSubtitle counts movies still needing subtitles that do have an
	// external text subtitle to use as the reference.
	WithExternalSubtitle int `json:"with_external_subtitle"`
	// NotAnalyzed counts movies still needing subtitles whose subtitle streams
	// have not been read yet (a scan stopped at the probe budget).
	NotAnalyzed int `json:"not_analyzed"`
}

// Canceled produces a cancel error without importing context everywhere.
type CanceledError struct{}

func (CanceledError) Error() string { return "operation canceled" }
