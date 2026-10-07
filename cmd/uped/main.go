// Command uped serves a LAN file drop: any device on the network opens the
// page, uploads files or folders, and any other device downloads them.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/stasnowak/Uped/internal/server"
	"github.com/stasnowak/Uped/web"
)

// version is set at build time with -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() {
	cfg, showVersion, err := parseConfig(os.Args[1:], os.Getenv, os.Stderr)
	var ue usageError
	switch {
	case errors.Is(err, flag.ErrHelp):
		return
	case errors.As(err, &ue):
		os.Exit(2) // the flag package already printed the problem and usage
	case err != nil:
		fmt.Fprintln(os.Stderr, "uped:", err)
		os.Exit(2)
	}
	if showVersion {
		fmt.Println("uped", version)
		return
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, cfg, logger); err != nil {
		logger.Error("uped stopped", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg Config, log *slog.Logger) error {
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return fmt.Errorf("data directory: %w", err)
	}

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}

	srv := &http.Server{
		Handler: server.New(server.Options{Version: version, Static: web.FS}),
		// Only the header read is bounded. ReadTimeout and WriteTimeout stay
		// zero because they cover the whole body: any value would cut off
		// multi-GB uploads, zip downloads and the live event stream.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	log.Info("uped starting",
		"version", version,
		"listen", ln.Addr().String(),
		"data", cfg.DataDir,
		"ttl", formatTTL(cfg.TTL),
		"min_free", formatSize(cfg.MinFree),
		"max_file_size", formatSize(cfg.MaxFileSize),
		"chunk_size", formatSize(cfg.ChunkSize),
	)

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}
