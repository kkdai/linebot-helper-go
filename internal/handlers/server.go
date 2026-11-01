package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/kkdai/linebot-helper-go/internal/agents"
	"github.com/kkdai/linebot-helper-go/internal/ai"
	"github.com/kkdai/linebot-helper-go/internal/config"
	"github.com/kkdai/linebot-helper-go/internal/gemini"
	"github.com/kkdai/linebot-helper-go/internal/line"
	"github.com/kkdai/linebot-helper-go/internal/loader"
	"github.com/kkdai/linebot-helper-go/internal/services"
	"github.com/kkdai/linebot-helper-go/internal/voice"
)

const (
	AnnotatedImageTTL     = 300 * time.Second
	SummaryTTL            = 600 * time.Second
	AudioTTL              = 300 * time.Second
	VoiceResumeTTL        = 900 * time.Second
	MaxTTSChars           = 250
	ReportCacheTTLSeconds = 7 * 86400
)

var specificRestaurantDishRegex = regexp.MustCompile(`幫我查一下\s*(.*?)\s*(?:的)?菜色`)

type binaryEntry struct {
	Data      []byte
	CreatedAt time.Time
}

type textEntry struct {
	Text      string
	CreatedAt time.Time
}

type resumeEntry struct {
	Handle    string
	CreatedAt time.Time
}

// Server holds all dependencies and state for the LINE Bot HTTP server.
type Server struct {
	Config          *config.Config
	GeminiClient    *gemini.Client
	LineClient      *line.Client
	Loader          *loader.Loader
	SessionManager  *services.SessionManager
	BookmarkService *services.BookmarkService
	ReportStore     *services.ReportStore
	BatchService    *services.BatchService
	Orchestrator    *agents.Orchestrator

	StaticDir     string
	JWKSURI       string
	GeminiLiveURL string // Optional override for testing WebSocket relay

	mu                   sync.RWMutex
	appBaseURL           string
	imageTempStore       map[string][]byte
	pendingAgenticVision map[string]bool
	annotatedImageStore  map[string]binaryEntry
	summaryStore         map[string]textEntry
	audioStore           map[string]binaryEntry
	voiceResumeHandles   map[string]resumeEntry
}

// NewServer creates and initializes a new Server.
func NewServer(
	cfg *config.Config,
	geminiClient *gemini.Client,
	lineClient *line.Client,
	l *loader.Loader,
	sm *services.SessionManager,
	bs *services.BookmarkService,
	rs *services.ReportStore,
	batchSvc *services.BatchService,
	orch *agents.Orchestrator,
) *Server {
	return &Server{
		Config:               cfg,
		GeminiClient:         geminiClient,
		LineClient:           lineClient,
		Loader:               l,
		SessionManager:       sm,
		BookmarkService:      bs,
		ReportStore:          rs,
		BatchService:         batchSvc,
		Orchestrator:         orch,
		StaticDir:            "static",
		JWKSURI:              services.DefaultJWKSURI,
		imageTempStore:       make(map[string][]byte),
		pendingAgenticVision: make(map[string]bool),
		annotatedImageStore:  make(map[string]binaryEntry),
		summaryStore:         make(map[string]textEntry),
		audioStore:           make(map[string]binaryEntry),
		voiceResumeHandles:   make(map[string]resumeEntry),
	}
}

// Handler returns an http.Handler with all routes registered.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", s.handleHealthCheck)
	mux.HandleFunc("POST /{$}", s.handleWebhookCallback)

	mux.HandleFunc("GET /reports/{report_id}", s.handleServeReport)
	mux.HandleFunc("GET /images/{image_id}", s.handleServeImage)
	mux.HandleFunc("GET /audio/{audio_id}", s.handleServeAudio)

	mux.HandleFunc("GET /liff/{$}", s.handleServeLIFF)
	mux.HandleFunc("GET /liff", s.handleServeLIFF)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.Dir(s.StaticDir))))

	mux.HandleFunc("GET /ws/voice/{session_id}", s.handleVoiceWS)

	mux.HandleFunc("POST /hn", s.handleHackerNews)
	mux.HandleFunc("POST /hf", s.handleHuggingFace)
	mux.HandleFunc("POST /urls", s.handleMultiURLs)

	mux.HandleFunc("POST /api/gemini-callback/static", s.handleGeminiStaticWebhook)
	mux.HandleFunc("POST /api/gemini-callback/dynamic", s.handleGeminiDynamicWebhook)

	return mux
}

func (s *Server) getAppBaseURL() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.appBaseURL
}

func (s *Server) setAppBaseURLIfEmpty(r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.appBaseURL != "" {
		return
	}
	proto := r.Header.Get("X-Forwarded-Proto")
	if proto == "" {
		proto = "https"
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	if host != "" {
		s.appBaseURL = fmt.Sprintf("%s://%s", proto, host)
		slog.Info("App base URL detected", "url", s.appBaseURL)
	}
}

func (s *Server) handleHealthCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK"))
}

func (s *Server) handleWebhookCallback(w http.ResponseWriter, r *http.Request) {
	s.setAppBaseURLIfEmpty(r)

	sig := r.Header.Get("X-Line-Signature")
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	events, err := line.ParseWebhookEvents(s.Config.ChannelSecret, body, sig)
	if err != nil {
		http.Error(w, `{"detail":"Invalid signature"}`, http.StatusBadRequest)
		return
	}

	for _, ev := range events {
		eventCopy := ev
		switch eventCopy.Type {
		case "message":
			go s.handleMessageEvent(context.Background(), eventCopy)
		case "postback":
			go s.handlePostbackEvent(context.Background(), eventCopy)
		}
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK"))
}

func (s *Server) handleServeReport(w http.ResponseWriter, r *http.Request) {
	reportID := r.PathValue("report_id")
	if s.ReportStore != nil {
		if htmlStr, ok := s.ReportStore.Get(reportID); ok && htmlStr != "" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(htmlStr))
			return
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(services.RenderExpiredPage()))
}

func (s *Server) storeAnnotatedImage(data []byte) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for k, v := range s.annotatedImageStore {
		if now.Sub(v.CreatedAt) > AnnotatedImageTTL {
			delete(s.annotatedImageStore, k)
		}
	}
	id := uuid.NewString()
	s.annotatedImageStore[id] = binaryEntry{Data: data, CreatedAt: now}
	return id
}

func (s *Server) handleServeImage(w http.ResponseWriter, r *http.Request) {
	imageID := r.PathValue("image_id")
	s.mu.Lock()
	entry, ok := s.annotatedImageStore[imageID]
	if ok && time.Since(entry.CreatedAt) > AnnotatedImageTTL {
		delete(s.annotatedImageStore, imageID)
		ok = false
	}
	s.mu.Unlock()

	if !ok {
		http.Error(w, "Image not found or expired", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(entry.Data)
}

func (s *Server) storeAudio(data []byte) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for k, v := range s.audioStore {
		if now.Sub(v.CreatedAt) > AudioTTL {
			delete(s.audioStore, k)
		}
	}
	id := uuid.NewString()
	s.audioStore[id] = binaryEntry{Data: data, CreatedAt: now}
	return id
}

func (s *Server) handleServeAudio(w http.ResponseWriter, r *http.Request) {
	audioID := r.PathValue("audio_id")
	s.mu.Lock()
	entry, ok := s.audioStore[audioID]
	if ok && time.Since(entry.CreatedAt) > AudioTTL {
		delete(s.audioStore, audioID)
		ok = false
	}
	s.mu.Unlock()

	if !ok {
		http.Error(w, "Audio not found or expired", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "audio/mp4")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(entry.Data)
}

func (s *Server) storeSummary(text string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for k, v := range s.summaryStore {
		if now.Sub(v.CreatedAt) > SummaryTTL {
			delete(s.summaryStore, k)
		}
	}
	id := uuid.NewString()
	s.summaryStore[id] = textEntry{Text: text, CreatedAt: now}
	return id
}

func (s *Server) getSummary(id string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.summaryStore[id]
	if !ok || time.Since(entry.CreatedAt) > SummaryTTL {
		delete(s.summaryStore, id)
		return "", false
	}
	return entry.Text, true
}

func (s *Server) handleServeLIFF(w http.ResponseWriter, r *http.Request) {
	indexPath := s.StaticDir + "/liff/index.html"
	raw, err := os.ReadFile(indexPath)
	if err != nil {
		http.Error(w, "LIFF app not found", http.StatusNotFound)
		return
	}
	htmlStr := strings.ReplaceAll(string(raw), "{{LIFF_ID}}", s.Config.LiffID)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(htmlStr))
}

func (s *Server) handleMessageEvent(ctx context.Context, event line.WebhookEvent) {
	if event.Message == nil {
		return
	}
	switch event.Source.Type {
	case "group":
		slog.Info("Group message ignored", "group_id", event.Source.GroupID)
		return
	case "room":
		slog.Info("Room message ignored", "room_id", event.Source.RoomID)
		return
	}

	userID := event.Source.UserID
	if userID == "" {
		return
	}

	switch event.Message.Type {
	case "text":
		msgText := event.Message.Text

		s.mu.RLock()
		pending := s.pendingAgenticVision[userID]
		s.mu.RUnlock()
		if pending {
			s.handleAgenticVisionWithPrompt(ctx, event, userID, msgText)
			return
		}

		if cmd, arg, ok := line.ParseBookmarkCommand(msgText); ok {
			s.handleBookmarkCommand(ctx, event, userID, cmd, arg)
			return
		}

		urls, mode := loader.ExtractURLAndMode(msgText)
		if len(urls) > 0 {
			s.handleURLMessage(ctx, event, userID, urls, mode)
		} else {
			s.handleTextMessageViaOrchestrator(ctx, event, userID, msgText, "", "", false)
		}

	case "image":
		s.handleImageMessage(ctx, event, userID)

	case "audio":
		s.handleAudioMessage(ctx, event, userID)

	case "location":
		s.handleLocationMessage(ctx, event, userID)
	}
}

func (s *Server) handleURLMessage(ctx context.Context, event line.WebhookEvent, userID string, urls []string, mode string) {
	var results []map[string]any

	for _, rawURL := range urls {
		slog.Info("Crawling URL", "url", rawURL)
		crawledText, err := s.Loader.LoadURL(ctx, rawURL, "normal")
		if err != nil {
			errMsg := line.FormatErrorMessage(err, "處理網址")
			results = append(results, line.NewTextMessage(fmt.Sprintf("%s\n\n%s", rawURL, errMsg), nil))
			continue
		}
		if strings.TrimSpace(crawledText) == "" {
			results = append(results, line.NewTextMessage(fmt.Sprintf("⚠️ 無法從此網址擷取內容：\n%s", rawURL), nil))
			continue
		}

		posts := ai.GenerateSocialMediaPosts(ctx, s.GeminiClient, crawledText)
		var bookmarkDocID string
		if s.BookmarkService != nil && s.BookmarkService.Available() && posts.SummaryAnalysis != "" {
			bookmarkDocID = s.BookmarkService.SaveCandidate(userID, rawURL, posts.Title, posts.SummaryAnalysis)
		}

		fbText := fmt.Sprintf("%s\n\n🔗 原文連結： %s", posts.Facebook, rawURL)
		liText := fmt.Sprintf("%s\n\n🔗 原文連結： %s", posts.LinkedIn, rawURL)
		thText := fmt.Sprintf("%s\n\n🔗 原文連結： %s", posts.Threads, rawURL)

		carousel := line.BuildSocialCarousel(posts.Title, posts.SummaryAnalysis, rawURL, bookmarkDocID, fbText, liText, thText)
		results = append(results, line.NewFlexMessage("📝 社群爆款文案摘要", carousel, nil))
		results = append(results,
			line.NewTextMessage(fmt.Sprintf("📘 Facebook 爆款文案：\n--------------------\n%s", fbText), nil),
			line.NewTextMessage(fmt.Sprintf("💼 LinkedIn 專業貼文：\n--------------------\n%s", liText), nil),
			line.NewTextMessage(fmt.Sprintf("💬 Threads 脆友討論：\n--------------------\n%s", thText), nil),
		)
	}

	if len(results) > 0 {
		if len(results) > 5 {
			results = results[:5]
		}
		_ = s.LineClient.ReplyMessages(ctx, event.ReplyToken, results)
	}
}

func (s *Server) handleTextMessageViaOrchestrator(
	ctx context.Context,
	event line.WebhookEvent,
	userID string,
	text string,
	pushUserID string,
	replyPrefix string,
	offerTTS bool,
) {
	msg := strings.TrimSpace(text)

	if m := specificRestaurantDishRegex.FindStringSubmatch(msg); len(m) > 1 {
		restaurantName := strings.TrimSpace(m[1])
		targetUID := pushUserID
		if targetUID == "" {
			targetUID = userID
		}
		ackText := fmt.Sprintf("🔍 收到！正在為您對「%s」進行深度評論與招牌菜色大數據分析，大約需要 1-2 分鐘，請稍候...", restaurantName)
		if event.ReplyToken != "" && pushUserID == "" {
			_ = s.LineClient.ReplyText(ctx, event.ReplyToken, ackText, nil)
		} else {
			_ = s.LineClient.PushText(ctx, targetUID, ackText, nil)
		}
		go s.runSpecificRestaurantAnalysisBackground(context.Background(), userID, restaurantName, targetUID)
		return
	}

	result := s.Orchestrator.ProcessText(ctx, userID, msg, "normal")
	answerText := agents.FormatOrchestratorResponse(result)
	responseText := replyPrefix + answerText

	if utf8.RuneCountInString(responseText) > 4500 {
		runes := []rune(responseText)
		responseText = string(runes[:4400]) + "\n\n... (訊息過長，已截斷)"
	}

	var qrItems []line.QuickReplyItem
	for _, resp := range result.Responses {
		if resp.Intent == "restaurant_search" && resp.HasLocation && resp.Status == "success" {
			dataBytes, _ := json.Marshal(map[string]any{
				"action":    "foodie_deep_analysis",
				"latitude":  resp.Latitude,
				"longitude": resp.Longitude,
			})
			qrItems = append(qrItems, line.NewPostbackButton(
				"🔍 深度評論分析 (Batch)",
				string(dataBytes),
				"🔍 進行深度評論與招牌菜色分析",
			))
			break
		}
	}

	if offerTTS && strings.TrimSpace(answerText) != "" {
		summaryID := s.storeSummary(answerText)
		dataBytes, _ := json.Marshal(map[string]string{
			"action":     "read_aloud",
			"summary_id": summaryID,
		})
		qrItems = append(qrItems, line.NewPostbackButton(
			"🔊 用語音聽",
			string(dataBytes),
			"🔊 用語音聽回覆",
		))
	}

	var qr *line.QuickReply
	if len(qrItems) > 0 {
		qr = line.NewQuickReply(qrItems...)
	}

	if pushUserID != "" {
		_ = s.LineClient.PushText(ctx, pushUserID, responseText, qr)
	} else {
		_ = s.LineClient.ReplyText(ctx, event.ReplyToken, responseText, qr)
	}
}

func (s *Server) handleImageMessage(ctx context.Context, event line.WebhookEvent, userID string) {
	imgBytes, err := s.LineClient.GetMessageContent(ctx, event.Message.ID)
	if err != nil {
		errMsg := line.FormatErrorMessage(err, "處理圖片")
		_ = s.LineClient.ReplyText(ctx, event.ReplyToken, errMsg, nil)
		return
	}

	s.mu.Lock()
	s.imageTempStore[userID] = imgBytes
	s.mu.Unlock()

	recData, _ := json.Marshal(map[string]string{"action": "image_analyze", "mode": "recognize"})
	agData, _ := json.Marshal(map[string]string{"action": "image_analyze", "mode": "agentic_vision"})
	qr := line.NewQuickReply(
		line.NewPostbackButton("識別圖片", string(recData), "識別圖片"),
		line.NewPostbackButton("Agentic Vision", string(agData), "Agentic Vision"),
	)
	_ = s.LineClient.ReplyText(ctx, event.ReplyToken, "📷 已收到圖片，請選擇分析方式：", qr)
}

func (s *Server) handleAgenticVisionWithPrompt(ctx context.Context, event line.WebhookEvent, userID, promptText string) {
	s.mu.Lock()
	delete(s.pendingAgenticVision, userID)
	imgData, ok := s.imageTempStore[userID]
	delete(s.imageTempStore, userID)
	s.mu.Unlock()

	if !ok || len(imgData) == 0 {
		_ = s.LineClient.ReplyText(ctx, event.ReplyToken, "⚠️ 圖片已過期，請重新傳送圖片。", nil)
		return
	}

	_ = s.LineClient.ReplyText(ctx, event.ReplyToken, fmt.Sprintf("⏳ 正在使用 Agentic Vision 分析中，請稍候...\n\n📝 指令：%s", promptText), nil)

	result := s.Orchestrator.ProcessImageAgentic(ctx, imgData, promptText)
	respText := agents.FormatOrchestratorResponse(result)
	if utf8.RuneCountInString(respText) > 4500 {
		runes := []rune(respText)
		respText = string(runes[:4400]) + "\n\n... (訊息過長，已截斷)"
	}

	messages := []map[string]any{line.NewTextMessage(respText, nil)}
	baseURL := s.getAppBaseURL()
	if baseURL != "" {
		for _, r := range result.Responses {
			for _, imgBytes := range r.Images {
				imgID := s.storeAnnotatedImage(imgBytes)
				imgURL := fmt.Sprintf("%s/images/%s", baseURL, imgID)
				messages = append(messages, line.NewImageMessage(imgURL, imgURL))
			}
		}
	}
	_ = s.LineClient.PushMessages(ctx, userID, messages)
}

func (s *Server) handleAudioMessage(ctx context.Context, event line.WebhookEvent, userID string) {
	s.LineClient.ShowLoadingAnimation(ctx, userID, 60)

	audioBytes, err := s.LineClient.GetMessageContent(ctx, event.Message.ID)
	if err != nil {
		errText := line.FormatErrorMessage(err, "處理語音訊息")
		if rErr := s.LineClient.ReplyText(ctx, event.ReplyToken, errText, nil); rErr != nil {
			_ = s.LineClient.PushText(ctx, userID, errText, nil)
		}
		return
	}

	transcription, err := ai.TranscribeAudio(ctx, s.GeminiClient, audioBytes, "audio/mp4")
	if err != nil {
		errText := line.FormatErrorMessage(err, "處理語音訊息")
		if rErr := s.LineClient.ReplyText(ctx, event.ReplyToken, errText, nil); rErr != nil {
			_ = s.LineClient.PushText(ctx, userID, errText, nil)
		}
		return
	}

	t := strings.TrimSpace(transcription)
	if t == "" {
		_ = s.LineClient.ReplyText(ctx, event.ReplyToken, "無法辨識語音內容，請重新錄製。", nil)
		return
	}

	s.handleTextMessageViaOrchestrator(
		ctx,
		event,
		userID,
		t,
		"",
		fmt.Sprintf("🎤 你說的是：%s\n\n", t),
		true,
	)
}

func (s *Server) handleLocationMessage(ctx context.Context, event line.WebhookEvent, userID string) {
	lat := event.Message.Latitude
	lng := event.Message.Longitude
	addr := event.Message.Address

	makeBtn := func(label, placeType string) line.QuickReplyItem {
		b, _ := json.Marshal(map[string]any{
			"action":     "search_nearby",
			"place_type": placeType,
			"latitude":   lat,
			"longitude":  lng,
			"address":    addr,
		})
		return line.NewPostbackButton(label, string(b), label)
	}

	qr := line.NewQuickReply(
		makeBtn("⛽ 找加油站", "gas_station"),
		makeBtn("🅿️ 找停車場", "parking"),
		makeBtn("🍴 找餐廳", "restaurant"),
	)

	if userID != "" && s.SessionManager != nil {
		s.SessionManager.GetOrCreateSession(userID)
		s.SessionManager.SetMetadata(userID, "last_location", map[string]any{
			"latitude":  lat,
			"longitude": lng,
			"address":   addr,
		})
	}

	displayAddr := addr
	if displayAddr == "" {
		displayAddr = "位置已記錄"
	}
	replyText := fmt.Sprintf("📍 已收到你的位置\n\n%s\n\n請選擇要搜尋的類型：", displayAddr)
	_ = s.LineClient.ReplyText(ctx, event.ReplyToken, replyText, qr)
}

func (s *Server) handleBookmarkCommand(ctx context.Context, event line.WebhookEvent, userID, command, argument string) {
	if s.BookmarkService == nil || !s.BookmarkService.Available() {
		_ = s.LineClient.ReplyText(ctx, event.ReplyToken, "🔖 書籤功能暫時無法使用，請稍後再試。", nil)
		return
	}

	switch command {
	case "save":
		rawURL := strings.TrimSpace(argument)
		if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
			_ = s.LineClient.ReplyText(ctx, event.ReplyToken, "用法：/save <網址>\n例如：/save https://example.com", nil)
			return
		}
		_ = s.LineClient.ReplyText(ctx, event.ReplyToken, "🔖 收到！正在擷取內容與產生摘要，完成後通知你。", nil)

		crawledText, err := s.Loader.LoadURL(ctx, rawURL, "normal")
		if err != nil {
			errMsg := line.FormatErrorMessage(err, "儲存書籤")
			_ = s.LineClient.PushText(ctx, userID, fmt.Sprintf("%s\n\n%s", rawURL, errMsg), nil)
			return
		}
		bs := ai.SummarizeForBookmark(ctx, s.GeminiClient, crawledText)
		s.BookmarkService.SaveDirect(userID, rawURL, bs.Title, bs.Summary)
		_ = s.LineClient.PushText(ctx, userID, fmt.Sprintf("✅ 已存入書籤：%s\n\n%s\n\n用 /list 查看所有書籤", bs.Title, bs.Summary), nil)

	case "list":
		bookmarks := s.BookmarkService.ListSaved(userID, 10)
		if len(bookmarks) == 0 {
			_ = s.LineClient.ReplyText(ctx, event.ReplyToken, "📭 還沒有任何書籤。\n傳網址後按「🔖 儲存書籤」，或用 /save <網址> 新增。", nil)
			return
		}
		carousel := line.BuildBookmarkCarousel(bookmarks)
		_ = s.LineClient.ReplyMessages(ctx, event.ReplyToken, []map[string]any{
			line.NewFlexMessage("🔖 我的書籤", carousel, nil),
		})

	case "search":
		kw := strings.TrimSpace(argument)
		if kw == "" {
			_ = s.LineClient.ReplyText(ctx, event.ReplyToken, "用法：/search <關鍵字>\n例如：/search python", nil)
			return
		}
		results := s.BookmarkService.Search(userID, kw, 10)
		if len(results) == 0 {
			_ = s.LineClient.ReplyText(ctx, event.ReplyToken, fmt.Sprintf("🔍 找不到包含「%s」的書籤。", kw), nil)
			return
		}
		carousel := line.BuildBookmarkCarousel(results)
		_ = s.LineClient.ReplyMessages(ctx, event.ReplyToken, []map[string]any{
			line.NewFlexMessage(fmt.Sprintf("🔍 搜尋：%s", kw), carousel, nil),
		})
	}
}

func (s *Server) handlePostbackEvent(ctx context.Context, event line.WebhookEvent) {
	if event.Postback == nil {
		return
	}
	userID := event.Source.UserID
	var data map[string]any
	if err := json.Unmarshal([]byte(event.Postback.Data), &data); err != nil {
		return
	}
	action, _ := data["action"].(string)

	switch action {
	case "search_nearby":
		s.handleMapSearchPostback(ctx, event, data, userID)
	case "foodie_deep_analysis":
		s.handleFoodieDeepAnalysisPostback(ctx, event, data, userID)
	case "specific_foodie_deep_analysis":
		s.handleSpecificFoodieDeepAnalysisPostback(ctx, event, data, userID)
	case "image_analyze":
		s.handleImageAnalyzePostback(ctx, event, data, userID)
	case "youtube_summary":
		s.handleYouTubeSummaryPostback(ctx, event, data, userID)
	case "read_aloud":
		s.handleReadAloudPostback(ctx, event, data, userID)
	case "save_bookmark":
		s.handleBookmarkSavePostback(ctx, event, data, userID)
	case "delete_bookmark":
		s.handleBookmarkDeletePostback(ctx, event, data, userID)
	case "research_report":
		s.handleResearchReportPostback(ctx, event, data, userID)
	}
}

func toFloat(v any) (float64, bool) {
	switch val := v.(type) {
	case float64:
		return val, true
	case float32:
		return float64(val), true
	case int:
		return float64(val), true
	case int64:
		return float64(val), true
	default:
		return 0, false
	}
}

func (s *Server) handleMapSearchPostback(ctx context.Context, event line.WebhookEvent, data map[string]any, userID string) {
	placeType, _ := data["place_type"].(string)
	lat, okLat := toFloat(data["latitude"])
	lng, okLng := toFloat(data["longitude"])

	if placeType == "" || !okLat || !okLng {
		_ = s.LineClient.ReplyText(ctx, event.ReplyToken, "❌ 位置資訊不完整，請重新傳送位置。", nil)
		return
	}

	_ = s.LineClient.ReplyText(ctx, event.ReplyToken, "🔍 搜尋中，請稍候...", nil)

	res := s.Orchestrator.LocationAgent.Search(ctx, lat, lng, placeType, "")
	respText := agents.FormatLocationResponse(res)

	var qr *line.QuickReply
	if placeType == "restaurant" && res.Status == "success" && len(res.RestaurantNames) > 0 {
		var items []line.QuickReplyItem
		limit := len(res.RestaurantNames)
		if limit > 3 {
			limit = 3
		}
		for _, name := range res.RestaurantNames[:limit] {
			cleanLabel := name
			runes := []rune(cleanLabel)
			if len(runes) > 10 {
				cleanLabel = string(runes[:9]) + "…"
			}
			b, _ := json.Marshal(map[string]string{
				"action":          "specific_foodie_deep_analysis",
				"restaurant_name": name,
			})
			items = append(items, line.NewPostbackButton(
				fmt.Sprintf("🍴 分析 %s", cleanLabel),
				string(b),
				fmt.Sprintf("🔍 進行「%s」深度評論與招牌菜色分析", name),
			))
		}
		qr = line.NewQuickReply(items...)
	}

	if userID != "" {
		_ = s.LineClient.PushText(ctx, userID, respText, qr)
	}
}

func (s *Server) resolveWebhookDomain() string {
	if s.Config.WebhookDomain != "" {
		return s.Config.WebhookDomain
	}
	baseURL := s.getAppBaseURL()
	if baseURL != "" {
		if u, err := url.Parse(baseURL); err == nil && u.Host != "" {
			return u.Host
		}
	}
	return ""
}

func (s *Server) handleSpecificFoodieDeepAnalysisPostback(ctx context.Context, event line.WebhookEvent, data map[string]any, userID string) {
	restaurantName, _ := data["restaurant_name"].(string)
	if restaurantName == "" || userID == "" {
		_ = s.LineClient.ReplyText(ctx, event.ReplyToken, "❌ 餐廳資訊不完整。", nil)
		return
	}

	if s.BatchService != nil {
		jobs, _ := s.BatchService.ListBatchJobs(ctx)
		prefix := fmt.Sprintf("FoodieAnalysis_%s_", userID)
		for _, j := range jobs {
			if strings.HasPrefix(j.DisplayName, prefix) &&
				!strings.Contains(j.State, "JOB_STATE_SUCCEEDED") &&
				!strings.Contains(j.State, "JOB_STATE_FAILED") {
				_ = s.LineClient.ReplyText(ctx, event.ReplyToken, "⏳ 您的深度分析任務正在執行中，請耐心等候結果送達。", nil)
				return
			}
		}
	}

	ackText := fmt.Sprintf("🔍 收到！正在為您對「%s」進行深度評論與招牌菜色大數據分析，大約需要 1-2 分鐘，請稍候...", restaurantName)
	_ = s.LineClient.ReplyText(ctx, event.ReplyToken, ackText, nil)
	go s.runSpecificRestaurantAnalysisBackground(context.Background(), userID, restaurantName, userID)
}

func (s *Server) handleFoodieDeepAnalysisPostback(ctx context.Context, event line.WebhookEvent, data map[string]any, userID string) {
	lat, okLat := toFloat(data["latitude"])
	lng, okLng := toFloat(data["longitude"])
	if !okLat || !okLng || userID == "" {
		_ = s.LineClient.ReplyText(ctx, event.ReplyToken, "❌ 位置資訊不完整，請重新傳送位置。", nil)
		return
	}

	ackText := "⏳ 正在為您搜集附近餐廳的 50+ 則最新評論，並建立 Gemini Batch API 批次大數據分析任務。\n\n本分析包含招牌菜推薦、整體口味與雷點摘要，預計需要 1~2 分鐘，分析完成後我會主動將專屬報告推播給您！"
	_ = s.LineClient.ReplyText(ctx, event.ReplyToken, ackText, nil)
	go s.runFoodieDeepAnalysisBackground(context.Background(), userID, lat, lng)
}

func (s *Server) runFoodieDeepAnalysisBackground(ctx context.Context, userID string, lat, lng float64) {
	restaurants, err := ai.GetNearbyRestaurantsForBatch(ctx, s.GeminiClient, lat, lng, 1, "zh-TW")
	if err != nil {
		_ = s.LineClient.PushText(ctx, userID, fmt.Sprintf("⚠️ 搜尋餐廳失敗: %v", err), nil)
		return
	}
	if len(restaurants) == 0 {
		_ = s.LineClient.PushText(ctx, userID, "⚠️ 附近似乎沒有找到合適的餐廳進行深度分析。", nil)
		return
	}

	subRes := s.BatchService.SubmitRestaurantBatchJob(
		ctx,
		userID,
		map[string]float64{"latitude": lat, "longitude": lng},
		restaurants,
		s.resolveWebhookDomain(),
	)
	if subRes.Status != "success" {
		_ = s.LineClient.PushText(ctx, userID, fmt.Sprintf("❌ %s", subRes.ErrorMessage), nil)
	}
}

func (s *Server) runSpecificRestaurantAnalysisBackground(ctx context.Context, userID, restaurantName, pushUserID string) {
	rest, err := ai.SearchSpecificRestaurantByName(ctx, s.GeminiClient, restaurantName)
	if err != nil || rest == nil || rest.Name == "" {
		_ = s.LineClient.PushText(ctx, pushUserID, fmt.Sprintf("⚠️ 找不到關於餐廳「%s」的評價資訊。", restaurantName), nil)
		return
	}

	subRes := s.BatchService.SubmitRestaurantBatchJob(
		ctx,
		userID,
		map[string]float64{"latitude": 0.0, "longitude": 0.0},
		[]ai.RestaurantDetail{*rest},
		s.resolveWebhookDomain(),
	)
	if subRes.Status != "success" {
		_ = s.LineClient.PushText(ctx, pushUserID, fmt.Sprintf("❌ %s", subRes.ErrorMessage), nil)
	}
}

func (s *Server) handleYouTubeSummaryPostback(ctx context.Context, event line.WebhookEvent, data map[string]any, userID string) {
	mode, _ := data["mode"].(string)
	rawURL, _ := data["url"].(string)
	if mode == "" || rawURL == "" {
		return
	}

	modeText := "Twitter 分享文案"
	if mode == "detail" {
		modeText = "詳細摘要"
	}
	_ = s.LineClient.ReplyText(ctx, event.ReplyToken, fmt.Sprintf("⏳ 正在生成%s，請稍候...", modeText), nil)

	res := s.Orchestrator.ContentAgent.SummarizeYouTube(ctx, rawURL, mode)
	var out string
	if res.Status != "success" {
		errMsg := res.ErrorMessage
		if errMsg == "" {
			errMsg = "無法生成影片摘要"
		}
		out = fmt.Sprintf("⚠️ %s", errMsg)
	} else {
		out = fmt.Sprintf("%s\n\n%s", rawURL, res.Summary)
	}
	if userID != "" {
		_ = s.LineClient.PushText(ctx, userID, out, nil)
	}
}

func (s *Server) handleImageAnalyzePostback(ctx context.Context, event line.WebhookEvent, data map[string]any, userID string) {
	mode, _ := data["mode"].(string)
	s.mu.RLock()
	_, hasImg := s.imageTempStore[userID]
	s.mu.RUnlock()

	if userID == "" || !hasImg {
		_ = s.LineClient.ReplyText(ctx, event.ReplyToken, "⚠️ 圖片已過期，請重新傳送圖片。", nil)
		return
	}

	if mode == "agentic_vision" {
		s.mu.Lock()
		s.pendingAgenticVision[userID] = true
		s.mu.Unlock()
		_ = s.LineClient.ReplyText(ctx, event.ReplyToken,
			"🔍 Agentic Vision 模式\n\n請輸入你想要分析的指令，例如：\n• 數一數圖片中有幾個人\n• 找出圖片中所有的文字\n• 分析圖表中的數據趨勢",
			nil,
		)
		return
	}

	s.mu.Lock()
	imgData := s.imageTempStore[userID]
	delete(s.imageTempStore, userID)
	s.mu.Unlock()

	_ = s.LineClient.ReplyText(ctx, event.ReplyToken, "⏳ 正在使用識別圖片分析中，請稍候...", nil)

	result := s.Orchestrator.ProcessImage(ctx, imgData, "")
	respText := agents.FormatOrchestratorResponse(result)
	if utf8.RuneCountInString(respText) > 4500 {
		runes := []rune(respText)
		respText = string(runes[:4400]) + "\n\n... (訊息過長，已截斷)"
	}
	_ = s.LineClient.PushText(ctx, userID, respText, nil)
}

func (s *Server) handleBookmarkSavePostback(ctx context.Context, event line.WebhookEvent, data map[string]any, userID string) {
	docID, _ := data["id"].(string)
	if docID == "" || s.BookmarkService == nil || !s.BookmarkService.Available() {
		_ = s.LineClient.ReplyText(ctx, event.ReplyToken, "🔖 書籤功能暫時無法使用，請稍後再試。", nil)
		return
	}

	doc := s.BookmarkService.ConfirmSave(userID, docID)
	if doc != nil {
		title, _ := doc["title"].(string)
		_ = s.LineClient.ReplyText(ctx, event.ReplyToken, fmt.Sprintf("✅ 已存入書籤：%s\n\n用 /list 查看所有書籤", title), nil)
	} else {
		_ = s.LineClient.ReplyText(ctx, event.ReplyToken, "⚠️ 書籤候選已過期，請重新傳送網址。", nil)
	}
}

func (s *Server) handleBookmarkDeletePostback(ctx context.Context, event line.WebhookEvent, data map[string]any, userID string) {
	docID, _ := data["id"].(string)
	if docID == "" || s.BookmarkService == nil || !s.BookmarkService.Available() {
		_ = s.LineClient.ReplyText(ctx, event.ReplyToken, "🔖 書籤功能暫時無法使用，請稍後再試。", nil)
		return
	}

	if s.BookmarkService.Delete(userID, docID) {
		_ = s.LineClient.ReplyText(ctx, event.ReplyToken, "🗑 已刪除書籤。", nil)
	} else {
		_ = s.LineClient.ReplyText(ctx, event.ReplyToken, "⚠️ 找不到這個書籤（可能已刪除）。", nil)
	}
}

func (s *Server) handleResearchReportPostback(ctx context.Context, event line.WebhookEvent, data map[string]any, userID string) {
	docID, _ := data["id"].(string)
	var doc map[string]any
	if docID != "" && s.BookmarkService != nil && s.BookmarkService.Available() {
		doc = s.BookmarkService.GetBookmark(userID, docID)
	}
	if doc == nil {
		_ = s.LineClient.ReplyText(ctx, event.ReplyToken, "⚠️ 資料已過期，請重新傳送網址再試一次。", nil)
		return
	}

	baseURL := s.getAppBaseURL()
	cachedReportID, _ := doc["report_id"].(string)
	cachedAt, hasCachedAt := toFloat(doc["report_generated_at"])
	title, _ := doc["title"].(string)
	rawURL, _ := doc["url"].(string)

	if cachedReportID != "" && hasCachedAt && baseURL != "" &&
		float64(time.Now().Unix())-cachedAt < ReportCacheTTLSeconds {
		reportURL := fmt.Sprintf("%s/reports/%s", baseURL, cachedReportID)
		_ = s.LineClient.ReplyText(ctx, event.ReplyToken, fmt.Sprintf("📄 研究報告（先前已產生）：%s\n\n%s", title, reportURL), nil)
		return
	}

	_ = s.LineClient.ReplyText(ctx, event.ReplyToken, "🔬 開始深入研究這篇文章（約 1-2 分鐘），完成後把報告連結傳給你。", nil)

	crawledText, err := s.Loader.LoadURL(ctx, rawURL, "normal")
	if err != nil {
		errMsg := line.FormatErrorMessage(err, "產生研究報告")
		_ = s.LineClient.PushText(ctx, userID, fmt.Sprintf("%s\n\n%s", rawURL, errMsg), nil)
		return
	}

	res, err := ai.GenerateResearchReport(ctx, s.GeminiClient, crawledText, rawURL)
	if err != nil {
		errMsg := line.FormatErrorMessage(err, "產生研究報告")
		_ = s.LineClient.PushText(ctx, userID, fmt.Sprintf("%s\n\n%s", rawURL, errMsg), nil)
		return
	}

	pageTitle := title
	if pageTitle == "" {
		pageTitle = "研究報告"
	}
	htmlPage := services.RenderReportPage(pageTitle, res.Markdown, rawURL, res.Sources)
	reportID := s.ReportStore.Put(htmlPage)
	s.BookmarkService.RecordReport(docID, reportID)

	if baseURL == "" {
		_ = s.LineClient.PushText(ctx, userID, "⚠️ 報告已產生但暫時無法提供連結，請稍後再試一次。", nil)
		return
	}

	reportURL := fmt.Sprintf("%s/reports/%s", baseURL, reportID)
	_ = s.LineClient.PushText(ctx, userID, fmt.Sprintf("📄 研究報告完成：%s\n\n%s", title, reportURL), nil)
}

func (s *Server) handleReadAloudPostback(ctx context.Context, event line.WebhookEvent, data map[string]any, userID string) {
	summaryID, _ := data["summary_id"].(string)
	if summaryID == "" {
		_ = s.LineClient.ReplyText(ctx, event.ReplyToken, "摘要已過期，請重新傳送網址。", nil)
		return
	}

	text, ok := s.getSummary(summaryID)
	if !ok {
		_ = s.LineClient.ReplyText(ctx, event.ReplyToken, "摘要已過期，請重新傳送網址。", nil)
		return
	}

	runes := []rune(text)
	if len(runes) > MaxTTSChars {
		text = string(runes[:MaxTTSChars])
	}

	baseURL := s.getAppBaseURL()
	if baseURL == "" {
		_ = s.LineClient.ReplyText(ctx, event.ReplyToken, "語音服務暫時無法使用，請稍後再試。", nil)
		return
	}

	m4aBytes, durationMs, err := s.GeminiClient.TextToSpeech(ctx, text)
	if err != nil {
		errText := line.FormatErrorMessage(err, "產生語音")
		_ = s.LineClient.PushText(ctx, userID, errText, nil)
		return
	}

	audioID := s.storeAudio(m4aBytes)
	audioURL := fmt.Sprintf("%s/audio/%s", baseURL, audioID)
	_ = s.LineClient.ReplyMessages(ctx, event.ReplyToken, []map[string]any{
		line.NewAudioMessage(audioURL, durationMs),
	})
}

func (s *Server) handleURLPushMessage(ctx context.Context, title string, urls []string, targetUserID, token string) {
	var results []map[string]any
	for _, rawURL := range urls {
		content, err := s.Loader.LoadURL(ctx, rawURL, "normal")
		if err != nil {
			errMsg := line.FriendlyErrorMessage(err, rawURL)
			results = append(results, line.NewTextMessage(fmt.Sprintf("%s\n%s\n\n%s", rawURL, title, errMsg), nil))
			continue
		}
		if strings.TrimSpace(content) == "" {
			results = append(results, line.NewTextMessage(fmt.Sprintf("%s\n%s\n\n⚠️ 無法從這個網址提取內容。", rawURL, title), nil))
			continue
		}
		summary, err := ai.SummarizeText(ctx, s.GeminiClient, content, "normal")
		if err != nil {
			errMsg := line.FriendlyErrorMessage(err, rawURL)
			results = append(results, line.NewTextMessage(fmt.Sprintf("%s\n%s\n\n%s", rawURL, title, errMsg), nil))
			continue
		}
		results = append(results, line.NewTextMessage(fmt.Sprintf("%s\n%s\n\n%s", rawURL, title, summary), nil))
	}

	if len(results) > 0 && targetUserID != "" && token != "" {
		client := s.LineClient.WithToken(token)
		_ = client.PushMessages(ctx, targetUserID, results)
	}
}

func (s *Server) handleHackerNews(w http.ResponseWriter, r *http.Request) {
	var data struct {
		Title    string `json:"title"`
		URL      string `json:"url"`
		StoryURL string `json:"StoryUrl"`
	}
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	urls := []string{data.URL}
	if data.StoryURL != "" {
		urls = append(urls, data.StoryURL)
	}
	s.handleURLPushMessage(r.Context(), data.Title, urls, s.Config.LineUserID, s.Config.ChannelAccessToken)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleHuggingFace(w http.ResponseWriter, r *http.Request) {
	var data struct {
		Title string `json:"title"`
		URL   string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	u := strings.ReplaceAll(data.URL, "paperswithcode.com", "huggingface.co")
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		http.Error(w, `{"detail":"Invalid URL protocol"}`, http.StatusBadRequest)
		return
	}
	s.handleURLPushMessage(r.Context(), data.Title, []string{u}, s.Config.LineUserID, s.Config.ChannelAccessTokenHF)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleMultiURLs(w http.ResponseWriter, r *http.Request) {
	var data struct {
		Title string   `json:"title"`
		URLs  []string `json:"urls"`
	}
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	if len(data.URLs) == 0 {
		http.Error(w, `{"detail":"urls must be a non-empty array"}`, http.StatusBadRequest)
		return
	}
	if len(data.URLs) > 5 {
		http.Error(w, `{"detail":"Maximum 5 URLs allowed"}`, http.StatusBadRequest)
		return
	}
	s.handleURLPushMessage(r.Context(), data.Title, data.URLs, s.Config.LineUserID, s.Config.ChannelAccessToken)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":         "ok",
		"processed_urls": len(data.URLs),
	})
}

func (s *Server) handleGeminiStaticWebhook(w http.ResponseWriter, r *http.Request) {
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	bodyStr := string(bodyBytes)

	if s.Config.WebhookSigningSecret != "" {
		if !services.VerifyStaticWebhook(bodyStr, r.Header, s.Config.WebhookSigningSecret) {
			http.Error(w, `{"detail":"Invalid signature"}`, http.StatusBadRequest)
			return
		}
	}

	var payload map[string]any
	if err := json.Unmarshal(bodyBytes, &payload); err != nil {
		http.Error(w, `{"detail":"Bad Request"}`, http.StatusBadRequest)
		return
	}

	s.dispatchBatchWebhookPayload(payload)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "received"})
}

func (s *Server) handleGeminiDynamicWebhook(w http.ResponseWriter, r *http.Request) {
	_, _ = io.ReadAll(r.Body)
	token := r.Header.Get("Webhook-Signature")
	if token == "" {
		http.Error(w, `{"detail":"Missing signature header"}`, http.StatusBadRequest)
		return
	}

	claims, err := services.VerifyDynamicWebhook(r.Context(), token, s.JWKSURI, nil)
	if err != nil {
		http.Error(w, `{"detail":"Invalid JWT signature"}`, http.StatusBadRequest)
		return
	}

	s.dispatchBatchWebhookPayload(claims)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "received"})
}

func (s *Server) dispatchBatchWebhookPayload(payload map[string]any) {
	eventType, _ := payload["type"].(string)
	eventData, _ := payload["data"].(map[string]any)
	if eventData == nil {
		eventData = map[string]any{}
	}

	batchID, _ := eventData["id"].(string)
	if batchID == "" {
		batchID, _ = eventData["name"].(string)
	}

	switch eventType {
	case "batch.succeeded":
		outputFileURI, _ := eventData["output_file_uri"].(string)
		var userID string
		if um, ok := eventData["user_metadata"].(map[string]any); ok {
			userID, _ = um["user_id"].(string)
		}
		if userID == "" {
			if um, ok := payload["user_metadata"].(map[string]any); ok {
				userID, _ = um["user_id"].(string)
			}
		}
		if batchID != "" && outputFileURI != "" {
			go s.ProcessBatchCompletedWebhook(context.Background(), batchID, outputFileURI, userID)
		}
	case "batch.failed":
		if batchID != "" {
			go s.ProcessBatchFailedWebhook(context.Background(), batchID)
		}
	}
}

// ProcessBatchCompletedWebhook downloads batch results and pushes them to the user on LINE.
func (s *Server) ProcessBatchCompletedWebhook(ctx context.Context, batchJobID, outputFileURI, userID string) bool {
	if s.BatchService == nil {
		return false
	}
	if userID == "" {
		if m := s.BatchService.GetJobMapping(batchJobID); m != nil {
			userID, _ = m["user_id"].(string)
		}
	}
	if userID == "" {
		if info, err := s.BatchService.GetBatchJob(ctx, batchJobID); err == nil && info != nil {
			if strings.HasPrefix(info.DisplayName, "FoodieAnalysis_") {
				parts := strings.Split(info.DisplayName, "_")
				if len(parts) >= 2 {
					userID = parts[1]
				}
			}
		}
	}
	if userID == "" {
		slog.Error("Cannot process batch: no user_id associated", "batch_job_id", batchJobID)
		return true
	}

	items := s.BatchService.DownloadAndParseBatchResults(ctx, outputFileURI)
	if len(items) == 0 {
		_ = s.LineClient.PushText(ctx, userID, "⚠️ 深度美食評論分析處理完成，但無法解析分析結果。", nil)
		return true
	}

	messages := []map[string]any{
		line.NewTextMessage("🎉 **您的附近餐廳深度評論與菜色大數據分析已出爐！** 👇", nil),
	}
	for _, item := range items {
		messages = append(messages, line.NewTextMessage(fmt.Sprintf("🍴 **%s**\n\n%s", item.Name, item.Analysis), nil))
	}
	if err := s.LineClient.PushMessages(ctx, userID, messages); err != nil {
		slog.Error("Failed to push batch results", "error", err)
		return false
	}
	return true
}

// ProcessBatchFailedWebhook notifies the user when a batch job fails.
func (s *Server) ProcessBatchFailedWebhook(ctx context.Context, batchJobID string) bool {
	if s.BatchService == nil {
		return false
	}
	if m := s.BatchService.GetJobMapping(batchJobID); m != nil {
		if userID, _ := m["user_id"].(string); userID != "" {
			_ = s.LineClient.PushText(ctx, userID, "❌ 很抱歉，您的美食深度評論分析任務執行失敗，請稍後重試。", nil)
		}
	}
	return true
}

// StartBatchPollingLoop polls Gemini Batch API every 60 seconds for completed/failed FoodieAnalysis jobs.
func (s *Server) StartBatchPollingLoop(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.pollBatchJobsOnce(ctx)
			}
		}
	}()
}

func (s *Server) pollBatchJobsOnce(ctx context.Context) {
	if s.BatchService == nil || s.BatchService.APIKey == "" {
		return
	}
	jobs, err := s.BatchService.ListBatchJobs(ctx)
	if err != nil {
		return
	}
	for _, job := range jobs {
		if !strings.HasPrefix(job.DisplayName, "FoodieAnalysis_") {
			continue
		}
		parts := strings.Split(job.DisplayName, "_")
		if len(parts) < 3 {
			continue
		}
		userID := parts[1]
		isSucceeded := strings.Contains(job.State, "JOB_STATE_SUCCEEDED")
		isFailed := strings.Contains(job.State, "JOB_STATE_FAILED")

		if isSucceeded {
			destFile := job.DestFile
			if destFile == "" {
				slashParts := strings.Split(job.Name, "/")
				destFile = "files/batch-" + slashParts[len(slashParts)-1]
			}
			if s.ProcessBatchCompletedWebhook(ctx, job.Name, destFile, userID) {
				_ = s.BatchService.DeleteBatchJob(ctx, job.Name)
			}
		} else if isFailed {
			if s.ProcessBatchFailedWebhook(ctx, job.Name) {
				_ = s.BatchService.DeleteBatchJob(ctx, job.Name)
			}
		}
	}
}

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func (s *Server) handleVoiceWS(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("session_id")
	browserWS, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer browserWS.Close()
	slog.Info("Voice WS connected", "session_id", sessionID)

	_ = browserWS.SetReadDeadline(time.Now().Add(15 * time.Second))
	_, initBytes, err := browserWS.ReadMessage()
	if err != nil {
		_ = browserWS.WriteJSON(map[string]string{"type": "error", "message": "初始化逾時"})
		return
	}
	_ = browserWS.SetReadDeadline(time.Time{})

	var initData struct {
		UserID         string   `json:"user_id"`
		Lat            *float64 `json:"lat"`
		Lng            *float64 `json:"lng"`
		Handsfree      bool     `json:"handsfree"`
		VADSensitivity string   `json:"vad_sensitivity"`
	}
	if err := json.Unmarshal(initBytes, &initData); err != nil {
		_ = browserWS.WriteJSON(map[string]string{"type": "error", "message": "語音服務暫時無法使用"})
		return
	}
	userID := initData.UserID
	if userID == "" {
		userID = sessionID
	}

	systemInst := voice.BuildSystemInstruction(initData.Lat, initData.Lng)
	state := &voice.SessionState{Handsfree: initData.Handsfree}

	s.mu.RLock()
	if re, ok := s.voiceResumeHandles[userID]; ok && time.Since(re.CreatedAt) < VoiceResumeTTL {
		state.SetResumeHandle(re.Handle)
	}
	s.mu.RUnlock()

	delegateFn := func(request string) {
		ctx := context.Background()
		result := s.Orchestrator.ProcessText(ctx, userID, request, "normal")
		respText := agents.FormatOrchestratorResponse(result)
		if utf8.RuneCountInString(respText) > 4500 {
			runes := []rune(respText)
			respText = string(runes[:4400]) + "\n\n... (訊息過長，已截斷)"
		}
		if err := s.LineClient.PushText(ctx, userID, fmt.Sprintf("🎤 語音助手轉交的任務完成：\n\n%s", respText), nil); err != nil {
			_ = s.LineClient.PushText(ctx, userID, "⚠️ 語音助手轉交的任務處理失敗，請稍後再試。", nil)
		}
	}

	toolHandler := voice.MakeToolHandler(initData.Lat, initData.Lng, s.GeminiClient, delegateFn, nil)
	pushFn := func(userSpeech, aiResponse string) {
		state.AddTurn(userSpeech, aiResponse)
	}

	liveURL := s.GeminiLiveURL
	if liveURL == "" {
		liveURL = fmt.Sprintf("wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent?key=%s", s.Config.GoogleAIAPIKey)
	}

	for {
		geminiWS, _, err := websocket.DefaultDialer.DialContext(r.Context(), liveURL, nil)
		if err != nil {
			slog.Error("Voice WS failed to connect to Gemini Live", "error", err)
			_ = browserWS.WriteJSON(map[string]string{"type": "error", "message": "語音服務暫時無法使用"})
			break
		}

		setupMsg := voice.BuildLiveSetupMessage(
			s.Config.VoiceModel,
			systemInst,
			initData.Handsfree,
			voice.BuildVoiceTools(),
			state.GetResumeHandle(),
			initData.VADSensitivity,
		)
		if err := geminiWS.WriteJSON(setupMsg); err != nil {
			_ = geminiWS.Close()
			break
		}

		doneCh := make(chan struct{})
		var once sync.Once
		signalDone := func() {
			once.Do(func() { close(doneCh) })
		}

		go func() {
			voice.BrowserToGemini(browserWS, geminiWS, state)
			signalDone()
		}()
		go func() {
			voice.GeminiToBrowser(r.Context(), browserWS, geminiWS, state, pushFn, toolHandler)
			signalDone()
		}()

		<-doneCh
		_ = geminiWS.Close()

		if h := state.GetResumeHandle(); h != "" {
			s.mu.Lock()
			s.voiceResumeHandles[userID] = resumeEntry{Handle: h, CreatedAt: time.Now()}
			s.mu.Unlock()
		}

		if state.PopGoAway() {
			slog.Info("Voice Live GoAway — reconnecting Gemini", "user_id", userID)
			continue
		}
		break
	}

	summary := voice.FormatSessionSummary(state.Turns)
	if summary != "" {
		_ = s.LineClient.PushText(context.Background(), userID, summary, nil)
	}
}
