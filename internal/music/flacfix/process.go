package flacfix

import (
	"context"
	"io/fs"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
)

// Genres translates genre tag values (genres.Translator).
type Genres interface {
	TranslateAll(values []string) []string
	Unknown(values []string) []string
}

type Options struct {
	FixMD5 bool
	// Genres translates GENRE tags when not nil.
	Genres Genres
	// CleanDescriptions turns the HTML in DESCRIPTION tags into plain text.
	CleanDescriptions bool
	// Confirm tests every fixed file with `flac -t`. Leave it off when the
	// caller runs `flac -t` itself afterwards.
	Confirm     bool
	DryRun      bool
	FlacBin     string
	MetaflacBin string
	Workers     int
}

// FileChange describes what happened to one file.
type FileChange struct {
	Path      string
	MD5       bool     // a missing MD5 signature was (or would be) written
	GenreFrom []string // set when the genres were (or would be) changed
	GenreTo   []string
	// Description is true when the DESCRIPTION tag was (or would be)
	// cleaned.
	Description bool
	Err         error
}

// Report sums up a run.
type Report struct {
	Files   int
	Changes []FileChange // files that changed or failed, sorted by path
	// UnknownGenres are genre names that look French but are not in the
	// translation table.
	UnknownGenres []string
}

// Descriptions returns how many files got a cleaned description.
func (r Report) Descriptions() int {
	n := 0
	for _, c := range r.Changes {
		if c.Description {
			n++
		}
	}
	return n
}

// Counts returns how many files got an MD5, new genres and errors.
func (r Report) Counts() (md5, genres, errs int) {
	for _, c := range r.Changes {
		if c.Err != nil {
			errs++
		}
		if c.MD5 {
			md5++
		}
		if c.GenreTo != nil {
			genres++
		}
	}
	return
}

// Dir fixes every .flac file below dir. Errors on single files are reported
// in the result; the returned error is only for an unreadable dir or ctx.
func Dir(ctx context.Context, dir string, o Options) (Report, error) {
	var paths []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && p != dir && strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir
		}
		if d.Type().IsRegular() && strings.EqualFold(filepath.Ext(p), ".flac") && !strings.HasPrefix(d.Name(), ".") {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		return Report{}, err
	}

	workers := o.Workers
	if workers <= 0 {
		workers = min(4, runtime.NumCPU())
	}
	var (
		mu      sync.Mutex
		rep     = Report{Files: len(paths)}
		unknown = map[string]bool{}
		jobs    = make(chan string)
		wg      sync.WaitGroup
	)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				c, unk := File(ctx, p, o)
				mu.Lock()
				if c.Err != nil || c.MD5 || c.GenreTo != nil || c.Description {
					rep.Changes = append(rep.Changes, c)
				}
				for _, u := range unk {
					if !unknown[u] {
						unknown[u] = true
						rep.UnknownGenres = append(rep.UnknownGenres, u)
					}
				}
				mu.Unlock()
			}
		}()
	}
	for _, p := range paths {
		select {
		case jobs <- p:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(jobs)
	wg.Wait()
	sort.Slice(rep.Changes, func(a, b int) bool { return rep.Changes[a].Path < rep.Changes[b].Path })
	sort.Strings(rep.UnknownGenres)
	return rep, ctx.Err()
}

// File fixes one file. It returns what changed and unknown genre names.
func File(ctx context.Context, path string, o Options) (FileChange, []string) {
	c := FileChange{Path: path}
	if o.FixMD5 {
		if o.DryRun {
			info, err := ReadStreamInfo(path)
			if err != nil {
				c.Err = err
				return c, nil
			}
			c.MD5 = !info.MD5Set()
		} else {
			res, err := FixMD5(ctx, path, o.FlacBin, false)
			if err != nil {
				c.Err = err
				// Do not edit tags of a file that may be damaged.
				return c, nil
			}
			c.MD5 = res == MD5Written
		}
	}
	var unknown []string
	var updates []TagUpdate
	if o.Genres != nil {
		values, err := ReadTag(path, "GENRE")
		if err != nil {
			c.Err = err
			return c, nil
		}
		if len(values) > 0 {
			unknown = o.Genres.Unknown(values)
			translated := o.Genres.TranslateAll(values)
			if !slices.Equal(values, translated) {
				c.GenreFrom, c.GenreTo = values, translated
				updates = append(updates, TagUpdate{Name: "GENRE", Values: translated})
			}
		}
	}
	if o.CleanDescriptions {
		values, err := ReadTag(path, "DESCRIPTION")
		if err != nil {
			c.Err = err
			return c, nil
		}
		cleaned := make([]string, len(values))
		for i, v := range values {
			cleaned[i] = CleanDescription(v)
		}
		if !slices.Equal(values, cleaned) {
			c.Description = true
			updates = append(updates, TagUpdate{Name: "DESCRIPTION", Values: cleaned})
		}
	}
	if len(updates) > 0 && !o.DryRun {
		if err := SetTags(ctx, path, updates, o.MetaflacBin); err != nil {
			c.Err = err
			c.GenreTo = nil
			c.Description = false
		}
	}
	// Prove the edits left the audio alone: flac -t decodes the file and
	// compares it with the MD5 taken from the original download.
	if o.Confirm && !o.DryRun && c.Err == nil && (c.MD5 || c.GenreTo != nil || c.Description) {
		if err := Test(ctx, path, o.FlacBin); err != nil {
			c.Err = err
			if c.MD5 {
				if info, rerr := ReadStreamInfo(path); rerr == nil {
					_ = patchMD5(path, info.MD5, [16]byte{})
				}
				c.MD5 = false
			}
		}
	}
	return c, unknown
}
