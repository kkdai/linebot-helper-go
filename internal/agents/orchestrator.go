package agents

import (
	"context"
	"fmt"
	"iter"
	"log/slog"
	"regexp"
	"strings"
	"sync"

	"github.com/kkdai/linebot-helper-go/internal/ai"
	"github.com/kkdai/linebot-helper-go/internal/config"
	"github.com/kkdai/linebot-helper-go/internal/gemini"
	"github.com/kkdai/linebot-helper-go/internal/loader"
	"github.com/kkdai/linebot-helper-go/internal/services"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/model"
	adkgemini "google.golang.org/adk/model/gemini"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

// ADKVersion records the Google ADK for Go version used by this package.
const ADKVersion = "v1.9.0"

// adkModelAdapter adapts *gemini.Client to google.golang.org/adk/model.LLM.
type adkModelAdapter struct {
	modelName string
	client    *gemini.Client
}

func (m *adkModelAdapter) Name() string {
	return m.modelName
}

func (m *adkModelAdapter) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		var contents []gemini.Content
		for _, c := range req.Contents {
			if c == nil {
				continue
			}
			var parts []gemini.Part
			for _, p := range c.Parts {
				if p != nil && p.Text != "" {
					parts = append(parts, gemini.NewTextPart(p.Text))
				}
			}
			contents = append(contents, gemini.Content{
				Role:  c.Role,
				Parts: parts,
			})
		}
		res, err := m.client.GenerateContent(ctx, gemini.GenerateOptions{
			Model:    m.modelName,
			Contents: contents,
		})
		if err != nil {
			yield(nil, err)
			return
		}
		yield(&model.LLMResponse{
			Content: &genai.Content{
				Role:  "model",
				Parts: []*genai.Part{{Text: res.Text}},
			},
			TurnComplete: true,
		}, nil)
	}
}

func newADKModel(cfg *config.Config, client *gemini.Client, modelName string) model.LLM {
	if modelName == "" {
		modelName = "gemini-3.1-flash-lite"
	}
	if cfg != nil && cfg.GoogleCloudProject != "" {
		loc := cfg.GoogleCloudLocation
		if loc == "" {
			loc = "global"
		}
		if llm, err := adkgemini.NewModel(context.Background(), modelName, &genai.ClientConfig{
			Backend:  genai.BackendVertexAI,
			Project:  cfg.GoogleCloudProject,
			Location: loc,
		}); err == nil {
			return llm
		}
	}
	return &adkModelAdapter{modelName: modelName, client: client}
}

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

// ContentAgentInstruction defines the system prompt for ContentAgent.
const ContentAgentInstruction = `你是內容摘要專家，專門處理網頁、影片和文件的摘要。

## 工作流程
1. 接收 URL 後，判斷內容類型（網頁、YouTube、PDF）
2. 使用適當的工具載入內容
3. 生成繁體中文摘要
4. 回傳格式化的結果

## 回應原則
- 使用台灣用語的繁體中文
- 摘要要簡潔但完整
- 包含關鍵重點和 hashtag
- 適合在 LINE 訊息中閱讀
`

// LocationAgentInstruction defines the system prompt for LocationAgent.
const LocationAgentInstruction = `你是地點搜尋專家，專門協助用戶找到附近的地點。

## 工作流程
1. 接收用戶的位置（經緯度）
2. 根據用戶需求搜尋附近地點（加油站、停車場、餐廳等）
3. 使用 Google Maps Grounding 取得最新資訊
4. 回傳格式化的搜尋結果

## 回應原則
- 使用台灣用語的繁體中文
- 提供實用的地點資訊（名稱、地址、距離）
- 適合在 LINE 訊息中閱讀
`

// VisionAgentInstruction defines the system prompt for VisionAgent.
const VisionAgentInstruction = `你是圖片分析專家，專門分析和描述圖片內容。

## 工作流程
1. 接收用戶上傳的圖片
2. 使用 Gemini 視覺模型分析圖片
3. 提供詳細的圖片描述

## 回應原則
- 使用台灣用語的繁體中文
- 描述要詳細但簡潔
- 識別圖片中的文字、物品、人物、場景
- 適合在 LINE 訊息中閱讀
`

// GitHubAgentInstruction defines the system prompt for GitHubAgent.
const GitHubAgentInstruction = `你是 GitHub 助手，專門處理 GitHub 相關的任務。

## 功能
1. 摘要 GitHub Issues
2. 追蹤專案更新
3. 提供開發進度報告

## 回應原則
- 使用台灣用語的繁體中文
- 摘要要簡潔但包含關鍵資訊
- 標明 Issue 編號和狀態
- 適合在 LINE 訊息中閱讀
`

// OrchestratorInstruction defines the system prompt for the root ADK Orchestrator agent.
const OrchestratorInstruction = `你是 LINE Bot 的主控 Agent (Orchestrator)。
你的職責是分析用戶訊息的意圖，並將任務分派給適當的專業 Agent。

## 可用的專業 Agent
1. **ChatAgent**: 處理一般對話和問答，支援 Google Search Grounding
2. **ContentAgent**: 處理 URL 內容摘要（網頁、YouTube、PDF）
3. **LocationAgent**: 處理地點搜尋（加油站、停車場、餐廳）
4. **VisionAgent**: 處理圖片分析
5. **GitHubAgent**: 處理 GitHub 相關操作

## 路由規則
- 訊息包含 URL → ContentAgent
- 訊息是 "@g" → GitHubAgent
- 訊息是系統指令 (/clear, /help, /status) → 直接處理
- 收到圖片 → VisionAgent
- 收到位置 → LocationAgent
- 其他文字訊息 → ChatAgent

## 複合任務
如果用戶的訊息包含多個意圖（例如：「幫我找餐廳，然後摘要這篇文章 https://...」），
你應該並行處理多個任務，然後彙整結果。
`

// ChatAgent handles conversational Q&A with session memory and Google Search Grounding.
type ChatAgent struct {
	Config         *config.Config
	Client         *gemini.Client
	SessionManager *services.SessionManager
	ADKAgent       agent.Agent
}

// NewChatAgent creates a new ChatAgent and initializes its Google ADK LLMAgent.
func NewChatAgent(cfg *config.Config, client *gemini.Client, sm *services.SessionManager) *ChatAgent {
	modelName := "gemini-3.1-flash-lite"
	if cfg != nil && cfg.ChatModel != "" {
		modelName = cfg.ChatModel
	}
	adkAg, err := llmagent.New(llmagent.Config{
		Name:        "chat_agent",
		Model:       newADKModel(cfg, client, modelName),
		Description: "對話式問答 Agent，處理一般文字訊息並支援 Google Search Grounding",
		Instruction: ChatAgentInstruction,
	})
	if err != nil {
		slog.Warn("Failed to create ADK chat_agent", "error", err)
	}
	return &ChatAgent{
		Config:         cfg,
		Client:         client,
		SessionManager: sm,
		ADKAgent:       adkAg,
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
	modelName := a.Config.ChatModel
	if modelName == "" {
		modelName = "gemini-3.1-flash-lite"
	}
	maxTokens := a.Config.MaxOutputTokens
	if maxTokens <= 0 {
		maxTokens = 2048
	}

	res, err := a.Client.GenerateContent(ctx, gemini.GenerateOptions{
		Model:              modelName,
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
	Config   *config.Config
	Client   *gemini.Client
	Loader   *loader.Loader
	ADKAgent agent.Agent
}

type urlToolArgs struct {
	URL  string `json:"url"`
	Mode string `json:"mode,omitempty"`
}

type urlToolResult struct {
	Status  string `json:"status"`
	Content string `json:"content"`
}

// NewContentAgent creates a new ContentAgent and initializes its Google ADK LLMAgent with function tools.
func NewContentAgent(cfg *config.Config, client *gemini.Client, l *loader.Loader) *ContentAgent {
	ca := &ContentAgent{
		Config: cfg,
		Client: client,
		Loader: l,
	}
	modelName := "gemini-3.1-flash-lite"
	if cfg != nil && cfg.FastModel != "" {
		modelName = cfg.FastModel
	}

	var adkTools []tool.Tool
	if loadTool, err := functiontool.New(functiontool.Config{
		Name:        "load_url_content",
		Description: "Load content from a web URL, YouTube video, or PDF document",
	}, func(ctx agent.ToolContext, args urlToolArgs) (urlToolResult, error) {
		res := ca.ProcessURL(ctx, args.URL, args.Mode, "normal")
		return urlToolResult{Status: res.Status, Content: res.Content}, nil
	}); err == nil {
		adkTools = append(adkTools, loadTool)
	}

	if ytTool, err := functiontool.New(functiontool.Config{
		Name:        "summarize_youtube_video",
		Description: "Summarize a YouTube video URL in Traditional Chinese",
	}, func(ctx agent.ToolContext, args urlToolArgs) (urlToolResult, error) {
		res := ca.SummarizeYouTube(ctx, args.URL, args.Mode)
		return urlToolResult{Status: res.Status, Content: res.Summary}, nil
	}); err == nil {
		adkTools = append(adkTools, ytTool)
	}

	adkAg, err := llmagent.New(llmagent.Config{
		Name:        "content_agent",
		Model:       newADKModel(cfg, client, modelName),
		Description: "內容摘要 Agent，處理網頁、YouTube 和 PDF 的摘要",
		Instruction: ContentAgentInstruction,
		Tools:       adkTools,
	})
	if err != nil {
		slog.Warn("Failed to create ADK content_agent", "error", err)
	}
	ca.ADKAgent = adkAg
	return ca
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
	Config   *config.Config
	Client   *gemini.Client
	ADKAgent agent.Agent
}

type locationToolArgs struct {
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	PlaceType   string  `json:"place_type"`
	CustomQuery string  `json:"custom_query,omitempty"`
}

type locationToolResult struct {
	Status string `json:"status"`
	Places string `json:"places"`
}

// NewLocationAgent creates a new LocationAgent and initializes its Google ADK LLMAgent.
func NewLocationAgent(cfg *config.Config, client *gemini.Client) *LocationAgent {
	la := &LocationAgent{
		Config: cfg,
		Client: client,
	}
	modelName := "gemini-3.1-flash-lite"
	if cfg != nil && cfg.FastModel != "" {
		modelName = cfg.FastModel
	}

	var adkTools []tool.Tool
	if mapsTool, err := functiontool.New(functiontool.Config{
		Name:        "search_nearby_places",
		Description: "Search for nearby places using Google Maps Grounding",
	}, func(ctx agent.ToolContext, args locationToolArgs) (locationToolResult, error) {
		res := la.Search(ctx, args.Latitude, args.Longitude, args.PlaceType, args.CustomQuery)
		return locationToolResult{Status: res.Status, Places: res.Places}, nil
	}); err == nil {
		adkTools = append(adkTools, mapsTool)
	}

	adkAg, err := llmagent.New(llmagent.Config{
		Name:        "location_agent",
		Model:       newADKModel(cfg, client, modelName),
		Description: "地點搜尋 Agent，使用 Google Maps Grounding 搜尋附近地點",
		Instruction: LocationAgentInstruction,
		Tools:       adkTools,
	})
	if err != nil {
		slog.Warn("Failed to create ADK location_agent", "error", err)
	}
	la.ADKAgent = adkAg
	return la
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
	Config   *config.Config
	Client   *gemini.Client
	ADKAgent agent.Agent
}

// NewVisionAgent creates a new VisionAgent and initializes its Google ADK LLMAgent.
func NewVisionAgent(cfg *config.Config, client *gemini.Client) *VisionAgent {
	modelName := "gemini-3.1-flash-lite"
	if cfg != nil && cfg.ChatModel != "" {
		modelName = cfg.ChatModel
	}
	adkAg, err := llmagent.New(llmagent.Config{
		Name:        "vision_agent",
		Model:       newADKModel(cfg, client, modelName),
		Description: "圖片分析 Agent，使用 Gemini 視覺模型分析圖片內容",
		Instruction: VisionAgentInstruction,
	})
	if err != nil {
		slog.Warn("Failed to create ADK vision_agent", "error", err)
	}
	return &VisionAgent{
		Config:   cfg,
		Client:   client,
		ADKAgent: adkAg,
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
	ADKAgent   agent.Agent
}

// NewGitHubAgent creates a new GitHubAgent and initializes its Google ADK LLMAgent.
func NewGitHubAgent(cfg *config.Config, client *gemini.Client) *GitHubAgent {
	modelName := "gemini-3.1-flash-lite"
	if cfg != nil && cfg.FastModel != "" {
		modelName = cfg.FastModel
	}
	adkAg, err := llmagent.New(llmagent.Config{
		Name:        "github_agent",
		Model:       newADKModel(cfg, client, modelName),
		Description: "GitHub Agent，處理 GitHub Issues 摘要和專案追蹤",
		Instruction: GitHubAgentInstruction,
	})
	if err != nil {
		slog.Warn("Failed to create ADK github_agent", "error", err)
	}
	return &GitHubAgent{
		Config:     cfg,
		Client:     client,
		APIBaseURL: ai.DefaultGitHubAPIBaseURL,
		ADKAgent:   adkAg,
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
	ADKAgent      agent.Agent
}

// NewOrchestrator creates a new Orchestrator with all specialist agents wired into the Google ADK agent tree.
func NewOrchestrator(cfg *config.Config, client *gemini.Client, sm *services.SessionManager, l *loader.Loader) *Orchestrator {
	if l != nil && l.YouTubeSummarizer == nil {
		l.YouTubeSummarizer = func(ctx context.Context, rawURL, mode string) (string, error) {
			return ai.SummarizeYouTubeVideo(ctx, client, rawURL, mode)
		}
	}
	chatAg := NewChatAgent(cfg, client, sm)
	contentAg := NewContentAgent(cfg, client, l)
	locAg := NewLocationAgent(cfg, client)
	visionAg := NewVisionAgent(cfg, client)
	ghAg := NewGitHubAgent(cfg, client)

	var subAgents []agent.Agent
	for _, a := range []agent.Agent{chatAg.ADKAgent, contentAg.ADKAgent, locAg.ADKAgent, visionAg.ADKAgent, ghAg.ADKAgent} {
		if a != nil {
			subAgents = append(subAgents, a)
		}
	}

	orchModel := "gemini-2.5-pro"
	if cfg != nil && cfg.OrchestratorModel != "" {
		orchModel = cfg.OrchestratorModel
	}
	rootADK, err := llmagent.New(llmagent.Config{
		Name:        "orchestrator",
		Model:       newADKModel(cfg, client, orchModel),
		Description: "LINE Bot 主控 Agent，負責意圖分析和任務路由",
		Instruction: OrchestratorInstruction,
		SubAgents:   subAgents,
	})
	if err != nil {
		slog.Warn("Failed to create ADK orchestrator", "error", err)
	} else {
		slog.Info("ADK Orchestrator created", "adk_version", ADKVersion, "sub_agents", len(subAgents))
	}

	return &Orchestrator{
		Config:        cfg,
		ChatAgent:     chatAg,
		ContentAgent:  contentAg,
		LocationAgent: locAg,
		VisionAgent:   visionAg,
		GitHubAgent:   ghAg,
		ADKAgent:      rootADK,
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
