package line

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const SummarySnippetLength = 100

var bookmarkCommandRegex = regexp.MustCompile(`(?s)^/(save|list|search)(?:\s+(.*))?$`)

// ParseBookmarkCommand parses /save, /list, and /search commands.
func ParseBookmarkCommand(text string) (command string, argument string, ok bool) {
	m := bookmarkCommandRegex.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return "", "", false
	}
	return m[1], strings.TrimSpace(m[2]), true
}

// BuildSummaryBubble constructs the first bubble in the URL social carousel ("📌 摘要與分析").
func BuildSummaryBubble(title, summaryAnalysis, url, docID string) map[string]any {
	footerContents := []any{
		map[string]any{
			"type":   "button",
			"style":  "link",
			"height": "sm",
			"action": map[string]any{
				"type":  "uri",
				"label": "🔗 開啟原文",
				"uri":   url,
			},
		},
	}

	if docID != "" {
		saveData, _ := json.Marshal(map[string]string{"action": "save_bookmark", "id": docID})
		reportData, _ := json.Marshal(map[string]string{"action": "research_report", "id": docID})

		footerContents = append([]any{
			map[string]any{
				"type":   "button",
				"style":  "primary",
				"color":  "#E67E22",
				"height": "sm",
				"action": map[string]any{
					"type":        "postback",
					"label":       "🔖 儲存書籤",
					"data":        string(saveData),
					"displayText": "🔖 儲存這篇文章",
				},
			},
			map[string]any{
				"type":   "button",
				"style":  "secondary",
				"height": "sm",
				"action": map[string]any{
					"type":        "postback",
					"label":       "📄 詳細研究報告",
					"data":        string(reportData),
					"displayText": "📄 產生詳細研究報告",
				},
			},
		}, footerContents...)
	}

	if title == "" {
		title = "（無標題）"
	}
	if summaryAnalysis == "" {
		summaryAnalysis = "（無摘要）"
	}

	return map[string]any{
		"type": "bubble",
		"size": "mega",
		"header": map[string]any{
			"type": "box",
			"layout": "vertical",
			"contents": []any{
				map[string]any{
					"type":   "text",
					"text":   "📌 摘要與分析",
					"weight": "bold",
					"color":  "#ffffff",
					"size":   "lg",
				},
			},
			"backgroundColor": "#E67E22",
			"paddingAll":      "md",
		},
		"body": map[string]any{
			"type":    "box",
			"layout":  "vertical",
			"spacing": "md",
			"contents": []any{
				map[string]any{
					"type":   "text",
					"text":   title,
					"weight": "bold",
					"size":   "md",
					"wrap":   true,
					"color":  "#111111",
				},
				map[string]any{
					"type":  "text",
					"text":  summaryAnalysis,
					"wrap":  true,
					"size":  "sm",
					"color": "#333333",
				},
			},
			"paddingAll": "lg",
		},
		"footer": map[string]any{
			"type":       "box",
			"layout":     "vertical",
			"spacing":    "sm",
			"contents":   footerContents,
			"paddingAll": "md",
		},
	}
}

func snippet(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return string(runes[:limit-1]) + "…"
}

// BuildBookmarkBubble constructs a single bookmark card bubble for /list and /search.
func BuildBookmarkBubble(bookmark map[string]any) map[string]any {
	dateStr := ""
	switch v := bookmark["created_at"].(type) {
	case float64:
		if v > 0 {
			dateStr = time.Unix(int64(v), 0).Format("2006-01-02")
		}
	case int64:
		if v > 0 {
			dateStr = time.Unix(v, 0).Format("2006-01-02")
		}
	}

	title, _ := bookmark["title"].(string)
	if title == "" {
		title = "（無標題）"
	}
	summary, _ := bookmark["summary"].(string)
	url, _ := bookmark["url"].(string)
	docID, _ := bookmark["doc_id"].(string)
	delData, _ := json.Marshal(map[string]string{"action": "delete_bookmark", "id": docID})

	return map[string]any{
		"type": "bubble",
		"size": "kilo",
		"body": map[string]any{
			"type":    "box",
			"layout":  "vertical",
			"spacing": "sm",
			"contents": []any{
				map[string]any{
					"type":   "text",
					"text":   title,
					"weight": "bold",
					"size":   "md",
					"wrap":   true,
					"color":  "#111111",
				},
				map[string]any{
					"type":  "text",
					"text":  snippet(summary, SummarySnippetLength),
					"wrap":  true,
					"size":  "sm",
					"color": "#555555",
				},
				map[string]any{
					"type":  "text",
					"text":  fmt.Sprintf("🕐 %s", dateStr),
					"size":  "xs",
					"color": "#999999",
				},
			},
			"paddingAll": "lg",
		},
		"footer": map[string]any{
			"type":    "box",
			"layout":  "horizontal",
			"spacing": "sm",
			"contents": []any{
				map[string]any{
					"type":   "button",
					"style":  "primary",
					"color":  "#2C6E49",
					"height": "sm",
					"flex":   2,
					"action": map[string]any{
						"type":  "uri",
						"label": "🔗 開啟連結",
						"uri":   url,
					},
				},
				map[string]any{
					"type":   "button",
					"style":  "secondary",
					"height": "sm",
					"flex":   1,
					"action": map[string]any{
						"type":        "postback",
						"label":       "🗑 刪除",
						"data":        string(delData),
						"displayText": "🗑 刪除書籤",
					},
				},
			},
			"paddingAll": "md",
		},
	}
}

// BuildBookmarkCarousel wraps multiple bookmark bubbles into a Flex carousel.
func BuildBookmarkCarousel(bookmarks []map[string]any) map[string]any {
	contents := make([]any, 0, len(bookmarks))
	for _, b := range bookmarks {
		contents = append(contents, BuildBookmarkBubble(b))
	}
	return map[string]any{
		"type":     "carousel",
		"contents": contents,
	}
}

func buildPlatformBubble(headerText, color, bodyText, copyLabel string) map[string]any {
	return map[string]any{
		"type": "bubble",
		"size": "mega",
		"header": map[string]any{
			"type":   "box",
			"layout": "vertical",
			"contents": []any{
				map[string]any{
					"type":   "text",
					"text":   headerText,
					"weight": "bold",
					"color":  "#ffffff",
					"size":   "lg",
				},
			},
			"backgroundColor": color,
			"paddingAll":      "md",
		},
		"body": map[string]any{
			"type":   "box",
			"layout": "vertical",
			"contents": []any{
				map[string]any{
					"type":  "text",
					"text":  bodyText,
					"wrap":  true,
					"size":  "sm",
					"color": "#333333",
				},
			},
			"paddingAll": "lg",
		},
		"footer": map[string]any{
			"type":   "box",
			"layout": "vertical",
			"contents": []any{
				map[string]any{
					"type":   "button",
					"style":  "primary",
					"color":  color,
					"height": "sm",
					"action": map[string]any{
						"type":          "clipboard",
						"label":         copyLabel,
						"clipboardText": bodyText,
					},
				},
			},
			"paddingAll": "md",
		},
	}
}

// BuildSocialCarousel creates the 4-bubble carousel (Summary + FB + LinkedIn + Threads).
func BuildSocialCarousel(title, summaryAnalysis, url, docID, fbText, liText, thText string) map[string]any {
	summaryBubble := BuildSummaryBubble(title, summaryAnalysis, url, docID)
	fbBubble := buildPlatformBubble("📘 Facebook 爆款文案", "#1877F2", fbText, "📋 複製 FB 文案")
	liBubble := buildPlatformBubble("💼 LinkedIn 專業貼文", "#0A66C2", liText, "📋 複製 LinkedIn 文案")
	thBubble := buildPlatformBubble("💬 Threads 脆友討論", "#000000", thText, "📋 複製 Threads 文案")

	return map[string]any{
		"type": "carousel",
		"contents": []any{
			summaryBubble,
			fbBubble,
			liBubble,
			thBubble,
		},
	}
}

// GitHubTrendFlexItem holds display fields for a single trending repository in a LINE Flex Message.
type GitHubTrendFlexItem struct {
	Rank          int
	Name          string
	URL           string
	Language      string
	TotalStars    string
	StarsAdded    string
	Status        string // "NEW", "RISING", "CONTINUOUS"
	PreviousRank  int
	Description   string
	AIHighlight   string
	Topics        []string
	License       string
	LatestRelease string
}

func trendStatusLabelAndColor(status string, prevRank int) (string, string) {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "RISING":
		if prevRank > 0 {
			return fmt.Sprintf("🔥 排名上升 (上週 #%d)", prevRank), "#D97706"
		}
		return "🔥 排名上升", "#D97706"
	case "CONTINUOUS":
		if prevRank > 0 {
			return fmt.Sprintf("🏆 持續霸榜 (上週 #%d)", prevRank), "#0969DA"
		}
		return "🏆 持續霸榜", "#0969DA"
	default:
		return "🆕 本週新進", "#2DA44E"
	}
}

// BuildGitHubTrendsOverviewBubble constructs the first overview bubble in the GitHub Trends Flex Carousel.
func BuildGitHubTrendsOverviewBubble(dateStr, macroSummary, reportURL string, items []GitHubTrendFlexItem) map[string]any {
	if dateStr == "" {
		dateStr = time.Now().Format("2006-01-02")
	}
	if strings.TrimSpace(macroSummary) == "" {
		macroSummary = "本週 GitHub Trending Top 10 熱門開源專案趨勢分析已出爐，向右滑動卡片查看各專案亮點！"
	}

	newCount, risingCount, contCount := 0, 0, 0
	for _, it := range items {
		switch strings.ToUpper(strings.TrimSpace(it.Status)) {
		case "RISING":
			risingCount++
		case "CONTINUOUS":
			contCount++
		default:
			newCount++
		}
	}

	bodyContents := []any{
		map[string]any{
			"type":   "text",
			"text":   "🌐 本週開源趨勢洞察",
			"weight": "bold",
			"size":   "md",
			"color":  "#111111",
		},
		map[string]any{
			"type":  "text",
			"text":  snippet(macroSummary, 280),
			"wrap":  true,
			"size":  "sm",
			"color": "#333333",
		},
		map[string]any{
			"type":   "separator",
			"margin": "md",
		},
		map[string]any{
			"type":   "box",
			"layout": "horizontal",
			"margin": "md",
			"contents": []any{
				map[string]any{
					"type":  "text",
					"text":  fmt.Sprintf("🆕 新進 %d", newCount),
					"size":  "xs",
					"color": "#2DA44E",
					"flex":  1,
				},
				map[string]any{
					"type":  "text",
					"text":  fmt.Sprintf("🔥 上升 %d", risingCount),
					"size":  "xs",
					"color": "#D97706",
					"flex":  1,
				},
				map[string]any{
					"type":  "text",
					"text":  fmt.Sprintf("🏆 霸榜 %d", contCount),
					"size":  "xs",
					"color": "#0969DA",
					"flex":  1,
				},
			},
		},
	}

	if len(items) > 0 {
		bodyContents = append(bodyContents, map[string]any{
			"type":   "text",
			"text":   "🏆 本週前三名速覽",
			"weight": "bold",
			"size":   "sm",
			"color":  "#111111",
			"margin": "md",
		})
		limit := len(items)
		if limit > 3 {
			limit = 3
		}
		medals := []string{"🥇", "🥈", "🥉"}
		for i := 0; i < limit; i++ {
			it := items[i]
			bodyContents = append(bodyContents, map[string]any{
				"type":  "text",
				"text":  fmt.Sprintf("%s %s (%s)", medals[i], it.Name, it.Language),
				"size":  "xs",
				"color": "#444444",
				"wrap":  true,
			})
		}
	}

	var footerContents []any
	if strings.TrimSpace(reportURL) != "" {
		footerContents = append(footerContents, map[string]any{
			"type":   "button",
			"style":  "primary",
			"color":  "#24292F",
			"height": "sm",
			"action": map[string]any{
				"type":  "uri",
				"label": "📄 查看完整研析報告",
				"uri":   reportURL,
			},
		})
		footerContents = append(footerContents, map[string]any{
			"type":   "button",
			"style":  "link",
			"height": "sm",
			"action": map[string]any{
				"type":  "uri",
				"label": "🔗 前往 GitHub Trending",
				"uri":   "https://github.com/trending?since=weekly",
			},
		})
	} else {
		footerContents = append(footerContents, map[string]any{
			"type":   "button",
			"style":  "primary",
			"color":  "#24292F",
			"height": "sm",
			"action": map[string]any{
				"type":  "uri",
				"label": "🔗 前往 GitHub Trending",
				"uri":   "https://github.com/trending?since=weekly",
			},
		})
	}

	return map[string]any{
		"type": "bubble",
		"size": "mega",
		"header": map[string]any{
			"type":            "box",
			"layout":          "vertical",
			"backgroundColor": "#24292F",
			"paddingAll":      "md",
			"contents": []any{
				map[string]any{
					"type":   "text",
					"text":   "🚀 GitHub 每週趨勢週報",
					"weight": "bold",
					"color":  "#FFFFFF",
					"size":   "lg",
				},
				map[string]any{
					"type":  "text",
					"text":  fmt.Sprintf("📅 %s ｜ Top %d 熱門專案", dateStr, len(items)),
					"color": "#D0D7DE",
					"size":  "xs",
				},
			},
		},
		"body": map[string]any{
			"type":       "box",
			"layout":     "vertical",
			"spacing":    "sm",
			"paddingAll": "lg",
			"contents":   bodyContents,
		},
		"footer": map[string]any{
			"type":       "box",
			"layout":     "vertical",
			"spacing":    "sm",
			"paddingAll": "md",
			"contents":   footerContents,
		},
	}
}

// BuildGitHubTrendRepoBubble constructs a Flex Bubble card for a single GitHub Trending repository.
func BuildGitHubTrendRepoBubble(item GitHubTrendFlexItem) map[string]any {
	statusLabel, statusColor := trendStatusLabelAndColor(item.Status, item.PreviousRank)

	repoName := item.Name
	if repoName == "" {
		repoName = "unknown/repo"
	}
	repoURL := item.URL
	if repoURL == "" {
		repoURL = "https://github.com/" + strings.TrimPrefix(repoName, "/")
	}
	lang := item.Language
	if lang == "" {
		lang = "General"
	}
	starsText := fmt.Sprintf("⭐ %s", item.TotalStars)
	if item.StarsAdded != "" {
		starsText = fmt.Sprintf("⭐ %s (%s)", item.TotalStars, item.StarsAdded)
	}

	highlight := strings.TrimSpace(item.AIHighlight)
	if highlight == "" {
		highlight = strings.TrimSpace(item.Description)
	}
	if highlight == "" {
		highlight = "本週 GitHub 熱門開源專案"
	}

	bodyContents := []any{
		map[string]any{
			"type":   "box",
			"layout": "horizontal",
			"contents": []any{
				map[string]any{
					"type":   "text",
					"text":   fmt.Sprintf("💻 %s", lang),
					"size":   "xs",
					"weight": "bold",
					"color":  "#57606A",
					"flex":   2,
				},
				map[string]any{
					"type":  "text",
					"text":  starsText,
					"size":  "xs",
					"color": "#9A6700",
					"align": "end",
					"flex":  3,
					"wrap":  true,
				},
			},
		},
		map[string]any{
			"type":   "separator",
			"margin": "sm",
		},
		map[string]any{
			"type":   "text",
			"text":   snippet(highlight, 220),
			"wrap":   true,
			"size":   "sm",
			"color":  "#24292F",
			"margin": "md",
		},
	}

	var metaParts []string
	if item.License != "" {
		metaParts = append(metaParts, "📜 "+item.License)
	}
	if item.LatestRelease != "" {
		metaParts = append(metaParts, "🏷️ "+item.LatestRelease)
	}
	if len(item.Topics) > 0 {
		limit := len(item.Topics)
		if limit > 3 {
			limit = 3
		}
		metaParts = append(metaParts, "#"+strings.Join(item.Topics[:limit], " #"))
	}
	if len(metaParts) > 0 {
		bodyContents = append(bodyContents, map[string]any{
			"type":   "text",
			"text":   strings.Join(metaParts, " ｜ "),
			"size":   "xxs",
			"color":  "#6E7781",
			"wrap":   true,
			"margin": "md",
		})
	}

	return map[string]any{
		"type": "bubble",
		"size": "mega",
		"header": map[string]any{
			"type":            "box",
			"layout":          "vertical",
			"backgroundColor": statusColor,
			"paddingAll":      "md",
			"contents": []any{
				map[string]any{
					"type":   "box",
					"layout": "horizontal",
					"contents": []any{
						map[string]any{
							"type":   "text",
							"text":   fmt.Sprintf("#%d", item.Rank),
							"weight": "bold",
							"color":  "#FFFFFF",
							"size":   "md",
							"flex":   1,
						},
						map[string]any{
							"type":  "text",
							"text":  statusLabel,
							"color": "#FFFFFF",
							"size":  "xs",
							"align": "end",
							"flex":  3,
						},
					},
				},
				map[string]any{
					"type":   "text",
					"text":   repoName,
					"weight": "bold",
					"color":  "#FFFFFF",
					"size":   "md",
					"wrap":   true,
				},
			},
		},
		"body": map[string]any{
			"type":       "box",
			"layout":     "vertical",
			"spacing":    "sm",
			"paddingAll": "lg",
			"contents":   bodyContents,
		},
		"footer": map[string]any{
			"type":       "box",
			"layout":     "vertical",
			"spacing":    "sm",
			"paddingAll": "md",
			"contents": []any{
				map[string]any{
					"type":   "button",
					"style":  "primary",
					"color":  "#24292F",
					"height": "sm",
					"action": map[string]any{
						"type":  "uri",
						"label": "🔗 開啟 GitHub 專案",
						"uri":   repoURL,
					},
				},
			},
		},
	}
}

// BuildGitHubTrendsCarousel builds a LINE Flex Carousel containing the Overview Bubble + up to 10 Repo Bubbles.
func BuildGitHubTrendsCarousel(dateStr, macroSummary, reportURL string, items []GitHubTrendFlexItem) map[string]any {
	limit := len(items)
	if limit > 10 {
		limit = 10
	}
	contents := make([]any, 0, limit+1)
	contents = append(contents, BuildGitHubTrendsOverviewBubble(dateStr, macroSummary, reportURL, items[:limit]))
	for i := 0; i < limit; i++ {
		contents = append(contents, BuildGitHubTrendRepoBubble(items[i]))
	}
	return map[string]any{
		"type":     "carousel",
		"contents": contents,
	}
}
