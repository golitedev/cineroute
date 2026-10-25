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
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"cineroute/internal/config"
	"cineroute/internal/prowlarr"
	"cineroute/internal/qbittorrent"
	"cineroute/internal/tmdb"
	"cineroute/internal/web"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "hash-password" {
		hash, err := web.HashPassword(os.Stdin)
		if err != nil {
			slog.Error("hash password", "err", err)
			os.Exit(1)
		}
		fmt.Println(hash)
		return
	}
	var cfgPath string
	var logLevel string
	flag.StringVar(&cfgPath, "config", "", "path to config file (default: $CINEROUTE_CONFIG or ./config.yaml)")
	flag.StringVar(&logLevel, "log-level", "", "log level: debug, info, warn or error (default: $CINEROUTE_LOG_LEVEL or info)")
	flag.Parse()

	if logLevel == "" {
		logLevel = os.Getenv("CINEROUTE_LOG_LEVEL")
	}
	applyLogLevel(logLevel)

	if cfgPath == "" {
		cfgPath = os.Getenv("CINEROUTE_CONFIG")
	}
	if cfgPath == "" {
		cfgPath = "config.yaml"
	}
	slog.Info("cineroute starting", "config", cfgPath, "log_level", effectiveLogLevel())
	cfg, err := config.Load(cfgPath)
	if err != nil {
		slog.Error("configuration", "err", err)
		os.Exit(1)
	}
	if err := cfg.ValidateServing(); err != nil {
		slog.Error("HTTP security configuration", "err", err)
		os.Exit(1)
	}
	if cfg.TMDB.APIKey == "" {
		slog.Warn("TMDB API key is not configured; TMDB search will be unavailable")
	}
	if cfg.Subtitles.Enabled {
		if cfg.Subtitles.OpenSubtitles.APIKey == "" {
			slog.Warn("subtitles are enabled but OpenSubtitles is not configured; set subtitles.opensubtitles.api_key or CINEROUTE_OS_API_KEY")
		}
		for _, tool := range []string{cfg.Subtitles.FFmpegPath, cfg.Subtitles.FFprobePath, cfg.Subtitles.AlassPath} {
			if strings.TrimSpace(tool) == "" {
				continue
			}
			if _, err := exec.LookPath(tool); err != nil {
				slog.Warn("subtitle tool not found", "tool", tool, "err", err)
			}
		}
	}

	qb, err := qbittorrent.New(cfg.QBittorrent.URL, cfg.QBittorrent.Username, cfg.QBittorrent.Password, 20*time.Second)
	if err != nil {
		slog.Error("qBittorrent client", "err", err)
		os.Exit(1)
	}
	var tmdbClient *tmdb.Client
	if cfg.TMDB.APIKey != "" {
		tmdbClient = tmdb.New(cfg.TMDB.APIKey, cfg.TMDB.Language, 15*time.Second)
	}
	var prowlarrClient *prowlarr.Client
	if cfg.Prowlarr.APIKey != "" {
		prowlarrClient = prowlarr.New(cfg.Prowlarr.URL, cfg.Prowlarr.APIKey, 30*time.Second, cfg.MaxUploadBytes)
	}

	srv := web.New(cfg, qb, tmdbClient, prowlarrClient)
	if err := serve(srv, cfg); err != nil {
		slog.Error("HTTP service", "err", err)
		os.Exit(1)
	}
}

// Bind every configured socket before starting workers or serving either UI.
// A failed public bind must not leave a partially started admin service.
func serve(srv *web.Server, cfg *config.Config) error {
	type listener struct {
		server *http.Server
		socket net.Listener
		name   string
	}
	listeners := []listener{}
	defer func() {
		for _, entry := range listeners {
			entry.socket.Close()
		}
	}()
	for _, entry := range []struct {
		address string
		handler http.Handler
		name    string
	}{
		{cfg.Listen, srv.Handler(), "admin"},
		{cfg.PublicListen, srv.PublicHandler(), "public"},
	} {
		if entry.address == "" {
			continue
		}
		socket, err := net.Listen("tcp", entry.address)
		if err != nil {
			return fmt.Errorf("bind %s listener: %w", entry.name, err)
		}
		httpSrv := &http.Server{Addr: entry.address, Handler: entry.handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 2 * time.Minute, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
		if entry.name == "public" {
			httpSrv.ReadTimeout = 20 * time.Second
			httpSrv.WriteTimeout = 45 * time.Second
		}
		listeners = append(listeners, listener{httpSrv, socket, entry.name})
	}
	if err := srv.Start(context.Background()); err != nil {
		return err
	}
	defer srv.Close()
	errorsCh := make(chan error, len(listeners))
	for _, entry := range listeners {
		slog.Info("cineroute listening", "interface", entry.name, "addr", entry.server.Addr)
		go func() {
			if err := entry.server.Serve(entry.socket); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errorsCh <- fmt.Errorf("%s listener: %w", entry.name, err)
			}
		}()
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(stop)
	var result error
	select {
	case <-stop:
	case result = <-errorsCh:
	}
	slog.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, entry := range listeners {
		if err := entry.server.Shutdown(ctx); err != nil {
			entry.server.Close()
		}
	}
	return result
}

// applyLogLevel sets the level of the default slog logger, which keeps the
// standard `2026/01/02 15:04:05 INFO message key=value` format while making
// debug logging available for troubleshooting.
func applyLogLevel(level string) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "", "info":
		slog.SetLogLoggerLevel(slog.LevelInfo)
	case "debug":
		slog.SetLogLoggerLevel(slog.LevelDebug)
	case "warn", "warning":
		slog.SetLogLoggerLevel(slog.LevelWarn)
	case "error":
		slog.SetLogLoggerLevel(slog.LevelError)
	default:
		slog.SetLogLoggerLevel(slog.LevelInfo)
		slog.Warn("unknown log level, using info", "level", level)
	}
}

// effectiveLogLevel reports the level in effect, for the startup log line. It
// returns the most verbose level the default logger accepts.
func effectiveLogLevel() string {
	for _, candidate := range []struct {
		name  string
		level slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"error", slog.LevelError},
	} {
		if slog.Default().Enabled(context.Background(), candidate.level) {
			return candidate.name
		}
	}
	return "off"
}
