package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/kkdai/linebot-helper-go/internal/ai"
	"github.com/kkdai/linebot-helper-go/internal/firestore"
	"github.com/kkdai/linebot-helper-go/internal/gemini"
	"github.com/kkdai/linebot-helper-go/internal/line"
	"github.com/kkdai/linebot-helper-go/internal/services"
)

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func parseLogLevel(raw string) slog.Level {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "DEBUG":
		return slog.LevelDebug
	case "WARN", "WARNING":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func main() {
	level := parseLogLevel(os.Getenv("LOG_LEVEL"))
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	slog.Info("Starting weekly GitHub Trending GCP Batch job")

	projectID := firstNonEmpty(os.Getenv("GOOGLE_CLOUD_PROJECT"), os.Getenv("PROJECT_ID"))
	location := firstNonEmpty(os.Getenv("GOOGLE_CLOUD_LOCATION"), os.Getenv("REGION"), "global")
	apiKey := firstNonEmpty(os.Getenv("GEMINI_API_KEY"), os.Getenv("GOOGLE_AI_API_KEY"))
	lineToken := firstNonEmpty(os.Getenv("ChannelAccessToken"), os.Getenv("LINE_CHANNEL_ACCESS_TOKEN"))
	lineUserID := firstNonEmpty(os.Getenv("LINE_USER_ID"))
	githubToken := firstNonEmpty(os.Getenv("GITHUB_TOKEN"))
	outputBucket := firstNonEmpty(os.Getenv("OUTPUT_BUCKET"), os.Getenv("GCS_OUTPUT_BUCKET"))
	timeRange := firstNonEmpty(os.Getenv("TREND_TIME_RANGE"), "weekly")
	modelName := firstNonEmpty(os.Getenv("TRENDS_MODEL"), ai.DefaultTrendsModel)
	appBaseURL := firstNonEmpty(os.Getenv("APP_BASE_URL"), os.Getenv("WEBHOOK_DOMAIN"))
	webhookURL := firstNonEmpty(os.Getenv("NOTIFICATION_WEBHOOK_URL"))

	if projectID == "" && apiKey == "" {
		slog.Error("Configuration error: either GOOGLE_CLOUD_PROJECT (Vertex AI) or GEMINI_API_KEY (Developer API) is required")
		os.Exit(1)
	}
	if lineToken == "" || lineUserID == "" {
		slog.Error("Configuration error: ChannelAccessToken (or LINE_CHANNEL_ACCESS_TOKEN) and LINE_USER_ID are required for LINE Flex Message push")
		os.Exit(1)
	}

	geminiClient := gemini.NewClient(projectID, location, apiKey)
	lineClient := line.NewClient(lineToken)

	var reportStore *services.ReportStore
	var archiveStore firestore.KVStore
	if projectID != "" {
		reportKV := firestore.NewFirestoreKVStore(projectID, services.ReportsCollection)
		reportStore = services.NewReportStore(reportKV)
		archiveStore = firestore.NewFirestoreKVStore(projectID, services.TrendsArchiveCollection)
	}

	runner := services.NewTrendsBatchRunner(
		services.TrendsBatchConfig{
			TimeRange:              timeRange,
			Limit:                  10,
			Model:                  modelName,
			GitHubToken:            githubToken,
			LineUserID:             lineUserID,
			AppBaseURL:             appBaseURL,
			OutputBucket:           outputBucket,
			NotificationWebhookURL: webhookURL,
		},
		geminiClient,
		lineClient,
		reportStore,
		archiveStore,
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Align with GCP Batch maxRunDuration (900s = 15m) with a 14-minute timeout
	runCtx, cancel := context.WithTimeout(ctx, 14*time.Minute)
	defer cancel()

	res, err := runner.Run(runCtx)
	if err != nil {
		slog.Error("Weekly GitHub Trending batch job failed", "error", err)
		os.Exit(1)
	}

	slog.Info("Weekly GitHub Trending batch job completed successfully",
		"date", res.Report.Date,
		"items_count", len(res.Report.Items),
		"report_id", res.ReportID,
		"report_url", res.ReportURL,
	)
}
