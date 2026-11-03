package services

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kkdai/linebot-helper-go/internal/firestore"
	"github.com/kkdai/linebot-helper-go/internal/gemini"
	"github.com/kkdai/linebot-helper-go/internal/line"
	"golang.org/x/oauth2"
)

func TestTrendsBatchRunnerEndToEnd(t *testing.T) {
	trendingHTML := `<!DOCTYPE html><html><body>
	<article class="Box-row">
	  <h2><a href="/google/adk-go">google / adk-go</a></h2>
	  <p>Agent Development Kit for Go</p>
	  <span itemprop="programmingLanguage">Go</span>
	  <a href="/google/adk-go/stargazers">15,000</a>
	  <span class="d-inline-block float-sm-right">3,200 stars this week</span>
	</article>
	<article class="Box-row">
	  <h2><a href="/kkdai/linebot-helper-go">kkdai / linebot-helper-go</a></h2>
	  <p>LINE Bot helper in Go</p>
	  <span itemprop="programmingLanguage">Go</span>
	  <a href="/kkdai/linebot-helper-go/stargazers">1,200</a>
	  <span class="d-inline-block float-sm-right">450 stars this week</span>
	</article>
	</body></html>`

	var (
		mu              sync.Mutex
		gcsUploaded     = make(map[string]string)
		pushedMessages  []map[string]any
		webhookReceived string
	)

	mockMux := http.NewServeMux()
	mockMux.HandleFunc("/trending", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(trendingHTML))
	})
	mockMux.HandleFunc("/repos/google/adk-go", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"topics":  []string{"ai", "adk"},
			"license": map[string]string{"spdx_id": "Apache-2.0"},
		})
	})
	mockMux.HandleFunc("/repos/google/adk-go/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v1.9.0"})
	})
	mockMux.HandleFunc("/repos/kkdai/linebot-helper-go", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"topics":  []string{"linebot", "gemini"},
			"license": map[string]string{"spdx_id": "MIT"},
		})
	})
	mockMux.HandleFunc("/repos/kkdai/linebot-helper-go/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mockMux.HandleFunc("/v1beta/models/", func(w http.ResponseWriter, r *http.Request) {
		llmJSON := `{
			"macro_summary": "本週開源社群聚焦於 Go 語言 AI Agent 生態系與即時通訊整合應用。",
			"projects": [
				{
					"name": "google/adk-go",
					"ai_highlight": "Google ADK 官方 Go 語言套件，提供模組化多代理開發體驗。",
					"deep_analysis": "透過標準化的 Tool 與 Agent 介面，大幅簡化企業級 AI 代理開發。"
				},
				{
					"name": "kkdai/linebot-helper-go",
					"ai_highlight": "結合 Gemini Live 與 ADK 的全功能 LINE Bot 助手。",
					"deep_analysis": "展示如何在無伺服器架構下整合多模態語音、視覺與批次分析。"
				}
			]
		}`
		_ = json.NewEncoder(w).Encode(map[string]any{
			"candidates": []map[string]any{
				{
					"content": map[string]any{
						"parts": []map[string]any{{"text": llmJSON}},
					},
				},
			},
		})
	})
	mockMux.HandleFunc("/storage/v1/b/test-bucket/o/", func(w http.ResponseWriter, r *http.Request) {
		prevJSON := `[{"rank": 4, "name": "kkdai/linebot-helper-go"}]`
		_, _ = w.Write([]byte(prevJSON))
	})
	mockMux.HandleFunc("/upload/storage/v1/b/test-bucket/o", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gcsUploaded[name] = string(b)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]string{"name": name})
	})
	mockMux.HandleFunc("/v2/bot/message/push", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			To       string           `json:"to"`
			Messages []map[string]any `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		pushedMessages = append(pushedMessages, body.Messages...)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{})
	})
	mockMux.HandleFunc("/webhook", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		webhookReceived = string(b)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mockMux)
	defer srv.Close()

	gClient := gemini.NewClient("", "global", "fake-gemini-key")
	gClient.DeveloperBaseURL = srv.URL

	lClient := line.NewClient("fake-line-token")
	lClient.APIBaseURL = srv.URL

	reportKV := firestore.NewMemoryKVStore(true)
	reportStore := NewReportStore(reportKV)
	archiveKV := firestore.NewMemoryKVStore(true)

	runner := NewTrendsBatchRunner(
		TrendsBatchConfig{
			TimeRange:              "weekly",
			Limit:                  10,
			Model:                  "gemini-2.5-flash",
			GitHubToken:            "fake-gh-token",
			LineUserID:             "U1234567890",
			AppBaseURL:             "https://bot.example.com",
			OutputBucket:           "test-bucket",
			NotificationWebhookURL: srv.URL + "/webhook",
			TrendingBaseURL:        srv.URL + "/trending",
			GitHubAPIBaseURL:       srv.URL,
			GCSBaseURL:             srv.URL,
		},
		gClient,
		lClient,
		reportStore,
		archiveKV,
	)
	runner.TokenSource = oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "fake-gcp-token"})

	res, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("runner.Run failed: %v", err)
	}

	if len(res.Report.Items) != 2 {
		t.Fatalf("expected 2 items in report, got %d", len(res.Report.Items))
	}
	if res.Report.Items[0].Status != "NEW" {
		t.Errorf("expected first item status NEW, got %s", res.Report.Items[0].Status)
	}
	if res.Report.Items[1].Status != "RISING" || res.Report.Items[1].PreviousRank != 4 {
		t.Errorf("expected second item status RISING from #4, got %s (#%d)", res.Report.Items[1].Status, res.Report.Items[1].PreviousRank)
	}
	if res.ReportID == "" || !strings.HasPrefix(res.ReportURL, "https://bot.example.com/reports/") {
		t.Errorf("unexpected report URL: %s", res.ReportURL)
	}
	if htmlStr, ok := reportStore.Get(res.ReportID); !ok || !strings.Contains(htmlStr, "google/adk-go") {
		t.Errorf("expected HTML report in ReportStore")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(pushedMessages) != 1 || pushedMessages[0]["type"] != "flex" {
		t.Fatalf("expected 1 Flex Message pushed to LINE, got %+v", pushedMessages)
	}
	if _, ok := gcsUploaded["archive/weekly_latest.json"]; !ok {
		t.Errorf("expected archive/weekly_latest.json uploaded to GCS, got keys: %v", gcsUploaded)
	}
	if !strings.Contains(webhookReceived, "GitHub Trending") {
		t.Errorf("expected webhook notification, got %s", webhookReceived)
	}
}
