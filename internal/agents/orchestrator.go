package agents

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"

	"github.com/kkdai/linebot-helper-go/internal/ai"
	"github.com/kkdai/linebot-helper-go/internal/config"
	"github.com/kkdai/linebot-helper-go/internal/gemini"
	"github.com/kkdai/linebot-helper-go/internal/loader"
	"github.com/kkdai/linebot-helper-go/internal/services"
)

// IntentType represents the type of user intent detected by the Orchestrator.
type IntentType string

const (
	IntentChat             IntentType = "chat"
	IntentURLSummary       IntentType = "url_summary"
	IntentYouTubeSummary   IntentType = "youtube"
	IntentImageAnalysis    IntentType = "image"
	IntentLocationSearch   IntentType = "location"
	IntentGitHubSummary    IntentType = "github"
	IntentCommand          IntentType = "command"
	IntentRestaurantSearch IntentType = "restaurant_search"
	IntentUnknown          IntentType = "unknown"
)

// Intent represents a detected user intent.
type Intent struct {
	Type       IntentType
	Confidence float64
	Data       map[string]string
}

// AgentResponse holds the standardized response from any specialist agent or command handler.
type AgentResponse struct {
	Status          string
	Intent          string
	Response        string
	Answer          string
	Content         string
	Summary         string
	Places          string
	Analysis        string
	URL             string
	ContentType     string
	Mode            string
	ErrorMessage    string
	HasHistory      bool
	NeedLocation    bool
	HasLocation     bool
	Latitude        float64
	Longitude       float64
	Address         string
	Sources         []gemini.GroundingSource
	RestaurantNames []string
	Images          [][]byte
}

// OrchestratorResult holds the aggregated result from Orchestrator processing.
type OrchestratorResult struct {
	Success   bool
	Responses []AgentResponse
	Intents   []Intent
	Error     string
}

// ChatAgentInstruction defines the system prompt for ChatAgent.
const ChatAgentInstruction = `你是一個智能助手，專門回答用戶的問題。

## 回應原則
1. 使用台灣用語的繁體中文回答
2. 如果需要最新資訊，請搜尋網路並提供準確的答案
3. 提供詳細且有用的回答，確保資訊來源可靠
4. 不要使用 markdown 格式（不要用 **、##、- 等符號），使用純文字回答
5. 回答要簡潔但完整，適合在 LINE 訊息中閱讀

## 回應格式
- 直接回答問題，不需要開場白
- 如果有多個重點，使用數字編號（1. 2. 3.）
- 適當使用換行來提高可讀性
`

// ChatAgent handles conversational Q&A with session memory and Google Search Grounding.
type ChatAgent struct {
	Config         *config.Config
	Client         *gemini.Client
	SessionManager *services.SessionManager
}

// NewChatAgent creates a new ChatAgent.
func NewChatAgent(cfg *config.Config, client *gemini.Client, sm *services.SessionManager) *ChatAgent {
	return &ChatAgent{
		Config:         cfg,
		Client:         client,
		SessionManager: sm,
	}
}

// Chat processes a user message within their conversation session.
func (a *ChatAgent) Chat(ctx context.Context, userID, message string) AgentResponse {
	session := a.SessionManager.GetOrCreateSession(userID)
	priorHistory := a.SessionManager.GetHistory(userID)

	var contents []gemini.Content
	for _, msg := range priorHistory {
		if strings.TrimSpace(msg.Content) == "" {
			continue
		}
		role := "user"
		if msg.Role != "user" {
			role = "model"
		}
		contents = append(contents, gemini.Content{
			Role:  role,
			Parts: []gemini.Part{gemini.NewTextPart(msg.Content)},
		})
	}

	prompt := fmt.Sprintf("%s\n\n問題：%s", ChatAgentInstruction, message)
	contents = append(contents, gemini.Content{
		Role:  "user",
		Parts: []gemini.Part{gemini.NewTextPart(prompt)},
	})

	temp := a.Config.Temperature
	model := a.Config.ChatModel
	if model == "" {
		model = "gemini-3.1-flash-lite"
	}
	maxTokens := a.Config.MaxOutputTokens
	if maxTokens <= 0 {
		maxTokens = 2048
	}

	res, err := a.Client.GenerateContent(ctx, gemini.GenerateOptions{
		Model:              model,
		Temperature:        &temp,
		MaxOutputTokens:    maxTokens,
		EnableGoogleSearch: a.Config.EnableGrounding,
		Contents:           contents,
	})
	if err != nil {
		errMsg := err.Error()
		if len(errMsg) > 100 {
			errMsg = errMsg[:100]
		}
		return AgentResponse{
			Status:       "error",
			Intent:       "chat",
			ErrorMessage: fmt.Sprintf("對話處理失敗: %s", errMsg),
		}
	}
	if strings.TrimSpace(res.Text) == "" {
		return AgentResponse{
			Status:       "error",
			Intent:       "chat",
			ErrorMessage: "對話處理失敗: API returned empty response",
		}
	}

	a.SessionManager.AddToHistory(session.UserID, "user", message)
	a.SessionManager.AddToHistory(session.UserID, "assistant", res.Text)

	return AgentResponse{
		Status:     "success",
		Intent:     "chat",
		Answer:     res.Text,
		Sources:    res.Sources,
		HasHistory: true,
	}
}

// ClearSession clears a user's conversation session.
func (a *ChatAgent) ClearSession(userID string) bool {
	return a.SessionManager.ClearSession(userID)
}

// GetSessionStatusMessage formats the user's current session status (/status).
func GetSessionStatusMessage(sm *services.SessionManager, userID string) string {
	info := sm.GetSessionInfo(userID)
	if info == nil {
		return "📊 目前沒有進行中的對話。\n\n發送任何訊息開始新對話！"
	}
	return fmt.Sprintf(`📊 對話狀態

💬 對話輪數：%d 條訊息
⏰ 開始時間：%s
🕐 最後活動：%s

使用 /clear 清除對話記憶`,
		info.HistoryCount,
		info.CreatedAt.Format("2006-01-02 15:04"),
		info.LastActive.Format("15:04"),
	)
}

// FormatChatResponse formats a ChatAgent response for LINE display.
func FormatChatResponse(res AgentResponse, includeSources bool) string {
	if res.Status != "success" {
		msg := res.ErrorMessage
		if msg == "" {
			msg = "對話處理失敗"
		}
		return "❌ " + msg
	}
	text := res.Answer
	if res.HasHistory {
		text = fmt.Sprintf("💬 [對話中]\n\n%s", text)
	}
	if includeSources && len(res.Sources) > 0 {
		text += "\n\n📚 參考來源：\n"
		limit := len(res.Sources)
		if limit > 3 {
			limit = 3
		}
		for i := 0; i < limit; i++ {
			src := res.Sources[i]
			title := src.Title
			if title == "" {
				title = "Unknown"
			}
			if src.URI != "" {
				text += fmt.Sprintf("%d. %s\n   %s\n", i+1, title, src.URI)
			}
		}
	}
	return text
}

// ContentAgent handles URL and YouTube content loading and summarization.
type ContentAgent struct {
	Config *config.Config
	Client *gemini.Client
	Loader *loader.Loader
}

// NewContentAgent creates a new ContentAgent.
func NewContentAgent(cfg *config.Config, client *gemini.Client, l *loader.Loader) *ContentAgent {
	return &ContentAgent{
		Config: cfg,
		Client: client,
		Loader: l,
	}
}

// ProcessURL loads and summarizes a URL.
func (a *ContentAgent) ProcessURL(ctx context.Context, rawURL, mode, youtubeMode string) AgentResponse {
	if mode == "" {
		mode = "normal"
	}
	if youtubeMode == "" {
		youtubeMode = "normal"
	}
	loaded := a.Loader.LoadURLContent(ctx, rawURL, youtubeMode)
	if loaded.Status != "success" {
		return AgentResponse{
			Status:       "error",
			Intent:       "url",
			URL:          rawURL,
			ErrorMessage: loaded.ErrorMessage,
		}
	}
	if loaded.ContentType == "youtube" {
		return AgentResponse{
			Status:      "success",
			Intent:      "url",
			Content:     loaded.Content,
			ContentType: "youtube",
			URL:         rawURL,
			Mode:        youtubeMode,
		}
	}

	summary, err := ai.SummarizeText(ctx, a.Client, loaded.Content, mode)
	if err != nil {
		return AgentResponse{
			Status:       "error",
			Intent:       "url",
			URL:          rawURL,
			ErrorMessage: err.Error(),
		}
	}
	return AgentResponse{
		Status:      "success",
		Intent:      "url",
		Content:     summary,
		ContentType: loaded.ContentType,
		URL:         rawURL,
		Mode:        mode,
	}
}

// SummarizeYouTube summarizes a YouTube video URL.
func (a *ContentAgent) SummarizeYouTube(ctx context.Context, youtubeURL, mode string) AgentResponse {
	if mode == "" {
		mode = "normal"
	}
	summary, err := ai.SummarizeYouTubeVideo(ctx, a.Client, youtubeURL, mode)
	if err != nil {
		errMsg := err.Error()
		return AgentResponse{
			Status:       "error",
			Intent:       "youtube",
			URL:          youtubeURL,
			Mode:         mode,
			ErrorMessage: errMsg,
		}
	}
	return AgentResponse{
		Status:  "success",
		Intent:  "youtube",
		Summary: summary,
		Content: summary,
		URL:     youtubeURL,
		Mode:    mode,
	}
}

// FormatContentResponse formats a ContentAgent response for display.
func FormatContentResponse(res AgentResponse, includeURL bool) string {
	if res.Status != "success" {
		msg := res.ErrorMessage
		if msg == "" {
			msg = "處理失敗"
		}
		return "❌ " + msg
	}
	text := res.Content
	if text == "" {
		text = res.Summary
	}
	if includeURL && res.URL != "" {
		text = fmt.Sprintf("%s\n\n%s", res.URL, text)
	}
	return text
}

// LocationAgent handles Google Maps Grounding place searches.
type LocationAgent struct {
	Config *config.Config
	Client *gemini.Client
}

// NewLocationAgent creates a new LocationAgent.
func NewLocationAgent(cfg *config.Config, client *gemini.Client) *LocationAgent {
	return &LocationAgent{
		Config: cfg,
		Client: client,
	}
}

// Search searches for nearby places around (latitude, longitude).
func (a *LocationAgent) Search(ctx context.Context, latitude, longitude float64, placeType, customQuery string) AgentResponse {
	if !a.Config.EnableMapsGrounding {
		return AgentResponse{
			Status:       "error",
			Intent:       "location",
			ErrorMessage: "Maps 搜尋功能已停用",
		}
	}
	res := ai.SearchNearbyPlaces(ctx, a.Client, latitude, longitude, placeType, customQuery, "zh-TW")
	if res.Status != "success" {
		return AgentResponse{
			Status:       "error",
			Intent:       "location",
			ErrorMessage: res.ErrorMessage,
		}
	}
	return AgentResponse{
		Status:          "success",
		Intent:          "location",
		Places:          res.Places,
		Latitude:        res.Latitude,
		Longitude:       res.Longitude,
		RestaurantNames: res.RestaurantNames,
	}
}

// FormatLocationResponse formats a LocationAgent response for display.
func FormatLocationResponse(res AgentResponse) string {
	if res.Status != "success" {
		msg := res.ErrorMessage
		if msg == "" {
			msg = "搜尋失敗"
		}
		return "❌ " + msg
	}
	if res.Places == "" {
		return "找不到附近地點"
	}
	return res.Places
}

// VisionAgent handles image analysis (standard and Agentic Vision).
type VisionAgent struct {
	Config *config.Config
	Client *gemini.Client
}

// NewVisionAgent creates a new VisionAgent.
func NewVisionAgent(cfg *config.Config, client *gemini.Client) *VisionAgent {
	return &VisionAgent{
		Config: cfg,
		Client: client,
	}
}

// Analyze analyzes an image using standard Gemini multimodal vision.
func (a *VisionAgent) Analyze(ctx context.Context, imageData []byte, prompt string) AgentResponse {
	analysis, err := ai.AnalyzeImage(ctx, a.Client, imageData, prompt)
	if err != nil {
		errMsg := err.Error()
		if len(errMsg) > 100 {
			errMsg = errMsg[:100]
		}
		return AgentResponse{
			Status:       "error",
			Intent:       "image",
			ErrorMessage: fmt.Sprintf("無法分析圖片: %s", errMsg),
		}
	}
	return AgentResponse{
		Status:   "success",
		Intent:   "image",
		Analysis: analysis,
	}
}

// AnalyzeAgentic analyzes an image using Agentic Vision with code execution.
func (a *VisionAgent) AnalyzeAgentic(ctx context.Context, imageData []byte, prompt string) AgentResponse {
	analysis, images, err := ai.AnalyzeImageAgentic(ctx, a.Client, imageData, prompt)
	if err != nil {
		errMsg := err.Error()
		if len(errMsg) > 100 {
			errMsg = errMsg[:100]
		}
		return AgentResponse{
			Status:       "error",
			Intent:       "image",
			ErrorMessage: fmt.Sprintf("Agentic Vision 分析失敗: %s", errMsg),
		}
	}
	return AgentResponse{
		Status:   "success",
		Intent:   "image",
		Analysis: analysis,
		Images:   images,
	}
}

// FormatVisionResponse formats a VisionAgent response for display.
func FormatVisionResponse(res AgentResponse) string {
	if res.Status != "success" {
		msg := res.ErrorMessage
		if msg == "" {
			msg = "圖片分析失敗"
		}
		return "❌ " + msg
	}
	analysis := res.Analysis
	if analysis == "" {
		analysis = "無法分析圖片"
	}
	return fmt.Sprintf("🖼️ 圖片分析結果\n\n%s", analysis)
}

// GitHubAgent handles GitHub issues summarization.
type GitHubAgent struct {
	Config     *config.Config
	Client     *gemini.Client
	APIBaseURL string
}

// NewGitHubAgent creates a new GitHubAgent.
func NewGitHubAgent(cfg *config.Config, client *gemini.Client) *GitHubAgent {
	return &GitHubAgent{
		Config:     cfg,
		Client:     client,
		APIBaseURL: ai.DefaultGitHubAPIBaseURL,
	}
}

// GetIssuesSummary summarizes recent GitHub issues from kkdai/bookmarks.
func (a *GitHubAgent) GetIssuesSummary(ctx context.Context) AgentResponse {
	summary, err := ai.SummarizedYesterdayGitHubIssues(ctx, a.Client, a.Config.GitHubToken, a.APIBaseURL)
	if err != nil {
		errMsg := err.Error()
		if len(errMsg) > 100 {
			errMsg = errMsg[:100]
		}
		return AgentResponse{
			Status:       "error",
			Intent:       "github",
			ErrorMessage: fmt.Sprintf("取得 GitHub Issues 時發生錯誤: %s", errMsg),
		}
	}
	if strings.TrimSpace(summary) == "" {
		summary = "📋 目前沒有新的 GitHub Issues"
	}
	return AgentResponse{
		Status:  "success",
		Intent:  "github",
		Summary: summary,
	}
}

// FormatGitHubResponse formats a GitHubAgent response for display.
func FormatGitHubResponse(res AgentResponse) string {
	if res.Status != "success" {
		msg := res.ErrorMessage
		if msg == "" {
			msg = "GitHub 操作失敗"
		}
		return "❌ " + msg
	}
	if res.Summary == "" {
		return "無資料"
	}
	return res.Summary
}

var (
	urlRegex     = regexp.MustCompile(`https?://[^\s<>"{}|\\^\x60\[\]]+`)
	youtubeRegex = regexp.MustCompile(`^https?://(?:www\.)?(?:youtube\.com|youtu\.be|m\.youtube\.com)[^\s]*`)
)

var restaurantKeywords = []string{
	"餐廳", "美食", "好吃", "小吃", "餐酒館", "咖啡廳", "早午餐", "火鍋", "燒肉", "壽司", "拉麵", "牛肉麵", "吃什麼", "點餐", "吃東西",
}

// Orchestrator routes user intents to specialized agents and supports parallel multi-intent execution.
type Orchestrator struct {
	Config        *config.Config
	ChatAgent     *ChatAgent
	ContentAgent  *ContentAgent
	LocationAgent *LocationAgent
	VisionAgent   *VisionAgent
	GitHubAgent   *GitHubAgent
}

// NewOrchestrator creates a new Orchestrator with all specialist agents wired.
func NewOrchestrator(cfg *config.Config, client *gemini.Client, sm *services.SessionManager, l *loader.Loader) *Orchestrator {
	if l != nil && l.YouTubeSummarizer == nil {
		l.YouTubeSummarizer = func(ctx context.Context, rawURL, mode string) (string, error) {
			return ai.SummarizeYouTubeVideo(ctx, client, rawURL, mode)
		}
	}
	return &Orchestrator{
		Config:        cfg,
		ChatAgent:     NewChatAgent(cfg, client, sm),
		ContentAgent:  NewContentAgent(cfg, client, l),
		LocationAgent: NewLocationAgent(cfg, client),
		VisionAgent:   NewVisionAgent(cfg, client),
		GitHubAgent:   NewGitHubAgent(cfg, client),
	}
}

// DetectIntents detects user intents from a text message.
func (o *Orchestrator) DetectIntents(message string) []Intent {
	var intents []Intent
	msgLower := strings.ToLower(strings.TrimSpace(message))

	switch msgLower {
	case "/clear", "/清除", "/reset", "/重置",
		"/status", "/狀態", "/info",
		"/help", "/幫助", "/說明",
		"/session-stats", "/stats":
		return []Intent{{
			Type:       IntentCommand,
			Confidence: 1.0,
			Data:       map[string]string{"command": msgLower},
		}}
	case "@g":
		return []Intent{{
			Type:       IntentGitHubSummary,
			Confidence: 1.0,
			Data:       map[string]string{},
		}}
	}

	urls := urlRegex.FindAllString(message, -1)
	if len(urls) > 0 {
		for _, u := range urls {
			if youtubeRegex.MatchString(u) {
				intents = append(intents, Intent{
					Type:       IntentYouTubeSummary,
					Confidence: 0.95,
					Data:       map[string]string{"url": u},
				})
			} else {
				intents = append(intents, Intent{
					Type:       IntentURLSummary,
					Confidence: 0.95,
					Data:       map[string]string{"url": u},
				})
			}
		}
	}

	if len(intents) == 0 {
		for _, kw := range restaurantKeywords {
			if strings.Contains(msgLower, kw) {
				intents = append(intents, Intent{
					Type:       IntentRestaurantSearch,
					Confidence: 0.9,
					Data:       map[string]string{"message": message},
				})
				break
			}
		}
	}

	if len(intents) == 0 {
		intents = append(intents, Intent{
			Type:       IntentChat,
			Confidence: 0.9,
			Data:       map[string]string{"message": message},
		})
	}

	return intents
}

// ProcessText detects intents from a text message and executes them (in parallel if multiple).
func (o *Orchestrator) ProcessText(ctx context.Context, userID, message, mode string) OrchestratorResult {
	if mode == "" {
		mode = "normal"
	}
	intents := o.DetectIntents(message)
	slog.Info("Detected intents", "count", len(intents), "user_id", userID)

	responses := make([]AgentResponse, len(intents))
	if len(intents) == 1 {
		responses[0] = o.routeIntent(ctx, userID, intents[0], mode)
	} else if len(intents) > 1 {
		var wg sync.WaitGroup
		for i, in := range intents {
			wg.Add(1)
			go func(idx int, intent Intent) {
				defer wg.Done()
				responses[idx] = o.routeIntent(ctx, userID, intent, mode)
			}(i, in)
		}
		wg.Wait()
	}

	allSuccess := len(responses) > 0
	for _, r := range responses {
		if r.Status != "success" {
			allSuccess = false
			break
		}
	}

	return OrchestratorResult{
		Success:   allSuccess,
		Responses: responses,
		Intents:   intents,
	}
}

func (o *Orchestrator) routeIntent(ctx context.Context, userID string, intent Intent, mode string) AgentResponse {
	switch intent.Type {
	case IntentCommand:
		return o.handleCommand(userID, intent.Data["command"])

	case IntentGitHubSummary:
		res := o.GitHubAgent.GetIssuesSummary(ctx)
		res.Intent = "github"
		return res

	case IntentYouTubeSummary:
		u := intent.Data["url"]
		ytMode := mode
		if ytMode == "detailed" {
			ytMode = "detail"
		}
		if ytMode != "normal" && ytMode != "detail" && ytMode != "twitter" {
			ytMode = "normal"
		}
		res := o.ContentAgent.SummarizeYouTube(ctx, u, ytMode)
		res.Intent = "youtube"
		res.URL = u
		return res

	case IntentURLSummary:
		u := intent.Data["url"]
		res := o.ContentAgent.ProcessURL(ctx, u, mode, "normal")
		res.Intent = "url"
		return res

	case IntentRestaurantSearch:
		rawLoc, ok := o.ChatAgent.SessionManager.GetMetadata(userID, "last_location")
		locMap, isMap := rawLoc.(map[string]any)
		if !ok || !isMap {
			return AgentResponse{
				Status:       "success",
				Intent:       "restaurant_search",
				Response:     "📍 偵測到您想要尋找美食！請點選下方按鈕或加號傳送您的『位置資訊』，我將利用 Google Maps Grounding 與 Gemini Batch API 為您進行深度評論與菜色分析！",
				NeedLocation: true,
			}
		}
		lat, _ := locMap["latitude"].(float64)
		lng, _ := locMap["longitude"].(float64)
		addr, _ := locMap["address"].(string)

		res := o.LocationAgent.Search(ctx, lat, lng, "restaurant", "")
		res.Intent = "restaurant_search"
		res.Latitude = lat
		res.Longitude = lng
		res.Address = addr
		res.HasLocation = true
		return res

	case IntentChat:
		res := o.ChatAgent.Chat(ctx, userID, intent.Data["message"])
		res.Intent = "chat"
		return res

	default:
		return AgentResponse{
			Status:       "error",
			Intent:       string(intent.Type),
			ErrorMessage: fmt.Sprintf("Unknown intent type: %s", intent.Type),
		}
	}
}

const helpText = `🤖 智能助手 (ADK Orchestrator)

💬 對話功能
發送任何問題，我會自動搜尋網路並提供詳細回答。
支援連續對話，我會記住我們的對話內容！

🔗 內容摘要
發送任何網址，我會自動擷取並摘要內容。
• 網頁文章
• YouTube 影片
• PDF 文件

📍 位置服務
發送你的位置，可以搜尋附近的：
• ⛽ 加油站
• 🅿️ 停車場
• 🍴 餐廳

🖼️ 圖片分析
發送圖片，我會分析圖片內容。

⚡ 特殊指令
/clear - 清除對話記憶
/status - 查看對話狀態
/stats - 查看 Session 統計
/help - 顯示此說明
@g - GitHub Issues 摘要

提示：對話會在 30 分鐘無互動後自動過期。`

func (o *Orchestrator) handleCommand(userID, command string) AgentResponse {
	switch command {
	case "/clear", "/清除", "/reset", "/重置":
		if o.ChatAgent.ClearSession(userID) {
			return AgentResponse{
				Status:   "success",
				Intent:   "command",
				Response: "✅ 對話已重置\n\n你可以開始新的對話了！",
			}
		}
		return AgentResponse{
			Status:   "success",
			Intent:   "command",
			Response: "📊 目前沒有進行中的對話。\n\n發送任何訊息開始新對話！",
		}

	case "/status", "/狀態", "/info":
		return AgentResponse{
			Status:   "success",
			Intent:   "command",
			Response: GetSessionStatusMessage(o.ChatAgent.SessionManager, userID),
		}

	case "/help", "/幫助", "/說明":
		return AgentResponse{
			Status:   "success",
			Intent:   "command",
			Response: helpText,
		}

	case "/session-stats", "/stats":
		if o.ChatAgent.SessionManager != nil {
			stats := o.ChatAgent.SessionManager.GetStats()
			text := fmt.Sprintf(`📈 Session 統計資訊

👥 活躍對話數：%d
💬 總訊息數：%d
⏱️ 最舊對話：%.1f 分鐘
🧹 清理次數：%d
🗑️ 已清理對話：%d`,
				stats.ActiveSessions,
				stats.TotalMessages,
				stats.OldestSessionAgeMinutes,
				stats.CleanupRuns,
				stats.SessionsCleaned,
			)
			return AgentResponse{
				Status:   "success",
				Intent:   "command",
				Response: text,
			}
		}
		return AgentResponse{
			Status:   "success",
			Intent:   "command",
			Response: "📊 Session 管理器未啟用",
		}

	default:
		return AgentResponse{
			Status:       "error",
			Intent:       "command",
			ErrorMessage: fmt.Sprintf("Unknown command: %s", command),
		}
	}
}

// ProcessImage analyzes an image using standard vision.
func (o *Orchestrator) ProcessImage(ctx context.Context, imageData []byte, prompt string) OrchestratorResult {
	res := o.VisionAgent.Analyze(ctx, imageData, prompt)
	res.Intent = "image"
	return OrchestratorResult{
		Success:   res.Status == "success",
		Responses: []AgentResponse{res},
		Intents:   []Intent{{Type: IntentImageAnalysis, Confidence: 1.0}},
	}
}

// ProcessImageAgentic analyzes an image using Agentic Vision.
func (o *Orchestrator) ProcessImageAgentic(ctx context.Context, imageData []byte, prompt string) OrchestratorResult {
	res := o.VisionAgent.AnalyzeAgentic(ctx, imageData, prompt)
	res.Intent = "image"
	return OrchestratorResult{
		Success:   res.Status == "success",
		Responses: []AgentResponse{res},
		Intents:   []Intent{{Type: IntentImageAnalysis, Confidence: 1.0}},
	}
}

// ProcessLocation searches for nearby places around (latitude, longitude).
func (o *Orchestrator) ProcessLocation(ctx context.Context, latitude, longitude float64, placeType string) OrchestratorResult {
	res := o.LocationAgent.Search(ctx, latitude, longitude, placeType, "")
	res.Intent = "location"
	return OrchestratorResult{
		Success:   res.Status == "success",
		Responses: []AgentResponse{res},
		Intents:   []Intent{{Type: IntentLocationSearch, Confidence: 1.0, Data: map[string]string{"place_type": placeType}}},
	}
}

// FormatOrchestratorResponse formats an OrchestratorResult into a single display string.
func FormatOrchestratorResponse(result OrchestratorResult) string {
	if len(result.Responses) == 0 {
		return "❌ 無法處理您的請求"
	}

	if len(result.Responses) == 1 {
		resp := result.Responses[0]
		if resp.Status != "success" {
			msg := resp.ErrorMessage
			if msg == "" {
				msg = "處理失敗"
			}
			return "❌ " + msg
		}

		switch resp.Intent {
		case "command":
			return resp.Response
		case "chat":
			return FormatChatResponse(resp, true)
		case "url", "youtube":
			return FormatContentResponse(resp, true)
		case "location":
			return FormatLocationResponse(resp)
		case "restaurant_search":
			if resp.HasLocation {
				return FormatLocationResponse(resp)
			}
			return resp.Response
		case "image":
			return FormatVisionResponse(resp)
		case "github":
			return FormatGitHubResponse(resp)
		default:
			if resp.Response != "" {
				return resp.Response
			}
			return resp.Content
		}
	}

	var parts []string
	for i, resp := range result.Responses {
		if resp.Status == "success" {
			switch resp.Intent {
			case "url", "youtube":
				parts = append(parts, FormatContentResponse(resp, true))
			case "chat":
				parts = append(parts, FormatChatResponse(resp, false))
			default:
				text := resp.Content
				if text == "" {
					text = resp.Response
				}
				parts = append(parts, text)
			}
		} else {
			msg := resp.ErrorMessage
			if msg == "" {
				msg = "失敗"
			}
			parts = append(parts, fmt.Sprintf("❌ 任務 %d: %s", i+1, msg))
		}
	}
	return strings.Join(parts, "\n\n---\n\n")
}
