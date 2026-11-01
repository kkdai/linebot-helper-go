package ai

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/kkdai/linebot-helper-go/internal/gemini"
	"github.com/kkdai/linebot-helper-go/internal/loader"
)

var YouTubePrompts = map[string]string{
	"normal": `請用台灣用語的繁體中文總結這部影片。

【輸出格式要求】
1. 不要使用任何 Markdown 語法（如 #, *, **, -, 等）
2. 使用純文字格式，適合直接發送到 LINE Bot
3. 條列式重點使用數字編號（1. 2. 3. ...）
4. 最後附上 3-5 個相關的 hashtag，使用半形 # 符號

【輸出結構】
📹 影片摘要

1. [第一個重點]
2. [第二個重點]
3. [第三個重點]
（依影片內容調整重點數量，建議 3-6 點）

🏷️ 標籤
#關鍵字1 #關鍵字2 #關鍵字3

【注意事項】
- 每個重點簡短有力，一行為限
- 標籤要符合台灣常用習慣
- 不要使用任何 markdown 格式符號
`,

	"detail": `請用台灣用語的繁體中文提供這部影片的詳細摘要（至少 300 字）。

【輸出格式要求】
1. 不要使用任何 Markdown 語法（如 #, *, **, -, 等）
2. 使用純文字格式，適合直接發送到 LINE Bot
3. 針對影片的每個主要段落進行整理

【輸出結構】
📹 影片詳細分析

▶️ 開場/前言
[整理開場內容，說明影片的主旨和背景]

▶️ 主要內容
[針對影片的核心內容進行段落式整理，每個重點段落都要詳細說明]

▶️ 結論/收尾
[整理影片的結論或總結]

💡 我的觀察
[從整體來看這部影片的價值、特色、適合觀眾等]

🏷️ 標籤
#關鍵字1 #關鍵字2 #關鍵字3

【注意事項】
- 內容要超過 300 字
- 段落間要有適當的分隔
- 不要使用任何 markdown 格式符號
`,

	"twitter": `請用台灣用語的繁體中文，將這部影片改寫成適合在 Twitter/X 發布的宣傳文案。

【輸出格式要求】
1. 不要使用任何 Markdown 語法（如 #, *, **, -, 等）
2. 使用純文字格式
3. 內容要吸引人點擊觀看
4. 字數控制在 200 字以內（不含 hashtag）
5. 語氣要輕鬆有趣，能引起共鳴

【輸出結構】
🐦 推薦分享

[用 2-3 句話說明為什麼要看這部影片]

💬 我的想法
[用 1-2 句話分享你的觀點或感想]

📺 影片重點
• [重點 1]
• [重點 2]
• [重點 3]

🔗 值得一看！

#關鍵字1 #關鍵字2 #關鍵字3 #關鍵字4 #關鍵字5

【注意事項】
- 語氣要親切有趣
- 重點要簡潔有力
- hashtag 要選擇熱門且相關的
- 不要使用任何 markdown 格式符號
`,
}

// DefaultImagePrompt is used when no custom prompt is provided for image analysis.
const DefaultImagePrompt = `請詳細描述這張圖片的內容，包括：
1. 主要物件或人物
2. 場景或背景
3. 任何可見的文字
4. 整體氛圍或情境

請使用台灣用語的繁體中文回答。`

// SummarizeYouTubeVideo summarizes a YouTube video using Vertex AI with 429 retry logic.
func SummarizeYouTubeVideo(ctx context.Context, client *gemini.Client, youtubeURL, mode string) (string, error) {
	if youtubeURL == "" {
		return "", fmt.Errorf("No YouTube URL provided")
	}
	if !loader.IsYouTubeURL(youtubeURL) {
		return "", fmt.Errorf("Invalid YouTube URL: %s", youtubeURL)
	}
	prompt, ok := YouTubePrompts[mode]
	if !ok {
		prompt = YouTubePrompts["normal"]
	}

	maxRetries := 2
	retryDelay := 5 * time.Second

	for attempt := 0; attempt < maxRetries; attempt++ {
		res, err := client.GenerateContent(ctx, gemini.GenerateOptions{
			Model: "gemini-3.1-flash-lite",
			Contents: []gemini.Content{
				{
					Role: "user",
					Parts: []gemini.Part{
						gemini.NewURIPart(youtubeURL, "video/mp4"),
						gemini.NewTextPart(prompt),
					},
				},
			},
		})
		if err == nil {
			if strings.TrimSpace(res.Text) == "" {
				return "", fmt.Errorf("No summary content generated")
			}
			return res.Text, nil
		}

		var statusErr *gemini.StatusError
		if errors.As(err, &statusErr) && statusErr.StatusCode == 429 {
			if attempt < maxRetries-1 {
				slog.Warn("Rate limit hit (429), retrying YouTube summary", "delay", retryDelay, "attempt", attempt+1)
				select {
				case <-ctx.Done():
					return "", ctx.Err()
				case <-time.After(retryDelay):
				}
				retryDelay *= 2
				continue
			}
			return "", fmt.Errorf("Vertex AI 使用量已達上限，請稍後再試。建議等待 1-2 分鐘後重試。")
		}
		return "", err
	}
	return "", fmt.Errorf("處理影片時發生未預期的錯誤")
}

// AnalyzeImage analyzes an image with Gemini multimodal capabilities.
func AnalyzeImage(ctx context.Context, client *gemini.Client, imageData []byte, prompt string) (string, error) {
	if len(imageData) == 0 {
		return "", fmt.Errorf("No image data provided")
	}
	if strings.TrimSpace(prompt) == "" {
		prompt = DefaultImagePrompt
	}
	temp := float32(0.5)
	zeroBudget := 0

	res, err := client.GenerateContent(ctx, gemini.GenerateOptions{
		Model:           "gemini-3.1-flash-lite",
		Temperature:     &temp,
		MaxOutputTokens: 8192,
		ThinkingBudget:  &zeroBudget,
		Contents: []gemini.Content{
			{
				Role: "user",
				Parts: []gemini.Part{
					gemini.NewTextPart(prompt),
					gemini.NewBytesPart(imageData, "image/png"),
				},
			},
		},
	})
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(res.Text) == "" {
		return "", fmt.Errorf("No analysis generated")
	}
	return res.Text, nil
}

// AnalyzeImageAgentic analyzes an image using gemini-3-flash-preview with code execution.
func AnalyzeImageAgentic(ctx context.Context, client *gemini.Client, imageData []byte, prompt string) (string, [][]byte, error) {
	if len(imageData) == 0 {
		return "", nil, fmt.Errorf("No image data provided")
	}
	if strings.TrimSpace(prompt) == "" {
		prompt = "請仔細分析這張圖片，使用 agentic vision 能力來深入檢視細節，包括放大、裁切、標註等。使用繁體中文回答。"
	}
	temp := float32(0.5)
	budget := 2048

	res, err := client.GenerateContent(ctx, gemini.GenerateOptions{
		Model:               "gemini-3-flash-preview",
		Temperature:         &temp,
		MaxOutputTokens:     4096,
		EnableCodeExecution: true,
		ThinkingBudget:      &budget,
		Contents: []gemini.Content{
			{
				Role: "user",
				Parts: []gemini.Part{
					gemini.NewTextPart(prompt),
					gemini.NewBytesPart(imageData, "image/png"),
				},
			},
		},
	})
	if err != nil {
		return "", nil, err
	}
	analysis := strings.TrimSpace(res.Text)
	if analysis == "" && len(res.Images) == 0 {
		return "", nil, fmt.Errorf("No analysis generated")
	}
	if analysis == "" {
		analysis = "（圖片已標註完成）"
	}
	return analysis, res.Images, nil
}

// TranscribeAudio transcribes audio bytes (m4a from LINE) using Gemini.
func TranscribeAudio(ctx context.Context, client *gemini.Client, audioBytes []byte, mimeType string) (string, error) {
	if mimeType == "" {
		mimeType = "audio/mp4"
	}
	res, err := client.GenerateContent(ctx, gemini.GenerateOptions{
		Model: "gemini-3.1-flash-lite",
		Contents: []gemini.Content{
			{
				Role: "user",
				Parts: []gemini.Part{
					gemini.NewBytesPart(audioBytes, mimeType),
					gemini.NewTextPart("請將以上語音內容完整轉錄成文字，保留原語言，不要加任何說明或前綴。"),
				},
			},
		},
	})
	if err != nil {
		return "", err
	}
	return res.Text, nil
}
