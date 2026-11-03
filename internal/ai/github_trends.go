package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kkdai/linebot-helper-go/internal/gemini"
	"golang.org/x/net/html"
)

const (
	DefaultGitHubTrendingURL = "https://github.com/trending"
	DefaultTrendsModel       = "gemini-2.5-flash"
)

// TrendStatus represents the week-over-week status of a trending repository.
type TrendStatus string

const (
	TrendStatusNew        TrendStatus = "NEW"
	TrendStatusRising     TrendStatus = "RISING"
	TrendStatusContinuous TrendStatus = "CONTINUOUS"
)

// TrendItem represents a single repository on GitHub Trending along with enriched metadata and AI analysis.
type TrendItem struct {
	Rank          int         `json:"rank"`
	Name          string      `json:"name"`
	URL           string      `json:"url"`
	Language      string      `json:"language"`
	TotalStars    string      `json:"total_stars"`
	StarsAdded    string      `json:"stars_added"`
	Description   string      `json:"description"`
	Topics        []string    `json:"topics,omitempty"`
	License       string      `json:"license,omitempty"`
	LatestRelease string      `json:"latest_release,omitempty"`
	Status        TrendStatus `json:"status,omitempty"`
	PreviousRank  int         `json:"previous_rank,omitempty"`
	AIHighlight   string      `json:"ai_highlight,omitempty"`
	DeepAnalysis  string      `json:"deep_analysis,omitempty"`
}

// TrendsReport holds the complete weekly GitHub Trending analysis output.
type TrendsReport struct {
	Date         string      `json:"date"`
	TimeRange    string      `json:"time_range"`
	MacroSummary string      `json:"macro_summary"`
	Items        []TrendItem `json:"items"`
	Markdown     string      `json:"markdown"`
}

// FetchGitHubTrending scrapes the GitHub Trending page and returns the top repositories.
func FetchGitHubTrending(ctx context.Context, since string, limit int, trendingBaseURL string, httpClient *http.Client) ([]TrendItem, error) {
	if since == "" {
		since = "weekly"
	}
	if limit <= 0 {
		limit = 10
	}
	if trendingBaseURL == "" {
		trendingBaseURL = DefaultGitHubTrendingURL
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}

	reqURL := fmt.Sprintf("%s?since=%s", strings.TrimRight(trendingBaseURL, "/"), since)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (GCP-Batch-Weekly-Crawler/1.0)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch github trending: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("github trending returned status %d", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return ParseGitHubTrendingHTML(string(bodyBytes), limit)
}

// ParseGitHubTrendingHTML parses GitHub Trending HTML and extracts up to limit TrendItems.
func ParseGitHubTrendingHTML(rawHTML string, limit int) ([]TrendItem, error) {
	if limit <= 0 {
		limit = 10
	}
	doc, err := html.Parse(strings.NewReader(rawHTML))
	if err != nil {
		return nil, fmt.Errorf("failed to parse github trending HTML: %w", err)
	}

	var articles []*html.Node
	var findArticles func(*html.Node)
	findArticles = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "article" && hasClass(n, "Box-row") {
			articles = append(articles, n)
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			findArticles(c)
		}
	}
	findArticles(doc)

	var items []TrendItem
	for idx, art := range articles {
		if len(items) >= limit {
			break
		}
		item := parseArticleNode(art, idx+1)
		if item.Name != "" {
			items = append(items, item)
		}
	}

	return items, nil
}

func parseArticleNode(article *html.Node, rank int) TrendItem {
	item := TrendItem{
		Rank:       rank,
		Language:   "General",
		TotalStars: "0",
	}

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "h2":
				if a := findFirstElement(n, "a"); a != nil {
					rawName := extractText(a)
					repoName := strings.Join(strings.Fields(rawName), "")
					item.Name = repoName
					item.URL = "https://github.com/" + strings.TrimPrefix(repoName, "/")
				}
			case "p":
				if item.Description == "" {
					item.Description = strings.TrimSpace(extractText(n))
				}
			case "span":
				if getAttr(n, "itemprop") == "programmingLanguage" {
					lang := strings.TrimSpace(extractText(n))
					if lang != "" {
						item.Language = lang
					}
				}
				if hasClass(n, "d-inline-block") && hasClass(n, "float-sm-right") {
					added := strings.TrimSpace(extractText(n))
					if added != "" {
						item.StarsAdded = strings.Join(strings.Fields(added), " ")
					}
				}
			case "a":
				href := getAttr(n, "href")
				if strings.HasSuffix(href, "/stargazers") {
					stars := strings.TrimSpace(extractText(n))
					if stars != "" {
						item.TotalStars = stars
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(article)
	return item
}

func hasClass(n *html.Node, className string) bool {
	cls := getAttr(n, "class")
	for _, c := range strings.Fields(cls) {
		if c == className {
			return true
		}
	}
	return false
}

func getAttr(n *html.Node, key string) string {
	for _, attr := range n.Attr {
		if strings.EqualFold(attr.Key, key) {
			return attr.Val
		}
	}
	return ""
}

func findFirstElement(n *html.Node, tag string) *html.Node {
	if n.Type == html.ElementNode && n.Data == tag {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findFirstElement(c, tag); found != nil {
			return found
		}
	}
	return nil
}

func extractText(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			sb.WriteString(node.Data)
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return sb.String()
}

// EnrichTrendItemsWithGitHubAPI enriches each TrendItem with repository metadata (topics, license, latest release) via GitHub REST API.
func EnrichTrendItemsWithGitHubAPI(ctx context.Context, items []TrendItem, githubToken, apiBaseURL string, httpClient *http.Client) []TrendItem {
	if apiBaseURL == "" {
		apiBaseURL = DefaultGitHubAPIBaseURL
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}

	enriched := make([]TrendItem, len(items))
	copy(enriched, items)

	for i := range enriched {
		repoName := strings.TrimSpace(enriched[i].Name)
		if repoName == "" {
			continue
		}
		repoURL := fmt.Sprintf("%s/repos/%s", strings.TrimRight(apiBaseURL, "/"), repoName)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, repoURL, nil)
		if err != nil {
			continue
		}
		req.Header.Set("Accept", "application/vnd.github.v3+json")
		if githubToken != "" {
			req.Header.Set("Authorization", "Bearer "+githubToken)
		}

		resp, err := httpClient.Do(req)
		if err != nil {
			slog.Debug("GitHub repo enrichment skipped", "repo", repoName, "error", err)
			continue
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			var repoMeta struct {
				Description string   `json:"description"`
				Topics      []string `json:"topics"`
				License     *struct {
					SPDXID string `json:"spdx_id"`
					Name   string `json:"name"`
				} `json:"license"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&repoMeta); err == nil {
				if enriched[i].Description == "" && repoMeta.Description != "" {
					enriched[i].Description = repoMeta.Description
				}
				if len(repoMeta.Topics) > 0 {
					if len(repoMeta.Topics) > 5 {
						enriched[i].Topics = repoMeta.Topics[:5]
					} else {
						enriched[i].Topics = repoMeta.Topics
					}
				}
				if repoMeta.License != nil {
					if repoMeta.License.SPDXID != "" && repoMeta.License.SPDXID != "NOASSERTION" {
						enriched[i].License = repoMeta.License.SPDXID
					} else if repoMeta.License.Name != "" {
						enriched[i].License = repoMeta.License.Name
					}
				}
			}
		}
		resp.Body.Close()

		relURL := fmt.Sprintf("%s/repos/%s/releases/latest", strings.TrimRight(apiBaseURL, "/"), repoName)
		relReq, err := http.NewRequestWithContext(ctx, http.MethodGet, relURL, nil)
		if err != nil {
			continue
		}
		relReq.Header.Set("Accept", "application/vnd.github.v3+json")
		if githubToken != "" {
			relReq.Header.Set("Authorization", "Bearer "+githubToken)
		}
		relResp, err := httpClient.Do(relReq)
		if err == nil {
			if relResp.StatusCode >= 200 && relResp.StatusCode < 300 {
				var relMeta struct {
					TagName string `json:"tag_name"`
				}
				if json.NewDecoder(relResp.Body).Decode(&relMeta) == nil && relMeta.TagName != "" {
					enriched[i].LatestRelease = relMeta.TagName
				}
			}
			relResp.Body.Close()
		}
	}

	return enriched
}

// ComputeTrendDeltas compares the current week's trending items against the previous week's items
// and marks each item as NEW, RISING, or CONTINUOUS.
func ComputeTrendDeltas(current []TrendItem, previous []TrendItem) []TrendItem {
	prevByName := make(map[string]TrendItem, len(previous))
	for _, p := range previous {
		prevByName[strings.ToLower(strings.TrimSpace(p.Name))] = p
	}

	result := make([]TrendItem, len(current))
	copy(result, current)

	for i := range result {
		key := strings.ToLower(strings.TrimSpace(result[i].Name))
		prev, exists := prevByName[key]
		if !exists {
			result[i].Status = TrendStatusNew
			result[i].PreviousRank = 0
		} else {
			result[i].PreviousRank = prev.Rank
			if prev.Rank > 0 && result[i].Rank < prev.Rank {
				result[i].Status = TrendStatusRising
			} else {
				result[i].Status = TrendStatusContinuous
			}
		}
	}
	return result
}

type llmTrendsAnalysisResponse struct {
	MacroSummary string `json:"macro_summary"`
	Projects     []struct {
		Name         string `json:"name"`
		AIHighlight  string `json:"ai_highlight"`
		DeepAnalysis string `json:"deep_analysis"`
	} `json:"projects"`
}

// AnalyzeGitHubTrends uses Gemini to generate structured Traditional Chinese insights and compiles the weekly Markdown report.
func AnalyzeGitHubTrends(ctx context.Context, client *gemini.Client, items []TrendItem, modelName string) (*TrendsReport, error) {
	if len(items) == 0 {
		return nil, fmt.Errorf("no trending items to analyze")
	}
	if modelName == "" {
		modelName = DefaultTrendsModel
	}

	itemsJSON, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return nil, err
	}

	prompt := fmt.Sprintf(`你是一位資深的軟體架構師與開源趨勢分析師。
請根據以下本週 GitHub Trending 前 %d 名專案的結構化資料，進行深層語意研析並以繁體中文（台灣習慣用語）輸出 JSON。

專案清單資料：
%s

請嚴格按照以下 JSON 格式輸出（不要包含任何額外的說明文字）：
{
  "macro_summary": "總結本週全球開源社群的 2~3 個核心技術趨勢（約 80~150 字，適合放在 LINE 總覽卡片與報告開頭）",
  "projects": [
    {
      "name": "owner/repo（必須與輸入的 name 完全一致）",
      "ai_highlight": "1~2 句話精煉出該專案的核心定位與最大技術亮點（約 40~70 字，適合顯示於 LINE Flex Message 卡片）",
      "deep_analysis": "深度解析（包含：專案定位領域、解決之傳統痛點、架構突破技術點、以及本週受到社群關注的原因，約 120~250 字）"
    }
  ]
}

語言與風格規範：
1. 必須使用繁體中文（台灣習慣用語）。
2. 客觀嚴謹、專業自然，不使用誇大行銷詞彙。
`, len(items), string(itemsJSON))

	useDevAPI := client.ProjectID == "" && client.APIKey != ""
	res, err := client.GenerateContent(ctx, gemini.GenerateOptions{
		Model:            modelName,
		UseDeveloperAPI:  useDevAPI,
		MaxOutputTokens:  8192,
		ResponseMimeType: "application/json",
		Contents: []gemini.Content{
			{Role: "user", Parts: []gemini.Part{gemini.NewTextPart(prompt)}},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("gemini trends analysis failed: %w", err)
	}

	enrichedItems := make([]TrendItem, len(items))
	copy(enrichedItems, items)

	macroSummary := "本週 GitHub 開源社群聚焦於 AI Agent 框架、開發者生產力工具與雲原生基礎設施效能優化。"
	cleanedJSON := extractJSONObject(res.Text)

	var parsed llmTrendsAnalysisResponse
	if uErr := json.Unmarshal([]byte(cleanedJSON), &parsed); uErr == nil {
		if strings.TrimSpace(parsed.MacroSummary) != "" {
			macroSummary = strings.TrimSpace(parsed.MacroSummary)
		}
		byName := make(map[string]struct {
			Highlight string
			Deep      string
		}, len(parsed.Projects))
		for _, p := range parsed.Projects {
			byName[strings.ToLower(strings.TrimSpace(p.Name))] = struct {
				Highlight string
				Deep      string
			}{
				Highlight: strings.TrimSpace(p.AIHighlight),
				Deep:      strings.TrimSpace(p.DeepAnalysis),
			}
		}
		for i := range enrichedItems {
			key := strings.ToLower(strings.TrimSpace(enrichedItems[i].Name))
			if analysis, ok := byName[key]; ok {
				enrichedItems[i].AIHighlight = analysis.Highlight
				enrichedItems[i].DeepAnalysis = analysis.Deep
			}
		}
	} else {
		slog.Warn("Failed to unmarshal structured LLM trends JSON, using raw text fallback", "error", uErr)
		if strings.TrimSpace(res.Text) != "" {
			macroSummary = strings.TrimSpace(res.Text)
		}
	}

	for i := range enrichedItems {
		if enrichedItems[i].AIHighlight == "" {
			if enrichedItems[i].Description != "" {
				enrichedItems[i].AIHighlight = enrichedItems[i].Description
			} else {
				enrichedItems[i].AIHighlight = "本週熱門開源專案"
			}
		}
		if enrichedItems[i].DeepAnalysis == "" {
			enrichedItems[i].DeepAnalysis = enrichedItems[i].AIHighlight
		}
		if enrichedItems[i].Status == "" {
			enrichedItems[i].Status = TrendStatusNew
		}
	}

	dateStr := time.Now().Format("2006-01-02")
	reportMD := CompileTrendsMarkdown(dateStr, macroSummary, enrichedItems)

	return &TrendsReport{
		Date:         dateStr,
		TimeRange:    "weekly",
		MacroSummary: macroSummary,
		Items:        enrichedItems,
		Markdown:     reportMD,
	}, nil
}

// FormatTrendStatusBadge returns a human-readable badge string for a TrendStatus.
func FormatTrendStatusBadge(status TrendStatus, prevRank int) string {
	switch status {
	case TrendStatusRising:
		if prevRank > 0 {
			return fmt.Sprintf("🔥 排名上升 (上週 #%d)", prevRank)
		}
		return "🔥 排名上升"
	case TrendStatusContinuous:
		if prevRank > 0 {
			return fmt.Sprintf("🏆 持續霸榜 (上週 #%d)", prevRank)
		}
		return "🏆 持續霸榜"
	default:
		return "🆕 本週新進"
	}
}

// CompileTrendsMarkdown compiles the structured Traditional Chinese Markdown report.
func CompileTrendsMarkdown(dateStr, macroSummary string, items []TrendItem) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# 🚀 GitHub Trending 每週開源趨勢研析週報 (%s)\n\n", dateStr))
	sb.WriteString("## 🌐 本週全球開源社群趨勢摘要\n\n")
	sb.WriteString(macroSummary + "\n\n")

	sb.WriteString("## 📊 Top 10 熱門專案總覽\n\n")
	sb.WriteString("| 排名 | 趨勢狀態 | 專案名稱 | 主要語言 | 星標數 (本週新增) | 核心亮點 |\n")
	sb.WriteString("| :--- | :--- | :--- | :--- | :--- | :--- |\n")
	for _, item := range items {
		badge := FormatTrendStatusBadge(item.Status, item.PreviousRank)
		starsInfo := item.TotalStars
		if item.StarsAdded != "" {
			starsInfo = fmt.Sprintf("%s (%s)", item.TotalStars, item.StarsAdded)
		}
		highlight := strings.ReplaceAll(item.AIHighlight, "|", "｜")
		highlight = strings.ReplaceAll(highlight, "\n", " ")
		sb.WriteString(fmt.Sprintf("| #%d | %s | [%s](%s) | %s | ⭐ %s | %s |\n",
			item.Rank, badge, item.Name, item.URL, item.Language, starsInfo, highlight))
	}
	sb.WriteString("\n---\n\n")

	sb.WriteString("## 🔍 專案深度解析\n\n")
	for _, item := range items {
		badge := FormatTrendStatusBadge(item.Status, item.PreviousRank)
		sb.WriteString(fmt.Sprintf("### #%d [%s](%s) — %s\n\n", item.Rank, item.Name, item.URL, badge))
		sb.WriteString(fmt.Sprintf("- **專案連結**：[%s](%s)\n", item.URL, item.URL))
		sb.WriteString(fmt.Sprintf("- **主要語言**：`%s`\n", item.Language))
		starsInfo := item.TotalStars
		if item.StarsAdded != "" {
			starsInfo = fmt.Sprintf("%s（%s）", item.TotalStars, item.StarsAdded)
		}
		sb.WriteString(fmt.Sprintf("- **星標熱度**：⭐ %s\n", starsInfo))
		if item.License != "" {
			sb.WriteString(fmt.Sprintf("- **開源授權**：`%s`\n", item.License))
		}
		if item.LatestRelease != "" {
			sb.WriteString(fmt.Sprintf("- **最新版本**：`%s`\n", item.LatestRelease))
		}
		if len(item.Topics) > 0 {
			sb.WriteString(fmt.Sprintf("- **主題標籤**：%s\n", strings.Join(item.Topics, ", ")))
		}
		if item.Description != "" {
			sb.WriteString(fmt.Sprintf("- **官方簡介**：%s\n", item.Description))
		}
		sb.WriteString(fmt.Sprintf("\n**💡 核心亮點**：\n%s\n\n", item.AIHighlight))
		sb.WriteString(fmt.Sprintf("**🔬 深度趨勢研析**：\n%s\n\n", item.DeepAnalysis))
	}

	return sb.String()
}
