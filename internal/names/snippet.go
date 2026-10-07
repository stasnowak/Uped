package names

import (
	"net/url"
	"strings"
	"time"
	"unicode"
)

const snippetSlugRunes = 40

// SnippetName picks a file name for a pasted text snippet: "link-<host>.txt"
// when the first line is a web address, otherwise a lowercase slug of the
// first non-empty line, otherwise "note-<YYYYMMDD-HHMMSS>.txt" using now.
func SnippetName(text string, now time.Time) string {
	var line string
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			line = l
			break
		}
	}

	if !strings.ContainsAny(line, " \t") {
		if u, err := url.Parse(line); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" {
			host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
			if slug := slugify(host, ".-", snippetSlugRunes-len("link-")); slug != "" {
				return Sanitize("link-" + slug + ".txt")
			}
		}
	}

	if slug := slugify(line, "", snippetSlugRunes); slug != "" {
		return Sanitize(slug + ".txt")
	}
	return "note-" + now.Format("20060102-150405") + ".txt"
}

// slugify lowercases s, keeps letters, digits and any rune in keep, turns
// every other run of characters into a single "-", and limits the result to
// max runes without leading or trailing dashes.
func slugify(s, keep string, max int) string {
	var b strings.Builder
	n := 0
	dash := false
	for _, r := range strings.ToLower(s) {
		if n >= max {
			break
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune(keep, r) {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
				n++
				if n >= max {
					break
				}
			}
			dash = false
			b.WriteRune(r)
			n++
			continue
		}
		dash = true
	}
	return strings.Trim(b.String(), "-.")
}
