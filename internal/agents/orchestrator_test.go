package agents

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kkdai/linebot-helper-go/internal/config"
	"github.com/kkdai/linebot-helper-go/internal/firestore"
	"github.com/kkdai/linebot-helper-go/internal/gemini"
	"github.com/kkdai/linebot-helper-go/internal/loader"
	"github.com/kkdai/linebot-helper-go/internal/services"
	"golang.org/x/oauth2"
)

func newTestOrchestrator(t *testing.T, geminiHandler http.HandlerFunc) (*Orchestrator, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(geminiHandler)
	client := gemini.NewClient("test-proj", "global", "test-key")
	client.VertexBaseURL = srv.URL
	client.TokenSource = oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "fake-token"})

	cfg := &config.Config{
		ChatModel:           "gemini-3.1-flash-lite",
		FastModel:           "gemini-3.1-flash-lite",
		EnableGrounding:     true,
		EnableMapsGrounding: true,
		Temperature:         0.7,
		MaxOutputTokens:     2048,
	}
	sm := services.NewSessionManager(30, 20, 300, firestore.NewMemoryKVStore(true))
	l := loader.NewLoader("", "")
	return NewOrchestrator(cfg, client, sm, l), srv
}

func TestDetectIntents(t *testing.T) {
	orch, srv := newTestOrchestrator(t, func(w http.ResponseWriter, r *http.Request) {})
	defer srv.Close()

	if orch.ADKAgent == nil {
		t.Fatal("expected Orchestrator.ADKAgent to be initialized with google.golang.org/adk")
	}
	if len(orch.ADKAgent.SubAgents()) != 5 {
		t.Fatalf("expected 5 ADK sub-agents, got %d", len(orch.ADKAgent.SubAgents()))
	}

	tests := []struct {
		msg      string
		expected []IntentType
	}{
		{"/clear", []IntentType{IntentCommand}},
		{"/狀態", []IntentType{IntentCommand}},
		{"/help", []IntentType{IntentCommand}},
		{"/stats", []IntentType{IntentCommand}},
		{"@g", []IntentType{IntentGitHubSummary}},
		{"https://youtu.be/dQw4w9WgXcQ", []IntentType{IntentYouTubeSummary}},
		{"https://example.com/article", []IntentType{IntentURLSummary}},
		{"看這兩篇 https://example.com/a 跟 https://www.youtube.com/watch?v=123", []IntentType{IntentURLSummary, IntentYouTubeSummary}},
		{"推薦附近有什麼好吃的拉麵？", []IntentType{IntentRestaurantSearch}},
		{"請問量子電腦是什麼？", []IntentType{IntentChat}},
	}

	for _, tc := range tests {
		intents := orch.DetectIntents(tc.msg)
		if len(intents) != len(tc.expected) {
			t.Fatalf("msg=%q: expected %d intents, got %d", tc.msg, len(tc.expected), len(intents))
		}
		for i, exp := range tc.expected {
			if intents[i].Type != exp {
				t.Errorf("msg=%q[%d]: expected %s, got %s", tc.msg, i, exp, intents[i].Type)
			}
		}
	}
}

func TestOrchestratorCommandsAndChat(t *testing.T) {
	orch, srv := newTestOrchestrator(t, func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"candidates": []map[string]any{
				{
					"content": map[string]any{
						"parts": []map[string]any{{"text": "這是測試回答"}},
					},
					"groundingMetadata": map[string]any{
						"groundingChunks": []map[string]any{
							{"web": map[string]any{"title": "Wiki", "uri": "https://zh.wikipedia.org"}},
						},
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
	defer srv.Close()

	ctx := context.Background()

	// 1. /status before session exists
	res := orch.ProcessText(ctx, "u1", "/status", "normal")
	out := FormatOrchestratorResponse(res)
	if !strings.Contains(out, "目前沒有進行中的對話") {
		t.Errorf("unexpected status output: %s", out)
	}

	// 2. Chat message creates session and returns formatted response with sources
	res = orch.ProcessText(ctx, "u1", "你好", "normal")
	out = FormatOrchestratorResponse(res)
	if !strings.Contains(out, "💬 [對話中]") || !strings.Contains(out, "這是測試回答") || !strings.Contains(out, "https://zh.wikipedia.org") {
		t.Errorf("unexpected chat output: %s", out)
	}

	// 3. /status after chat
	res = orch.ProcessText(ctx, "u1", "/status", "normal")
	out = FormatOrchestratorResponse(res)
	if !strings.Contains(out, "對話輪數：2 條訊息") {
		t.Errorf("unexpected active status output: %s", out)
	}

	// 4. /stats
	res = orch.ProcessText(ctx, "u1", "/stats", "normal")
	out = FormatOrchestratorResponse(res)
	if !strings.Contains(out, "活躍對話數：1") {
		t.Errorf("unexpected stats output: %s", out)
	}

	// 5. /help
	res = orch.ProcessText(ctx, "u1", "/help", "normal")
	out = FormatOrchestratorResponse(res)
	if !strings.Contains(out, "智能助手") {
		t.Errorf("unexpected help output: %s", out)
	}

	// 6. /clear
	res = orch.ProcessText(ctx, "u1", "/clear", "normal")
	out = FormatOrchestratorResponse(res)
	if !strings.Contains(out, "對話已重置") {
		t.Errorf("unexpected clear output: %s", out)
	}
}

func TestOrchestratorRestaurantSearch(t *testing.T) {
	orch, srv := newTestOrchestrator(t, func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"candidates": []map[string]any{
				{
					"content": map[string]any{
						"parts": []map[string]any{{"text": `["鼎泰豐"]`}},
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
	defer srv.Close()

	ctx := context.Background()

	// Without last_location in session metadata
	res := orch.ProcessText(ctx, "u-food", "附近有什麼好吃的餐廳", "normal")
	if len(res.Responses) != 1 || !res.Responses[0].NeedLocation {
		t.Fatalf("expected NeedLocation=true, got %+v", res.Responses)
	}
	out := FormatOrchestratorResponse(res)
	if !strings.Contains(out, "偵測到您想要尋找美食") {
		t.Errorf("unexpected need_location prompt: %s", out)
	}

	// With last_location in session metadata
	orch.ChatAgent.SessionManager.GetOrCreateSession("u-food")
	orch.ChatAgent.SessionManager.SetMetadata("u-food", "last_location", map[string]any{
		"latitude":  25.033,
		"longitude": 121.565,
		"address":   "台北市信義區",
	})
	res = orch.ProcessText(ctx, "u-food", "附近有什麼好吃的餐廳", "normal")
	if len(res.Responses) != 1 || !res.Responses[0].HasLocation {
		t.Fatalf("expected HasLocation=true, got %+v", res.Responses)
	}
	out = FormatOrchestratorResponse(res)
	if !strings.Contains(out, "附近的餐廳") {
		t.Errorf("unexpected restaurant search output: %s", out)
	}
}

func TestOrchestratorParallelMultiIntent(t *testing.T) {
	orch, srv := newTestOrchestrator(t, func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"candidates": []map[string]any{
				{
					"content": map[string]any{
						"parts": []map[string]any{{"text": "影片重點摘要"}},
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
	defer srv.Close()

	ctx := context.Background()
	res := orch.ProcessText(ctx, "u-multi", "請看 https://youtu.be/abc12345678 與 https://youtu.be/xyz98765432", "normal")
	if !res.Success || len(res.Responses) != 2 {
		t.Fatalf("expected 2 successful parallel responses, got %+v", res)
	}
	out := FormatOrchestratorResponse(res)
	if !strings.Contains(out, "---") || !strings.Contains(out, "https://youtu.be/abc12345678") || !strings.Contains(out, "https://youtu.be/xyz98765432") {
		t.Errorf("unexpected multi-intent output: %s", out)
	}
}
