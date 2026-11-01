package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kkdai/linebot-helper-go/internal/gemini"
	"golang.org/x/oauth2"
)

type fakeTokenSource struct{}

func (fakeTokenSource) Token() (*oauth2.Token, error) {
	return &oauth2.Token{AccessToken: "fake-tok"}, nil
}

func TestBuildSocialMediaPrompt_HasHumanizeGuidelines(t *testing.T) {
	p := BuildSocialMediaPrompt("測試文章內容")
	if !strings.Contains(p, "人性化守則") || !strings.Contains(p, "先保事實 → 再去 AI 味 → 最後加人味") {
		t.Error("expected HumanizeGuidelines in social media prompt")
	}
	if !strings.Contains(p, "台灣在地化") {
		t.Error("expected Taiwan localization rules in prompt")
	}
}

func TestGenerateSocialMediaPostsAndBookmarkSummary(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"candidates": []any{
				map[string]any{
					"content": map[string]any{
						"role": "model",
						"parts": []any{
							map[string]any{
								"text": `{"title":"AI 趨勢","summary_analysis":"分析內容","facebook":"FB 文案","linkedin":"LI 文案","threads":"Threads 文案"}`,
							},
						},
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := gemini.NewClient("proj", "global", "key")
	client.VertexBaseURL = srv.URL
	client.TokenSource = fakeTokenSource{}

	posts := GenerateSocialMediaPosts(context.Background(), client, "文章內文")
	if posts.Title != "AI 趨勢" || posts.Facebook != "FB 文案" {
		t.Errorf("unexpected social posts: %+v", posts)
	}
}

func TestSearchNearbyPlaces(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		text := "1. 鼎泰豐\n2. 阿宗麵線"
		if callCount == 2 {
			text = `["鼎泰豐", "阿宗麵線"]`
		}
		resp := map[string]any{
			"candidates": []any{
				map[string]any{
					"content": map[string]any{
						"role": "model",
						"parts": []any{
							map[string]any{"text": text},
						},
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := gemini.NewClient("proj", "global", "key")
	client.VertexBaseURL = srv.URL
	client.TokenSource = fakeTokenSource{}

	res := SearchNearbyPlaces(context.Background(), client, 25.033, 121.565, "restaurant", "", "zh-TW")
	if res.Status != "success" || len(res.RestaurantNames) != 2 {
		t.Errorf("unexpected nearby places result: %+v", res)
	}
}
