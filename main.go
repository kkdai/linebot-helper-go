package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kkdai/linebot-helper-go/internal/agents"
	"github.com/kkdai/linebot-helper-go/internal/config"
	"github.com/kkdai/linebot-helper-go/internal/firestore"
	"github.com/kkdai/linebot-helper-go/internal/gemini"
	"github.com/kkdai/linebot-helper-go/internal/handlers"
	"github.com/kkdai/linebot-helper-go/internal/line"
	"github.com/kkdai/linebot-helper-go/internal/loader"
	"github.com/kkdai/linebot-helper-go/internal/services"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := config.LoadFromEnv()
	if err != nil {
		slog.Error("Configuration error", "error", err)
		os.Exit(1)
	}

	if cfg.FirecrawlKey != "" {
		slog.Info("Firecrawl API key detected - will use for PTT, Medium, and OpenAI URLs")
	} else {
		slog.Info("No Firecrawl API key - using standard web scraping methods for all sites")
	}
	if cfg.LiffID == "" {
		slog.Warn("LIFF_ID env var not set — /liff/ will serve with unsubstituted placeholder")
	}
	slog.Info("Vertex AI configured", "project", cfg.GoogleCloudProject, "location", cfg.GoogleCloudLocation)

	geminiClient := gemini.NewClient(cfg.GoogleCloudProject, cfg.GoogleCloudLocation, cfg.GoogleAIAPIKey)
	lineClient := line.NewClient(cfg.ChannelAccessToken)

	sessionStore := firestore.NewFirestoreKVStore(cfg.GoogleCloudProject, services.SessionCollection)
	bookmarkStore := firestore.NewFirestoreKVStore(cfg.GoogleCloudProject, services.BookmarksCollection)
	reportKV := firestore.NewFirestoreKVStore(cfg.GoogleCloudProject, services.ReportsCollection)
	batchKV := firestore.NewFirestoreKVStore(cfg.GoogleCloudProject, services.BatchJobsCollection)

	sm := services.NewSessionManager(cfg.SessionTimeoutMinutes, cfg.MaxHistoryLength, 300, sessionStore)
	bs := services.NewBookmarkService(bookmarkStore)
	rs := services.NewReportStore(reportKV)
	batchSvc := services.NewBatchService(cfg.GoogleAIAPIKey, batchKV, services.DefaultMappingFile)
	urlLoader := loader.NewLoader(cfg.FirecrawlKey, cfg.SingleFilePath)
	orch := agents.NewOrchestrator(cfg, geminiClient, sm, urlLoader)

	appServer := handlers.NewServer(cfg, geminiClient, lineClient, urlLoader, sm, bs, rs, batchSvc, orch)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	sm.StartCleanupTask(ctx)
	appServer.StartBatchPollingLoop(ctx)

	addr := ":" + cfg.Port
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           appServer.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()

	slog.Info("Starting server", "addr", addr)
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("Server exited", "error", err)
		os.Exit(1)
	}
}
