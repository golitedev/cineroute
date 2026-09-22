package subtitles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"cineroute/internal/subtitles/opensubtitles"
)

// OSClient is the slice of the OpenSubtitles API the pipeline needs. It is an
// interface so tests can drive the pipeline without network access.
type OSClient interface {
	Configured() bool
	Search(ctx context.Context, query opensubtitles.SearchQuery) (opensubtitles.SearchResponse, error)
	Features(ctx context.Context, query string) (opensubtitles.FeatureResponse, error)
	Download(ctx context.Context, fileID int) (opensubtitles.DownloadResponse, error)
	DownloadBytes(ctx context.Context, link string) ([]byte, error)
	UserInfo(ctx context.Context) (opensubtitles.UserInfoResponse, error)
}

// runOptions configures one pipeline run for an item.
type runOptions struct {
	// refreshSearch ignores the cached search results.
	refreshSearch bool
	// maxCandidates caps how many downloads are attempted for the item.
	maxCandidates int
	// refreshQuota fetches /infos/user before the candidate loop.
	refreshQuota bool
	// previousStatus is the item's status before processing started, so an
	// already-installed subtitle keeps its `added` record.
	previousStatus string
}

// errQuotaStop signals the batch must stop because the quota reserve was hit.
var errQuotaStop = errors.New("opensubtitles quota reserve reached")

// processItem runs the full workflow for one item and returns the resulting
// status.
//
// One run can serve several target languages. OpenSubtitles is searched once for
// every language the movie is missing, the timing reference is extracted at most
// once and reused by all of them, and each language is then downloaded,
// synchronized and installed on its own. A language without a safe candidate is
// simply left unresolved: the languages that do have candidates still proceed.
func (m *Manager) processItem(ctx context.Context, item *Item, opts runOptions) (string, error) {
	external, err := discoverExternalSubtitles(filepath.Dir(item.VideoPath), item.VideoName)
	if err != nil {
		return StatusFailed, fmt.Errorf("read movie folder: %w", err)
	}
	// A new run invalidates the previous message; the summary of this run (for
	// example "en no match") is written before returning.
	item.Error = ""

	// A scan only probes up to scan_batch_size videos per run, so a movie can be
	// queued without ever being analyzed. Probe it here instead of reporting
	// "no reference" from missing data.
	if !item.Probed {
		m.setStage(item, "probe", StatusProcessing)
		slog.Info("subtitles: probing movie on demand", "id", item.ID, "video", item.VideoPath)
		info, probeErr := m.prober.Probe(ctx, item.VideoPath)
		if probeErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return StatusPending, ctxErr
			}
			return StatusFailed, fmt.Errorf("probe video: %w", probeErr)
		}
		item.EmbeddedSubStreams = info.Streams
		item.DurationMS = info.DurationMS
		item.Probed = true
		item.ExistingSubLanguages = mergeLanguages(external, info.Streams)
		slog.Info("subtitles: movie analyzed", "id", item.ID, "embedded_streams", len(info.Streams), "duration_ms", info.DurationMS)
	}

	// Rebuild the per-language state from what is on disk right now, so a
	// subtitle that appeared (or was deleted) since the last scan decides what
	// this run has to do.
	item.Targets = m.reconcileItemTargets(item, external)
	missing := item.MissingTargets()
	if len(missing) == 0 {
		item.Error = ""
		slog.Info("subtitles: every target language already has a subtitle",
			"id", item.ID, "video", item.VideoPath, "targets", describeTargets(m.cfg.TargetLanguages))
		if opts.previousStatus == StatusAdded || opts.previousStatus == StatusAddedReview {
			return opts.previousStatus, nil
		}
		return StatusHasTargets, nil
	}
	item.Error = ""

	workDir := m.itemWorkDir(item.ID)
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return StatusFailed, fmt.Errorf("create work directory %s: %w (the container user must be able to write subtitles.work_dir; see the Subtitles section of the README)", workDir, err)
	}
	item.WorkDir = workDir

	// 1. Search once for every missing language. The search needs only the movie
	// identity, so a movie OpenSubtitles has nothing for is abandoned before an
	// embedded reference is extracted, which demuxes the whole video file and
	// costs minutes per movie.
	m.setStage(item, "search", StatusProcessing)
	candidates, err := m.collectCandidates(ctx, item, missing, opts)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return StatusPending, ctxErr
		}
		return StatusFailed, err
	}
	slog.Info("subtitles: candidates audited",
		"id", item.ID, "safe", len(candidates), "languages", describeTargets(missing))

	if opts.refreshQuota {
		m.refreshQuota(ctx)
	}
	// The quota is checked here as well as in the download loop: once the reserve
	// is reached nothing can be downloaded, so demuxing a movie to sync a
	// subtitle that will never be fetched is pure waste.
	if remaining, known := m.quotaRemaining(); known && remaining <= m.cfg.QuotaReserve {
		slog.Warn("subtitles: stopping before the reference extraction at the download quota reserve",
			"id", item.ID, "remaining", remaining, "reserve", m.cfg.QuotaReserve)
		return StatusPending, errQuotaStop
	}

	// 2. Work through the languages one at a time. The reference is resolved
	// lazily, on the first language that actually has a candidate.
	var reference *referenceResult
	for index := range missing {
		language := missing[index]
		if ctxErr := ctx.Err(); ctxErr != nil {
			return StatusPending, ctxErr
		}
		target := item.Target(language)
		if target == nil {
			// Defensive: an item stored before this language was configured.
			item.Targets = append(item.Targets, TargetState{Language: language, Status: StatusPending})
			target = item.Target(language)
		}

		list := candidatesForLanguage(candidates, language)
		if len(list) == 0 {
			target.Status = StatusNoMatch
			target.Error = "no safe candidate was found"
			slog.Warn("subtitles: no safe candidate for a language", "id", item.ID, "language", language)
			continue
		}

		if reference == nil {
			resolved, refErr := m.resolveReference(ctx, item, external, workDir)
			if refErr != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return StatusPending, ctxErr
				}
				status := StatusNoReference
				if errors.Is(refErr, context.DeadlineExceeded) {
					// A timed-out demux is not the same as a movie without a
					// reference: the extraction never finished, so the movie must
					// not be filed as "no reference" and forgotten.
					status = StatusFailed
				}
				// The reference is shared, so every language not reached yet fails
				// for the same reason.
				for _, remaining := range missing[index:] {
					if pending := item.Target(remaining); pending != nil {
						pending.Status = status
						pending.Error = refErr.Error()
					}
				}
				slog.Warn("subtitles: no usable timing reference",
					"id", item.ID, "video", item.VideoPath, "err", refErr)
				item.Error = summarizeTargetErrors(item.Targets)
				return status, refErr
			}
			reference = resolved
		}

		m.setStage(item, "download", StatusDownloading)
		status, targetErr := m.syncTarget(ctx, item, target, list, reference, workDir, opts)
		if targetErr != nil {
			// A canceled run or an exhausted quota says nothing about the movie,
			// so the item stays queued instead of being filed as a finding.
			if errors.Is(targetErr, context.Canceled) || errors.Is(targetErr, errQuotaStop) {
				target.Status = StatusPending
				target.Error = ""
				return StatusPending, targetErr
			}
			if opensubtitles.IsHardStop(targetErr) {
				target.Status = StatusFailed
				target.Error = targetErr.Error()
				item.Error = summarizeTargetErrors(item.Targets)
				return StatusFailed, targetErr
			}
			slog.Warn("subtitles: language failed", "id", item.ID, "language", language, "err", targetErr)
			target.Status = StatusFailed
			target.Error = targetErr.Error()
			continue
		}
		target.Status = status
		if status == StatusAdded || status == StatusAddedReview {
			slog.Info("subtitles: installed target subtitle",
				"id", item.ID,
				"video", item.VideoPath,
				"language", target.Language,
				"output", target.OutputPath,
				"file_id", target.FileID,
				"release", target.Release,
				"variant", target.Variant)
		}
	}

	// 3. Summarize the run: the item status describes the movie as a whole, the
	// per-language outcomes explain which subtitles are still missing.
	item.Targets = dedupeTargetOrder(item.Targets, m.cfg.TargetLanguages)
	item.Error = summarizeTargetErrors(item.Targets)
	status := aggregateItemStatus(item.Targets)
	attrs := []any{"id", item.ID, "video", item.VideoPath, "status", status, "targets", targetSummary(item.Targets)}
	if item.ReferenceKind != "" {
		attrs = append(attrs, "reference", item.ReferenceKind+"/"+item.ReferenceLang)
	}
	if item.Error != "" {
		slog.Warn("subtitles: run finished with missing languages", attrs...)
	} else {
		slog.Info("subtitles: run finished", attrs...)
	}
	if status == StatusFailed || status == StatusNeedsReview || status == StatusNoMatch || status == StatusNoReference {
		return status, errors.New(item.Error)
	}
	return status, nil
}

// reconcileItemTargets rebuilds the per-language state of an item from its
// current external files and embedded streams, carrying earlier outcomes,
// chosen candidates and metrics forward.
func (m *Manager) reconcileItemTargets(item *Item, external []externalSubtitle) []TargetState {
	current := targetStates(m.cfg.TargetLanguages, external, item.EmbeddedSubStreams)
	return reconcileTargets(item.Targets, current, item.VideoPath, false)
}

// dedupeTargetOrder keeps the configured language order when an item carries
// languages that are no longer configured (or is missing a newly added one).
func dedupeTargetOrder(targets []TargetState, languages []string) []TargetState {
	out := make([]TargetState, 0, len(languages))
	seen := map[string]bool{}
	for _, language := range languages {
		if target := findTarget(targets, language); target != nil {
			seen[language] = true
			out = append(out, *target)
			continue
		}
		out = append(out, TargetState{Language: language, Status: StatusPending})
	}
	for _, target := range targets {
		if !seen[target.Language] {
			out = append(out, target)
		}
	}
	return out
}

// targetSummary renders the per-language outcomes for the log.
func targetSummary(targets []TargetState) string {
	parts := make([]string, 0, len(targets))
	for _, target := range targets {
		parts = append(parts, target.Language+" "+targetLabel(target.Status))
	}
	return strings.Join(parts, ", ")
}

// summarizeTargetErrors lists the languages that still have no subtitle, so the
// card explains a partial or unmatched movie without opening it.
func summarizeTargetErrors(targets []TargetState) string {
	var parts []string
	for _, target := range targets {
		if target.resolved() {
			continue
		}
		line := target.Language + " " + targetLabel(target.Status)
		if target.Error != "" {
			line += ": " + target.Error
		}
		parts = append(parts, line)
	}
	return strings.Join(parts, "; ")
}

// aggregateItemStatus folds the per-language outcomes into the status of the
// movie as a whole.
func aggregateItemStatus(targets []TargetState) string {
	resolved, added, review := 0, 0, 0
	statuses := map[string]bool{}
	for _, target := range targets {
		switch target.Status {
		case TargetPresent:
			resolved++
		case StatusAdded:
			resolved++
			added++
		case StatusAddedReview:
			resolved++
			added++
			review++
		default:
			statuses[target.Status] = true
		}
	}
	switch {
	case resolved == len(targets):
		if review > 0 {
			return StatusAddedReview
		}
		if added > 0 {
			return StatusAdded
		}
		return StatusHasTargets
	case resolved > 0:
		return StatusPartial
	case statuses[StatusFailed]:
		return StatusFailed
	case statuses[StatusNeedsReview]:
		return StatusNeedsReview
	case statuses[StatusNoReference]:
		return StatusNoReference
	case statuses[StatusNoMatch]:
		return StatusNoMatch
	default:
		return StatusPending
	}
}

// candidatesForLanguage filters the audited candidates to one target language
// and orders them the way that language has to be tried: safe candidates first,
// then the preferred regional variant (Latin American Spanish before Castilian),
// and otherwise the audited order that the caller already applied.
func candidatesForLanguage(candidates []Candidate, language string) []Candidate {
	var out []Candidate
	for _, candidate := range candidates {
		if languageMatches(language, candidate.Language) {
			out = append(out, candidate)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Safe != out[j].Safe {
			return out[i].Safe
		}
		return variantRank(out[i].Variant) < variantRank(out[j].Variant)
	})
	return out
}

// referenceResult is the materialized timing reference shared by every target
// language of one movie.
type referenceResult struct {
	path string
	cues []TimeSpan
}

// resolveReference walks the ranked reference candidates and materializes the
// first usable one as `reference.srt` in the work directory.
func (m *Manager) resolveReference(ctx context.Context, item *Item, external []externalSubtitle, workDir string) (*referenceResult, error) {
	choices := RankReferences(item, external, m.cfg.ReferenceLanguages)
	if len(choices) == 0 {
		slog.Warn("subtitles: no usable reference subtitle",
			"id", item.ID, "video", item.VideoPath,
			"probed", item.Probed,
			"external_subtitles", len(item.ExternalSubtitles),
			"embedded_streams", len(item.EmbeddedSubStreams))
		return nil, errors.New("no text subtitle or embedded text stream can be used as a reference; only image-based subtitles were found")
	}

	// The reference chosen by an earlier run is cleared first: a stale kind would
	// otherwise mask a reference that this run failed to produce.
	item.ReferenceKind = ""
	item.ReferenceLang = ""
	item.ReferenceStream = 0
	item.ReferencePath = ""
	referencePath := filepath.Join(workDir, "reference.srt")

	// Walk every reference candidate: a forced/signs-only track or an empty
	// extraction must not abandon a movie that has another usable stream.
	var referenceErr error
	timedOut := false
	for index := range choices {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		choice := choices[index]
		m.setStage(item, "reference", StatusProcessing)
		slog.Info("subtitles: trying reference",
			"id", item.ID,
			"candidate", index+1,
			"of", len(choices),
			"kind", choice.Kind,
			"language", choice.Lang,
			"stream", choice.Stream,
			"path", choice.Path)
		_ = os.Remove(referencePath)
		if err := m.materializeReference(ctx, item, &choice, referencePath); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			slog.Warn("subtitles: reference unusable", "id", item.ID, "kind", choice.Kind, "stream", choice.Stream, "err", err)
			referenceErr = err
			if errors.Is(err, context.DeadlineExceeded) {
				// The demux ran out of time. Every remaining candidate would have
				// to read the same video file again, so trying the next embedded
				// stream would cost another full timeout for the same answer.
				timedOut = true
				slog.Warn("subtitles: giving up on the embedded reference after a timeout", "id", item.ID, "stream", choice.Stream)
				break
			}
			continue
		}
		cues, err := CueTimesFile(referencePath)
		if err != nil {
			slog.Warn("subtitles: reference unreadable", "id", item.ID, "kind", choice.Kind, "stream", choice.Stream, "err", err)
			referenceErr = fmt.Errorf("reference subtitle is not usable: %w", err)
			continue
		}
		if len(cues) < m.cfg.MinReferenceCues {
			slog.Warn("subtitles: reference too small", "id", item.ID, "kind", choice.Kind, "stream", choice.Stream, "cues", len(cues), "minimum", m.cfg.MinReferenceCues)
			referenceErr = fmt.Errorf("reference subtitle has only %d cue(s); too small to align against", len(cues))
			continue
		}
		item.ReferenceKind = choice.Kind
		item.ReferenceLang = choice.Lang
		item.ReferenceStream = choice.Stream
		item.ReferencePath = choice.Path
		slog.Info("subtitles: reference ready",
			"id", item.ID, "kind", choice.Kind, "language", choice.Lang, "stream", choice.Stream, "cues", len(cues), "path", referencePath)
		return &referenceResult{path: referencePath, cues: cues}, nil
	}
	if referenceErr == nil {
		referenceErr = errors.New("no usable reference subtitle")
	}
	if timedOut {
		return nil, fmt.Errorf("embedded subtitle extraction did not finish in time; the video may be on a slow or sleeping disk: %w", referenceErr)
	}
	return nil, referenceErr
}

// syncTarget downloads, aligns and installs one target language against the
// shared timing reference. It returns the language outcome, and an error only
// when the whole run has to stop (cancel, quota reserve, a hard OpenSubtitles
// error) or the language failed outright.
func (m *Manager) syncTarget(ctx context.Context, item *Item, target *TargetState, candidates []Candidate, reference *referenceResult, workDir string, opts runOptions) (string, error) {
	maxCandidates := opts.maxCandidates
	if maxCandidates <= 0 {
		maxCandidates = m.cfg.MaxCandidates
	}
	if maxCandidates <= 0 {
		maxCandidates = 5
	}
	attempted := 0
	var bestFallback *Candidate
	for index := range candidates {
		candidate := candidates[index]
		if attempted >= maxCandidates {
			break
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return StatusPending, ctxErr
		}
		if remaining, known := m.quotaRemaining(); known && remaining <= m.cfg.QuotaReserve {
			return StatusPending, errQuotaStop
		}
		attempted++
		item.Attempts++
		slog.Info("subtitles: trying candidate",
			"id", item.ID,
			"language", target.Language,
			"attempt", attempted,
			"file_id", candidate.FileID,
			"release", candidate.Release,
			"variant", candidate.Variant,
			"score", fmt.Sprintf("%.1f", candidate.Score),
			"category", candidate.Category)

		rawPath := filepath.Join(workDir, strconv.Itoa(candidate.FileID)+".raw.srt")
		if !IsValidSRTFile(rawPath) {
			if err := m.downloadCandidate(ctx, candidate, rawPath); err != nil {
				slog.Warn("subtitles: download failed", "id", item.ID, "language", target.Language, "file_id", candidate.FileID, "err", err)
				m.recordAttempt(item, candidate, "download_error", nil, err.Error())
				if opensubtitles.IsHardStop(err) {
					return StatusFailed, err
				}
				continue
			}
			slog.Info("subtitles: downloaded candidate subtitle", "id", item.ID, "language", target.Language, "file_id", candidate.FileID, "path", rawPath)
		}
		if bestFallback == nil {
			fallback := candidate
			bestFallback = &fallback
		}

		m.setStage(item, "sync", StatusSyncing)
		alignedPath := filepath.Join(workDir, strconv.Itoa(candidate.FileID)+".aligned.srt")
		report, syncErr := m.runAlass(ctx, reference.path, rawPath, alignedPath)
		if syncErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return StatusPending, ctxErr
			}
			m.recordAttempt(item, candidate, "alass_error", nil, report.Detail+" "+syncErr.Error())
			continue
		}

		alignedText, err := readSubtitleText(alignedPath)
		if err != nil {
			m.recordAttempt(item, candidate, "alass_error", nil, err.Error())
			continue
		}
		promos, malformed := 0, 0
		if m.cfg.RemovePromoCues {
			alignedText, promos, malformed = StripPromoAndRepair(alignedText)
		}
		outputCues, err := CueTimesFromText(alignedText)
		if err != nil {
			m.recordAttempt(item, candidate, "invalid_output", nil, err.Error())
			continue
		}
		metrics := ComputeMetrics(outputCues, reference.cues, m.cfg.Accept)
		metrics.RemovedPromos = promos
		metrics.RemovedMalformed = malformed
		metrics.AlassBlocks = len(report.Blocks)
		metrics.AlassFPS = report.FPS
		for _, block := range report.Blocks {
			metrics.AlassShifts = append(metrics.AlassShifts, block.Shift)
		}
		synchronized := 0
		for _, block := range report.Blocks {
			synchronized += block.Count
		}
		if len(reference.cues) > 0 {
			metrics.AlassCoverage = float64(synchronized) / float64(len(reference.cues))
		}
		metrics.CoarseIssues = CoarseIssues(reference.cues, mustCueTimes(rawPath), outputCues, report)

		attrs := []any{
			"id", item.ID,
			"language", target.Language,
			"file_id", candidate.FileID,
			"cues", metrics.Cues,
			"within_2s", fmt.Sprintf("%.0f%%", metrics.Within2*100),
			"p90_s", fmt.Sprintf("%.2f", metrics.P90),
			"start_gap_min", fmt.Sprintf("%.1f", metrics.StartGap),
			"end_gap_min", fmt.Sprintf("%.1f", metrics.EndGap),
			"zero_cues", metrics.ZeroStartCues,
			"overrun_min", fmt.Sprintf("%.1f", metrics.Overrun),
			"score", fmt.Sprintf("%.0f", metrics.Score),
			"alass_blocks", metrics.AlassBlocks,
			"alass_fps", metrics.AlassFPS,
			"acceptable", metrics.Acceptable,
		}
		if len(metrics.CoarseIssues) > 0 {
			attrs = append(attrs, "issues", strings.Join(metrics.CoarseIssues, "; "))
		}
		slog.Info("subtitles: alignment metrics", attrs...)

		if !metrics.Acceptable {
			slog.Warn("subtitles: candidate rejected by the timing gate",
				"id", item.ID, "language", target.Language, "file_id", candidate.FileID, "release", candidate.Release)
			m.recordAttempt(item, candidate, "rejected_timing", &metrics, report.Detail)
			continue
		}
		if promos > 0 || malformed > 0 {
			if err := writeFileAtomic(alignedPath, []byte(alignedText), 0o644); err != nil {
				return StatusFailed, err
			}
		}
		if err := m.installSubtitle(item, target, alignedText, &metrics, candidate, report); err != nil {
			return StatusFailed, err
		}
		return StatusAdded, nil
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		return StatusPending, ctxErr
	}
	// No candidate passed the strict timing gate.
	if m.cfg.AllowUnalignedFallback && bestFallback != nil && bestFallback.Safe {
		rawPath := filepath.Join(workDir, strconv.Itoa(bestFallback.FileID)+".raw.srt")
		if text, err := readSubtitleText(rawPath); err == nil {
			if m.cfg.RemovePromoCues {
				text, _, _ = StripPromoAndRepair(text)
			}
			metrics := &Metrics{Acceptable: false}
			if err := m.installSubtitle(item, target, text, metrics, *bestFallback, AlassReport{}); err != nil {
				return StatusFailed, err
			}
			target.Status = StatusAddedReview
			target.Error = "installed without a passing alass alignment because the reference is unusable"
			slog.Warn("subtitles: installed without alignment (allow_unaligned_fallback)",
				"id", item.ID, "video", item.VideoPath, "language", target.Language, "output", target.OutputPath)
			return StatusAddedReview, nil
		}
	}
	slog.Warn("subtitles: no candidate passed the timing gate",
		"id", item.ID, "video", item.VideoPath, "language", target.Language, "attempts", attempted)
	target.Error = fmt.Sprintf("no candidate passed the timing gate after %d attempt(s)", attempted)
	return StatusNeedsReview, nil
}

// materializeReference produces a normalized UTF-8 SRT reference in the work
// directory, either by extracting an embedded stream or by converting an
// external subtitle, mirroring the Python `ffmpeg -f srt` normalization.
func (m *Manager) materializeReference(ctx context.Context, item *Item, choice *referenceChoice, outputPath string) error {
	if choice.Kind == "embedded" {
		if choice.Stream < 0 {
			return errors.New("embedded reference stream index is missing")
		}
		// An embedded extraction demuxes the whole video file, so the page is
		// given the position ffmpeg has reached while it works.
		progress := func(sample ExtractProgress) {
			m.setStepDetail(item.ID, formatExtractProgress(sample, item.DurationMS))
		}
		if err := m.prober.ExtractSubtitle(ctx, item.VideoPath, choice.Stream, outputPath, progress); err != nil {
			return err
		}
	} else {
		if choice.Path == "" {
			return errors.New("external reference path is missing")
		}
		if err := m.prober.ConvertToSRT(ctx, choice.Path, outputPath); err != nil {
			return err
		}
	}
	if !IsValidSRTFile(outputPath) {
		return errors.New("reference normalization produced no valid SRT")
	}
	return nil
}

// collectCandidates runs the cached multi-query OpenSubtitles search and returns
// the safe candidates ordered by identity score, porting the query set of
// `second-pass-swedish.py`. It needs only the movie identity, which is why the
// pipeline searches before it extracts a reference from the video file.
//
// languages are the target languages this run is missing; they are requested in a
// single search so one API round trip serves every one of them.
func (m *Manager) collectCandidates(ctx context.Context, item *Item, languages []string, opts runOptions) ([]Candidate, error) {
	languagesParam := searchLanguages(languages)
	scoped := func(query opensubtitles.SearchQuery) opensubtitles.SearchQuery {
		query.Languages = languagesParam
		return query
	}

	merged := map[int]opensubtitles.Item{}
	if !opts.refreshSearch {
		for _, candidate := range m.candidatesFor(item.ID) {
			var raw opensubtitles.Item
			if err := json.Unmarshal(candidate.Raw, &raw); err != nil {
				continue
			}
			merged[candidate.FileID] = raw
		}
	}

	// moviehash is optional and only a precision boost.
	if m.cfg.UseMoviehash && item.VideoSize > 0 && !opts.refreshSearch {
		if _, cached := m.searchFor(item.ID, "hash"); !cached {
			if hash, err := OpenSubtitlesHash(item.VideoPath); err == nil {
				m.runSearchQuery(ctx, item, "hash", scoped(opensubtitles.SearchQuery{
					MovieHash: hash, MovieBytes: item.VideoSize,
				}), merged)
			}
		}
	}

	title := strings.TrimSpace(item.Title)
	if title == "" {
		title = strings.TrimSpace(item.VideoName)
	}
	if err := m.runSearchQuery(ctx, item, "title_year", scoped(opensubtitles.SearchQuery{Query: title, Year: item.Year}), merged); err != nil {
		return nil, err
	}
	releaseQuery := title
	if item.Year > 0 {
		releaseQuery = fmt.Sprintf("%s %d", title, item.Year)
	}
	queries := []struct {
		kind  string
		query opensubtitles.SearchQuery
	}{
		{"title_only", scoped(opensubtitles.SearchQuery{Query: title})},
		{"release", scoped(opensubtitles.SearchQuery{Query: releaseQuery})},
	}

	featureID, acceptedTitles, featureErr := m.resolveFeature(ctx, item, title)
	if featureErr != nil && !errors.Is(featureErr, context.Canceled) {
		// Feature resolution is best-effort; the title queries above still run.
		m.lastFeatureErr = featureErr.Error()
	}
	if featureID != "" {
		queries = append(queries, struct {
			kind  string
			query opensubtitles.SearchQuery
		}{"exact_feature", scoped(opensubtitles.SearchQuery{FeatureID: featureID})})
	}

	for _, entry := range queries {
		if err := m.runSearchQuery(ctx, item, entry.kind, entry.query, merged); err != nil {
			return nil, err
		}
	}

	// Sweep the remaining feature pages only while more safe candidates are
	// useful; this keeps the API call count bounded without changing outcomes.
	if featureID != "" {
		canonical := m.featureCanonical(item.ID)
		for page := 1; page <= 5; page++ {
			if m.languagesSatisfied(item, merged, acceptedTitles, featureID, languages) {
				break
			}
			kind := "feature_page_" + strconv.Itoa(page)
			if canonical != "" {
				if err := m.runSearchQuery(ctx, item, kind, scoped(opensubtitles.SearchQuery{Query: canonical, Page: page}), merged); err != nil {
					return nil, err
				}
			}
		}
	}

	candidates := m.auditCandidates(item, merged, acceptedTitles, featureID)
	m.storeCandidates(item.ID, candidates)
	return safeCandidates(candidates), nil
}

// languagesSatisfied reports whether every requested language already has enough
// safe candidates, which is what stops the feature-page sweep. Counting per
// language matters now that one search serves several of them: a pile of Swedish
// candidates must not stop the sweep before Spanish has any.
func (m *Manager) languagesSatisfied(item *Item, merged map[int]opensubtitles.Item, acceptedTitles map[string]bool, featureID string, languages []string) bool {
	want := m.cfg.MaxCandidates
	if want <= 0 {
		want = 5
	}
	counts := map[string]int{}
	for _, raw := range merged {
		safe, _, _, _, _ := CandidateSafety(m.reference(item), raw, releaseName(raw), acceptedTitles, m.cfg.TitleOverrides, featureID)
		if !safe {
			continue
		}
		language := normalizeLanguage(raw.Attributes.Language)
		for _, target := range languages {
			if languageMatches(target, language) {
				counts[target]++
			}
		}
	}
	for _, target := range languages {
		if counts[target] < want {
			return false
		}
	}
	return true
}

// runSearchQuery executes one query unless an identical cached result exists,
// merging the returned items into the accumulator.
func (m *Manager) runSearchQuery(ctx context.Context, item *Item, kind string, query opensubtitles.SearchQuery, merged map[int]opensubtitles.Item) error {
	cacheKey := cacheKeyFor(kind, query)
	if record, ok := m.searchFor(item.ID, kind); ok && record.Query == cacheKey && (record.Status == "ok" || record.Status == "no_results") {
		slog.Debug("subtitles: search cache hit", "id", item.ID, "kind", kind, "results", record.ResultCount)
		return nil
	}
	if !m.osClient.Configured() {
		return errors.New("opensubtitles is not configured (set subtitles.opensubtitles.api_key or CINEROUTE_OS_API_KEY)")
	}
	slog.Info("subtitles: OpenSubtitles search", "id", item.ID, "kind", kind, "query", cacheKey)
	response, err := m.osClient.Search(ctx, query)
	if err != nil {
		slog.Warn("subtitles: OpenSubtitles search failed", "id", item.ID, "kind", kind, "err", err)
		m.recordSearch(item.ID, kind, cacheKey, "error", 0, err.Error())
		return err
	}
	slog.Info("subtitles: OpenSubtitles search results", "id", item.ID, "kind", kind, "results", len(response.Data))
	for _, result := range response.Data {
		file, ok := bestFile(m.reference(item), result.Attributes.Files)
		if !ok {
			continue
		}
		fileID := file.FileID.Int()
		if fileID <= 0 {
			continue
		}
		merged[fileID] = result
	}
	m.recordSearch(item.ID, kind, cacheKey, "ok", len(response.Data), "")
	return nil
}

func cacheKeyFor(kind string, query opensubtitles.SearchQuery) string {
	parts := []string{kind}
	if query.Query != "" {
		parts = append(parts, "q="+query.Query)
	}
	if query.Year > 0 {
		parts = append(parts, "year="+strconv.Itoa(query.Year))
	}
	if query.FeatureID != "" {
		parts = append(parts, "id="+query.FeatureID)
	}
	if query.MovieHash != "" {
		parts = append(parts, "hash="+query.MovieHash)
	}
	if query.Page > 1 {
		parts = append(parts, "page="+strconv.Itoa(query.Page))
	}
	return strings.Join(parts, "|")
}

// resolveFeature resolves the canonical OpenSubtitles feature and the accepted
// title set, porting `resolve_feature`.
func (m *Manager) resolveFeature(ctx context.Context, item *Item, title string) (string, map[string]bool, error) {
	accepted := map[string]bool{NormalizedTitle(title): true}
	if override, ok := m.cfg.TitleOverrides[NormalizedTitle(title)]; ok {
		accepted[NormalizedTitle(override)] = true
	}
	featureID, raw, cached, err := m.store.loadFeature(item.ID)
	if err != nil {
		return "", accepted, err
	}
	if cached {
		m.recordFeatureCanonical(item.ID, raw)
		if featureID != "" {
			accepted = addFeatureTitles(accepted, raw)
		}
		return featureID, accepted, nil
	}
	if !m.osClient.Configured() {
		return "", accepted, errors.New("opensubtitles is not configured")
	}
	response, err := m.osClient.Features(ctx, title)
	if err != nil {
		return "", accepted, err
	}
	target := NormalizedIdentity(title)
	type choice struct {
		score float64
		id    string
		raw   json.RawMessage
	}
	var choices []choice
	for _, feature := range response.Data {
		attributes := feature.Attributes
		if attributes.FeatureType != "" && attributes.FeatureType != "Movie" {
			continue
		}
		names := append([]string{attributes.Title, attributes.OriginalTitle}, attributes.TitleAka...)
		bestSimilarity := 0.0
		exactName := false
		for _, name := range names {
			if strings.TrimSpace(name) == "" {
				continue
			}
			normalized := NormalizedIdentity(name)
			if normalized == target {
				exactName = true
			}
			if similarity := Ratio(target, normalized); similarity > bestSimilarity {
				bestSimilarity = similarity
			}
		}
		yearDelta := 0
		if attributes.Year.Int() > 0 && item.Year > 0 {
			yearDelta = absInt(attributes.Year.Int() - item.Year)
		}
		if !exactName && bestSimilarity < 0.78 {
			continue
		}
		if !exactName && yearDelta > 1 {
			continue
		}
		score := bestSimilarity * 100
		switch yearDelta {
		case 0:
			score += 30
		case 1:
			score += 20
		case 2:
			score += 10
		}
		id := attributes.FeatureID.String()
		if id == "" {
			id = feature.ID
		}
		choices = append(choices, choice{score: score, id: id, raw: feature.Raw})
	}
	if len(choices) == 0 {
		encoded, _ := json.Marshal(response)
		_ = m.store.saveFeature(item.ID, "", encoded)
		return "", accepted, nil
	}
	sort.SliceStable(choices, func(i, j int) bool { return choices[i].score > choices[j].score })
	selected := choices[0]
	_ = m.store.saveFeature(item.ID, selected.id, selected.raw)
	accepted = addFeatureTitles(accepted, selected.raw)
	m.recordFeatureCanonical(item.ID, selected.raw)
	return selected.id, accepted, nil
}

// addFeatureTitles widens the accepted title set with the canonical feature
// titles, which is how the manual pipeline recovered localized filenames.
func addFeatureTitles(accepted map[string]bool, raw json.RawMessage) map[string]bool {
	var feature opensubtitles.Feature
	if err := json.Unmarshal(raw, &feature); err != nil {
		return accepted
	}
	names := append([]string{feature.Attributes.Title, feature.Attributes.OriginalTitle}, feature.Attributes.TitleAka...)
	for _, name := range names {
		if normalized := NormalizedTitle(name); normalized != "" {
			accepted[normalized] = true
		}
	}
	return accepted
}

// auditCandidates rescores every merged item with CandidateSafety and labels it
// with the audit category.
func (m *Manager) auditCandidates(item *Item, merged map[int]opensubtitles.Item, acceptedTitles map[string]bool, featureID string) []Candidate {
	reference := m.reference(item)
	out := make([]Candidate, 0, len(merged))
	for fileID, raw := range merged {
		release := releaseName(raw)
		safe, score, safetyReasons, movieTitle, featureYear := CandidateSafety(reference, raw, release, acceptedTitles, m.cfg.TitleOverrides, featureID)
		category, auditReasons := AuditCandidate(reference, raw, release)
		passScore, passReasons, _, passOK := ScoreCandidate(reference, raw)
		if !passOK {
			passScore = rejectedScore
		}
		reasons := append(append([]string{}, auditReasons...), safetyReasons...)
		reasons = append(reasons, passReasons...)
		language := normalizeLanguage(raw.Attributes.Language)
		variant := spanishVariant(language, release)
		if label := variantLabel(variant); label != "" {
			reasons = append(reasons, label)
		}
		out = append(out, Candidate{
			FileID:      fileID,
			Score:       score,
			PassScore:   passScore,
			Category:    category,
			Safe:        safe,
			Release:     release,
			Language:    language,
			Variant:     variant,
			MovieTitle:  movieTitle,
			FeatureYear: featureYear,
			Reasons:     reasons,
			Attributes:  raw.Attributes,
			Raw:         raw.Raw,
		})
	}
	// Ordering decides which downloads are spent first, so it must be explicit
	// and stable: safe before unsafe, then the identity score, then a strong
	// audit result over a source fallback, then the first-pass release score
	// (filename/source/edition similarity), and finally the file id so two runs
	// always choose the same candidate.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Safe != out[j].Safe {
			return out[i].Safe
		}
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].Category != out[j].Category {
			return out[i].Category == CategoryStrong
		}
		if out[i].PassScore != out[j].PassScore {
			return out[i].PassScore > out[j].PassScore
		}
		return out[i].FileID < out[j].FileID
	})
	for index := range out {
		out[index].Rank = index + 1
	}
	return out
}

func safeCandidates(candidates []Candidate) []Candidate {
	out := make([]Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Safe {
			out = append(out, candidate)
		}
	}
	return out
}

// releaseName ports the release label selection used throughout the pipeline.
func releaseName(item opensubtitles.Item) string {
	if strings.TrimSpace(item.Attributes.Release) != "" {
		return item.Attributes.Release
	}
	if len(item.Attributes.Files) > 0 {
		return item.Attributes.Files[0].FileName
	}
	return ""
}

// reference builds the scoring reference for an item.
func (m *Manager) reference(item *Item) Reference {
	return Reference{
		Basename: strings.TrimSuffix(item.VideoName, filepath.Ext(item.VideoName)),
		Title:    item.Title,
		Year:     item.Year,
	}
}

// downloadCandidate fetches one candidate and validates it before writing the
// staged raw file with the same crash-safe pattern as the Python client.
func (m *Manager) downloadCandidate(ctx context.Context, candidate Candidate, rawPath string) error {
	response, err := m.osClient.Download(ctx, candidate.FileID)
	if err != nil {
		return err
	}
	m.updateQuotaFromDownload(response)
	data, err := m.osClient.DownloadBytes(ctx, response.Link)
	if err != nil {
		return err
	}
	text, err := DecodeSubtitlePayload(data)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(rawPath, []byte(text), 0o644); err != nil {
		return err
	}
	if !IsValidSRTFile(rawPath) {
		_ = os.Remove(rawPath)
		return errors.New("downloaded subtitle failed SRT validation")
	}
	return nil
}

// runAlass normalizes the reference before every run, matching the proven
// pipeline, and maps alass failures into readable errors.
func (m *Manager) runAlass(ctx context.Context, referencePath, rawPath, alignedPath string) (AlassReport, error) {
	_ = os.Remove(alignedPath)
	result, err := m.syncer.Sync(ctx, referencePath, rawPath, alignedPath, SyncOptions{
		Binary:       m.cfg.AlassPath,
		NoSplit:      m.cfg.AlassNoSplit,
		SplitPenalty: m.cfg.AlassSplitPenalty,
		Timeout:      m.cfg.SyncTimeout,
	})
	if err != nil {
		return result.Report, err
	}
	if !IsValidSRTFile(alignedPath) {
		return result.Report, errors.New("alass produced no valid SRT")
	}
	return result.Report, nil
}

// installSubtitle writes one accepted subtitle next to the movie, atomically and
// without ever overwriting an existing valid file. Each target language has its
// own `<video>.<language>.srt`, so installing Spanish never touches Swedish.
func (m *Manager) installSubtitle(item *Item, target *TargetState, text string, metrics *Metrics, candidate Candidate, report AlassReport) error {
	finalPath := subtitleOutputPath(item.VideoPath, target.Language)
	if IsValidSRTFile(finalPath) {
		return fmt.Errorf("a %s subtitle already exists; refusing to overwrite", target.Language)
	}
	if err := writeFileAtomic(finalPath, []byte(text), 0o644); err != nil {
		return err
	}
	info, err := os.Stat(finalPath)
	if err != nil {
		return fmt.Errorf("verify installed subtitle: %w", err)
	}
	target.Status = StatusAdded
	target.OutputPath = finalPath
	target.OutputBytes = info.Size()
	target.FileID = candidate.FileID
	target.Release = candidate.Release
	target.Category = candidate.Category
	target.Score = candidate.Score
	target.Variant = candidate.Variant
	target.Metrics = metrics
	target.Error = ""
	m.recordAttempt(item, candidate, "accepted", metrics, report.Detail)
	return nil
}

// subtitleOutputPath is `<video-basename>.<language>.srt` beside the movie file.
func subtitleOutputPath(videoPath, language string) string {
	base := strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath))
	return filepath.Join(filepath.Dir(videoPath), base+"."+language+".srt")
}

// itemWorkDir is the per-item scratch directory under the configured work dir.
func (m *Manager) itemWorkDir(itemID string) string {
	return filepath.Join(m.cfg.WorkDir, itemID)
}

// writeFileAtomic writes data to a `.part` file in the destination directory and
// renames it into place, so a crash can never leave a partial subtitle.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}
	part := filepath.Join(dir, "."+filepath.Base(path)+".part")
	if err := os.WriteFile(part, data, mode); err != nil {
		_ = os.Remove(part)
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := os.Rename(part, path); err != nil {
		_ = os.Remove(part)
		return fmt.Errorf("move temporary file into place: %w", err)
	}
	return nil
}

func readSubtitleText(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read subtitle: %w", err)
	}
	text := decodeSubtitle(data)
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.TrimPrefix(text, "\ufeff")
	return text, nil
}

func mustCueTimes(path string) []TimeSpan {
	cues, err := CueTimesFile(path)
	if err != nil {
		return nil
	}
	return cues
}

func (m *Manager) setStage(item *Item, stage, status string) {
	// item is the pipeline's private copy, so these writes are race-free. The
	// live item is updated under the lock as well: a movie can spend minutes
	// extracting an embedded reference, and the list must show that it is being
	// worked on rather than still looking pending.
	item.Step = stage
	item.Status = status
	item.StepDetail = ""
	m.mu.Lock()
	m.batch.Stage = stage
	if live, ok := m.byID[item.ID]; ok && live != item {
		live.Status = status
		live.Step = stage
		// A new stage starts without the previous stage's progress.
		live.StepDetail = ""
		live.UpdatedAt = time.Now()
	}
	m.mu.Unlock()
	if m.onStage != nil {
		m.onStage(item, stage, status)
	}
}

// setStepDetail publishes stage progress on the live item. It deliberately
// leaves UpdatedAt alone: the page reads that field as the time the current
// stage started, so touching it would reset the elapsed-time display.
func (m *Manager) setStepDetail(itemID, detail string) {
	m.mu.Lock()
	if live, ok := m.byID[itemID]; ok {
		live.StepDetail = detail
	}
	m.mu.Unlock()
}

func (m *Manager) recordAttempt(item *Item, candidate Candidate, status string, metrics *Metrics, detail string) {
	attempt := Attempt{
		ItemID:   item.ID,
		FileID:   candidate.FileID,
		Language: candidate.Language,
		Status:   status,
		Category: candidate.Category,
		Release:  candidate.Release,
		Metrics:  metrics,
		Detail:   truncateDetail(detail),
		At:       time.Now(),
	}
	_ = m.store.addAttempt(attempt)
	m.mu.Lock()
	m.attempts[item.ID] = append(m.attempts[item.ID], attempt)
	m.mu.Unlock()
}

func truncateDetail(detail string) string {
	if len(detail) > 2000 {
		return detail[:2000]
	}
	return detail
}

// formatExtractProgress renders one extraction sample as the short line shown
// next to the "reference" stage, for example "62% · 1:15:02 of 2:01:00". The
// video duration comes from the probe, so until it is known only the position
// or the elapsed time can be reported.
func formatExtractProgress(sample ExtractProgress, durationMS int64) string {
	switch {
	case sample.PositionMS > 0 && durationMS > 0:
		percent := sample.PositionMS * 100 / durationMS
		if percent > 100 {
			percent = 100
		}
		return fmt.Sprintf("%d%% · %s of %s", percent, formatClockMS(sample.PositionMS), formatClockMS(durationMS))
	case sample.PositionMS > 0:
		return formatClockMS(sample.PositionMS) + " read"
	default:
		return formatElapsedMS(sample.Elapsed.Milliseconds()) + " elapsed"
	}
}

// formatClockMS renders a millisecond offset as h:mm:ss, or m:ss below an hour.
func formatClockMS(ms int64) string {
	if ms < 0 {
		ms = 0
	}
	total := ms / 1000
	hours := total / 3600
	minutes := (total / 60) % 60
	seconds := total % 60
	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, seconds)
	}
	return fmt.Sprintf("%d:%02d", minutes, seconds)
}

// formatElapsedMS renders a wait as "9s", "3m 20s" or "1h 4m".
func formatElapsedMS(ms int64) string {
	seconds := ms / 1000
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	minutes := seconds / 60
	if minutes < 60 {
		return fmt.Sprintf("%dm %ds", minutes, seconds%60)
	}
	return fmt.Sprintf("%dh %dm", minutes/60, minutes%60)
}
