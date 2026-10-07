package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
)

// Config is the runtime configuration. Every field can be set by a flag or,
// as a fallback, by the matching UPED_* environment variable. Flags win.
type Config struct {
	Listen      string        // address to listen on, e.g. ":8080"
	DataDir     string        // root of all stored data
	TTL         time.Duration // items expire this long after upload; 0 disables expiry
	MinFree     int64         // refuse uploads that would leave less free disk than this (bytes)
	MaxFileSize int64         // largest accepted file in bytes; 0 means unlimited
	ChunkSize   int64         // upload chunk size advertised to clients (bytes)
}

const (
	minChunkSize = 64 << 10 // 64 KiB
	maxChunkSize = 1 << 30  // 1 GiB
)

func defaultConfig() Config {
	return Config{
		Listen:      ":8080",
		DataDir:     "/var/lib/uped",
		TTL:         168 * time.Hour,
		MinFree:     1 << 30,
		MaxFileSize: 0,
		ChunkSize:   16 << 20,
	}
}

// usageError marks a command-line error the flag package has already printed.
type usageError struct{ error }

func (e usageError) Unwrap() error { return e.error }

// parseConfig builds a Config from defaults, then the environment (via
// getenv), then args. It returns showVersion=true when --version was given.
// Usage text and flag errors are written to out.
func parseConfig(args []string, getenv func(string) string, out io.Writer) (cfg Config, showVersion bool, err error) {
	cfg = defaultConfig()

	fs := flag.NewFlagSet("uped", flag.ContinueOnError)
	fs.SetOutput(out)

	type envFlag struct {
		name, env, usage string
		value            flag.Value
	}
	flags := []envFlag{
		{"listen", "UPED_LISTEN", "`ADDR` to listen on, e.g. :8080 or 192.168.1.50:8080", (*stringValue)(&cfg.Listen)},
		{"data", "UPED_DATA_DIR", "`DIR` where uploads are stored", (*stringValue)(&cfg.DataDir)},
		{"ttl", "UPED_TTL", "delete items this `DURATION` after upload, e.g. 168h or 7d; 0 disables expiry", (*durationValue)(&cfg.TTL)},
		{"min-free", "UPED_MIN_FREE", "refuse uploads that would leave less than this `SIZE` of free disk, e.g. 1G", (*sizeValue)(&cfg.MinFree)},
		{"max-file-size", "UPED_MAX_FILE_SIZE", "largest accepted file `SIZE`, e.g. 20G; 0 means unlimited", (*sizeValue)(&cfg.MaxFileSize)},
		{"chunk-size", "UPED_CHUNK_SIZE", "upload chunk `SIZE` sent to browsers, e.g. 16M", (*sizeValue)(&cfg.ChunkSize)},
	}
	for _, f := range flags {
		if v := getenv(f.env); v != "" {
			if err := f.value.Set(v); err != nil {
				return Config{}, false, fmt.Errorf("%s=%q: %w", f.env, v, err)
			}
		}
		fs.Var(f.value, f.name, fmt.Sprintf("%s (env %s)", f.usage, f.env))
	}
	fs.BoolVar(&showVersion, "version", false, "print the version and exit")

	fs.Usage = func() {
		fmt.Fprintf(out, "Usage: uped [flags]\n\nuped serves a LAN file drop page. Flags override UPED_* environment variables.\n\nFlags:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return Config{}, false, err
		}
		return Config{}, false, usageError{err}
	}
	if fs.NArg() > 0 {
		return Config{}, false, fmt.Errorf("unexpected argument %q (all options are flags, see --help)", fs.Arg(0))
	}
	if showVersion {
		return cfg, true, nil
	}
	if err := cfg.validate(); err != nil {
		return Config{}, false, err
	}
	return cfg, false, nil
}

func (c Config) validate() error {
	switch {
	case strings.TrimSpace(c.Listen) == "":
		return errors.New("listen address must not be empty")
	case strings.TrimSpace(c.DataDir) == "":
		return errors.New("data directory must not be empty")
	case c.TTL < 0:
		return errors.New("ttl must not be negative")
	case c.ChunkSize < minChunkSize || c.ChunkSize > maxChunkSize:
		return fmt.Errorf("chunk size must be between %s and %s", formatSize(minChunkSize), formatSize(maxChunkSize))
	case c.MaxFileSize < 0 || c.MinFree < 0:
		return errors.New("sizes must not be negative")
	}
	return nil
}

// parseSize parses a byte count such as "0", "1048576", "512K", "16M",
// "16MiB", "1G" or "2TB". Suffixes are case-insensitive binary multiples
// (K = 1024). Fractions and negative numbers are rejected.
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty size")
	}
	upper := strings.ToUpper(s)
	upper = strings.TrimSuffix(upper, "IB")
	upper = strings.TrimSuffix(upper, "B")
	mult := int64(1)
	if n := len(upper); n > 0 {
		switch upper[n-1] {
		case 'K':
			mult = 1 << 10
		case 'M':
			mult = 1 << 20
		case 'G':
			mult = 1 << 30
		case 'T':
			mult = 1 << 40
		}
		if mult != 1 {
			upper = upper[:n-1]
		}
	}
	n, err := strconv.ParseInt(strings.TrimSpace(upper), 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid size %q (use a whole number with an optional K, M, G or T suffix, e.g. 512M)", s)
	}
	if n > math.MaxInt64/mult {
		return 0, fmt.Errorf("size %q is too large", s)
	}
	return n * mult, nil
}

// formatSize renders n with the largest binary suffix that divides it exactly.
func formatSize(n int64) string {
	for _, u := range []struct {
		suffix string
		mult   int64
	}{{"T", 1 << 40}, {"G", 1 << 30}, {"M", 1 << 20}, {"K", 1 << 10}} {
		if n != 0 && n%u.mult == 0 {
			return strconv.FormatInt(n/u.mult, 10) + u.suffix
		}
	}
	return strconv.FormatInt(n, 10)
}

// parseTTL accepts Go durations ("168h", "90m", "0") plus whole days ("7d").
func parseTTL(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.ParseInt(days, 10, 64)
		if err != nil || n < 0 || n > math.MaxInt64/int64(24*time.Hour) {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q (examples: 168h, 7d, 0)", s)
	}
	if d < 0 {
		return 0, fmt.Errorf("duration %q must not be negative", s)
	}
	return d, nil
}

// formatTTL renders whole days as "7d" and anything else as a Go duration.
func formatTTL(d time.Duration) string {
	if d > 0 && d%(24*time.Hour) == 0 {
		return strconv.FormatInt(int64(d/(24*time.Hour)), 10) + "d"
	}
	return d.String()
}

type stringValue string

func (v *stringValue) String() string     { return string(*v) }
func (v *stringValue) Set(s string) error { *v = stringValue(s); return nil }

type sizeValue int64

func (v *sizeValue) String() string { return formatSize(int64(*v)) }
func (v *sizeValue) Set(s string) error {
	n, err := parseSize(s)
	if err != nil {
		return err
	}
	*v = sizeValue(n)
	return nil
}

type durationValue time.Duration

func (v *durationValue) String() string { return formatTTL(time.Duration(*v)) }
func (v *durationValue) Set(s string) error {
	d, err := parseTTL(s)
	if err != nil {
		return err
	}
	*v = durationValue(d)
	return nil
}
