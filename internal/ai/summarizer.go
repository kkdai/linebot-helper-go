package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/kkdai/linebot-helper-go/internal/gemini"
)

var SummarizePrompts = map[string]string{
	"short": `用台灣用語的繁體中文，用 1-3 個重點總結文章核心內容。務必極度簡潔。

原文： "{text}"

# 要求
- 只列出 1-3 個最關鍵重點
- 每個重點不超過 15 字
- 直接列出重點，不需要前言
- 結尾加入 2-3 個英文 hashtag

# 範例輸出：
- AI 技術快速發展
- 影響就業市場
- 需要政策規範
#AI #Technology #Policy`,

	"normal": `用台灣用語的繁體中文，簡潔地以條列式總結文章重點。在摘要後直接加入相關的英文 hashtag，以空格分隔。內容來源可以是網頁、文章、論文、影片字幕或逐字稿。

原文： "{text}"
請遵循以下步驟來完成此任務：

# 步驟
1. 從提供的內容中提取重要重點，無論來源是網頁、文章、論文、影片字幕或逐字稿。
2. 將重點整理成條列式，確保每一點為簡短且明確的句子。
3. 使用符合台灣用語的簡潔繁體中文。
4. 在摘要結尾處，加入至少三個相關的英文 hashtag，並以空格分隔。

# 輸出格式
- 重點應以條列式列出，每一點應為一個短句或片語，語言必須簡潔明瞭。
- 最後加入至少三個相關的英文 hashtag，每個 hashtag 之間用空格分隔。

# 範例
輸入：
文章內容：
台灣的報告指出，環境保護的重要性日益增加。許多人開始選擇使用可重複使用的產品。政府也實施了多項政策來降低廢物。

摘要：

輸出：
- 環境保護重要性增加
- 越來越多人使用可重複產品
- 政府實施減廢政策
#EnvironmentalProtection #Sustainability #Taiwan

reply in zh-TW`,

	"detailed": `用台灣用語的繁體中文，詳細地以條列式總結文章內容，包含背景、主要論點、細節和結論。

原文： "{text}"

# 要求
1. 提供完整的文章背景和上下文
2. 詳細列出所有重要論點和細節
3. 包含具體的數據、案例或例子（如果有）
4. 分析文章的結論和影響
5. 使用台灣用語的繁體中文
6. 結尾加入相關的英文 hashtag

# 輸出格式

【背景】
- 提供文章背景和上下文

【主要內容】
- 詳細列出所有重要論點
- 包含具體細節和數據
- 列出關鍵案例或例子

【結論與影響】
- 總結文章結論
- 分析可能的影響

#Hashtag1 #Hashtag2 #Hashtag3

reply in zh-TW`,
}

// HumanizeGuidelines contains the speak-human-tw guidelines from loader/langtools.py.
const HumanizeGuidelines = `# 人性化守則（最高優先，凌駕以下排版技巧）：
寫得像一個真實、有觀點的台灣人，而不是 AI 在「生成內容」。順序：先保事實 → 再去 AI 味 → 最後加人味。

## A. 先刪（刪掉不用補）
1. AI 開場定型句／時代大帽子：不要用「在當今這個時代」「隨著 AI 快速發展」「讓我們一起來看看」「老實說？」「你有沒有想過…」。第一句就該有只有這篇文章才有的資訊。
2. 對話殘留與諂媚：不要「好問題！」「希望這對你有幫助」「以下是為你整理的…」。
3. 通用積極結論、罐頭收尾：不要「未來充滿無限可能」「讓我們一起邁向…」「總的來說」「綜上所述」。允許停在最後一個具體句子上，不必補新結尾。

## B. 再具體化（寫不出具體就刪，不要換句空話）
4. 誇大意義詞落地：「標誌著／見證了／奠定基礎／體現了／不僅僅是」改成具體事實，寫不出來就刪。
5. 假推論與無源權威：「這意味著…」問得出誰在推論、根據什麼再留；「研究顯示／業界專家認為」沒出處就刪，不要編造來源或數字。
6. 補立場：「各有優缺點／因人而異／取決於多方面因素」代表整段沒判斷，改成明確選擇與理由。
7. 具體 ＞ 抽象：能講「一個月省 3 小時」就不要講「大幅提升效率」。

## C. 再降格式
8. 「不是 A，而是 B」與「不僅…更…」整篇最多各一次，其餘改直述。
9. 「首先／其次／最後」不硬湊三點：需要幾點寫幾點，結構服從邏輯不服從對稱。
10. 破折號每 300–500 字最多 1 次；粗體一段最多 2–3 個詞。

## D. 台灣在地化（AI 產繁中最容易露餡處）
11. 中國用語一律替換：視頻→影片、質量→品質、信息→資訊、網絡→網路、軟件→軟體、水平→水準、立馬→馬上、默認→預設、反饋→回饋、支持(功能)→支援、性價比→CP值、給力→很到位、靠譜→可靠、接地氣→貼近日常；「賦能／閉環／抓手」這類整句重寫成「讓誰能做到什麼」。
12. 全形標點：中文句一律「，。：；！？「」（）、」；刪節號用「⋯⋯」不用「...」；並列用頓號「、」。
13. 台灣語氣詞：社群口語可用「喔／耶／啦／欸／齁」收尾；砍中國腔「哈／好噠／是滴」。量詞用台灣慣用（一部影片、一支手機、一則貼文）。

## E. 加人味（乾淨只是及格線）
14. 對事實做出反應，不只報告事實；適當用第一人稱「我」，那是誠實不是不專業。
15. 句長長短交錯、允許輕微不對稱，最有話說的那點給兩倍篇幅。
16. 但人味是作者的：不要替作者發明沒說過的經歷、故事或立場。

（emoji 用量依各平台指南處理，共同原則是調味不是裝飾、不要每行都放。）
`

// SummarizeText summarizes text using the specified mode ("short", "normal", "detailed").
func SummarizeText(ctx context.Context, client *gemini.Client, text, mode string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("No text content provided for summarization")
	}
	tpl, ok := SummarizePrompts[mode]
	if !ok {
		tpl = SummarizePrompts["normal"]
	}
	prompt := strings.ReplaceAll(tpl, "{text}", text)
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
		return "", fmt.Errorf("No summary generated")
	}
	return res.Text, nil
}

// BuildSocialMediaPrompt builds the prompt for generating viral social media posts.
func BuildSocialMediaPrompt(text string) string {
	return fmt.Sprintf(`請針對以下網頁內容，完成兩件事：
1. 產出文章標題（title，15 字內）與「摘要與重點分析」（summary_analysis，150-250 字：先 2-3 句摘要核心內容，再 2-3 句分析重點與為什麼值得讀。此欄位是給讀者快速理解文章用的，語氣中性直述即可，不是社群貼文）。
2. 為三個不同的社群平台（Facebook、LinkedIn、Meta Threads）各撰寫一篇容易「爆款」（高互動、高分享、吸引眼球）的繁體中文（台灣用語）分享貼文。

網頁內容：
%s

%s
# 寫作指南：

## 1. Facebook 爆款貼文：
- 吸引人的 Hook：第一句話必須非常吸睛，善用好奇心、痛點或誇張的開頭。但依人性化守則，開場改用「真實痛點／真實場景」，禁止假掰誇張詞。
- 版面排版：多用 Emoji，段落清晰，使用條列式（Bullet points）整理核心觀點。
- 呼籲行動（CTA）：結尾提出一個好回答的問題，引導讀者留言或分享。
- Hashtags：加入 3-5 個相關的熱門 Hashtag。
- 長度：約 200-400 字。

## 2. LinkedIn 專業貼文：
- 專業 Hook：第一句從商業洞察、職場學習、趨勢分析或個人省思出發。
- 內容結構：語氣專業、理性，分享文章的核心價值、給職場人士或企業的具體 Takeaways。此平台人性化守則權重高：專業不等於八股，要像一個有實戰經驗的真人在分享觀點，可用第一人稱與具體經歷。禁止 buzzword 空堆（如「賦能」「數位轉型」「無縫接軌」連發），並嚴禁 AI 正式腔套語（如「值得我們深入探討」「至關重要」「不容忽視的現象」「在當今這個時代」）。每個 Takeaway 要具體可執行。
- 呼籲行動：徵求專業意見或開啟思辨討論，例如：「你怎麼看這個趨勢？」
- Hashtags：加入 3-5 個專業領域的 Hashtag。
- 長度：約 300-500 字。

## 3. Meta Threads 脆友討論：
- Threads 脆友 Hook：極度口語化、像跟朋友講話，第一句要帶有強烈共鳴、槽點、吐槽、或一針見血的觀點。
- 內容風格：段落極短（每段 1-2 句話），善用白話文、網路用語或迷因感。以分享八卦、大實話或內行人才懂的梗為佳。此平台人性化守則權重最高：要像真的脆友在講話，容許不完美、口語破碎感，善用台灣語氣詞（喔／耶／啦／欸／齁）收尾，嚴禁任何 AI 腔。emoji 收斂，整篇最多 1 個。
- 呼籲行動：隨性引導留言，例如：「有人也是這樣嗎？」
- Hashtags：不使用或僅使用 1 個 Hashtag。
- 長度：約 150-300 字。
`, text, HumanizeGuidelines)
}

// SocialMediaPosts holds the structured JSON output for social media copy generation.
type SocialMediaPosts struct {
	Title           string `json:"title"`
	SummaryAnalysis string `json:"summary_analysis"`
	Facebook        string `json:"facebook"`
	LinkedIn        string `json:"linkedin"`
	Threads         string `json:"threads"`
}

// GenerateSocialMediaPosts generates structured social media copy for FB, LinkedIn, and Threads.
func GenerateSocialMediaPosts(ctx context.Context, client *gemini.Client, text string) SocialMediaPosts {
	if strings.TrimSpace(text) == "" {
		return SocialMediaPosts{
			Title:           "無法取得網頁內容",
			SummaryAnalysis: "無法取得網頁內容，無法產生摘要。",
			Facebook:        "無法取得網頁內容，無法產生文案。",
			LinkedIn:        "無法取得網頁內容，無法產生文案。",
			Threads:         "無法取得網頁內容，無法產生文案。",
		}
	}

	prompt := BuildSocialMediaPrompt(text)
	temp := float32(0.7)
	schema := map[string]any{
		"type": "OBJECT",
		"properties": map[string]any{
			"title":            map[string]any{"type": "STRING"},
			"summary_analysis": map[string]any{"type": "STRING"},
			"facebook":         map[string]any{"type": "STRING"},
			"linkedin":         map[string]any{"type": "STRING"},
			"threads":          map[string]any{"type": "STRING"},
		},
		"required": []string{"title", "summary_analysis", "facebook", "linkedin", "threads"},
	}

	res, err := client.GenerateContent(ctx, gemini.GenerateOptions{
		Model:            "gemini-3.1-flash-lite",
		Temperature:      &temp,
		MaxOutputTokens:  8192,
		ResponseMimeType: "application/json",
		ResponseSchema:   schema,
		Contents: []gemini.Content{
			{Role: "user", Parts: []gemini.Part{gemini.NewTextPart(prompt)}},
		},
	})
	if err == nil && strings.TrimSpace(res.Text) != "" {
		var posts SocialMediaPosts
		if uErr := json.Unmarshal([]byte(res.Text), &posts); uErr == nil {
			return posts
		}
		err = fmt.Errorf("failed to parse social media JSON")
	}

	slog.Error("Error generating social media posts", "error", err)
	errStr := ""
	if err != nil {
		errStr = err.Error()
		if len(errStr) > 100 {
			errStr = errStr[:100]
		}
	}
	return SocialMediaPosts{
		Title:           "摘要生成失敗",
		SummaryAnalysis: fmt.Sprintf("生成摘要失敗：%s", errStr),
		Facebook:        fmt.Sprintf("生成 Facebook 文案失敗：%s", errStr),
		LinkedIn:        fmt.Sprintf("生成 LinkedIn 文案失敗：%s", errStr),
		Threads:         fmt.Sprintf("生成 Threads 文案失敗：%s", errStr),
	}
}

// BookmarkSummary holds the title and summary for /save bookmark command.
type BookmarkSummary struct {
	Title   string `json:"title"`
	Summary string `json:"summary"`
}

// SummarizeForBookmark generates a title and summary analysis for the /save bookmark command.
func SummarizeForBookmark(ctx context.Context, client *gemini.Client, text string) BookmarkSummary {
	if strings.TrimSpace(text) == "" {
		return BookmarkSummary{
			Title:   "無法取得網頁內容",
			Summary: "無法取得網頁內容，無法產生摘要。",
		}
	}

	prompt := fmt.Sprintf(`請針對以下網頁內容，產出文章標題（title，15 字內）與「摘要與重點分析」
（summary，150-250 字繁體中文台灣用語：先 2-3 句摘要核心內容，再 2-3 句分析重點
與值得注意之處。純文字，不用 markdown 符號）。

網頁內容：
%s`, text)

	temp := float32(0.3)
	schema := map[string]any{
		"type": "OBJECT",
		"properties": map[string]any{
			"title":   map[string]any{"type": "STRING"},
			"summary": map[string]any{"type": "STRING"},
		},
		"required": []string{"title", "summary"},
	}

	res, err := client.GenerateContent(ctx, gemini.GenerateOptions{
		Model:            "gemini-3.1-flash-lite",
		Temperature:      &temp,
		MaxOutputTokens:  4096,
		ResponseMimeType: "application/json",
		ResponseSchema:   schema,
		Contents: []gemini.Content{
			{Role: "user", Parts: []gemini.Part{gemini.NewTextPart(prompt)}},
		},
	})
	if err == nil && strings.TrimSpace(res.Text) != "" {
		var bs BookmarkSummary
		if uErr := json.Unmarshal([]byte(res.Text), &bs); uErr == nil {
			return bs
		}
		err = fmt.Errorf("failed to parse bookmark summary JSON")
	}

	slog.Error("Error generating bookmark summary", "error", err)
	errStr := ""
	if err != nil {
		errStr = err.Error()
		if len(errStr) > 100 {
			errStr = errStr[:100]
		}
	}
	return BookmarkSummary{
		Title:   "摘要生成失敗",
		Summary: fmt.Sprintf("生成摘要失敗：%s", errStr),
	}
}

// ResearchReportResult holds the Markdown report and cited sources.
type ResearchReportResult struct {
	Markdown string
	Sources  []gemini.GroundingSource
}

// GenerateResearchReport deep-dives an article with Google Search Grounding (falling back to non-grounded on tool failure).
func GenerateResearchReport(ctx context.Context, client *gemini.Client, text, url string) (*ResearchReportResult, error) {
	if strings.TrimSpace(text) == "" {
		return &ResearchReportResult{}, nil
	}

	prompt := fmt.Sprintf(`你是一位嚴謹的研究分析師。請針對以下文章內容撰寫一份詳細的研究報告，
繁體中文（台灣用語），Markdown 格式（從 ## 層級開始，不要放文章大標題）。

必要結構：
## 執行摘要（3-5 句話講清楚這篇在說什麼、為什麼重要）
## 背景脈絡（這個主題的來龍去脈，搭配你搜尋到的相關資訊）
## 核心論點與證據（逐點整理文章的主張與支撐證據，標注證據強弱）
## 數據與事實整理（文中的關鍵數字、日期、人物、機構，用表格或清單）
## 對照觀點與批判（搜尋相關報導，比對其他觀點；指出文章的盲點、假設或爭議）
## 延伸問題（3-5 個值得進一步追究的問題）

要求：
- 請主動搜尋補充文章外的背景與對照資訊，並在內文標注資訊來自搜尋還是原文
- 具體優於抽象；沒有根據的推論明確標注「推測」
- 全形標點，不要 AI 腔套語

原文網址：%s

文章內容：
%s`, url, text)

	call := func(withGrounding bool) (*gemini.GenerateResult, error) {
		temp := float32(0.4)
		return client.GenerateContent(ctx, gemini.GenerateOptions{
			Model:              "gemini-3.1-flash-lite",
			Temperature:        &temp,
			MaxOutputTokens:    16384,
			EnableGoogleSearch: withGrounding,
			Contents: []gemini.Content{
				{Role: "user", Parts: []gemini.Part{gemini.NewTextPart(prompt)}},
			},
		})
	}

	res, err := call(true)
	if err != nil {
		slog.Warn("Grounded research call failed, retrying without tools", "error", err)
		res, err = call(false)
	}
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(res.Text) == "" {
		return nil, fmt.Errorf("Empty response text from Gemini")
	}
	return &ResearchReportResult{
		Markdown: res.Text,
		Sources:  res.Sources,
	}, nil
}
