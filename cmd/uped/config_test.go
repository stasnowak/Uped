package main

import (
	"bytes"
	"errors"
	"flag"
	"net"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDefaults(t *testing.T) {
	cfg, showVersion, err := parseConfig(nil, env(nil), &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if showVersion {
		t.Fatal("showVersion = true without --version")
	}
	if cfg != defaultConfig() {
		t.Fatalf("cfg = %+v, want defaults %+v", cfg, defaultConfig())
	}
	want := Config{Listen: ":8080", DataDir: "/var/lib/uped", TTL: 168 * time.Hour, MinFree: 1 << 30, ChunkSize: 16 << 20}
	if cfg != want {
		t.Fatalf("defaults = %+v, want %+v", cfg, want)
	}
}

func TestEnvThenFlagsPrecedence(t *testing.T) {
	e := env(map[string]string{
		"UPED_LISTEN":        "127.0.0.1:9000",
		"UPED_DATA_DIR":      "/srv/drop",
		"UPED_TTL":           "2d",
		"UPED_MIN_FREE":      "512M",
		"UPED_MAX_FILE_SIZE": "20G",
		"UPED_CHUNK_SIZE":    "8MiB",
	})
	cfg, _, err := parseConfig(nil, e, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	want := Config{Listen: "127.0.0.1:9000", DataDir: "/srv/drop", TTL: 48 * time.Hour, MinFree: 512 << 20, MaxFileSize: 20 << 30, ChunkSize: 8 << 20}
	if cfg != want {
		t.Fatalf("from env = %+v, want %+v", cfg, want)
	}

	cfg, _, err = parseConfig([]string{"--listen", ":7000", "--ttl", "0", "--chunk-size=4M"}, e, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	want.Listen, want.TTL, want.ChunkSize = ":7000", 0, 4<<20
	if cfg != want {
		t.Fatalf("flags over env = %+v, want %+v", cfg, want)
	}
}

func TestVersionFlag(t *testing.T) {
	_, showVersion, err := parseConfig([]string{"--version"}, env(nil), &bytes.Buffer{})
	if err != nil || !showVersion {
		t.Fatalf("--version: showVersion=%v err=%v", showVersion, err)
	}
}

func TestHelpFlag(t *testing.T) {
	var out bytes.Buffer
	_, _, err := parseConfig([]string{"--help"}, env(nil), &out)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("--help err = %v, want flag.ErrHelp", err)
	}
	for _, s := range []string{"UPED_LISTEN", "UPED_DATA_DIR", "UPED_TTL", "UPED_MIN_FREE", "UPED_MAX_FILE_SIZE", "UPED_CHUNK_SIZE", "-version"} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("usage text lacks %q:\n%s", s, out.String())
		}
	}
}

func TestErrors(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		env     map[string]string
		wantSub string
		usage   bool // reported by the flag package already
	}{
		{name: "bad env size", env: map[string]string{"UPED_MIN_FREE": "lots"}, wantSub: "UPED_MIN_FREE"},
		{name: "bad env ttl", env: map[string]string{"UPED_TTL": "1 week"}, wantSub: "UPED_TTL"},
		{name: "bad flag size", args: []string{"--max-file-size", "1.5G"}, usage: true},
		{name: "unknown flag", args: []string{"--nope"}, usage: true},
		{name: "positional arg", args: []string{"/srv/drop"}, wantSub: "unexpected argument"},
		{name: "chunk too small", args: []string{"--chunk-size", "1K"}, wantSub: "chunk size"},
		{name: "chunk too big", args: []string{"--chunk-size", "2G"}, wantSub: "chunk size"},
		{name: "empty listen", args: []string{"--listen", ""}, wantSub: "listen"},
		{name: "empty data", args: []string{"--data", " "}, wantSub: "data directory"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := parseConfig(c.args, env(c.env), &bytes.Buffer{})
			if err == nil {
				t.Fatal("no error")
			}
			var ue usageError
			if got := errors.As(err, &ue); got != c.usage {
				t.Fatalf("usageError = %v, want %v (err %v)", got, c.usage, err)
			}
			if !strings.Contains(err.Error(), c.wantSub) {
				t.Fatalf("err %q does not mention %q", err, c.wantSub)
			}
		})
	}
}

func TestParseSize(t *testing.T) {
	ok := map[string]int64{
		"0": 0, "1024": 1024, "512K": 512 << 10, "16M": 16 << 20, "16m": 16 << 20,
		"16MB": 16 << 20, "16MiB": 16 << 20, "16mib": 16 << 20, " 1G ": 1 << 30,
		"2T": 2 << 40, "100B": 100, "8 M": 8 << 20,
	}
	for in, want := range ok {
		got, err := parseSize(in)
		if err != nil || got != want {
			t.Errorf("parseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "abc", "-1", "1.5G", "K", "MiB", "1X", "0x10", "9999999T", "1BB"} {
		if got, err := parseSize(in); err == nil {
			t.Errorf("parseSize(%q) = %d, want error", in, got)
		}
	}
}

func TestFormatSize(t *testing.T) {
	cases := map[int64]string{0: "0", 1000: "1000", 1024: "1K", 16 << 20: "16M", 1 << 30: "1G", 3 << 40: "3T", 1536: "1536"}
	for in, want := range cases {
		if got := formatSize(in); got != want {
			t.Errorf("formatSize(%d) = %q, want %q", in, got, want)
		}
	}
	for _, n := range []int64{0, 1, 1024, 1536, 16 << 20, 5 << 40} {
		back, err := parseSize(formatSize(n))
		if err != nil || back != n {
			t.Errorf("round trip %d -> %q -> %d, %v", n, formatSize(n), back, err)
		}
	}
}

func TestParseTTL(t *testing.T) {
	ok := map[string]time.Duration{"168h": 168 * time.Hour, "7d": 7 * 24 * time.Hour, "0": 0, "0d": 0, "90m": 90 * time.Minute, "1h30m": 90 * time.Minute}
	for in, want := range ok {
		got, err := parseTTL(in)
		if err != nil || got != want {
			t.Errorf("parseTTL(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "d", "-1h", "-2d", "1w", "7days", "1.5d", "999999999999d"} {
		if got, err := parseTTL(in); err == nil {
			t.Errorf("parseTTL(%q) = %v, want error", in, got)
		}
	}
}

func TestFormatTTL(t *testing.T) {
	cases := map[time.Duration]string{0: "0s", 168 * time.Hour: "7d", 24 * time.Hour: "1d", 90 * time.Minute: "1h30m0s", 36 * time.Hour: "36h0m0s"}
	for in, want := range cases {
		if got := formatTTL(in); got != want {
			t.Errorf("formatTTL(%v) = %q, want %q", in, got, want)
		}
		if back, err := parseTTL(formatTTL(in)); err != nil || back != in {
			t.Errorf("round trip %v -> %q -> %v, %v", in, formatTTL(in), back, err)
		}
	}
}

func TestReachableURLs(t *testing.T) {
	specific := &net.TCPAddr{IP: net.ParseIP("192.168.1.50"), Port: 8080}
	if got := reachableURLs(specific); len(got) != 1 || got[0] != "http://192.168.1.50:8080/" {
		t.Errorf("specific address -> %q", got)
	}
	wildcard := &net.TCPAddr{IP: net.IPv6unspecified, Port: 8080}
	got := reachableURLs(wildcard)
	if len(got) == 0 {
		t.Fatal("wildcard address gave no URLs")
	}
	for _, u := range got {
		if !strings.HasPrefix(u, "http://") || !strings.HasSuffix(u, ":8080/") || strings.Contains(u, "127.0.0.1") {
			t.Errorf("bad URL %q", u)
		}
	}
	if reachableURLs(&net.UDPAddr{}) != nil {
		t.Error("non-TCP address gave URLs")
	}
}
