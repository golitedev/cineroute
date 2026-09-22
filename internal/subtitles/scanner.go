package subtitles

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"cineroute/internal/library"
)

// RemoteRoot is one configured remote movie root and its drive.
type RemoteRoot struct {
	DriveID string
	Path    string
}

// RemoteMovieRoots resolves every configured remote movie root, including the
// conventional /mN -> /mrN alias, so subtitles land where companions do.
func RemoteMovieRoots(drives []library.Drive) []RemoteRoot {
	var out []RemoteRoot
	for _, drive := range drives {
		root, ok := library.RemoteMovieRoot(drive)
		if !ok || strings.TrimSpace(root) == "" {
			continue
		}
		out = append(out, RemoteRoot{DriveID: drive.ID, Path: root})
	}
	return out
}

// externalSubtitle is a subtitle file sitting next to a video file.
type externalSubtitle struct {
	Path     string
	FileName string
	Language string
	Ext      string
	Usable   bool
}

// referenceChoice is the chosen timing reference for one item.
type referenceChoice struct {
	Kind   string
	Lang   string
	Stream int
	Path   string
}

// usableReferenceExtensions are the text subtitle formats alass can parse and
// ffmpeg can normalize. `.sub`/`.idx` are VobSub bitmaps and are not usable.
var usableReferenceExtensions = map[string]bool{
	".srt": true,
	".ass": true,
	".ssa": true,
	".vtt": true,
}

var anySubtitleExtensions = map[string]bool{
	".srt": true, ".ass": true, ".ssa": true, ".vtt": true, ".sub": true, ".idx": true,
}

// subtitleItemID is a stable id for one remote video file.
func subtitleItemID(driveID, relativePath string) string {
	sum := sha256.Sum256([]byte(driveID + "\x00" + filepath.ToSlash(relativePath)))
	return "s_" + hex.EncodeToString(sum[:])[:16]
}

// discoverExternalSubtitles finds subtitle files named after the video file,
// for example `Movie.mkv` -> `Movie.en.srt`, `Movie.es.forced.srt`.
func discoverExternalSubtitles(dir, videoName string) ([]externalSubtitle, error) {
	base := strings.TrimSuffix(videoName, filepath.Ext(videoName))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []externalSubtitle
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if !anySubtitleExtensions[ext] {
			continue
		}
		if !strings.HasPrefix(name, base+".") {
			continue
		}
		if strings.Contains(strings.ToLower(name), ".downloaded.") {
			// Staged raw downloads from the manual pipeline are never final.
			continue
		}
		remainder := strings.TrimSuffix(strings.TrimPrefix(name, base+"."), filepath.Ext(name))
		if remainder == "" {
			continue
		}
		out = append(out, externalSubtitle{
			Path:     filepath.Join(dir, name),
			FileName: name,
			Language: languageFromSuffixParts(strings.Split(remainder, ".")),
			Ext:      ext,
			Usable:   usableReferenceExtensions[ext],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FileName < out[j].FileName })
	return out, nil
}

// languageFromSuffixParts picks the first recognized language token from the
// name segments between the video basename and the extension.
func languageFromSuffixParts(parts []string) string {
	for _, part := range parts {
		if language := normalizeLanguage(part); language != "" {
			return language
		}
	}
	return ""
}

// RankReferences returns every usable timing reference for a movie in the order
// the manual workflow preferred them: per preferred language an external
// subtitle first and then an embedded stream, then any external subtitle, then
// the remaining embedded streams with non-forced tracks ahead of forced ones.
// The pipeline walks the list so one unusable stream (a forced/signs-only track,
// an empty extract) does not abandon the whole movie.
func RankReferences(item *Item, external []externalSubtitle, referenceLanguages []string) []referenceChoice {
	var out []referenceChoice
	seen := map[string]bool{}
	add := func(choice referenceChoice) {
		key := choice.Kind + "|" + choice.Lang + "|" + itoa(choice.Stream) + "|" + choice.Path
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, choice)
	}
	for _, lang := range referenceLanguages {
		for _, sub := range external {
			if sub.Usable && sub.Language == lang {
				add(referenceChoice{Kind: "external", Lang: lang, Stream: -1, Path: sub.Path})
			}
		}
		if stream, ok := bestEmbeddedStream(item.EmbeddedSubStreams, lang); ok {
			add(referenceChoice{Kind: "embedded", Lang: stream.Language, Stream: stream.Index})
		}
	}
	for _, sub := range external {
		if sub.Usable {
			add(referenceChoice{Kind: "external", Lang: sub.Language, Stream: -1, Path: sub.Path})
		}
	}
	for _, stream := range item.EmbeddedSubStreams {
		if stream.Usable && !stream.Forced {
			add(referenceChoice{Kind: "embedded", Lang: stream.Language, Stream: stream.Index})
		}
	}
	for _, stream := range item.EmbeddedSubStreams {
		if stream.Usable && stream.Forced {
			add(referenceChoice{Kind: "embedded", Lang: stream.Language, Stream: stream.Index})
		}
	}
	return out
}

// ChooseReference returns the first ranked reference, or nil when the movie has
// no usable reference at all.
func ChooseReference(item *Item, external []externalSubtitle, referenceLanguages []string) *referenceChoice {
	choices := RankReferences(item, external, referenceLanguages)
	if len(choices) == 0 {
		return nil
	}
	first := choices[0]
	return &first
}

// bestEmbeddedStream returns the first usable embedded stream for a language,
// preferring non-forced streams because forced/signs-only tracks are too small
// to align against.
func bestEmbeddedStream(streams []EmbeddedSubtitle, lang string) (EmbeddedSubtitle, bool) {
	var matches []EmbeddedSubtitle
	for _, stream := range streams {
		if !stream.Usable {
			continue
		}
		if lang != "" && stream.Language != lang {
			continue
		}
		matches = append(matches, stream)
	}
	if len(matches) == 0 {
		return EmbeddedSubtitle{}, false
	}
	for _, stream := range matches {
		if !stream.Forced {
			return stream, true
		}
	}
	return matches[0], true
}

// targetStates reports, for every configured target language, whether the movie
// already has a usable subtitle and where it comes from.
func targetStates(targets []string, external []externalSubtitle, streams []EmbeddedSubtitle) []TargetState {
	out := make([]TargetState, 0, len(targets))
	for _, target := range targets {
		state := TargetState{Language: target, Status: StatusPending}
		for _, sub := range external {
			if usableReferenceExtensions[sub.Ext] && languageMatches(target, sub.Language) {
				state.Sources = append(state.Sources, "external "+sub.FileName)
			}
		}
		for _, stream := range streams {
			if stream.Usable && languageMatches(target, stream.Language) {
				state.Sources = append(state.Sources, "embedded stream #"+itoa(stream.Index))
			}
		}
		if len(state.Sources) > 0 {
			state.Present = true
			state.Status = TargetPresent
		}
		out = append(out, state)
	}
	return out
}

// terminalTargetStatus reports whether a per-language outcome is final, so a
// rescan keeps showing what the last run found instead of queueing that language
// again for every batch.
func terminalTargetStatus(status string) bool {
	switch status {
	case StatusNoMatch, StatusNeedsReview, StatusNoReference, StatusFailed:
		return true
	default:
		return false
	}
}

func findTarget(targets []TargetState, language string) *TargetState {
	for index := range targets {
		if targets[index].Language == language {
			return &targets[index]
		}
	}
	return nil
}

// reconcileTargets folds a previous run's per-language outcome into what is on
// disk now: a subtitle that is present or installed wins, a replaced video resets
// everything, and a final outcome (no match, needs review) is kept so that
// language is not retried automatically on every batch.
func reconcileTargets(previous, current []TargetState, videoPath string, videoChanged bool) []TargetState {
	for index := range current {
		state := &current[index]
		prior := findTarget(previous, state.Language)
		if prior != nil {
			state.FileID = prior.FileID
			state.Release = prior.Release
			state.Category = prior.Category
			state.Score = prior.Score
			state.Variant = prior.Variant
			state.Metrics = prior.Metrics
			state.Error = prior.Error
			state.OutputPath = prior.OutputPath
			state.OutputBytes = prior.OutputBytes
		}
		installed := subtitleOutputPath(videoPath, state.Language)
		switch {
		case state.Present:
			state.Status = TargetPresent
			state.Error = ""
		case IsValidSRTFile(installed):
			state.Status = StatusAdded
			if prior != nil && (prior.Status == StatusAddedReview || prior.Status == StatusAdded) {
				state.Status = prior.Status
			}
			state.OutputPath = installed
			if info, err := os.Stat(installed); err == nil {
				state.OutputBytes = info.Size()
			}
			state.Error = ""
		case videoChanged:
			*state = TargetState{Language: state.Language, Status: StatusPending}
		case prior != nil && terminalTargetStatus(prior.Status):
			state.Status = prior.Status
		default:
			state.Status = StatusPending
			state.Error = ""
			state.OutputPath = ""
			state.OutputBytes = 0
		}
	}
	return current
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}

// applyScanResult builds or refreshes the durable item for one video file.
func applyScanResult(existing *Item, driveID, root, folderName, videoPath string, info os.FileInfo, media MediaInfo, external []externalSubtitle, targetLanguages []string, now time.Time) *Item {
	relative, err := filepath.Rel(root, videoPath)
	if err != nil {
		relative = videoPath
	}
	videoName := filepath.Base(videoPath)
	videoBase := strings.TrimSuffix(videoName, filepath.Ext(videoName))
	title, year := ParseTitleYear(videoBase)
	if year == 0 {
		if folderTitle, folderYear, ok := library.ParseMovieFolder(folderName); ok {
			if strings.TrimSpace(title) == "" {
				title = folderTitle
			}
			year = folderYear
		}
	}
	if strings.TrimSpace(title) == "" {
		title = strings.TrimSuffix(folderName, filepath.Ext(folderName))
	}

	targets := targetStates(targetLanguages, external, media.Streams)
	externalRefs := make([]ExternalSubtitleRef, 0, len(external))
	hasExternal := false
	for _, sub := range external {
		externalRefs = append(externalRefs, ExternalSubtitleRef{
			FileName: sub.FileName,
			Language: sub.Language,
			Ext:      sub.Ext,
			Usable:   sub.Usable,
		})
		if sub.Usable {
			hasExternal = true
		}
	}
	languages := mergeLanguages(external, media.Streams)

	item := &Item{
		ID:                   subtitleItemID(driveID, relative),
		DriveID:              driveID,
		RemoteRoot:           root,
		FolderName:           folderName,
		FolderPath:           filepath.Dir(videoPath),
		VideoPath:            videoPath,
		VideoName:            videoName,
		VideoSize:            info.Size(),
		VideoMtime:           info.ModTime().Unix(),
		DurationMS:           media.DurationMS,
		Title:                title,
		Year:                 year,
		Status:               StatusPending,
		Probed:               media.Probed,
		ExistingSubLanguages: languages,
		ExternalSubtitles:    externalRefs,
		EmbeddedSubStreams:   media.Streams,
		HasExternalSubtitle:  hasExternal,
		Targets:              targets,
		CreatedAt:            now,
		UpdatedAt:            now,
		ReferenceStream:      -1,
	}
	videoChanged := existing != nil && (existing.VideoSize != item.VideoSize || existing.VideoMtime != item.VideoMtime)
	if existing == nil {
		if item.HasAllTargets() {
			item.Status = StatusHasTargets
		}
		return item
	}

	// Preserve stable fields and prior work, then reconcile the status with what
	// is actually on disk now.
	item.CreatedAt = existing.CreatedAt
	item.Attempts = existing.Attempts
	item.WorkDir = existing.WorkDir
	item.ReferenceKind = existing.ReferenceKind
	item.ReferenceLang = existing.ReferenceLang
	item.ReferenceStream = existing.ReferenceStream
	item.ReferencePath = existing.ReferencePath
	item.Error = existing.Error
	item.Targets = reconcileTargets(existing.Targets, item.Targets, videoPath, videoChanged)
	if videoChanged {
		// A replaced video invalidates the probe cache and every previous sync.
		item.ReferenceKind = ""
		item.ReferenceLang = ""
		item.ReferenceStream = -1
		item.ReferencePath = ""
		item.Error = ""
	}

	switch {
	case videoChanged:
		item.Status = StatusPending
	case item.HasAllTargets():
		if existing.Status == StatusAdded || existing.Status == StatusAddedReview {
			item.Status = existing.Status
		} else {
			item.Status = StatusHasTargets
		}
	case isTransientStatus(existing.Status), isHasTargetsStatus(existing.Status),
		existing.Status == StatusAdded, existing.Status == StatusAddedReview:
		// A target subtitle disappeared (or work was interrupted).
		item.Status = StatusPending
		item.Error = ""
	default:
		item.Status = existing.Status
	}
	return item
}

func dedupeStrings(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

// skipVideoFile filters out extras that are never worth a subtitle: tiny files
// and obvious sample clips.
func skipVideoFile(name string, size, minBytes int64, skipSamples bool) bool {
	if size < minBytes {
		return true
	}
	if skipSamples {
		lower := strings.ToLower(name)
		if strings.Contains(lower, "sample") && size < 500*1024*1024 {
			return true
		}
	}
	return false
}

// mergeLanguages lists every subtitle language visible for a video, from external
// files and from embedded streams.
func mergeLanguages(external []externalSubtitle, streams []EmbeddedSubtitle) []string {
	languages := make([]string, 0, len(external)+len(streams))
	for _, sub := range external {
		if sub.Language != "" {
			languages = append(languages, sub.Language)
		}
	}
	for _, stream := range streams {
		if stream.Language != "" {
			languages = append(languages, stream.Language)
		}
	}
	return dedupeStrings(languages)
}
