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

// ChooseReference picks the timing reference with the precedence the manual
// workflow used: per preferred language, an external subtitle first, then an
// embedded stream; then any external subtitle; then any usable embedded stream.
func ChooseReference(item *Item, external []externalSubtitle, referenceLanguages []string) *referenceChoice {
	for _, lang := range referenceLanguages {
		for _, sub := range external {
			if sub.Usable && sub.Language == lang {
				return &referenceChoice{Kind: "external", Lang: lang, Stream: -1, Path: sub.Path}
			}
		}
		if stream, ok := bestEmbeddedStream(item.EmbeddedSubStreams, lang); ok {
			return &referenceChoice{Kind: "embedded", Lang: stream.Language, Stream: stream.Index}
		}
	}
	for _, sub := range external {
		if sub.Usable {
			return &referenceChoice{Kind: "external", Lang: sub.Language, Stream: -1, Path: sub.Path}
		}
	}
	if stream, ok := bestEmbeddedStream(item.EmbeddedSubStreams, ""); ok {
		return &referenceChoice{Kind: "embedded", Lang: stream.Language, Stream: stream.Index}
	}
	return nil
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

// hasSwedishSubtitle reports whether an item already exposes the target
// language, either as an external file or an embedded stream.
func hasSwedishSubtitle(targetLanguage string, external []externalSubtitle, streams []EmbeddedSubtitle) (bool, []string) {
	var sources []string
	for _, sub := range external {
		if sub.Language == targetLanguage && usableReferenceExtensions[sub.Ext] {
			sources = append(sources, "external "+sub.FileName)
		}
	}
	for _, stream := range streams {
		if stream.Usable && stream.Language == targetLanguage {
			sources = append(sources, "embedded stream #"+itoa(stream.Index))
		}
	}
	return len(sources) > 0, sources
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
func applyScanResult(existing *Item, driveID, root, folderName, videoPath string, info os.FileInfo, media MediaInfo, external []externalSubtitle, targetLanguage string, now time.Time) *Item {
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

	swedish, sources := hasSwedishSubtitle(targetLanguage, external, media.Streams)
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
	languages := make([]string, 0, len(external))
	for _, sub := range external {
		if sub.Language != "" {
			languages = append(languages, sub.Language)
		}
	}
	for _, stream := range media.Streams {
		if stream.Language != "" {
			languages = append(languages, stream.Language)
		}
	}
	languages = dedupeStrings(languages)

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
		ExistingSubLanguages: languages,
		ExternalSubtitles:    externalRefs,
		EmbeddedSubStreams:   media.Streams,
		HasExternalSubtitle:  hasExternal,
		HasSwedish:           swedish,
		SwedishSources:       sources,
		CreatedAt:            now,
		UpdatedAt:            now,
		ReferenceStream:      -1,
	}
	if existing == nil {
		if swedish {
			item.Status = StatusHasSwedish
		}
		return item
	}

	// Preserve stable fields and prior work, then reconcile the status with what
	// is actually on disk now.
	item.CreatedAt = existing.CreatedAt
	item.Attempts = existing.Attempts
	item.ChosenFileID = existing.ChosenFileID
	item.ChosenRelease = existing.ChosenRelease
	item.ChosenCategory = existing.ChosenCategory
	item.ChosenScore = existing.ChosenScore
	item.Metrics = existing.Metrics
	item.OutputPath = existing.OutputPath
	item.OutputBytes = existing.OutputBytes
	item.WorkDir = existing.WorkDir
	item.ReferenceKind = existing.ReferenceKind
	item.ReferenceLang = existing.ReferenceLang
	item.ReferenceStream = existing.ReferenceStream
	item.ReferencePath = existing.ReferencePath
	item.Error = existing.Error

	videoChanged := existing.VideoSize != item.VideoSize || existing.VideoMtime != item.VideoMtime
	if videoChanged {
		// A replaced video invalidates the probe cache and any previous sync.
		item.ChosenFileID = 0
		item.ChosenRelease = ""
		item.ChosenCategory = ""
		item.ChosenScore = 0
		item.Metrics = nil
		item.OutputPath = ""
		item.OutputBytes = 0
		item.Error = ""
	}

	switch {
	case videoChanged:
		item.Status = StatusPending
	case swedish:
		if existing.Status == StatusAdded || existing.Status == StatusAddedReview {
			item.Status = existing.Status
		} else {
			item.Status = StatusHasSwedish
		}
	case isTransientStatus(existing.Status), existing.Status == StatusHasSwedish,
		existing.Status == StatusAdded, existing.Status == StatusAddedReview:
		// The Swedish subtitle disappeared (or work was interrupted).
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
