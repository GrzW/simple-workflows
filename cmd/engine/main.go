package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"workflow-engine/internal/api"
	"workflow-engine/internal/engine"
	"workflow-engine/internal/storage"
)

const (
	defaultPort        = "8080"
	defaultDBPath      = "data/workflows.db"
	defaultConcurrency = "4"
	defaultLimitStr    = "10"
	defaultOffsetStr   = "0"

	httpShutdownTimeoutSeconds = 5
	httpShutdownTimeout        = httpShutdownTimeoutSeconds * time.Second
)

type config struct {
	port          string // TCP port the HTTP server listens on
	dbPath        string // path to the SQLite database file
	concurrency   int    // number of parallel workflow-execution workers
	defaultLimit  int    // fallback limit for listing workflows if not specified
	defaultOffset int    // fallback offset for listing workflows if not specified
}

func main() {
	initLogger()

	logger := slog.Default().With("component", "startup")
	logger.Info("workflow engine initialising…")

	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		logger.Warn("failed to load .env", "error", err)
	}

	cfg, err := loadConfig()
	if err != nil {
		logger.Error("configuration error", "error", err)
		os.Exit(1)
	}
	logger.Info("config loaded",
		"port", cfg.port,
		"db_path", cfg.dbPath,
		"concurrency", cfg.concurrency,
		"default_limit", cfg.defaultLimit,
		"default_offset", cfg.defaultOffset,
	)

	rootCtx, rootCancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer rootCancel()

	logger.Info("opening SQLite database", "db_path", cfg.dbPath)

	store, err := storage.NewSQLiteStorage(cfg.dbPath)
	if err != nil {
		logger.Error("failed to open storage", "error", err)
		os.Exit(1)
	}
	defer func() {
		shutdownLogger := slog.Default().With("component", "shutdown")
		shutdownLogger.Info("closing storage")
		if err = store.Close(); err != nil {
			shutdownLogger.Error("storage close error", "error", err)
		}
	}()

	logger.Info("storage ready")

	logger.Info("creating engine", "concurrency", cfg.concurrency)

	eng := engine.NewEngine(store, cfg.concurrency)
	eng.Start(rootCtx)

	logger.Info("engine started")

	logger.Info("scanning for incomplete workflows...")

	go func() {
		if recoveryErr := eng.RecoverOutstandingWork(rootCtx); recoveryErr != nil {
			logger.Warn("crash recovery failed", "error", recoveryErr)
		}
	}()

	mux := http.NewServeMux()
	api.NewAPIHandler(store, eng, cfg.defaultLimit, cfg.defaultOffset).RegisterRoutes(mux)

	addr := ":" + cfg.port
	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("HTTP server listening", "addr", addr)
		if listenErr := srv.ListenAndServe(); !errors.Is(listenErr, http.ErrServerClosed) {
			serverErr <- listenErr
		}
		close(serverErr)
	}()

	var shutdownLogger *slog.Logger
	select {
	case <-rootCtx.Done():
		shutdownLogger = slog.Default().With("component", "shutdown")
		shutdownLogger.Info("signal received — beginning graceful shutdown")

	case err = <-serverErr:
		shutdownLogger = slog.Default().With("component", "shutdown")
		shutdownLogger.Error("HTTP server crashed", "error", err)
		os.Exit(1)
	}

	shutdownLogger.Info("stopping HTTP server", "timeout_seconds", httpShutdownTimeoutSeconds)

	shutdownCtx, shutdownCancel := context.WithTimeout(
		context.Background(),
		httpShutdownTimeout,
	)
	defer shutdownCancel()

	if err = srv.Shutdown(shutdownCtx); err != nil {
		shutdownLogger.Error("HTTP server shutdown error", "error", err)
	}

	shutdownLogger.Info("stopping engine workers...")
	eng.Stop()
	shutdownLogger.Info("engine stopped")

	shutdownLogger.Info("workflow engine exited cleanly")
}

func initLogger() {
	logLevelStr := envOr("LOG_LEVEL", "info")
	logFormatStr := envOr("LOG_FORMAT", "json")

	var level slog.Level
	switch strings.ToLower(logLevelStr) {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{
		Level: level,
	}

	var handler slog.Handler
	if strings.ToLower(logFormatStr) == "text" {
		handler = slog.NewTextHandler(os.Stderr, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	}

	slog.SetDefault(slog.New(handler))
}

func loadConfig() (config, error) {
	port := envOr("PORT", defaultPort)
	dbPath := envOr("DB_PATH", defaultDBPath)

	concurrencyStr := envOr("CONCURRENCY", defaultConcurrency)
	concurrency, err := strconv.Atoi(concurrencyStr)
	if err != nil || concurrency < 1 {
		return config{}, fmt.Errorf("invalid CONCURRENCY value %q: must be a positive integer", concurrencyStr)
	}

	defaultLimitStrVal := envOr("DEFAULT_LIMIT", defaultLimitStr)
	defaultLimit, err := strconv.Atoi(defaultLimitStrVal)
	if err != nil || defaultLimit < 0 {
		return config{}, fmt.Errorf("invalid DEFAULT_LIMIT value %q: must be a positive integer", defaultLimitStrVal)
	}

	defaultOffsetStrVal := envOr("DEFAULT_OFFSET", defaultOffsetStr)
	defaultOffset, err := strconv.Atoi(defaultOffsetStrVal)
	if err != nil || defaultOffset < 0 {
		return config{}, fmt.Errorf("invalid DEFAULT_OFFSET value %q: must be a positive integer", defaultOffsetStrVal)
	}

	return config{
		port:          port,
		dbPath:        dbPath,
		concurrency:   concurrency,
		defaultLimit:  defaultLimit,
		defaultOffset: defaultOffset,
	}, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return fallback
}
