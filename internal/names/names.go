// Package names turns client-supplied names and paths into safe on-disk
// names: segment sanitising, path splitting, duplicate renaming
// ("name (1).ext"), device labels from User-Agent strings, and snippet names.
//
// Every name this package produces is valid UTF-8, contains no path
// separator or control character, is not "." or "..", and fits in 255 bytes,
// so it is safe as a single path segment on Linux and portable to Windows
// and macOS when downloaded or unzipped.
package names

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"path"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxNameBytes is the longest segment Sanitize and Dedupe produce. It matches
// NAME_MAX on Linux filesystems.
const MaxNameBytes = 255

// maxExtBytes is the longest extension kept intact when a name is
// shortened or numbered; anything longer is treated as part of the stem.
const maxExtBytes = 32

// Limits on a whole relative path.
const (
	MaxDepth     = 64
	MaxPathBytes = 3072
)

// ErrBadPath reports a client path that is malformed or tries to escape.
var ErrBadPath = errors.New("invalid path")

// Sanitize turns one client-supplied file or folder name into a safe segment.
// Only the part after the last slash or backslash is kept. Control and bidi
// formatting characters are dropped, characters Windows forbids are replaced
// with "_", leading spaces and trailing dots or spaces are trimmed, reserved
// Windows device names get a "_" prefix, and the result is cut to
// MaxNameBytes keeping the extension. An empty result becomes "unnamed".
// Sanitize is idempotent.
func Sanitize(name string) string {
	name = strings.ToValidUTF8(name, "_")
	name = strings.ReplaceAll(name, `\`, "/")
	name = strings.TrimRight(name, "/")
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}

	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		switch {
		case unicode.IsControl(r), unicode.Is(unicode.Bidi_Control, r), r == 0xFEFF:
			// drop
		case strings.ContainsRune(`:*?"<>|`, r):
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	name = trimName(b.String())
	if isReservedWindowsName(name) {
		name = "_" + name
	}
	name = trimName(truncateName(name, MaxNameBytes))
	if name == "" {
		return "unnamed"
	}
	return name
}

// trimName removes leading whitespace and trailing dots and whitespace.
func trimName(s string) string {
	s = strings.TrimLeftFunc(s, unicode.IsSpace)
	return strings.TrimRightFunc(s, func(r rune) bool { return r == '.' || unicode.IsSpace(r) })
}

var reservedStems = func() map[string]bool {
	m := map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true, "CONIN$": true, "CONOUT$": true}
	for i := 0; i <= 9; i++ {
		m["COM"+strconv.Itoa(i)] = true
		m["LPT"+strconv.Itoa(i)] = true
	}
	return m
}()

func isReservedWindowsName(name string) bool {
	stem, _, _ := strings.Cut(name, ".")
	return reservedStems[strings.ToUpper(strings.TrimRightFunc(stem, unicode.IsSpace))]
}

// truncateName cuts name to at most max bytes on a rune boundary, keeping a
// short extension when it can.
func truncateName(name string, max int) string {
	if len(name) <= max {
		return name
	}
	ext := extension(name)
	if len(ext) > maxExtBytes || len(ext) >= max {
		ext = ""
	}
	stem := cutBytes(name[:len(name)-len(ext)], max-len(ext))
	stem = strings.TrimRightFunc(stem, func(r rune) bool { return r == '.' || unicode.IsSpace(r) })
	if stem == "" {
		return cutBytes(name, max)
	}
	return stem + ext
}

// cutBytes returns the longest prefix of s that is at most n bytes and ends
// on a rune boundary.
func cutBytes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// extension returns the extension Dedupe and truncation keep intact:
// ".tar.gz"-style double extensions, a normal ".ext", or "" for dotfiles
// such as ".bashrc".
func extension(name string) string {
	ext := path.Ext(name)
	if ext == name || ext == "." {
		return ""
	}
	stem := name[:len(name)-len(ext)]
	if inner := path.Ext(stem); strings.EqualFold(inner, ".tar") && inner != stem {
		return inner + ext
	}
	return ext
}

// SplitRel splits a client path that must refer to something that already
// exists, such as a folder to list or a file to download. Slashes and
// backslashes both separate segments; empty and "." segments are ignored, so
// "" and "/" mean the root and an empty slice is returned. Segments are not
// rewritten: one that Sanitize would change is rejected,
// because silently mapping it could point at a different file.
func SplitRel(p string) ([]string, error) {
	return splitRel(p, false)
}

// CleanRel splits a client path for something about to be created, such as
// the target folder of an upload. Like SplitRel, but each segment is passed
// through Sanitize instead of being rejected.
func CleanRel(p string) ([]string, error) {
	return splitRel(p, true)
}

func splitRel(p string, clean bool) ([]string, error) {
	if len(p) > MaxPathBytes || strings.IndexByte(p, 0) >= 0 || !utf8.ValidString(p) {
		return nil, ErrBadPath
	}
	parts := strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' })
	segs := make([]string, 0, len(parts))
	for _, s := range parts {
		switch {
		case s == ".":
			continue
		case s == "..":
			return nil, ErrBadPath
		case clean:
			s = Sanitize(s)
		case Sanitize(s) != s:
			return nil, ErrBadPath
		}
		segs = append(segs, s)
	}
	if len(segs) > MaxDepth {
		return nil, ErrBadPath
	}
	return segs, nil
}

// Join renders segments as a slash-separated relative path ("" for the root).
func Join(segs []string) string {
	return strings.Join(segs, "/")
}

// Dedupe returns name if exists reports it free, otherwise the first free
// "stem (n).ext" for n = 1, 2, ... A trailing " (n)" already on the stem is
// replaced rather than stacked, and the result stays within MaxNameBytes.
func Dedupe(exists func(string) bool, name string) string {
	if !exists(name) {
		return name
	}
	ext := extension(name)
	if len(ext) > maxExtBytes {
		ext = "" // a "extension" that long is just part of the name
	}
	stem := name[:len(name)-len(ext)]
	if i := strings.LastIndex(stem, " ("); i > 0 && strings.HasSuffix(stem, ")") {
		if _, err := strconv.Atoi(stem[i+2 : len(stem)-1]); err == nil {
			stem = stem[:i]
		}
	}
	for n := 1; n < 100000; n++ {
		suffix := " (" + strconv.Itoa(n) + ")"
		s := cutBytes(stem, MaxNameBytes-len(ext)-len(suffix))
		candidate := s + suffix + ext
		if !exists(candidate) {
			return candidate
		}
	}
	// Practically unreachable: 100000 numbered copies exist. Fall back to a
	// random suffix, which cannot be confused with the numbered series.
	var b [6]byte
	_, _ = rand.Read(b[:])
	return cutBytes(stem, MaxNameBytes-len(ext)-16) + " (" + hex.EncodeToString(b[:]) + ")" + ext
}
