package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"cineroute/internal/config"
	"cineroute/internal/prowlarr"
	"cineroute/internal/qbittorrent"
	"cineroute/internal/tmdb"
	"cineroute/internal/web"
)

func main() {
	var cfgPath string
	flag.StringVar(&cfgPath, "config", "", "path to config file (default: $CINEROUTE_CONFIG or ./config.yaml)")
	flag.Parse()

	if cfgPath == "" {
		cfgPath = os.Getenv("CINEROUTE_CONFIG")
	}
	if cfgPath == "" {
		cfgPath = "config.yaml"
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		slog.Error("configuration", "err", err)
		os.Exit(1)
	}
	if cfg.TMDB.APIKey == "" {
		slog.Warn("TMDB API key is not configured; TMDB search will be unavailable")
	}
	if cfg.AuthPassword == "" {
		slog.Warn("no auth password configured; the web interface is unauthenticated")
	}
	if cfg.Companion.Enabled && cfg.Prowlarr.APIKey == "" {
		slog.Warn("1080p companion search is enabled but Prowlarr is not configured")
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
	if cfg.Companion.Enabled {
		prowlarrClient = prowlarr.New(cfg.Prowlarr.URL, cfg.Prowlarr.APIKey, 30*time.Second, cfg.MaxUploadBytes)
	}

	srv := web.New(cfg, qb, tmdbClient, prowlarrClient)
	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		slog.Info("cineroute listening", "addr", cfg.Listen)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http server", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	slog.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
}
