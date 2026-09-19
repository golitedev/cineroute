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
func (m *Manager) processItem(ctx context.Context, item *Item, opts runOptions) (string, error) {
	target := m.cfg.TargetLanguage
	if target == "" {
		target = "sv"
	}

	external, err := discoverExternalSubtitles(filepath.Dir(item.VideoPath), item.VideoName)
	if err != nil {
		return StatusFailed, fmt.Errorf("read movie folder: %w", err)
	}

	// A scan only probes up to scan_batch_size videos per run, so a movie can be
	// queued without ever being analyzed. Probe it here instead of reporting
	// "no reference" from missing data.
	if !item.Probed {
		m.setStage(item, "probe", StatusProcessing)
		slog.Info("subtitles: probing movie on demand", "id", item.ID, "video", item.VideoPath)
		info, probeErr := m.prober.Probe(ctx, item.VideoPath)
		if probeErr != nil {
			return StatusFailed, fmt.Errorf("probe video: %w", probeErr)
		}
		item.EmbeddedSubStreams = info.Streams
		item.DurationMS = info.DurationMS
		item.Probed = true
		item.ExistingSubLanguages = mergeLanguages(external, info.Streams)
		slog.Info("subtitles: movie analyzed", "id", item.ID, "embedded_streams", len(info.Streams), "duration_ms", info.DurationMS)
	}

	if swedish, sources := hasSwedishSubtitle(target, external, item.EmbeddedSubStreams); swedish {
		item.HasSwedish = true
		item.SwedishSources = sources
		slog.Info("subtitles: movie already has Swedish subtitles", "id", item.ID, "video", item.VideoPath, "sources", strings.Join(sources, ", "))
		if opts.previousStatus == StatusAdded || opts.previousStatus == StatusAddedReview {
			return opts.previousStatus, nil
		}
		return StatusHasSwedish, nil
	}

	// An existing final subtitle means the work is already done.
	finalPath := m.outputPath(item)
	if IsValidSRTFile(finalPath) {
		slog.Info("subtitles: subtitle already installed", "id", item.ID, "path", finalPath)
		item.OutputPath = finalPath
		if info, err := os.Stat(finalPath); err == nil {
			item.OutputBytes = info.Size()
		}
		return StatusHasSwedish, nil
	}

	workDir := m.itemWorkDir(item.ID)
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return StatusFailed, fmt.Errorf("create work directory %s: %w (the container user must be able to write subtitles.work_dir; see the Subtitles section of the README)", workDir, err)
	}
	item.WorkDir = workDir
	referencePath := filepath.Join(workDir, "reference.srt")

	choices := RankReferences(item, external, m.cfg.ReferenceLanguages)
	if len(choices) == 0 {
		slog.Warn("subtitles: no usable reference subtitle",
			"id", item.ID, "video", item.VideoPath,
			"probed", item.Probed,
			"external_subtitles", len(item.ExternalSubtitles),
			"embedded_streams", len(item.EmbeddedSubStreams))
		return StatusNoReference, errors.New("no text subtitle or embedded text stream can be used as a reference; only image-based subtitles were found")
	}

	// Walk every reference candidate: a forced/signs-only track or an empty
	// extraction must not abandon a movie that has another usable stream.
	var referenceCues []TimeSpan
	var referenceErr error
	for index := range choices {
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
			slog.Warn("subtitles: reference unusable", "id", item.ID, "kind", choice.Kind, "stream", choice.Stream, "err", err)
			referenceErr = err
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
		referenceCues = cues
		item.ReferenceKind = choice.Kind
		item.ReferenceLang = choice.Lang
		item.ReferenceStream = choice.Stream
		item.ReferencePath = choice.Path
		slog.Info("subtitles: reference ready",
			"id", item.ID, "kind", choice.Kind, "language", choice.Lang, "stream", choice.Stream, "cues", len(cues), "path", referencePath)
		break
	}
	if item.ReferenceKind == "" {
		if referenceErr == nil {
			referenceErr = errors.New("no usable reference subtitle")
		}
		return StatusNoReference, referenceErr
	}

	m.setStage(item, "search", StatusProcessing)
	candidates, err := m.collectCandidates(ctx, item, referenceCues, opts)
	if err != nil {
		return StatusFailed, err
	}
	if len(candidates) == 0 {
		slog.Warn("subtitles: no safe candidate found", "id", item.ID, "video", item.VideoPath)
		return StatusNoMatch, errors.New("no safe Swedish subtitle candidate was found")
	}
	slog.Info("subtitles: candidates audited", "id", item.ID, "safe", len(candidates))
	slog.Debug("subtitles: top candidate",
		"id", item.ID,
		"file_id", candidates[0].FileID,
		"release", candidates[0].Release,
		"score", fmt.Sprintf("%.1f", candidates[0].Score),
		"category", candidates[0].Category)

	if opts.refreshQuota {
		m.refreshQuota(ctx)
	}

	m.setStage(item, "download", StatusDownloading)
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
		if ctx.Err() != nil {
			return StatusPending, ctx.Err()
		}
		if remaining, known := m.quotaRemaining(); known && remaining <= m.cfg.QuotaReserve {
			return StatusPending, errQuotaStop
		}
		attempted++
		item.Attempts++
		slog.Info("subtitles: trying candidate",
			"id", item.ID,
			"attempt", attempted,
			"file_id", candidate.FileID,
			"release", candidate.Release,
			"score", fmt.Sprintf("%.1f", candidate.Score),
			"category", candidate.Category)

		rawPath := filepath.Join(workDir, strconv.Itoa(candidate.FileID)+".raw.srt")
		if !IsValidSRTFile(rawPath) {
			if err := m.downloadCandidate(ctx, candidate, rawPath); err != nil {
				slog.Warn("subtitles: download failed", "id", item.ID, "file_id", candidate.FileID, "err", err)
				m.recordAttempt(item, candidate, "download_error", nil, err.Error())
				if opensubtitles.IsHardStop(err) {
					return StatusPending, err
				}
				continue
			}
			slog.Info("subtitles: downloaded candidate subtitle", "id", item.ID, "file_id", candidate.FileID, "path", rawPath)
		}
		if bestFallback == nil {
			fallback := candidate
			bestFallback = &fallback
		}

		m.setStage(item, "sync", StatusSyncing)
		alignedPath := filepath.Join(workDir, strconv.Itoa(candidate.FileID)+".aligned.srt")
		report, syncErr := m.runAlass(ctx, referencePath, rawPath, alignedPath)
		if syncErr != nil {
			m.recordAttempt(item, candidate, "alass_error", nil, report.Detail+" "+syncErr.Error())
			continue
		}

		alignedText, err := readSubtitleText(alignedPath)
		if err != nil {
			m.recordAttempt(item, candidate, "alass_error", nil, err.Error())
			continue
		}
		cleaned, promos, malformed := 0, 0, 0
		if m.cfg.RemovePromoCues {
			cleaned = 1
			alignedText, promos, malformed = StripPromoAndRepair(alignedText)
		}
		_ = cleaned
		outputCues, err := CueTimesFromText(alignedText)
		if err != nil {
			m.recordAttempt(item, candidate, "invalid_output", nil, err.Error())
			continue
		}
		metrics := ComputeMetrics(outputCues, referenceCues, m.cfg.Accept)
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
		if len(referenceCues) > 0 {
			metrics.AlassCoverage = float64(synchronized) / float64(len(referenceCues))
		}
		metrics.CoarseIssues = CoarseIssues(referenceCues, mustCueTimes(rawPath), outputCues, report)

		attrs := []any{
			"id", item.ID,
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
				"id", item.ID, "file_id", candidate.FileID, "release", candidate.Release)
			m.recordAttempt(item, candidate, "rejected_timing", &metrics, report.Detail)
			continue
		}
		if promos > 0 || malformed > 0 {
			if err := writeFileAtomic(alignedPath, []byte(alignedText), 0o644); err != nil {
				return StatusFailed, err
			}
		}
		if err := m.installSubtitle(item, alignedText, &metrics, candidate, report); err != nil {
			return StatusFailed, err
		}
		slog.Info("subtitles: installed Swedish subtitle",
			"id", item.ID,
			"video", item.VideoPath,
			"output", item.OutputPath,
			"file_id", candidate.FileID,
			"release", candidate.Release)
		return StatusAdded, nil
	}

	// No candidate passed the strict timing gate.
	if m.cfg.AllowUnalignedFallback && bestFallback != nil && bestFallback.Safe {
		rawPath := filepath.Join(workDir, strconv.Itoa(bestFallback.FileID)+".raw.srt")
		if text, err := readSubtitleText(rawPath); err == nil {
			if m.cfg.RemovePromoCues {
				text, _, _ = StripPromoAndRepair(text)
			}
			metrics := &Metrics{Acceptable: false}
			if err := m.installSubtitle(item, text, metrics, *bestFallback, AlassReport{}); err != nil {
				return StatusFailed, err
			}
			slog.Warn("subtitles: installed without alignment (allow_unaligned_fallback)",
				"id", item.ID, "video", item.VideoPath, "output", item.OutputPath)
			item.Error = "installed without a passing alass alignment because the reference is unusable"
			return StatusAddedReview, nil
		}
	}
	slog.Warn("subtitles: no candidate passed the timing gate",
		"id", item.ID, "video", item.VideoPath, "attempts", attempted)
	return StatusNeedsReview, fmt.Errorf("no candidate passed the timing gate after %d attempt(s)", attempted)
}

// materializeReference produces a normalized UTF-8 SRT reference in the work
// directory, either by extracting an embedded stream or by converting an
// external subtitle, mirroring the Python `ffmpeg -f srt` normalization.
func (m *Manager) materializeReference(ctx context.Context, item *Item, choice *referenceChoice, outputPath string) error {
	if choice.Kind == "embedded" {
		if choice.Stream < 0 {
			return errors.New("embedded reference stream index is missing")
		}
		if err := m.prober.ExtractSubtitle(ctx, item.VideoPath, choice.Stream, outputPath); err != nil {
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
// `second-pass-swedish.py`.
func (m *Manager) collectCandidates(ctx context.Context, item *Item, referenceCues []TimeSpan, opts runOptions) ([]Candidate, error) {
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
				m.runSearchQuery(ctx, item, "hash", opensubtitles.SearchQuery{
					MovieHash: hash, MovieBytes: item.VideoSize,
				}, merged)
			}
		}
	}

	title := strings.TrimSpace(item.Title)
	if title == "" {
		title = strings.TrimSpace(item.VideoName)
	}
	if err := m.runSearchQuery(ctx, item, "title_year", opensubtitles.SearchQuery{Query: title, Year: item.Year}, merged); err != nil {
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
		{"title_only", opensubtitles.SearchQuery{Query: title}},
		{"release", opensubtitles.SearchQuery{Query: releaseQuery}},
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
		}{"exact_feature", opensubtitles.SearchQuery{FeatureID: featureID}})
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
			safe := m.countSafeCandidates(item, merged, acceptedTitles, featureID)
			if safe >= m.cfg.MaxCandidates {
				break
			}
			kind := "feature_page_" + strconv.Itoa(page)
			if canonical != "" {
				if err := m.runSearchQuery(ctx, item, kind, opensubtitles.SearchQuery{Query: canonical, Page: page}, merged); err != nil {
					return nil, err
				}
			}
		}
	}

	candidates := m.auditCandidates(item, merged, acceptedTitles, featureID)
	m.storeCandidates(item.ID, candidates)
	return safeCandidates(candidates), nil
}

func (m *Manager) countSafeCandidates(item *Item, merged map[int]opensubtitles.Item, acceptedTitles map[string]bool, featureID string) int {
	count := 0
	for _, raw := range merged {
		safe, _, _, _, _ := CandidateSafety(m.reference(item), raw, releaseName(raw), acceptedTitles, m.cfg.TitleOverrides, featureID)
		if safe {
			count++
		}
	}
	return count
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
		reasons := append(append([]string{}, auditReasons...), safetyReasons...)
		out = append(out, Candidate{
			FileID:      fileID,
			Score:       score,
			Category:    category,
			Safe:        safe,
			Release:     release,
			MovieTitle:  movieTitle,
			FeatureYear: featureYear,
			Reasons:     reasons,
			Attributes:  raw.Attributes,
			Raw:         raw.Raw,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Safe != out[j].Safe {
			return out[i].Safe
		}
		return out[i].Score > out[j].Score
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

// installSubtitle writes the accepted subtitle next to the movie, atomically and
// without ever overwriting an existing valid file.
func (m *Manager) installSubtitle(item *Item, text string, metrics *Metrics, candidate Candidate, report AlassReport) error {
	finalPath := m.outputPath(item)
	if IsValidSRTFile(finalPath) {
		return errors.New("a Swedish subtitle already exists; refusing to overwrite")
	}
	if err := writeFileAtomic(finalPath, []byte(text), 0o644); err != nil {
		return err
	}
	info, err := os.Stat(finalPath)
	if err != nil {
		return fmt.Errorf("verify installed subtitle: %w", err)
	}
	item.OutputPath = finalPath
	item.OutputBytes = info.Size()
	item.ChosenFileID = candidate.FileID
	item.ChosenRelease = candidate.Release
	item.ChosenCategory = candidate.Category
	item.ChosenScore = candidate.Score
	item.Metrics = metrics
	item.Error = ""
	m.recordAttempt(item, candidate, "accepted", metrics, report.Detail)
	return nil
}

// outputPath is `<video-basename>.<target-language>.srt` beside the movie file.
func (m *Manager) outputPath(item *Item) string {
	target := m.cfg.TargetLanguage
	if target == "" {
		target = "sv"
	}
	base := strings.TrimSuffix(item.VideoName, filepath.Ext(item.VideoName))
	return filepath.Join(filepath.Dir(item.VideoPath), base+"."+target+".srt")
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
	m.mu.Lock()
	m.batch.Stage = stage
	if live, ok := m.byID[item.ID]; ok && live != item {
		live.Status = status
		live.Step = stage
		live.UpdatedAt = time.Now()
	}
	m.mu.Unlock()
	if m.onStage != nil {
		m.onStage(item, stage, status)
	}
}

func (m *Manager) recordAttempt(item *Item, candidate Candidate, status string, metrics *Metrics, detail string) {
	attempt := Attempt{
		ItemID:   item.ID,
		FileID:   candidate.FileID,
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
