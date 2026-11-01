package handlers

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kkdai/linebot-helper-go/internal/agents"
	"github.com/kkdai/linebot-helper-go/internal/config"
	"github.com/kkdai/linebot-helper-go/internal/firestore"
	"github.com/kkdai/linebot-helper-go/internal/gemini"
	"github.com/kkdai/linebot-helper-go/internal/line"
	"github.com/kkdai/linebot-helper-go/internal/loader"
	"github.com/kkdai/linebot-helper-go/internal/services"
	"golang.org/x/oauth2"
)

type recordedLineCall struct {
	Path string
	Body map[string]any
}

func setupTestServer(t *testing.T) (*Server, *[]recordedLineCall, *sync.Mutex, func()) {
	t.Helper()

	var lineCalls []recordedLineCall
	var lineMu sync.Mutex

	lineSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/content") {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("fake-media-bytes"))
			return
		}
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		lineMu.Lock()
		lineCalls = append(lineCalls, recordedLineCall{Path: r.URL.Path, Body: m})
		lineMu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))

	geminiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"candidates": []map[string]any{
				{
					"content": map[string]any{
						"parts": []map[string]any{{"text": "測試回應文字"}},
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))

	tmpStatic := t.TempDir()
	_ = os.MkdirAll(filepath.Join(tmpStatic, "liff"), 0o755)
	_ = os.WriteFile(filepath.Join(tmpStatic, "liff", "index.html"), []byte("<html>LIFF={{LIFF_ID}}</html>"), 0o644)

	cfg := &config.Config{
		Port:                 "8080",
		ChannelSecret:        "test-secret",
		ChannelAccessToken:   "test-token",
		ChannelAccessTokenHF: "test-token-hf",
		LineUserID:           "U-admin",
		GoogleCloudProject:   "test-proj",
		GoogleCloudLocation:  "global",
		GoogleAIAPIKey:       "test-ai-key",
		LiffID:               "12345-abcde",
		ChatModel:            "gemini-3.1-flash-lite",
		FastModel:            "gemini-3.1-flash-lite",
		EnableGrounding:      true,
		EnableMapsGrounding:  true,
	}

	gClient := gemini.NewClient(cfg.GoogleCloudProject, cfg.GoogleCloudLocation, cfg.GoogleAIAPIKey)
	gClient.VertexBaseURL = geminiSrv.URL
	gClient.DeveloperBaseURL = geminiSrv.URL
	gClient.TokenSource = oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "fake-tok"})

	lClient := line.NewClient(cfg.ChannelAccessToken)
	lClient.APIBaseURL = lineSrv.URL
	lClient.DataAPIBaseURL = lineSrv.URL

	sm := services.NewSessionManager(30, 20, 300, firestore.NewMemoryKVStore(true))
	bs := services.NewBookmarkService(firestore.NewMemoryKVStore(true))
	rs := services.NewReportStore(firestore.NewMemoryKVStore(true))
	batchSvc := services.NewBatchService(cfg.GoogleAIAPIKey, firestore.NewMemoryKVStore(true), filepath.Join(tmpStatic, "batch.json"))
	urlLoader := loader.NewLoader("", "")
	urlLoader.SingleFileRunner = func(ctx context.Context, rawURL, singleFilePath string) (string, error) {
		return "<html><body>測試文章內容</body></html>", nil
	}
	orch := agents.NewOrchestrator(cfg, gClient, sm, urlLoader)

	srv := NewServer(cfg, gClient, lClient, urlLoader, sm, bs, rs, batchSvc, orch)
	srv.StaticDir = tmpStatic

	cleanup := func() {
		lineSrv.Close()
		geminiSrv.Close()
	}
	return srv, &lineCalls, &lineMu, cleanup
}

func signBody(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func TestServerRoutes(t *testing.T) {
	srv, _, _, cleanup := setupTestServer(t)
	defer cleanup()

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 1. GET /
	resp, err := http.Get(ts.URL + "/")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / failed: status=%v, err=%v", resp.StatusCode, err)
	}
	resp.Body.Close()

	// 2. GET /liff/
	resp, err = http.Get(ts.URL + "/liff/")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /liff/ failed: %v", err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "LIFF=12345-abcde") {
		t.Errorf("expected LIFF_ID injected, got %s", string(b))
	}

	// 3. GET /reports/{id} (404 and 200)
	resp, _ = http.Get(ts.URL + "/reports/missing")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for missing report, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	repID := srv.ReportStore.Put("<html>Saved Report</html>")
	resp, _ = http.Get(ts.URL + "/reports/" + repID)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for existing report, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 4. GET /images/{id} and /audio/{id}
	imgID := srv.storeAnnotatedImage([]byte("PNGDATA"))
	resp, _ = http.Get(ts.URL + "/images/" + imgID)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/png" {
		t.Errorf("unexpected /images response: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	resp.Body.Close()

	audID := srv.storeAudio([]byte("M4ADATA"))
	resp, _ = http.Get(ts.URL + "/audio/" + audID)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "audio/mp4" {
		t.Errorf("unexpected /audio response: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	resp.Body.Close()
}

func TestWebhookSignatureAndAsyncAudioFlow(t *testing.T) {
	srv, lineCalls, lineMu, cleanup := setupTestServer(t)
	defer cleanup()

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Invalid signature -> 400
	badReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/", strings.NewReader(`{"events":[]}`))
	badReq.Header.Set("X-Line-Signature", "invalid")
	badResp, err := http.DefaultClient.Do(badReq)
	if err != nil || badResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 on invalid signature, got %v", badResp.StatusCode)
	}
	badResp.Body.Close()

	// Valid audio message event -> returns 200 immediately and processes in background
	payloadBytes, _ := json.Marshal(map[string]any{
		"events": []map[string]any{
			{
				"type":       "message",
				"replyToken": "rt-audio-1",
				"source": map[string]any{
					"type":   "user",
					"userId": "U-audio",
				},
				"message": map[string]any{
					"id":   "m-audio-1",
					"type": "audio",
				},
			},
		},
	})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/", bytes.NewReader(payloadBytes))
	req.Header.Set("X-Line-Signature", signBody(srv.Config.ChannelSecret, payloadBytes))
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on valid webhook, got %v, err=%v", resp.StatusCode, err)
	}
	resp.Body.Close()

	// Wait for background goroutine to invoke loading animation and reply
	deadline := time.Now().Add(2 * time.Second)
	var sawLoading, sawReply bool
	for time.Now().Before(deadline) {
		lineMu.Lock()
		for _, c := range *lineCalls {
			if c.Path == "/v2/bot/chat/loading/start" {
				sawLoading = true
			}
			if c.Path == "/v2/bot/message/reply" {
				sawReply = true
			}
		}
		lineMu.Unlock()
		if sawLoading && sawReply {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if !sawLoading || !sawReply {
		t.Fatalf("expected loading animation (%v) and reply (%v) from audio message flow", sawLoading, sawReply)
	}
}
