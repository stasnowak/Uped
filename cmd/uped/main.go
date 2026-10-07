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
	"strconv"
	"syscall"
	"time"

	"github.com/stasnowak/Uped/internal/server"
	"github.com/stasnowak/Uped/internal/store"
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
	st, err := store.Open(cfg.DataDir, store.Options{
		TTL:         cfg.TTL,
		MinFree:     cfg.MinFree,
		MaxFileSize: cfg.MaxFileSize,
		Logger:      log,
	})
	if err != nil {
		return err
	}
	defer func() {
		if err := st.Close(); err != nil {
			log.Error("closing store", "err", err)
		}
	}()

	handler, err := server.New(server.Options{
		Version:   version,
		Static:    web.FS,
		Store:     st,
		ChunkSize: cfg.ChunkSize,
		Logger:    log,
	})
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}

	srv := &http.Server{
		Handler: handler,
		// Only the header read is bounded. ReadTimeout and WriteTimeout stay
		// zero because they cover the whole body: any value would cut off
		// multi-GB uploads, zip downloads and the live event stream. Stalled
		// transfers are cut by per-request idle deadlines in the handlers.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	srv.RegisterOnShutdown(handler.Close) // end live event streams

	log.Info("uped starting",
		"version", version,
		"listen", ln.Addr().String(),
		"data", cfg.DataDir,
		"ttl", formatTTL(cfg.TTL),
		"min_free", formatSize(cfg.MinFree),
		"max_file_size", formatSize(cfg.MaxFileSize),
		"chunk_size", formatSize(cfg.ChunkSize),
	)
	for _, u := range reachableURLs(ln.Addr()) {
		log.Info("open this on any device on your network", "url", u)
	}

	sweepCtx, stopSweeper := context.WithCancel(ctx)
	defer stopSweeper()
	go st.RunSweeper(sweepCtx, 15*time.Minute)

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

// reachableURLs lists the URLs other devices can use. For a wildcard
// listen address that means one per non-loopback IPv4 address, private
// (LAN) addresses first.
func reachableURLs(addr net.Addr) []string {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok {
		return nil
	}
	port := strconv.Itoa(tcp.Port)
	if !tcp.IP.IsUnspecified() {
		return []string{"http://" + net.JoinHostPort(tcp.IP.String(), port) + "/"}
	}
	var private, public []string
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.To4() == nil || ipn.IP.IsLoopback() || ipn.IP.IsLinkLocalUnicast() {
				continue
			}
			u := "http://" + net.JoinHostPort(ipn.IP.String(), port) + "/"
			if ipn.IP.IsPrivate() {
				private = append(private, u)
			} else {
				public = append(public, u)
			}
		}
	}
	urls := append(private, public...)
	if len(urls) == 0 {
		urls = []string{"http://localhost:" + port + "/"}
	}
	return urls
}
