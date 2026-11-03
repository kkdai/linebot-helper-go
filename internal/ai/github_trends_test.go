package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kkdai/linebot-helper-go/internal/gemini"
)

const sampleTrendingHTML = `<!DOCTYPE html>
<html>
<body>
  <article class="Box-row">
    <h2 class="h3 lh-condensed">
      <a href="/google/adk-go">
        google /
        <span class="text-normal">adk-go</span>
      </a>
    </h2>
    <p class="col-9 color-fg-muted my-1 pr-4">
      An open-source Agent Development Kit for Go developers.
    </p>
    <div class="f6 color-fg-muted mt-2">
      <span itemprop="programmingLanguage">Go</span>
      <a href="/google/adk-go/stargazers">12,345</a>
      <span class="d-inline-block float-sm-right">2,100 stars this week</span>
    </div>
  </article>
  <article class="Box-row">
    <h2 class="h3 lh-condensed">
      <a href="/kkdai/linebot-helper-go">
        kkdai / linebot-helper-go
      </a>
    </h2>
    <p class="col-9 color-fg-muted my-1 pr-4">
      LINE Bot helper powered by Gemini and ADK.
    </p>
    <div class="f6 color-fg-muted mt-2">
      <span itemprop="programmingLanguage">Go</span>
      <a href="/kkdai/linebot-helper-go/stargazers">888</a>
      <span class="d-inline-block float-sm-right">300 stars this week</span>
    </div>
  </article>
</body>
</html>`

func TestParseAndFetchGitHubTrending(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("since") != "weekly" {
			t.Errorf("expected since=weekly, got %s", r.URL.RawQuery)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleTrendingHTML))
	}))
	defer srv.Close()

	items, err := FetchGitHubTrending(context.Background(), "weekly", 10, srv.URL, srv.Client())
	if err != nil {
		t.Fatalf("FetchGitHubTrending failed: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].Name != "google/adk-go" || items[0].Language != "Go" || items[0].TotalStars != "12,345" {
		t.Errorf("unexpected first item: %+v", items[0])
	}
	if items[1].Name != "kkdai/linebot-helper-go" || items[1].StarsAdded != "300 stars this week" {
		t.Errorf("unexpected second item: %+v", items[1])
	}
}

func TestComputeTrendDeltas(t *testing.T) {
	prev := []TrendItem{
		{Rank: 1, Name: "old/top1"},
		{Rank: 5, Name: "kkdai/linebot-helper-go"},
		{Rank: 2, Name: "steady/project"},
	}
	curr := []TrendItem{
		{Rank: 1, Name: "google/adk-go"},
		{Rank: 2, Name: "kkdai/linebot-helper-go"},
		{Rank: 3, Name: "steady/project"},
	}

	deltas := ComputeTrendDeltas(curr, prev)
	if deltas[0].Status != TrendStatusNew || deltas[0].PreviousRank != 0 {
		t.Errorf("expected item 0 to be NEW, got %+v", deltas[0])
	}
	if deltas[1].Status != TrendStatusRising || deltas[1].PreviousRank != 5 {
		t.Errorf("expected item 1 to be RISING from #5, got %+v", deltas[1])
	}
	if deltas[2].Status != TrendStatusContinuous || deltas[2].PreviousRank != 2 {
		t.Errorf("expected item 2 to be CONTINUOUS from #2, got %+v", deltas[2])
	}
}

func TestEnrichAndAnalyzeGitHubTrends(t *testing.T) {
	ghSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/google/adk-go":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"topics":  []string{"ai", "agents", "golang"},
				"license": map[string]string{"spdx_id": "Apache-2.0"},
			})
		case "/repos/google/adk-go/releases/latest":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name": "v1.9.0",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ghSrv.Close()

	geminiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		llmJSON := `{
			"macro_summary": "本週全球開源社群以多代理協作框架與高並發雲原生工具為核心趨勢。",
			"projects": [
				{
					"name": "google/adk-go",
					"ai_highlight": "Google 推出的 Go 語言 Agent 開發套件，支援多層級 Sub-Agent 協作。",
					"deep_analysis": "解決 Go 開發者在建構複雜多代理系統時缺乏標準化調度框架的痛點。"
				}
			]
		}`
		resp := map[string]any{
			"candidates": []map[string]any{
				{
					"content": map[string]any{
						"parts": []map[string]any{
							{"text": llmJSON},
						},
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer geminiSrv.Close()

	rawItems := []TrendItem{
		{
			Rank:        1,
			Name:        "google/adk-go",
			URL:         "https://github.com/google/adk-go",
			Language:    "Go",
			TotalStars:  "12,345",
			StarsAdded:  "2,100 stars this week",
			Description: "Agent Development Kit for Go",
			Status:      TrendStatusNew,
		},
	}

	enriched := EnrichTrendItemsWithGitHubAPI(context.Background(), rawItems, "test-token", ghSrv.URL, ghSrv.Client())
	if enriched[0].License != "Apache-2.0" || enriched[0].LatestRelease != "v1.9.0" || len(enriched[0].Topics) != 3 {
		t.Fatalf("unexpected enriched metadata: %+v", enriched[0])
	}

	gClient := gemini.NewClient("", "global", "fake-api-key")
	gClient.DeveloperBaseURL = geminiSrv.URL

	report, err := AnalyzeGitHubTrends(context.Background(), gClient, enriched, "gemini-2.5-flash")
	if err != nil {
		t.Fatalf("AnalyzeGitHubTrends failed: %v", err)
	}
	if !strings.Contains(report.MacroSummary, "多代理協作框架") {
		t.Errorf("unexpected macro summary: %s", report.MacroSummary)
	}
	if !strings.Contains(report.Items[0].AIHighlight, "Agent 開發套件") {
		t.Errorf("unexpected AI highlight: %s", report.Items[0].AIHighlight)
	}
	if !strings.Contains(report.Markdown, "google/adk-go") || !strings.Contains(report.Markdown, "Apache-2.0") {
		t.Errorf("expected markdown report to include repo and license, got:\n%s", report.Markdown)
	}
}
