package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kkdai/linebot-helper-go/internal/ai"
	"github.com/kkdai/linebot-helper-go/internal/firestore"
	"github.com/kkdai/linebot-helper-go/internal/gemini"
	"github.com/kkdai/linebot-helper-go/internal/line"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	TrendsArchiveCollection = "github_trends_archive"
	DefaultGCSBaseURL       = "https://storage.googleapis.com"
)

// TrendsBatchConfig configures the weekly GitHub Trending GCP Batch runner.
type TrendsBatchConfig struct {
	TimeRange              string // default: "weekly"
	Limit                  int    // default: 10
	Model                  string // default: "gemini-2.5-flash"
	GitHubToken            string
	LineUserID             string
	AppBaseURL             string // e.g., https://linebot-helper-go-xxx.run.app for /reports/{id}
	OutputBucket           string // optional GCS bucket name for archiving reports and JSON
	NotificationWebhookURL string // optional webhook URL (Slack/Discord/generic)
	TrendingBaseURL        string // optional override for testing
	GitHubAPIBaseURL       string // optional override for testing
	GCSBaseURL             string // optional override for testing
}

// TrendsBatchResult summarizes the output of a TrendsBatchRunner execution.
type TrendsBatchResult struct {
	Report       *ai.TrendsReport
	ReportID     string
	ReportURL    string
	FlexCarousel map[string]any
}

// TrendsBatchRunner executes the 5-stage GitHub Trending batch pipeline.
type TrendsBatchRunner struct {
	Config       TrendsBatchConfig
	GeminiClient *gemini.Client
	LineClient   *line.Client
	ReportStore  *ReportStore
	ArchiveStore firestore.KVStore
	HTTPClient   *http.Client
	TokenSource  oauth2.TokenSource
}

// NewTrendsBatchRunner creates a new TrendsBatchRunner.
func NewTrendsBatchRunner(
	cfg TrendsBatchConfig,
	geminiClient *gemini.Client,
	lineClient *line.Client,
	reportStore *ReportStore,
	archiveStore firestore.KVStore,
) *TrendsBatchRunner {
	if cfg.TimeRange == "" {
		cfg.TimeRange = "weekly"
	}
	if cfg.Limit <= 0 {
		cfg.Limit = 10
	}
	if cfg.Model == "" {
		cfg.Model = ai.DefaultTrendsModel
	}
	if cfg.GCSBaseURL == "" {
		cfg.GCSBaseURL = DefaultGCSBaseURL
	}
	return &TrendsBatchRunner{
		Config:       cfg,
		GeminiClient: geminiClient,
		LineClient:   lineClient,
		ReportStore:  reportStore,
		ArchiveStore: archiveStore,
		HTTPClient:   &http.Client{Timeout: 60 * time.Second},
	}
}

// Run executes the 5-stage weekly GitHub Trending pipeline end-to-end:
//  1. Ingestion & GitHub REST API Enrichment
//  2. Delta Analysis against previous week's archive
//  3. LLM Semantic Analysis & Insight Generation
//  4. Markdown & HTML Report Compilation
//  5. Archival (GCS / Firestore) & LINE Flex Message Delivery
func (r *TrendsBatchRunner) Run(ctx context.Context) (*TrendsBatchResult, error) {
	slog.Info("Stage 1: Fetching GitHub Trending repositories", "since", r.Config.TimeRange, "limit", r.Config.Limit)
	rawItems, err := ai.FetchGitHubTrending(ctx, r.Config.TimeRange, r.Config.Limit, r.Config.TrendingBaseURL, r.HTTPClient)
	if err != nil {
		return nil, fmt.Errorf("stage 1 fetch trending failed: %w", err)
	}
	if len(rawItems) == 0 {
		return nil, fmt.Errorf("stage 1 returned 0 trending repositories")
	}

	enrichedItems := ai.EnrichTrendItemsWithGitHubAPI(ctx, rawItems, r.Config.GitHubToken, r.Config.GitHubAPIBaseURL, r.HTTPClient)
	slog.Info("Stage 1 complete: Fetched and enriched repositories", "count", len(enrichedItems))

	slog.Info("Stage 2: Loading previous week archive for delta analysis")
	prevItems := r.loadPreviousLatest(ctx)
	deltaItems := ai.ComputeTrendDeltas(enrichedItems, prevItems)
	slog.Info("Stage 2 complete: Computed trend deltas", "previous_count", len(prevItems))

	slog.Info("Stage 3 & 4: Running Gemini LLM structured analysis and compiling Markdown report", "model", r.Config.Model)
	report, err := ai.AnalyzeGitHubTrends(ctx, r.GeminiClient, deltaItems, r.Config.Model)
	if err != nil {
		return nil, fmt.Errorf("stage 3 llm analysis failed: %w", err)
	}
	report.TimeRange = r.Config.TimeRange

	var reportID, reportURL string
	if r.ReportStore != nil {
		title := fmt.Sprintf("GitHub Trending 每週開源趨勢週報 (%s)", report.Date)
		htmlPage := RenderReportPage(title, report.Markdown, "https://github.com/trending?since="+r.Config.TimeRange, nil)
		reportID = r.ReportStore.Put(htmlPage)
		if base := strings.TrimRight(strings.TrimSpace(r.Config.AppBaseURL), "/"); base != "" && reportID != "" {
			if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
				base = "https://" + base
			}
			reportURL = fmt.Sprintf("%s/reports/%s", base, reportID)
		}
	}

	slog.Info("Stage 5: Archiving artifacts and delivering notifications", "report_id", reportID, "report_url", reportURL)
	r.saveArchive(ctx, report)

	flexItems := ToGitHubTrendFlexItems(report.Items)
	carousel := line.BuildGitHubTrendsCarousel(report.Date, report.MacroSummary, reportURL, flexItems)

	if r.LineClient != nil && strings.TrimSpace(r.Config.LineUserID) != "" {
		altText := fmt.Sprintf("🚀 本週 GitHub Trending Top %d 趨勢週報 (%s)", len(report.Items), report.Date)
		flexMsg := line.NewFlexMessage(altText, carousel, nil)
		if err := r.LineClient.PushMessages(ctx, r.Config.LineUserID, []map[string]any{flexMsg}); err != nil {
			return nil, fmt.Errorf("stage 5 line flex push failed: %w", err)
		}
		slog.Info("Stage 5: Successfully pushed GitHub Trends Flex Message to LINE", "user_id", r.Config.LineUserID)
	}

	if strings.TrimSpace(r.Config.NotificationWebhookURL) != "" {
		r.notifyWebhook(ctx, report, reportURL)
	}

	return &TrendsBatchResult{
		Report:       report,
		ReportID:     reportID,
		ReportURL:    reportURL,
		FlexCarousel: carousel,
	}, nil
}

// ToGitHubTrendFlexItems converts []ai.TrendItem to []line.GitHubTrendFlexItem.
func ToGitHubTrendFlexItems(items []ai.TrendItem) []line.GitHubTrendFlexItem {
	out := make([]line.GitHubTrendFlexItem, 0, len(items))
	for _, it := range items {
		out = append(out, line.GitHubTrendFlexItem{
			Rank:          it.Rank,
			Name:          it.Name,
			URL:           it.URL,
			Language:      it.Language,
			TotalStars:    it.TotalStars,
			StarsAdded:    it.StarsAdded,
			Status:        string(it.Status),
			PreviousRank:  it.PreviousRank,
			Description:   it.Description,
			AIHighlight:   it.AIHighlight,
			Topics:        it.Topics,
			License:       it.License,
			LatestRelease: it.LatestRelease,
		})
	}
	return out
}

func (r *TrendsBatchRunner) loadPreviousLatest(ctx context.Context) []ai.TrendItem {
	// 1. Try GCS if OutputBucket is configured
	if bucket := strings.TrimSpace(r.Config.OutputBucket); bucket != "" {
		if data, err := r.downloadGCSObject(ctx, bucket, "archive/weekly_latest.json"); err == nil && len(data) > 0 {
			var items []ai.TrendItem
			if json.Unmarshal(data, &items) == nil && len(items) > 0 {
				return items
			}
		}
	}

	// 2. Fallback to Firestore ArchiveStore
	if r.ArchiveStore != nil && r.ArchiveStore.IsAvailable() {
		if doc := r.ArchiveStore.Load("weekly_latest"); doc != nil {
			if rawJSON, ok := doc["items_json"].(string); ok && rawJSON != "" {
				var items []ai.TrendItem
				if json.Unmarshal([]byte(rawJSON), &items) == nil {
					return items
				}
			}
		}
	}
	return nil
}

func (r *TrendsBatchRunner) saveArchive(ctx context.Context, report *ai.TrendsReport) {
	itemsBytes, err := json.MarshalIndent(report.Items, "", "  ")
	if err != nil {
		return
	}

	// 1. Save to Firestore ArchiveStore if available
	if r.ArchiveStore != nil && r.ArchiveStore.IsAvailable() {
		doc := map[string]any{
			"date":          report.Date,
			"macro_summary": report.MacroSummary,
			"items_json":    string(itemsBytes),
			"markdown":      report.Markdown,
			"created_at":    float64(time.Now().UnixNano()) / 1e9,
		}
		r.ArchiveStore.Save("trends_"+report.Date, doc)
		r.ArchiveStore.Save("weekly_latest", doc)
	}

	// 2. Upload to GCS if OutputBucket is configured
	bucket := strings.TrimSpace(r.Config.OutputBucket)
	if bucket == "" {
		return
	}
	reportPath := fmt.Sprintf("reports/weekly_trends_%s.md", report.Date)
	if err := r.uploadGCSObject(ctx, bucket, reportPath, []byte(report.Markdown), "text/markdown; charset=utf-8"); err != nil {
		slog.Warn("Failed to upload Markdown report to GCS", "bucket", bucket, "path", reportPath, "error", err)
	}
	jsonPath := fmt.Sprintf("archive/trends_%s.json", report.Date)
	if err := r.uploadGCSObject(ctx, bucket, jsonPath, itemsBytes, "application/json"); err != nil {
		slog.Warn("Failed to upload dated JSON archive to GCS", "bucket", bucket, "path", jsonPath, "error", err)
	}
	if err := r.uploadGCSObject(ctx, bucket, "archive/weekly_latest.json", itemsBytes, "application/json"); err != nil {
		slog.Warn("Failed to upload weekly_latest.json to GCS", "bucket", bucket, "error", err)
	} else {
		slog.Info("Published artifacts to GCS", "bucket", bucket, "report_path", reportPath)
	}
}

func (r *TrendsBatchRunner) gcsAuthToken(ctx context.Context) string {
	ts := r.TokenSource
	if ts == nil {
		if creds, err := google.FindDefaultCredentials(ctx, "https://www.googleapis.com/auth/cloud-platform"); err == nil {
			ts = creds.TokenSource
			r.TokenSource = ts
		}
	}
	if ts == nil {
		return ""
	}
	tok, err := ts.Token()
	if err != nil {
		return ""
	}
	return tok.AccessToken
}

func (r *TrendsBatchRunner) downloadGCSObject(ctx context.Context, bucket, objectName string) ([]byte, error) {
	base := strings.TrimRight(r.Config.GCSBaseURL, "/")
	u := fmt.Sprintf("%s/storage/v1/b/%s/o/%s?alt=media", base, url.PathEscape(bucket), url.PathEscape(objectName))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if tok := r.gcsAuthToken(ctx); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := r.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("gcs download status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func (r *TrendsBatchRunner) uploadGCSObject(ctx context.Context, bucket, objectName string, data []byte, contentType string) error {
	base := strings.TrimRight(r.Config.GCSBaseURL, "/")
	u := fmt.Sprintf("%s/upload/storage/v1/b/%s/o?uploadType=media&name=%s", base, url.PathEscape(bucket), url.QueryEscape(objectName))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	if tok := r.gcsAuthToken(ctx); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := r.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("gcs upload status %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

func (r *TrendsBatchRunner) notifyWebhook(ctx context.Context, report *ai.TrendsReport, reportURL string) {
	text := fmt.Sprintf("🚀 *本週 GitHub Trending Top %d 週報已產出 (%s)*\n%s", len(report.Items), report.Date, report.MacroSummary)
	if reportURL != "" {
		text += fmt.Sprintf("\n📄 完整報告：%s", reportURL)
	}
	payload, _ := json.Marshal(map[string]string{"text": text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.Config.NotificationWebhookURL, bytes.NewReader(payload))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.HTTPClient.Do(req)
	if err != nil {
		slog.Warn("Failed to send notification webhook", "error", err)
		return
	}
	defer resp.Body.Close()
	slog.Info("Notification sent to webhook", "status", resp.StatusCode)
}
