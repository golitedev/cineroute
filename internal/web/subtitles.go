package web

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"cineroute/internal/config"
	"cineroute/internal/subtitles"
)

// subtitleRuntimeConfig adapts the YAML configuration to the subtitle
// subsystem's runtime configuration, keeping the built-in defaults for fields
// the file leaves unset.
func subtitleRuntimeConfig(cfg *config.Config) subtitles.Config {
	out := subtitles.DefaultConfig()
	s := cfg.Subtitles
	out.Enabled = s.Enabled
	if s.StatePath != "" {
		out.StatePath = s.StatePath
	}
	if s.WorkDir != "" {
		out.WorkDir = s.WorkDir
	}
	if s.WorkRetentionDays != 0 {
		out.WorkRetentionDays = s.WorkRetentionDays
	}
	if s.TargetLanguage != "" {
		out.TargetLanguage = s.TargetLanguage
	}
	if len(s.ReferenceLanguages) > 0 {
		out.ReferenceLanguages = append([]string(nil), s.ReferenceLanguages...)
	}
	if s.FFmpegPath != "" {
		out.FFmpegPath = s.FFmpegPath
	}
	if s.FFprobePath != "" {
		out.FFprobePath = s.FFprobePath
	}
	if s.AlassPath != "" {
		out.AlassPath = s.AlassPath
	}
	if s.ScanBatchSize != 0 {
		out.ScanBatchSize = s.ScanBatchSize
	}
	if s.RunBatchSize != 0 {
		out.RunBatchSize = s.RunBatchSize
	}
	if s.RequestIntervalMS != 0 {
		out.RequestInterval = time.Duration(s.RequestIntervalMS) * time.Millisecond
	}
	out.QuotaReserve = s.QuotaReserve
	if s.MaxCandidates != 0 {
		out.MaxCandidates = s.MaxCandidates
	}
	out.UseMoviehash = s.UseMoviehash
	out.RemovePromoCues = s.RemovePromoCues
	out.AllowUnalignedFallback = s.AllowUnalignedFallback
	if s.MinReferenceCues != 0 {
		out.MinReferenceCues = s.MinReferenceCues
	}
	if s.MinVideoBytes != 0 {
		out.MinVideoBytes = s.MinVideoBytes
	}
	out.SkipSampleFiles = s.SkipSampleFiles
	if s.ProbeTimeoutSeconds != 0 {
		out.ProbeTimeout = time.Duration(s.ProbeTimeoutSeconds) * time.Second
	}
	if s.ExtractTimeoutSeconds != 0 {
		out.ExtractTimeout = time.Duration(s.ExtractTimeoutSeconds) * time.Second
	}
	if s.SyncTimeoutSeconds != 0 {
		out.SyncTimeout = time.Duration(s.SyncTimeoutSeconds) * time.Second
	}
	out.AlassNoSplit = s.AlassNoSplit
	if s.AlassSplitPenalty != 0 {
		out.AlassSplitPenalty = s.AlassSplitPenalty
	}
	out.OverwriteExisting = s.OverwriteExisting
	if s.Accept.MinWithin2 != 0 || s.Accept.MaxP90Seconds != 0 {
		out.Accept = subtitles.AcceptCriteria{
			MinWithin2:        s.Accept.MinWithin2,
			MaxP90Seconds:     s.Accept.MaxP90Seconds,
			MaxStartGapMin:    s.Accept.MaxStartGapMin,
			MaxEndGapMin:      s.Accept.MaxEndGapMin,
			MaxZeroStartCues:  s.Accept.MaxZeroStartCues,
			MaxCueMinutes:     s.Accept.MaxCueMinutes,
			MaxOverrunMinutes: s.Accept.MaxOverrunMinutes,
		}
	}
	if len(s.TitleOverrides) > 0 {
		out.TitleOverrides = map[string]string{}
		for local, canonical := range s.TitleOverrides {
			out.TitleOverrides[subtitles.NormalizedTitle(local)] = canonical
		}
	}
	if s.OpenSubtitles.BaseURL != "" {
		out.OpenSubtitles.BaseURL = s.OpenSubtitles.BaseURL
	}
	out.OpenSubtitles.APIKey = s.OpenSubtitles.APIKey
	out.OpenSubtitles.Username = s.OpenSubtitles.Username
	out.OpenSubtitles.Password = s.OpenSubtitles.Password
	out.OpenSubtitles.RequestInterval = out.RequestInterval
	return out
}

func (s *Server) listSubtitles(w http.ResponseWriter, r *http.Request) {
	if s.subtitles == nil {
		writeErr(w, http.StatusServiceUnavailable, "subtitle subsystem is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, s.subtitles.View(r.URL.Query().Get("open")))
}

func (s *Server) updateSubtitleSettings(w http.ResponseWriter, r *http.Request) {
	if s.subtitles == nil {
		writeErr(w, http.StatusServiceUnavailable, "subtitle subsystem is unavailable")
		return
	}
	var body struct {
		RunBatchSize           *int  `json:"run_batch_size"`
		RequestIntervalMS      *int  `json:"request_interval_ms"`
		MaxCandidates          *int  `json:"max_candidates"`
		QuotaReserve           *int  `json:"quota_reserve"`
		UseMoviehash           *bool `json:"use_moviehash"`
		RemovePromoCues        *bool `json:"remove_promo_cues"`
		AllowUnalignedFallback *bool `json:"allow_unaligned_fallback"`
		MinReferenceCues       *int  `json:"min_reference_cues"`
		AlassNoSplit           *bool `json:"alass_no_split"`
		AlassSplitPenalty      *int  `json:"alass_split_penalty"`
		OverwriteExisting      *bool `json:"overwrite_existing"`
		WorkRetentionDays      *int  `json:"work_retention_days"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	current := s.subtitles.Settings()
	patch := current
	if body.RunBatchSize != nil {
		patch.RunBatchSize = *body.RunBatchSize
	}
	if body.RequestIntervalMS != nil {
		patch.RequestIntervalMS = *body.RequestIntervalMS
	}
	if body.MaxCandidates != nil {
		patch.MaxCandidates = *body.MaxCandidates
	}
	if body.QuotaReserve != nil {
		patch.QuotaReserve = *body.QuotaReserve
	}
	if body.UseMoviehash != nil {
		patch.UseMoviehash = *body.UseMoviehash
	}
	if body.RemovePromoCues != nil {
		patch.RemovePromoCues = *body.RemovePromoCues
	}
	if body.AllowUnalignedFallback != nil {
		patch.AllowUnalignedFallback = *body.AllowUnalignedFallback
	}
	if body.MinReferenceCues != nil {
		patch.MinReferenceCues = *body.MinReferenceCues
	}
	if body.AlassNoSplit != nil {
		patch.AlassNoSplit = *body.AlassNoSplit
	}
	if body.AlassSplitPenalty != nil {
		patch.AlassSplitPenalty = *body.AlassSplitPenalty
	}
	if body.OverwriteExisting != nil {
		patch.OverwriteExisting = *body.OverwriteExisting
	}
	if body.WorkRetentionDays != nil {
		patch.WorkRetentionDays = *body.WorkRetentionDays
	}
	if err := s.subtitles.UpdateSettings(patch); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.subtitles.View(""))
}

func (s *Server) scanSubtitles(w http.ResponseWriter, r *http.Request) {
	if s.subtitles == nil {
		writeErr(w, http.StatusServiceUnavailable, "subtitle subsystem is unavailable")
		return
	}
	if err := s.subtitles.StartScan(); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, s.subtitles.View(""))
}

func (s *Server) runSubtitles(w http.ResponseWriter, r *http.Request) {
	if s.subtitles == nil {
		writeErr(w, http.StatusServiceUnavailable, "subtitle subsystem is unavailable")
		return
	}
	if err := s.subtitles.StartRun(); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, s.subtitles.View(""))
}

func (s *Server) cancelSubtitleJob(w http.ResponseWriter, r *http.Request) {
	if s.subtitles == nil {
		writeErr(w, http.StatusServiceUnavailable, "subtitle subsystem is unavailable")
		return
	}
	if err := s.subtitles.CancelScan(); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, s.subtitles.View(""))
}

func (s *Server) clearSubtitleWork(w http.ResponseWriter, r *http.Request) {
	if s.subtitles == nil {
		writeErr(w, http.StatusServiceUnavailable, "subtitle subsystem is unavailable")
		return
	}
	removed, err := s.subtitles.ClearWork()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	view := s.subtitles.View("")
	writeJSON(w, http.StatusOK, map[string]any{"removed": removed, "view": view})
}

func (s *Server) runSubtitleItem(w http.ResponseWriter, r *http.Request) {
	s.subtitleItemAction(w, r, func(id string) error { return s.subtitles.RunOne(id, false) })
}

func (s *Server) retrySubtitleItem(w http.ResponseWriter, r *http.Request) {
	s.subtitleItemAction(w, r, func(id string) error { return s.subtitles.RunOne(id, true) })
}

func (s *Server) subtitleItemAction(w http.ResponseWriter, r *http.Request, action func(string) error) {
	if s.subtitles == nil {
		writeErr(w, http.StatusServiceUnavailable, "subtitle subsystem is unavailable")
		return
	}
	id := r.PathValue("id")
	if err := action(id); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, s.subtitles.View(id))
}

func (s *Server) skipSubtitleItem(w http.ResponseWriter, r *http.Request) {
	if s.subtitles == nil {
		writeErr(w, http.StatusServiceUnavailable, "subtitle subsystem is unavailable")
		return
	}
	id := r.PathValue("id")
	if err := s.subtitles.Skip(id); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.subtitles.View(id))
}

func (s *Server) resetSubtitleItem(w http.ResponseWriter, r *http.Request) {
	if s.subtitles == nil {
		writeErr(w, http.StatusServiceUnavailable, "subtitle subsystem is unavailable")
		return
	}
	id := r.PathValue("id")
	if err := s.subtitles.Reset(id); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.subtitles.View(id))
}

// subtitlesStatus is the compact integration block exposed by /api/status.
type subtitlesStatus struct {
	Enabled       bool   `json:"enabled"`
	FFmpeg        string `json:"ffmpeg"`
	Alass         string `json:"alass"`
	AlassVersion  string `json:"alass_version,omitempty"`
	OpenSubtitles string `json:"opensubtitles"`
	WorkDir       string `json:"work_dir"`
	WorkDirOK     bool   `json:"work_dir_writable"`
	QuotaKnown    bool   `json:"quota_known"`
	QuotaLeft     int    `json:"quota_remaining"`
	QuotaAllowed  int    `json:"quota_allowed"`
	QuotaResetAt  string `json:"quota_reset_at,omitempty"`
	StateError    string `json:"state_error,omitempty"`
}

func (s *Server) subtitlesStatus(ctx context.Context) subtitlesStatus {
	if s.subtitles == nil {
		return subtitlesStatus{}
	}
	integration := s.subtitles.Integration(ctx)
	quota := s.subtitles.Quota()
	status := subtitlesStatus{
		Enabled:       s.subtitles.Enabled(),
		FFmpeg:        integration.FFmpeg,
		Alass:         integration.Alass,
		AlassVersion:  integration.AlassVersion,
		OpenSubtitles: integration.OpenSubtitles,
		WorkDir:       integration.WorkDir,
		WorkDirOK:     integration.WorkDirOK,
		QuotaKnown:    quota.Known,
		QuotaLeft:     quota.Remaining,
		QuotaAllowed:  quota.Allowed,
		QuotaResetAt:  quota.ResetAt,
	}
	if err := s.subtitles.StateError(); err != nil {
		status.StateError = err.Error()
	}
	return status
}
