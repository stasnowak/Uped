package names

import (
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestSanitize(t *testing.T) {
	cases := map[string]string{
		"photo.jpg":                   "photo.jpg",
		"Zażółć gęślą jaźń.txt":       "Zażółć gęślą jaźń.txt",
		"🐢 turtle.png":                "🐢 turtle.png",
		".bashrc":                     ".bashrc",
		"../../etc/passwd":            "passwd",
		`C:\Users\me\file.txt`:        "file.txt",
		"a/b/":                        "b",
		"CON.txt":                     "_CON.txt",
		"con":                         "_con",
		"LPT1":                        "_LPT1",
		"com0.log":                    "_com0.log",
		"CON .txt":                    "_CON .txt",
		"Console.txt":                 "Console.txt",
		"a:b*c?.txt":                  "a_b_c_.txt",
		`say "hi" <now> | later`:      "say _hi_ _now_ _ later",
		"Screenshot 14:30:12.png":     "Screenshot 14_30_12.png",
		"trailing dots...":            "trailing dots",
		"  spaced name  ":             "spaced name",
		"name. . .":                   "name",
		"tab\there":                   "tabhere",
		"nul\x00byte":                 "nulbyte",
		"line\nbreak\r.txt":           "linebreak.txt",
		"\u202Egnp.exe":               "gnp.exe",
		"\ufeffbom.txt":               "bom.txt",
		"a\xffb":                      "a_b",
		"":                            "unnamed",
		".":                           "unnamed",
		"..":                          "unnamed",
		"...":                         "unnamed",
		"/":                           "unnamed",
		"   ":                         "unnamed",
		"\u00a0nbsp\u00a0.":           "nbsp",
		"archive.tar.gz":              "archive.tar.gz",
		"x (abc).txt":                 "x (abc).txt",
		"semi;colon,comma&amp'q.txt":  "semi;colon,comma&amp'q.txt",
		"percent%20encoded%2F..%2F.x": "percent%20encoded%2F..%2F.x",
	}
	for in, want := range cases {
		if got := Sanitize(in); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizeLength(t *testing.T) {
	long := strings.Repeat("a", 296) + ".txt"
	got := Sanitize(long)
	if len(got) != MaxNameBytes || !strings.HasSuffix(got, ".txt") {
		t.Errorf("300-byte ASCII name -> %d bytes %q..., want %d bytes ending .txt", len(got), got[:10], MaxNameBytes)
	}

	multi := strings.Repeat("ż", 300) + ".jpg" // 604 bytes
	got = Sanitize(multi)
	if len(got) > MaxNameBytes || !utf8.ValidString(got) || !strings.HasSuffix(got, ".jpg") {
		t.Errorf("multibyte name -> %d bytes, valid=%v, %q", len(got), utf8.ValidString(got), got[len(got)-8:])
	}

	hugeExt := "a." + strings.Repeat("x", 300)
	if got := Sanitize(hugeExt); len(got) > MaxNameBytes || got == "" {
		t.Errorf("huge extension -> %d bytes", len(got))
	}
}

func checkSanitized(t *testing.T, in, got string) {
	t.Helper()
	switch {
	case got == "" || got == "." || got == "..":
		t.Fatalf("Sanitize(%q) = %q, not a usable name", in, got)
	case len(got) > MaxNameBytes:
		t.Fatalf("Sanitize(%q) is %d bytes", in, len(got))
	case !utf8.ValidString(got):
		t.Fatalf("Sanitize(%q) = %q is not valid UTF-8", in, got)
	case strings.ContainsAny(got, "/\\\x00"):
		t.Fatalf("Sanitize(%q) = %q contains a separator or NUL", in, got)
	case Sanitize(got) != got:
		t.Fatalf("Sanitize is not idempotent: %q -> %q -> %q", in, got, Sanitize(got))
	}
	if segs, err := SplitRel(got); err != nil || len(segs) != 1 || segs[0] != got {
		t.Fatalf("SplitRel(Sanitize(%q)) = %q, %v; want the single segment back", in, segs, err)
	}
}

func FuzzSanitize(f *testing.F) {
	for _, s := range []string{"photo.jpg", "../x", "CON", "a\xffb", " . ", strings.Repeat("ż", 200) + ".tar.gz", "\u202e", "a/b\\c", "x (3).txt"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		got := Sanitize(in)
		checkSanitized(t, in, got)
		// A renamed duplicate must be just as valid as the original.
		checkSanitized(t, in+" (dedupe)", Dedupe(func(n string) bool { return n == got }, got))
	})
}

func TestSplitRel(t *testing.T) {
	ok := map[string][]string{
		"":                      {},
		"/":                     {},
		"a/b":                   {"a", "b"},
		"/a//b/":                {"a", "b"},
		`a\b`:                   {"a", "b"},
		"./a/./b":               {"a", "b"},
		"Zażółć/gęślą jaźń":     {"Zażółć", "gęślą jaźń"},
		"photo (1).jpg":         {"photo (1).jpg"},
		"_CON.txt":              {"_CON.txt"},
		"trip/day 1/IMG_1.HEIC": {"trip", "day 1", "IMG_1.HEIC"},
	}
	for in, want := range ok {
		got, err := SplitRel(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("SplitRel(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	bad := []string{"..", "../x", "a/../b", "a/..", `..\x`, "a\x00b", "a:b", "CON", "a/con.txt", "x.", " lead", "a\xffb",
		strings.Repeat("a/", MaxDepth+1), strings.Repeat("x", MaxPathBytes+1)}
	for _, in := range bad {
		if got, err := SplitRel(in); err != ErrBadPath {
			t.Errorf("SplitRel(%q) = %q, %v; want ErrBadPath", in, got, err)
		}
	}
}

func TestCleanRel(t *testing.T) {
	ok := map[string][]string{
		"":              {},
		"a:b/c":         {"a_b", "c"},
		"CON/x.":        {"_CON", "x"},
		" trip /day1":   {"trip", "day1"},
		"Photos/2026":   {"Photos", "2026"},
		"a/\u202eb/./c": {"a", "b", "c"},
	}
	for in, want := range ok {
		got, err := CleanRel(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("CleanRel(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"../x", "a/../../b", "a\x00", "a\xffb"} {
		if got, err := CleanRel(in); err != ErrBadPath {
			t.Errorf("CleanRel(%q) = %q, %v; want ErrBadPath", in, got, err)
		}
	}
	if got := Join([]string{"a", "b c"}); got != "a/b c" {
		t.Errorf("Join = %q", got)
	}
	if got := Join(nil); got != "" {
		t.Errorf("Join(nil) = %q", got)
	}
}

func existsIn(names ...string) func(string) bool {
	set := map[string]bool{}
	for _, n := range names {
		set[n] = true
	}
	return func(s string) bool { return set[s] }
}

func TestDedupe(t *testing.T) {
	cases := []struct {
		existing []string
		in, want string
	}{
		{nil, "photo.jpg", "photo.jpg"},
		{[]string{"photo.jpg"}, "photo.jpg", "photo (1).jpg"},
		{[]string{"photo.jpg", "photo (1).jpg"}, "photo.jpg", "photo (2).jpg"},
		{[]string{"photo (1).jpg"}, "photo (1).jpg", "photo (2).jpg"},
		{[]string{"photo.jpg", "photo (1).jpg", "photo (2).jpg"}, "photo (1).jpg", "photo (3).jpg"},
		{[]string{"archive.tar.gz"}, "archive.tar.gz", "archive (1).tar.gz"},
		{[]string{".bashrc"}, ".bashrc", ".bashrc (1)"},
		{[]string{"README"}, "README", "README (1)"},
		{[]string{"x (abc).txt"}, "x (abc).txt", "x (abc) (1).txt"},
		{[]string{"trip"}, "trip", "trip (1)"},
	}
	for _, c := range cases {
		if got := Dedupe(existsIn(c.existing...), c.in); got != c.want {
			t.Errorf("Dedupe(%q with %q) = %q, want %q", c.in, c.existing, got, c.want)
		}
	}

	long := strings.Repeat("ż", 125) + ".jpg" // 254 bytes
	got := Dedupe(existsIn(long), long)
	if len(got) > MaxNameBytes || !utf8.ValidString(got) || !strings.HasSuffix(got, " (1).jpg") {
		t.Errorf("Dedupe(long) = %d bytes %q", len(got), got[len(got)-12:])
	}

	// Regression: a 255-byte name whose "extension" is most of it used to
	// panic with a negative slice bound (found by FuzzSanitize).
	longExt := Sanitize("a." + strings.Repeat("x", 300))
	if got := Dedupe(existsIn(longExt), longExt); len(got) > MaxNameBytes || !strings.HasSuffix(got, " (1)") || Sanitize(got) != got {
		t.Errorf("Dedupe(long extension) = %d bytes ...%q", len(got), got[len(got)-8:])
	}

	always := func(string) bool { return true }
	got = Dedupe(always, "x.txt")
	if !strings.HasPrefix(got, "x (") || !strings.HasSuffix(got, ").txt") || len(got) > MaxNameBytes {
		t.Errorf("Dedupe with everything taken = %q", got)
	}
}

func TestDeviceLabel(t *testing.T) {
	cases := map[string]string{
		"Mozilla/5.0 (iPhone; CPU iPhone OS 18_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.5 Mobile/15E148 Safari/604.1":         "iPhone Safari",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 18_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/138.0.7204.156 Mobile/15E148 Safari/604.1": "iPhone Chrome",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 18_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) FxiOS/140.0 Mobile/15E148 Safari/605.1.15":       "iPhone Firefox",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 18_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) EdgiOS/138.0 Mobile/15E148 Safari/605.1.15":      "iPhone Edge",
		"Mozilla/5.0 (iPad; CPU OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1":                  "iPad Safari",
		"Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Mobile Safari/537.36":                                 "Android Chrome",
		"Mozilla/5.0 (Linux; Android 14; SM-S918B) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/28.0 Chrome/130.0.0.0 Mobile Safari/537.36":      "Android Samsung Internet",
		"Mozilla/5.0 (Android 15; Mobile; rv:141.0) Gecko/141.0 Firefox/141.0":                                                                            "Android Firefox",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36":                                 "Windows Chrome",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36 Edg/138.0.0.0":                   "Windows Edge",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:141.0) Gecko/20100101 Firefox/141.0":                                                                "Windows Firefox",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/137.0.0.0 Safari/537.36 OPR/121.0.0.0":                   "Windows Opera",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.5 Safari/605.1.15":                           "Mac Safari",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36":                           "Mac Chrome",
		"Mozilla/5.0 (X11; Linux x86_64; rv:141.0) Gecko/20100101 Firefox/141.0":                                                                          "Linux Firefox",
		"Mozilla/5.0 (X11; Ubuntu; Linux x86_64; rv:141.0) Gecko/20100101 Firefox/141.0":                                                                  "Linux Firefox",
		"Mozilla/5.0 (X11; CrOS x86_64 14541.0.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36":                                  "ChromeOS Chrome",
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/140.0.7339.16 Safari/537.36":                               "Linux Chrome",
		"curl/8.5.0":         "curl",
		"Wget/1.21.4":        "wget",
		"Go-http-client/1.1": "Unknown device",
		"":                   "Unknown device",
	}
	for ua, want := range cases {
		if got := DeviceLabel(ua); got != want {
			t.Errorf("DeviceLabel(%.60q) = %q, want %q", ua, got, want)
		}
	}
}

func TestSnippetName(t *testing.T) {
	now := time.Date(2026, 10, 7, 14, 30, 12, 0, time.UTC)
	cases := map[string]string{
		"Shopping list\nmilk\neggs":                   "shopping-list.txt",
		"  \n\n  Zakupy na środę!  \n":                "zakupy-na-środę.txt",
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ": "link-youtube.com.txt",
		"http://192.168.1.10:8080/x":                  "link-192.168.1.10.txt",
		"HTTPS://Example.COM/a b":                     "https-example-com-a-b.txt",
		"https://example.com is cool":                 "https-example-com-is-cool.txt",
		"!!!???":                                      "note-20261007-143012.txt",
		"":                                            "note-20261007-143012.txt",
		"CON":                                         "_con.txt",
		"ftp://files.example.com/x":                   "ftp-files-example-com-x.txt",
	}
	for in, want := range cases {
		if got := SnippetName(in, now); got != want {
			t.Errorf("SnippetName(%q) = %q, want %q", in, got, want)
		}
	}
	long := SnippetName(strings.Repeat("word ", 30), now)
	if n := utf8.RuneCountInString(strings.TrimSuffix(long, ".txt")); n > snippetSlugRunes || strings.HasSuffix(strings.TrimSuffix(long, ".txt"), "-") {
		t.Errorf("long snippet name %q has %d runes or a trailing dash", long, n)
	}
}
