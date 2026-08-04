// Package torrentmeta parses .torrent metainfo without trusting its content.
// The bencode decoder is bounded (depth and item limits) and path components
// are validated against traversal and length limits.
package torrentmeta

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	maxBencodeDepth    = 64
	maxBencodeItems    = 1000000
	maxPathComponents  = 64
	maxComponentBytes  = 255
	maxFileCount       = 100000
	maxTrackerCount    = 256
	maxTrackerURLBytes = 4096
)

var (
	ErrInvalidBencode = errors.New("invalid bencoding")
	ErrDepthExceeded  = fmt.Errorf("bencode nesting exceeds %d levels", maxBencodeDepth)
	ErrItemLimit      = fmt.Errorf("bencode collection exceeds %d items", maxBencodeItems)
	ErrDuplicateKey   = errors.New("duplicate key in bencode dictionary")
	ErrSizeOverflow   = errors.New("torrent size overflows int64")
	ErrUnsafePath     = errors.New("torrent contains unsafe path")
	ErrNoFiles        = errors.New("torrent contains no files")
	ErrSymlink        = errors.New("torrent contains symlink entries, which are not supported")
	ErrNotMetainfo    = errors.New("not a metainfo file")
)

type bval struct {
	dict map[string]*bval
	list []*bval
	num  int64
	str  []byte
	raw  []byte
}

type decoder struct {
	data  []byte
	pos   int
	depth int
	items int
}

func (d *decoder) value() (*bval, error) {
	if d.pos >= len(d.data) {
		return nil, ErrInvalidBencode
	}
	switch d.data[d.pos] {
	case 'd':
		return d.dict()
	case 'l':
		return d.list()
	case 'i':
		return d.integer()
	default:
		return d.bstring()
	}
}

func (d *decoder) dict() (*bval, error) {
	d.depth++
	if d.depth > maxBencodeDepth {
		return nil, ErrDepthExceeded
	}
	defer func() { d.depth-- }()
	start := d.pos
	d.pos++ // 'd'
	m := make(map[string]*bval)
	for {
		if d.pos >= len(d.data) {
			return nil, ErrInvalidBencode
		}
		if d.data[d.pos] == 'e' {
			d.pos++
			break
		}
		k, err := d.bstring()
		if err != nil {
			return nil, err
		}
		key := string(k.str)
		if _, dup := m[key]; dup {
			return nil, ErrDuplicateKey
		}
		v, err := d.value()
		if err != nil {
			return nil, err
		}
		m[key] = v
	}
	d.items += len(m)
	if d.items > maxBencodeItems {
		return nil, ErrItemLimit
	}
	return &bval{dict: m, raw: d.data[start:d.pos]}, nil
}

func (d *decoder) list() (*bval, error) {
	d.depth++
	if d.depth > maxBencodeDepth {
		return nil, ErrDepthExceeded
	}
	defer func() { d.depth-- }()
	d.pos++ // 'l'
	var l []*bval
	for {
		if d.pos >= len(d.data) {
			return nil, ErrInvalidBencode
		}
		if d.data[d.pos] == 'e' {
			d.pos++
			break
		}
		v, err := d.value()
		if err != nil {
			return nil, err
		}
		l = append(l, v)
	}
	d.items += len(l)
	if d.items > maxBencodeItems {
		return nil, ErrItemLimit
	}
	return &bval{list: l}, nil
}

func (d *decoder) integer() (*bval, error) {
	start := d.pos
	d.pos++ // 'i'
	if d.pos >= len(d.data) {
		return nil, ErrInvalidBencode
	}
	neg := false
	if d.data[d.pos] == '-' {
		neg = true
		d.pos++
	}
	if d.pos >= len(d.data) || d.data[d.pos] < '0' || d.data[d.pos] > '9' {
		return nil, ErrInvalidBencode
	}
	digitsStart := d.pos
	if d.data[d.pos] == '0' {
		d.pos++
		if d.pos < len(d.data) && d.data[d.pos] >= '0' && d.data[d.pos] <= '9' {
			return nil, ErrInvalidBencode // leading zero
		}
	} else {
		for d.pos < len(d.data) && d.data[d.pos] >= '0' && d.data[d.pos] <= '9' {
			d.pos++
		}
	}
	if d.pos >= len(d.data) || d.data[d.pos] != 'e' {
		return nil, ErrInvalidBencode
	}
	n, err := strconv.ParseInt(string(d.data[digitsStart:d.pos]), 10, 64)
	if err != nil {
		return nil, ErrInvalidBencode
	}
	if neg {
		n = -n
	}
	d.pos++
	return &bval{num: n, raw: d.data[start:d.pos]}, nil
}

func (d *decoder) bstring() (*bval, error) {
	rest := d.data[d.pos:]
	colon := bytes.IndexByte(rest, ':')
	if colon < 0 {
		return nil, ErrInvalidBencode
	}
	if colon > 1 && rest[0] == '0' {
		return nil, ErrInvalidBencode // leading zero length
	}
	n, err := strconv.ParseInt(string(rest[:colon]), 10, 64)
	if err != nil || n < 0 {
		return nil, ErrInvalidBencode
	}
	if int(n) > len(rest)-colon-1 {
		return nil, ErrInvalidBencode
	}
	s := rest[colon+1 : colon+1+int(n)]
	d.pos += colon + 1 + int(n)
	return &bval{str: s}, nil
}

// File is one payload file. RelPath is relative to the expected qBittorrent
// content root; FullPath includes the structural root for display.
type File struct {
	RelPath  []string
	FullPath []string
	Length   int64
	Padding  bool
}

// Kind describes the payload topology.
type Kind string

const (
	KindSingleFile    Kind = "single"
	KindRootedMulti   Kind = "rooted-multi"
	KindRootlessMulti Kind = "rootless-multi"
)

type MetaInfo struct {
	Name        string
	Size        int64
	Kind        Kind
	RootName    string
	RootFolder  bool // the explicit value to send to qBittorrent
	Files       []File
	InfoHashV1  string
	MetaVersion int
	Private     bool
	Trackers    []string
}

// Parse decodes metainfo and derives the structural topology. The raw byte
// span of the info dictionary is used for the v1 info hash, so the original
// bytes are never re-encoded.
func Parse(data []byte) (*MetaInfo, error) {
	if len(data) == 0 {
		return nil, ErrNotMetainfo
	}
	d := &decoder{data: data}
	root, err := d.value()
	if err != nil {
		return nil, err
	}
	if d.pos != len(data) {
		return nil, ErrInvalidBencode // trailing garbage
	}
	if root.dict == nil {
		return nil, ErrNotMetainfo
	}
	info := root.dict["info"]
	if info == nil || info.dict == nil || info.raw == nil {
		return nil, ErrNotMetainfo
	}

	mi := &MetaInfo{}
	sum := sha1.Sum(info.raw)
	mi.InfoHashV1 = hex.EncodeToString(sum[:])

	if mv := info.dict["meta version"]; mv != nil {
		mi.MetaVersion = int(mv.num)
	}
	if mi.MetaVersion < 0 || mi.MetaVersion > 2 {
		return nil, fmt.Errorf("unsupported meta version %d", mi.MetaVersion)
	}
	if mi.MetaVersion == 0 {
		mi.MetaVersion = 1
	}

	mi.Name = dictStr(info, "name")
	if uname := dictStr(info, "name.utf-8"); uname != "" {
		mi.Name = uname
	}
	if mi.Name != "" {
		if err := validatePath([]string{mi.Name}); err != nil {
			return nil, err
		}
	}

	if p := info.dict["private"]; p != nil {
		mi.Private = p.num != 0
	}

	if a := root.dict["announce"]; a != nil && a.str != nil {
		mi.Trackers = append(mi.Trackers, string(a.str))
	}
	if al := root.dict["announce-list"]; al != nil && al.list != nil {
		for _, tier := range al.list {
			if tier.list == nil {
				continue
			}
			for _, tr := range tier.list {
				if tr.str != nil {
					mi.Trackers = append(mi.Trackers, string(tr.str))
				}
			}
		}
	}
	if len(mi.Trackers) > maxTrackerCount {
		return nil, fmt.Errorf("tracker count exceeds %d", maxTrackerCount)
	}
	for _, t := range mi.Trackers {
		if len(t) > maxTrackerURLBytes {
			return nil, fmt.Errorf("tracker URL exceeds %d bytes", maxTrackerURLBytes)
		}
	}

	switch {
	case info.dict["length"] != nil:
		// Single file.
		ln := info.dict["length"].num
		if ln < 0 {
			return nil, ErrInvalidBencode
		}
		mi.Kind = KindSingleFile
		mi.RootFolder = false
		f := File{RelPath: []string{mi.Name}, FullPath: []string{mi.Name}, Length: ln}
		mi.Files = append(mi.Files, f)
		mi.Size = ln

	case info.dict["files"] != nil && info.dict["files"].list != nil:
		// v1 multi-file: paths are relative to a structural root named info.name.
		mi.Kind = KindRootedMulti
		mi.RootName = mi.Name
		mi.RootFolder = true
		for i, fe := range info.dict["files"].list {
			if i >= maxFileCount {
				return nil, fmt.Errorf("file count exceeds %d", maxFileCount)
			}
			f, err := parseV1File(fe)
			if err != nil {
				return nil, err
			}
			f.FullPath = append([]string{mi.Name}, f.RelPath...)
			mi.Files = append(mi.Files, f)
			mi.Size += f.Length
			if mi.Size < 0 {
				return nil, ErrSizeOverflow
			}
		}

	case info.dict["file tree"] != nil && info.dict["file tree"].dict != nil:
		// v2 / hybrid: the file tree may or may not be rooted under info.name.
		ft := info.dict["file tree"]
		rooted := !v2Rootless(ft, mi.Name)
		mi.Kind = KindRootedMulti
		if !rooted {
			mi.Kind = KindRootlessMulti
		}
		mi.RootName = ""
		if rooted {
			mi.RootName = mi.Name
		}
		mi.RootFolder = rooted
		if err := mi.walkV2Tree(ft, nil); err != nil {
			return nil, err
		}

	default:
		return nil, ErrNoFiles
	}

	if len(mi.Files) == 0 {
		return nil, ErrNoFiles
	}
	return mi, nil
}

func parseV1File(fe *bval) (File, error) {
	if fe.dict == nil {
		return File{}, ErrInvalidBencode
	}
	ln := fe.dict["length"]
	if ln == nil || ln.num < 0 {
		return File{}, ErrInvalidBencode
	}
	var path []string
	if p := fe.dict["path.utf-8"]; p != nil && p.list != nil {
		path = strList(p)
	} else if p := fe.dict["path"]; p != nil && p.list != nil {
		path = strList(p)
	}
	if len(path) == 0 {
		return File{}, ErrInvalidBencode
	}
	if err := validatePath(path); err != nil {
		return File{}, err
	}
	f := File{RelPath: path, Length: ln.num}
	if a := fe.dict["attr"]; a != nil {
		attr := string(a.str)
		if strings.Contains(attr, "l") {
			return File{}, ErrSymlink
		}
		f.Padding = strings.Contains(attr, "p")
	}
	return f, nil
}

// v2Rootless reports whether the file tree is not structurally rooted under
// info.name (BEP 52 rootless torrents). qBittorrent then gets root_folder=false.
func v2Rootless(ft *bval, name string) bool {
	if ft.dict == nil {
		return true
	}
	if name == "" {
		return true
	}
	top := ft.dict[name]
	if top == nil || top.dict == nil {
		return true
	}
	for k := range top.dict {
		if k == "" {
			return true // the named entry is a file, not a directory
		}
	}
	return false
}

func (mi *MetaInfo) walkV2Tree(ft *bval, prefix []string) error {
	if ft.dict == nil {
		return ErrInvalidBencode
	}
	for k, v := range ft.dict {
		if v.dict == nil {
			return ErrInvalidBencode
		}
		if leaf, ok := v.dict[""]; ok {
			// File leaf: properties live under the empty-string key.
			if leaf.dict == nil {
				return ErrInvalidBencode
			}
			ln := leaf.dict["length"]
			if ln == nil || ln.num < 0 {
				return ErrInvalidBencode
			}
			if a := leaf.dict["attr"]; a != nil && strings.Contains(string(a.str), "l") {
				return ErrSymlink
			}
			path := make([]string, 0, len(prefix)+1)
			path = append(path, prefix...)
			path = append(path, k)
			if err := validatePath(path); err != nil {
				return err
			}
			f := File{FullPath: path, Length: ln.num}
			if mi.RootFolder {
				f.RelPath = append([]string{}, path[len(prefix):]...)
			} else {
				f.RelPath = append([]string{}, path...)
			}
			mi.Files = append(mi.Files, f)
			mi.Size += ln.num
			if mi.Size < 0 {
				return ErrSizeOverflow
			}
			if len(mi.Files) > maxFileCount {
				return fmt.Errorf("file count exceeds %d", maxFileCount)
			}
			continue
		}
		// Directory: recurse.
		child := make([]string, 0, len(prefix)+1)
		child = append(child, prefix...)
		child = append(child, k)
		if err := validatePath(child); err != nil {
			return err
		}
		if err := mi.walkV2Tree(v, child); err != nil {
			return err
		}
	}
	return nil
}

// RelPaths returns each file's path relative to the qBittorrent content root,
// joined with "/". This is the exact list expected from /api/v2/torrents/files.
func (mi *MetaInfo) RelPaths() []string {
	out := make([]string, 0, len(mi.Files))
	for _, f := range mi.Files {
		out = append(out, strings.Join(f.RelPath, "/"))
	}
	return out
}

// ContentPath returns the expected qBittorrent content_path for a save path.
func (mi *MetaInfo) ContentPath(savePath string) string {
	switch mi.Kind {
	case KindSingleFile:
		return joinPath(savePath, mi.Name)
	case KindRootedMulti:
		return joinPath(savePath, mi.RootName)
	default:
		return strings.TrimRight(savePath, "/")
	}
}

func joinPath(base, name string) string {
	base = strings.TrimRight(base, "/")
	if name == "" {
		return base
	}
	return base + "/" + name
}

func validatePath(path []string) error {
	if len(path) > maxPathComponents {
		return fmt.Errorf("path has %d components, maximum is %d", len(path), maxPathComponents)
	}
	for _, c := range path {
		if c == "" || c == "." || c == ".." {
			return ErrUnsafePath
		}
		if strings.ContainsAny(c, "/\x00") {
			return ErrUnsafePath
		}
		if len(c) > maxComponentBytes {
			return fmt.Errorf("path component exceeds %d bytes", maxComponentBytes)
		}
	}
	return nil
}

func dictStr(d *bval, key string) string {
	if v, ok := d.dict[key]; ok && v.str != nil {
		return string(v.str)
	}
	return ""
}

func strList(l *bval) []string {
	out := make([]string, 0, len(l.list))
	for _, v := range l.list {
		if v.str == nil {
			return nil
		}
		out = append(out, string(v.str))
	}
	return out
}
