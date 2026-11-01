package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/kkdai/linebot-helper-go/internal/gemini"
)

const DefaultGitHubAPIBaseURL = "https://api.github.com"

type githubIssue struct {
	Title       string `json:"title"`
	Body        string `json:"body"`
	HTMLURL     string `json:"html_url"`
	PullRequest any    `json:"pull_request"`
	Labels      []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

func fetchGitHubIssues(ctx context.Context, repo, accessToken, since, apiBaseURL string) []githubIssue {
	if apiBaseURL == "" {
		apiBaseURL = DefaultGitHubAPIBaseURL
	}
	url := fmt.Sprintf("%s/repos/%s/issues?state=all&per_page=100", strings.TrimRight(apiBaseURL, "/"), repo)
	if since != "" {
		url += "&since=" + since
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	if accessToken != "" {
		req.Header.Set("Authorization", "token "+accessToken)
	}

	httpClient := &http.Client{Timeout: 15 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		slog.Error("Error fetching GitHub issues", "error", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}

	var all []githubIssue
	if err := json.NewDecoder(resp.Body).Decode(&all); err != nil {
		return nil
	}
	var filtered []githubIssue
	for _, iss := range all {
		if iss.PullRequest == nil {
			filtered = append(filtered, iss)
		}
	}
	return filtered
}

func formatIssuesForSummary(issues []githubIssue) string {
	if len(issues) == 0 {
		return "沒有找到任何 GitHub issues"
	}
	var parts []string
	for _, iss := range issues {
		title := iss.Title
		if title == "" {
			title = "No title"
		}
		body := iss.Body
		if body == "" {
			body = "No description"
		}
		runes := []rune(body)
		if len(runes) > 500 {
			body = string(runes[:500])
		}
		var labels []string
		for _, l := range iss.Labels {
			labels = append(labels, l.Name)
		}
		labelStr := "None"
		if len(labels) > 0 {
			labelStr = strings.Join(labels, ", ")
		}
		parts = append(parts, fmt.Sprintf("\n標題: %s\nURL: %s\n標籤: %s\n內容: %s...\n---\n",
			title, iss.HTMLURL, labelStr, body))
	}
	return strings.Join(parts, "\n")
}

// SummarizedYesterdayGitHubIssues fetches recent issues from kkdai/bookmarks and summarizes them with Gemini.
func SummarizedYesterdayGitHubIssues(ctx context.Context, client *gemini.Client, githubToken, apiBaseURL string) (string, error) {
	var issues []githubIssue
	pastDays := 1
	for len(issues) <= 5 && pastDays < 30 {
		sinceDay := time.Now().UTC().AddDate(0, 0, -pastDays).Format("2006-01-02T15:04:05Z")
		issues = fetchGitHubIssues(ctx, "kkdai/bookmarks", githubToken, sinceDay, apiBaseURL)
		pastDays++
	}
	if len(issues) == 0 {
		return "最近沒有新的 GitHub issues", nil
	}

	issuesText := formatIssuesForSummary(issues)
	prompt := fmt.Sprintf(`這些資料是我昨天搜集的文章，我想要總結這些資料，請幫我總結一下。 寫成一篇短文來分享我昨天有學到哪些內容，
幫我在每一段最後加上原有的 URL 連結(url 不要使用 markdown, 直接給 url)，這樣我可以隨時回去查看原文。
請去除掉所有的 tags, links, 和其他不必要的資訊，只保留文章的主要內容，我的角色是 Evan ，喜歡 LLM 跟 AI 相關的技術。:

%s

請用繁體中文總結以上內容。
`, issuesText)

	zero := float32(0)
	res, err := client.GenerateContent(ctx, gemini.GenerateOptions{
		Model:           "gemini-3.1-flash-lite",
		Temperature:     &zero,
		MaxOutputTokens: 2048,
		Contents: []gemini.Content{
			{Role: "user", Parts: []gemini.Part{gemini.NewTextPart(prompt)}},
		},
	})
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(res.Text) == "" {
		return "無法生成摘要", nil
	}
	return res.Text, nil
}

var queryTemplates = map[string]string{
	"gas_station": "請幫我找出附近的加油站，並列出名稱、距離和地址。",
	"parking":     "請幫我找出附近的停車場，並列出名稱、收費方式（如果有）和地址。",
	"restaurant":  "請幫我找出附近評價不錯的餐廳，並列出名稱、類型和地址。",
}

var placeTypeNames = map[string]string{
	"gas_station": "加油站",
	"parking":     "停車場",
	"restaurant":  "餐廳",
}

var placeTypeEmojis = map[string]string{
	"gas_station": "⛽",
	"parking":     "🅿️",
	"restaurant":  "🍴",
}

// NearbyPlacesResult holds the result of SearchNearbyPlaces.
type NearbyPlacesResult struct {
	Status          string
	Places          string
	PlaceType       string
	Latitude        float64
	Longitude       float64
	RestaurantNames []string
	ErrorMessage    string
}

// SearchNearbyPlaces searches for nearby places using Google Maps Grounding via Vertex AI.
func SearchNearbyPlaces(ctx context.Context, client *gemini.Client, latitude, longitude float64, placeType, customQuery, languageCode string) NearbyPlacesResult {
	if placeType == "" {
		placeType = "restaurant"
	}
	if languageCode == "" {
		languageCode = "zh-TW"
	}
	query := customQuery
	if query == "" {
		var ok bool
		query, ok = queryTemplates[placeType]
		if !ok {
			query = queryTemplates["restaurant"]
		}
	}

	res, err := client.GenerateContent(ctx, gemini.GenerateOptions{
		Model:            "gemini-3.1-flash-lite",
		EnableGoogleMaps: true,
		LatLng:           &gemini.LatLng{Latitude: latitude, Longitude: longitude},
		LanguageCode:     languageCode,
		Contents: []gemini.Content{
			{Role: "user", Parts: []gemini.Part{gemini.NewTextPart(query)}},
		},
	})
	if err != nil {
		errMsg := err.Error()
		if len(errMsg) > 100 {
			errMsg = errMsg[:100]
		}
		return NearbyPlacesResult{
			Status:       "error",
			ErrorMessage: fmt.Sprintf("無法取得附近地點資訊: %s", errMsg),
		}
	}

	emoji, ok := placeTypeEmojis[placeType]
	if !ok {
		emoji = "📍"
	}
	placeName, ok := placeTypeNames[placeType]
	if !ok {
		placeName = "地點"
	}
	formatted := fmt.Sprintf("%s 附近的%s：\n\n%s", emoji, placeName, res.Text)

	var names []string
	if placeType == "restaurant" && strings.TrimSpace(res.Text) != "" {
		extractPrompt := fmt.Sprintf(`請從以下文字中擷取所有餐廳的名稱，並以 JSON 陣列格式返回（例如：["餐廳A", "餐廳B"]）。請直接輸出 JSON 陣列，不要包含任何 markdown 標記（如 `+"```json"+`）或說明文字。

%s`, res.Text)
		extRes, extErr := client.GenerateContent(ctx, gemini.GenerateOptions{
			Model: "gemini-3.1-flash-lite",
			Contents: []gemini.Content{
				{Role: "user", Parts: []gemini.Part{gemini.NewTextPart(extractPrompt)}},
			},
		})
		if extErr == nil && extRes != nil {
			extText := strings.TrimSpace(extRes.Text)
			if json.Unmarshal([]byte(extText), &names) != nil {
				if idxStart := strings.Index(extText, "["); idxStart != -1 {
					if idxEnd := strings.LastIndex(extText, "]"); idxEnd > idxStart {
						_ = json.Unmarshal([]byte(extText[idxStart:idxEnd+1]), &names)
					}
				}
			}
		}
		var cleaned []string
		for _, n := range names {
			n = strings.TrimSpace(n)
			if n != "" {
				cleaned = append(cleaned, n)
			}
			if len(cleaned) >= 3 {
				break
			}
		}
		names = cleaned
	}

	return NearbyPlacesResult{
		Status:          "success",
		Places:          formatted,
		PlaceType:       placeType,
		Latitude:        latitude,
		Longitude:       longitude,
		RestaurantNames: names,
	}
}

// RestaurantDetail holds structured restaurant info and reviews for Batch API analysis.
type RestaurantDetail struct {
	Name    string   `json:"name"`
	Address string   `json:"address"`
	Rating  string   `json:"rating"`
	Reviews []string `json:"reviews"`
}

var jsonFenceRegex = regexp.MustCompile(`(?is)` + "```(?:json)?\\s*(.*?)\\s*```")

func extractJSONObject(text string) string {
	text = strings.TrimSpace(text)
	if m := jsonFenceRegex.FindStringSubmatch(text); len(m) > 1 {
		return strings.TrimSpace(m[1])
	}
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start != -1 && end > start {
		return strings.TrimSpace(text[start : end+1])
	}
	return text
}

// GetNearbyRestaurantsForBatch finds nearby restaurants with Maps Grounding and enriches them with reviews via Google Search Grounding.
func GetNearbyRestaurantsForBatch(ctx context.Context, client *gemini.Client, latitude, longitude float64, limit int, languageCode string) ([]RestaurantDetail, error) {
	if limit <= 0 {
		limit = 3
	}
	if languageCode == "" {
		languageCode = "zh-TW"
	}
	query := fmt.Sprintf(`請幫我搜尋此座標附近的 %d 家評價不錯的熱門餐廳。
請務必以 JSON 格式返回，格式必須符合以下 JSON 結構：
{
  "restaurants": [
    {
      "name": "餐廳名稱",
      "address": "餐廳地址",
      "rating": "綜合評分",
      "reviews": []
    }
  ]
}
注意：請將 reviews 欄位保留為空陣列 []，我們後續會透過搜尋補充。請直接輸出 JSON，不要包含任何說明文字。`, limit)

	res, err := client.GenerateContent(ctx, gemini.GenerateOptions{
		Model:            "gemini-3.1-flash-lite",
		EnableGoogleMaps: true,
		LatLng:           &gemini.LatLng{Latitude: latitude, Longitude: longitude},
		LanguageCode:     languageCode,
		Contents: []gemini.Content{
			{Role: "user", Parts: []gemini.Part{gemini.NewTextPart(query)}},
		},
	})
	if err != nil {
		return nil, err
	}
	cleaned := extractJSONObject(res.Text)
	var list struct {
		Restaurants []RestaurantDetail `json:"restaurants"`
	}
	if err := json.Unmarshal([]byte(cleaned), &list); err != nil {
		return nil, fmt.Errorf("failed to parse restaurants JSON: %w", err)
	}

	if len(list.Restaurants) > limit {
		list.Restaurants = list.Restaurants[:limit]
	}

	for i := range list.Restaurants {
		rest := &list.Restaurants[i]
		if rest.Name == "" {
			continue
		}
		searchQuery := fmt.Sprintf("請搜尋該餐廳的網路評價、推薦特色菜色、用餐感受與氛圍：%s，地址：%s。請給出大約 5 句話，總結該餐廳的熱門招牌菜、給人的環境與服務感受、以及網友的真實評論心得。", rest.Name, rest.Address)
		sRes, sErr := client.GenerateContent(ctx, gemini.GenerateOptions{
			Model:              "gemini-3.1-flash-lite",
			EnableGoogleSearch: true,
			Contents: []gemini.Content{
				{Role: "user", Parts: []gemini.Part{gemini.NewTextPart(searchQuery)}},
			},
		})
		if sErr == nil && sRes != nil && strings.TrimSpace(sRes.Text) != "" {
			rest.Reviews = []string{strings.TrimSpace(sRes.Text)}
		} else {
			rest.Reviews = []string{"暫無網路評價資訊"}
		}
	}

	return list.Restaurants, nil
}

// SearchSpecificRestaurantByName searches for a specific restaurant by name using Google Search Grounding.
func SearchSpecificRestaurantByName(ctx context.Context, client *gemini.Client, restaurantName string) (*RestaurantDetail, error) {
	query := fmt.Sprintf(`請搜尋這家餐廳的正確名稱、完整地址、目前 Google Map 評分、以及綜合評價與菜色特色：%s。
請務必以 JSON 格式返回，格式必須符合以下 JSON 結構：
{
  "name": "餐廳名稱",
  "address": "餐廳地址",
  "rating": "綜合評分",
  "reviews": [
    "網友評價、招牌特色菜色、用餐感受與氛圍的綜合心得摘要"
  ]
}
請直接輸出 JSON，不要包含任何額外的說明文字。`, restaurantName)

	res, err := client.GenerateContent(ctx, gemini.GenerateOptions{
		Model:              "gemini-3.1-flash-lite",
		EnableGoogleSearch: true,
		Contents: []gemini.Content{
			{Role: "user", Parts: []gemini.Part{gemini.NewTextPart(query)}},
		},
	})
	if err != nil {
		return nil, err
	}
	cleaned := extractJSONObject(res.Text)
	var detail RestaurantDetail
	if err := json.Unmarshal([]byte(cleaned), &detail); err != nil {
		return nil, err
	}
	return &detail, nil
}
