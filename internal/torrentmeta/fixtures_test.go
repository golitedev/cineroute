package torrentmeta

import (
	"bytes"
	"strconv"
	"strings"
)

func beInt(n int64) string { return "i" + strconv.FormatInt(n, 10) + "e" }

func beStr(s string) string {
	return strconv.Itoa(len(s)) + ":" + s
}

// makeTorrent builds a minimal metainfo byte blob. files==nil produces a
// single-file torrent; otherwise a multi-file torrent with the given paths.
func makeTorrent(name string, files map[string]int64) []byte {
	var b bytes.Buffer
	b.WriteString("d")
	b.WriteString(beStr("info"))
	b.WriteString("d")
	b.WriteString(beStr("name"))
	b.WriteString(beStr(name))
	b.WriteString(beStr("piece length"))
	b.WriteString(beInt(16384))
	b.WriteString(beStr("pieces"))
	b.WriteString(beStr(string(bytes.Repeat([]byte{0}, 20))))
	if files == nil {
		b.WriteString(beStr("length"))
		b.WriteString(beInt(1234))
	} else {
		b.WriteString(beStr("files"))
		b.WriteString("l")
		for path, ln := range files {
			b.WriteString("d")
			b.WriteString(beStr("length"))
			b.WriteString(beInt(ln))
			b.WriteString(beStr("path"))
			b.WriteString("l")
			for _, c := range strings.Split(path, "/") {
				b.WriteString(beStr(c))
			}
			b.WriteString("e")
			b.WriteString("e")
		}
		b.WriteString("e")
	}
	b.WriteString("e")
	b.WriteString("e")
	return b.Bytes()
}

// makeV2Torrent builds a v2-style torrent whose file tree is
// { top: { file: { "": { length: 1000 } } } }.
func makeV2Torrent(name, top, file string) []byte {
	tree := "d" + beStr(top) + "d" + beStr(file) + "d" + "0:" + "d" + beStr("length") + beInt(1000) + "e" + "e" + "e" + "e"
	info := "d" + beStr("name") + beStr(name) +
		beStr("meta version") + beInt(2) +
		beStr("piece length") + beInt(16384) +
		beStr("file tree") + tree + "e"
	return []byte("d" + beStr("info") + info + "e")
}
