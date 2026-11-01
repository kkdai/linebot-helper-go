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
