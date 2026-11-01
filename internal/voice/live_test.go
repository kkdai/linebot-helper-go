package voice

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestBuildLiveSetupMessageAndSystemInstruction(t *testing.T) {
	lat := 25.033
	lng := 121.565
	inst := BuildSystemInstruction(&lat, &lng)
	if !strings.Contains(inst, "25.033000") || !strings.Contains(inst, "121.565000") {
		t.Errorf("expected coordinates in instruction, got %s", inst)
	}

	instNoLoc := BuildSystemInstruction(nil, nil)
	if !strings.Contains(instNoLoc, "未提供位置") {
		t.Errorf("expected no-location notice, got %s", instNoLoc)
	}

	// PTT disables automatic VAD
	msgPTT := BuildLiveSetupMessage("", inst, false, BuildVoiceTools(), "h-123", "")
	setupPTT := msgPTT["setup"].(map[string]any)
	ricPTT := setupPTT["realtimeInputConfig"].(map[string]any)
	aadPTT := ricPTT["automaticActivityDetection"].(map[string]any)
	if aadPTT["disabled"] != true {
		t.Errorf("expected automaticActivityDetection.disabled=true in PTT mode")
	}
	resump := setupPTT["sessionResumption"].(map[string]any)
	if resump["handle"] != "h-123" {
		t.Errorf("expected handle=h-123, got %v", resump["handle"])
	}

	// Handsfree with low VAD sensitivity
	msgHF := BuildLiveSetupMessage("", inst, true, BuildVoiceTools(), "", "low")
	setupHF := msgHF["setup"].(map[string]any)
	ricHF := setupHF["realtimeInputConfig"].(map[string]any)
	aadHF := ricHF["automaticActivityDetection"].(map[string]any)
	if aadHF["startOfSpeechSensitivity"] != "START_SENSITIVITY_LOW" {
		t.Errorf("expected START_SENSITIVITY_LOW, got %v", aadHF)
	}
}

func TestFormatSessionSummary(t *testing.T) {
	if s := FormatSessionSummary(nil); s != "" {
		t.Errorf("expected empty summary for nil turns, got %q", s)
	}
	turns := []Turn{
		{UserSpeech: "你好", AIResponse: "嗨，需要幫忙嗎"},
		{UserSpeech: "天氣如何", AIResponse: "晴天 30 度"},
	}
	s := FormatSessionSummary(turns)
	if !strings.HasPrefix(s, "🎤") || !strings.Contains(s, "你好") || !strings.Contains(s, "晴天 30 度") {
		t.Errorf("unexpected summary: %s", s)
	}

	var manyTurns []Turn
	for i := 0; i < 25; i++ {
		manyTurns = append(manyTurns, Turn{
			UserSpeech: strings.Repeat("問", 200),
			AIResponse: strings.Repeat("答", 200),
		})
	}
	longSummary := FormatSessionSummary(manyTurns)
	if len([]rune(longSummary)) > 4500 {
		t.Errorf("expected summary truncated to <=4500 runes, got %d", len([]rune(longSummary)))
	}
}

func TestMakeToolHandler(t *testing.T) {
	doneCh := make(chan string, 1)
	lat := 25.03
	lng := 121.56

	th := MakeToolHandler(&lat, &lng, nil, func(req string) {
		doneCh <- req
	}, func(ctx context.Context, lat, lng float64, placeType, customQuery string) map[string]any {
		return map[string]any{
			"status":     "success",
			"places":     "測試停車場",
			"place_type": placeType,
		}
	})

	ctx := context.Background()
	res := th(ctx, "delegate_to_assistant", map[string]any{"request": "幫我摘要這部影片"})
	if res["status"] != "accepted" {
		t.Fatalf("expected status=accepted, got %+v", res)
	}
	select {
	case got := <-doneCh:
		if got != "幫我摘要這部影片" {
			t.Errorf("unexpected delegated request: %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for background delegate")
	}

	mapsRes := th(ctx, "search_nearby_places", map[string]any{"place_type": "parking"})
	if mapsRes["status"] != "success" || mapsRes["places"] != "測試停車場" {
		t.Errorf("unexpected maps tool result: %+v", mapsRes)
	}

	noLocHandler := MakeToolHandler(nil, nil, nil, nil, nil)
	noLocRes := noLocHandler(ctx, "search_nearby_places", map[string]any{})
	if noLocRes["status"] != "error" {
		t.Errorf("expected error when coordinates missing, got %+v", noLocRes)
	}
}

func makeWSPair(t *testing.T) (clientConn, serverConn *websocket.Conn, cleanup func()) {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	serverCh := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err == nil {
			serverCh <- c
		}
	}))
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	client, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		srv.Close()
		t.Fatalf("failed to dial test ws: %v", err)
	}
	server := <-serverCh
	return client, server, func() {
		_ = client.Close()
		_ = server.Close()
		srv.Close()
	}
}

func TestBrowserToGeminiAndGeminiToBrowser(t *testing.T) {
	browserClient, browserServer, clean1 := makeWSPair(t)
	defer clean1()
	geminiClient, geminiServer, clean2 := makeWSPair(t)
	defer clean2()

	state := &SessionState{Handsfree: false}

	// Run BrowserToGemini
	go BrowserToGemini(browserServer, geminiClient, state)

	// Send start_of_speech, binary PCM, text, end_of_speech from browserClient
	_ = browserClient.WriteJSON(map[string]string{"type": "start_of_speech"})
	_ = browserClient.WriteMessage(websocket.BinaryMessage, []byte{0x01, 0x02})
	_ = browserClient.WriteJSON(map[string]string{"type": "text", "text": "現在幾點"})
	_ = browserClient.WriteJSON(map[string]string{"type": "end_of_speech"})

	var received []map[string]any
	for i := 0; i < 4; i++ {
		_, msg, err := geminiServer.ReadMessage()
		if err != nil {
			t.Fatalf("failed to read from geminiServer: %v", err)
		}
		var m map[string]any
		_ = json.Unmarshal(msg, &m)
		received = append(received, m)
	}

	// Verify start_of_speech, audio PCM, text, end_of_speech
	if _, ok := received[0]["realtimeInput"].(map[string]any)["activityStart"]; !ok {
		t.Errorf("expected activityStart first, got %+v", received[0])
	}
	audioObj := received[1]["realtimeInput"].(map[string]any)["audio"].(map[string]any)
	if audioObj["mimeType"] != "audio/pcm;rate=16000" || audioObj["data"] != base64.StdEncoding.EncodeToString([]byte{0x01, 0x02}) {
		t.Errorf("unexpected audio payload: %+v", audioObj)
	}
	if received[2]["realtimeInput"].(map[string]any)["text"] != "現在幾點" {
		t.Errorf("unexpected text payload: %+v", received[2])
	}
	if _, ok := received[3]["realtimeInput"].(map[string]any)["activityEnd"]; !ok {
		t.Errorf("expected activityEnd fourth, got %+v", received[3])
	}

	// Now test GeminiToBrowser
	pushedCh := make(chan Turn, 1)
	doneRelay := make(chan struct{})
	go func() {
		GeminiToBrowser(context.Background(), browserServer, geminiClient, state, func(u, a string) {
			pushedCh <- Turn{UserSpeech: u, AIResponse: a}
		}, func(ctx context.Context, name string, args map[string]any) map[string]any {
			return map[string]any{"status": "success", "places": "拉麵店"}
		})
		close(doneRelay)
	}()

	// Send sessionResumptionUpdate, inputTranscription, outputTranscription, turnComplete, and goAway from geminiServer
	_ = geminiServer.WriteJSON(map[string]any{
		"sessionResumptionUpdate": map[string]any{
			"resumable": true,
			"newHandle": "handle-xyz",
		},
	})
	_ = geminiServer.WriteJSON(map[string]any{
		"serverContent": map[string]any{
			"inputTranscription":  map[string]any{"text": "你好"},
			"outputTranscription": map[string]any{"text": "嗨，很高興為你服務"},
			"turnComplete":        true,
		},
	})
	_ = geminiServer.WriteJSON(map[string]any{
		"goAway": map[string]any{"timeLeft": "5s"},
	})

	<-doneRelay

	if state.GetResumeHandle() != "handle-xyz" {
		t.Errorf("expected resumeHandle=handle-xyz, got %q", state.GetResumeHandle())
	}
	if !state.GoAway {
		t.Errorf("expected state.GoAway=true after goAway")
	}

	select {
	case turn := <-pushedCh:
		if turn.UserSpeech != "你好" || turn.AIResponse != "嗨，很高興為你服務" {
			t.Errorf("unexpected turn pushed: %+v", turn)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for pushFn")
	}
}
