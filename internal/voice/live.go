package voice

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kkdai/linebot-helper-go/internal/ai"
	"github.com/kkdai/linebot-helper-go/internal/gemini"
)

const (
	DefaultVoiceModel = "gemini-3.1-flash-live-preview"
	DefaultVoiceName  = "Aoede"
	VoiceResumeTTL    = 900 * time.Second
)

// Turn represents one completed user-AI voice conversation turn.
type Turn struct {
	UserSpeech string
	AIResponse string
}

// SessionState holds mutable state for an active voice session.
type SessionState struct {
	mu           sync.Mutex
	Handsfree    bool
	Interrupted  bool
	ResumeHandle string
	GoAway       bool
	Turns        []Turn
}

// SetInterrupted sets the interrupted flag thread-safely.
func (s *SessionState) SetInterrupted(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Interrupted = v
}

// IsInterrupted reads the interrupted flag thread-safely.
func (s *SessionState) IsInterrupted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Interrupted
}

// SetResumeHandle updates the session resumption handle thread-safely.
func (s *SessionState) SetResumeHandle(h string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ResumeHandle = h
}

// GetResumeHandle returns the current session resumption handle.
func (s *SessionState) GetResumeHandle() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ResumeHandle
}

// AddTurn appends a completed turn to the session state.
func (s *SessionState) AddTurn(userSpeech, aiResponse string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Turns = append(s.Turns, Turn{UserSpeech: userSpeech, AIResponse: aiResponse})
}

// BuildVoiceTools returns the Gemini Live tool declarations (Google Search + delegate_to_assistant + search_nearby_places).
func BuildVoiceTools() []map[string]any {
	return []map[string]any{
		{
			"googleSearch": map[string]any{},
		},
		{
			"functionDeclarations": []map[string]any{
				{
					"name": "delegate_to_assistant",
					"description": "把需要較長時間的任務（摘要網頁、YouTube 影片、PDF、深入研究問題等）" +
						"轉交給文字助手背景處理，結果會自動推送到使用者的 LINE 聊天室。" +
						"呼叫後請口頭告訴使用者：任務已受理，結果稍後會出現在聊天室。",
					"parameters": map[string]any{
						"type": "OBJECT",
						"properties": map[string]any{
							"request": map[string]any{
								"type":        "STRING",
								"description": "要轉交的完整任務描述（含網址等必要資訊）",
							},
						},
						"required": []string{"request"},
					},
				},
				{
					"name":        "search_nearby_places",
					"description": "查詢使用者目前位置附近的地點（餐廳、停車場、加油站）。使用者的座標由系統自動帶入，不需要提供。",
					"parameters": map[string]any{
						"type": "OBJECT",
						"properties": map[string]any{
							"place_type": map[string]any{
								"type":        "STRING",
								"enum":        []string{"restaurant", "parking", "gas_station"},
								"description": "地點類型",
							},
							"custom_query": map[string]any{
								"type":        "STRING",
								"description": "自訂查詢語句（例如「附近的日式拉麵店」），可省略",
							},
						},
					},
				},
			},
		},
	}
}

// BuildSystemInstruction constructs the system instruction for the voice assistant, injecting user coordinates if provided.
func BuildSystemInstruction(lat, lng *float64) string {
	locationInfo := "使用者未提供位置資訊，地點查詢時請請求使用者口述位置"
	if lat != nil && lng != nil && (*lat != 0 || *lng != 0) {
		locationInfo = fmt.Sprintf("使用者目前位置：緯度 %.6f，經度 %.6f", *lat, *lng)
	}
	return fmt.Sprintf(`你是一個整合多種工具的語音助手，透過 LINE Bot 服務使用者。

%s

你可以：
- 查詢附近地點（使用 search_nearby_places 工具查詢餐廳、停車場、加油站等）
- 回答一般問題與天氣、交通等即時資訊（搭配 Google Search）
- 摘要網頁、YouTube 影片或 PDF 等需要較長時間的任務：使用 delegate_to_assistant
  工具轉交給文字助手，並告訴使用者結果稍後會出現在 LINE 聊天室

請用繁體中文回應，語氣自然口語，適合直接用語音播放。不要使用條列符號或 markdown 格式，改用自然的說話方式。每次回應控制在 50 字以內。`, locationInfo)
}

// BuildLiveSetupMessage builds the initial BidiGenerateContent setup payload.
func BuildLiveSetupMessage(model, systemInstruction string, handsfree bool, tools []map[string]any, resumeHandle, vadSensitivity string) map[string]any {
	if model == "" {
		model = DefaultVoiceModel
	}
	if !strings.HasPrefix(model, "models/") {
		model = "models/" + model
	}

	resumption := map[string]any{}
	if resumeHandle != "" {
		resumption["handle"] = resumeHandle
	}

	setup := map[string]any{
		"model":             model,
		"sessionResumption": resumption,
		"contextWindowCompression": map[string]any{
			"slidingWindow": map[string]any{},
		},
		"generationConfig": map[string]any{
			"responseModalities": []string{"AUDIO"},
			"speechConfig": map[string]any{
				"voiceConfig": map[string]any{
					"prebuiltVoiceConfig": map[string]any{
						"voiceName": DefaultVoiceName,
					},
				},
			},
		},
		"outputAudioTranscription": map[string]any{},
		"inputAudioTranscription":  map[string]any{},
		"systemInstruction": map[string]any{
			"role": "system",
			"parts": []map[string]any{
				{"text": systemInstruction},
			},
		},
	}

	if len(tools) > 0 {
		setup["tools"] = tools
	}

	if !handsfree {
		setup["realtimeInputConfig"] = map[string]any{
			"automaticActivityDetection": map[string]any{
				"disabled": true,
			},
		}
	} else if vadSensitivity == "low" {
		setup["realtimeInputConfig"] = map[string]any{
			"automaticActivityDetection": map[string]any{
				"startOfSpeechSensitivity": "START_SENSITIVITY_LOW",
				"endOfSpeechSensitivity":   "END_SENSITIVITY_LOW",
			},
		}
	}

	return map[string]any{"setup": setup}
}

// FormatSessionSummary formats all completed voice turns into a single LINE message (max 4500 runes).
func FormatSessionSummary(turns []Turn) string {
	if len(turns) == 0 {
		return ""
	}
	lines := []string{fmt.Sprintf("🎤 語音對話摘要（共 %d 輪）", len(turns))}
	for i, t := range turns {
		lines = append(lines, fmt.Sprintf("\n%d. 你：%s\n   🤖：%s", i+1, t.UserSpeech, t.AIResponse))
	}
	text := strings.Join(lines, "\n")
	runes := []rune(text)
	if len(runes) > 4500 {
		text = string(runes[:4400]) + "\n\n... (訊息過長，已截斷)"
	}
	return text
}

// ToolHandler executes a Gemini Live function call.
type ToolHandler func(ctx context.Context, name string, args map[string]any) map[string]any

// MakeToolHandler creates a ToolHandler with injected user coordinates, Gemini client for Maps Grounding, and background delegate callback.
func MakeToolHandler(
	lat, lng *float64,
	geminiClient *gemini.Client,
	delegateFn func(request string),
	searchOverride func(ctx context.Context, lat, lng float64, placeType, customQuery string) map[string]any,
) ToolHandler {
	return func(ctx context.Context, name string, args map[string]any) map[string]any {
		switch name {
		case "delegate_to_assistant":
			reqStr, _ := args["request"].(string)
			reqStr = strings.TrimSpace(reqStr)
			if delegateFn == nil || reqStr == "" {
				return map[string]any{
					"status":        "error",
					"error_message": "目前無法轉交任務",
				}
			}
			go delegateFn(reqStr)
			return map[string]any{
				"status":  "accepted",
				"message": "任務已受理，結果將推送到 LINE 聊天室",
			}

		case "search_nearby_places":
			if lat == nil || lng == nil {
				return map[string]any{
					"status":        "error",
					"error_message": "沒有使用者座標，請改用 Google Search 或請使用者口述位置",
				}
			}
			placeType, _ := args["place_type"].(string)
			if placeType == "" {
				placeType = "restaurant"
			}
			customQuery, _ := args["custom_query"].(string)

			if searchOverride != nil {
				return searchOverride(ctx, *lat, *lng, placeType, customQuery)
			}
			res := ai.SearchNearbyPlaces(ctx, geminiClient, *lat, *lng, placeType, customQuery, "zh-TW")
			if res.Status != "success" {
				return map[string]any{
					"status":        "error",
					"error_message": res.ErrorMessage,
				}
			}
			return map[string]any{
				"status":     "success",
				"places":     res.Places,
				"place_type": res.PlaceType,
				"latitude":   res.Latitude,
				"longitude":  res.Longitude,
			}

		default:
			return map[string]any{
				"status":        "error",
				"error_message": fmt.Sprintf("未知的工具：%s", name),
			}
		}
	}
}

// BrowserToGemini relays PCM audio and control events from the browser WebSocket to the Gemini Live WebSocket.
func BrowserToGemini(browserWS, geminiWS *websocket.Conn, state *SessionState) {
	for {
		msgType, data, err := browserWS.ReadMessage()
		if err != nil {
			return
		}
		if msgType == websocket.BinaryMessage && len(data) > 0 {
			payload := map[string]any{
				"realtimeInput": map[string]any{
					"audio": map[string]any{
						"data":     base64.StdEncoding.EncodeToString(data),
						"mimeType": "audio/pcm;rate=16000",
					},
				},
			}
			if err := geminiWS.WriteJSON(payload); err != nil {
				slog.Error("BrowserToGemini WriteJSON audio error", "error", err)
				return
			}
		} else if msgType == websocket.TextMessage {
			var event map[string]any
			if err := json.Unmarshal(data, &event); err != nil {
				continue
			}
			etype, _ := event["type"].(string)
			switch etype {
			case "start_of_speech":
				if !state.Handsfree {
					_ = geminiWS.WriteJSON(map[string]any{
						"realtimeInput": map[string]any{
							"activityStart": map[string]any{},
						},
					})
				}
			case "end_of_speech":
				if !state.Handsfree {
					_ = geminiWS.WriteJSON(map[string]any{
						"realtimeInput": map[string]any{
							"activityEnd": map[string]any{},
						},
					})
				}
			case "text":
				text, _ := event["text"].(string)
				text = strings.TrimSpace(text)
				if text != "" {
					_ = geminiWS.WriteJSON(map[string]any{
						"realtimeInput": map[string]any{
							"text": text,
						},
					})
				}
			case "interrupt":
				state.SetInterrupted(true)
			}
		}
	}
}

// GeminiToBrowser relays Gemini Live server events back to the browser WebSocket and handles tool calls.
func GeminiToBrowser(
	ctx context.Context,
	browserWS, geminiWS *websocket.Conn,
	state *SessionState,
	pushFn func(userSpeech, aiResponse string),
	toolHandler ToolHandler,
) {
	var aiTextAccum []string
	var userTextAccum []string
	turnInterrupted := false

	for {
		_, msgBytes, err := geminiWS.ReadMessage()
		if err != nil {
			return
		}

		var raw map[string]any
		if err := json.Unmarshal(msgBytes, &raw); err != nil {
			continue
		}

		if _, ok := raw["setupComplete"]; ok {
			continue
		}

		// 1. SessionResumptionUpdate
		if sru, ok := raw["sessionResumptionUpdate"].(map[string]any); ok {
			resumable, _ := sru["resumable"].(bool)
			newHandle, _ := sru["newHandle"].(string)
			if newHandle == "" {
				newHandle, _ = sru["new_handle"].(string)
			}
			if resumable && newHandle != "" {
				state.SetResumeHandle(newHandle)
			}
		}

		// 2. GoAway
		if _, hasGoAway := raw["goAway"]; hasGoAway {
			state.mu.Lock()
			state.GoAway = true
			state.mu.Unlock()
			return
		}
		if _, hasGoAway := raw["go_away"]; hasGoAway {
			state.mu.Lock()
			state.GoAway = true
			state.mu.Unlock()
			return
		}

		// 3. ServerContent
		if sc, ok := raw["serverContent"].(map[string]any); ok {
			if modelTurn, ok := sc["modelTurn"].(map[string]any); ok {
				if parts, ok := modelTurn["parts"].([]any); ok {
					for _, p := range parts {
						pm, ok := p.(map[string]any)
						if !ok {
							continue
						}
						if inline, ok := pm["inlineData"].(map[string]any); ok {
							b64, _ := inline["data"].(string)
							if b64 != "" {
								if audioBytes, err := base64.StdEncoding.DecodeString(b64); err == nil && len(audioBytes) > 0 {
									_ = browserWS.WriteMessage(websocket.BinaryMessage, audioBytes)
								}
							}
						}
					}
				}
			}

			if outTrans, ok := sc["outputTranscription"].(map[string]any); ok {
				t, _ := outTrans["text"].(string)
				if t != "" {
					aiTextAccum = append(aiTextAccum, t)
					_ = browserWS.WriteJSON(map[string]string{
						"type": "text_chunk",
						"text": t,
					})
				}
			}

			if inTrans, ok := sc["inputTranscription"].(map[string]any); ok {
				t, _ := inTrans["text"].(string)
				if t != "" {
					userTextAccum = append(userTextAccum, t)
					_ = browserWS.WriteJSON(map[string]string{
						"type": "user_transcript",
						"text": strings.Join(userTextAccum, ""),
					})
				}
			}

			if inter, _ := sc["interrupted"].(bool); inter {
				turnInterrupted = true
				_ = browserWS.WriteJSON(map[string]string{
					"type": "interrupted",
				})
			}

			if tc, _ := sc["turnComplete"].(bool); tc {
				if turnInterrupted || state.IsInterrupted() {
					turnInterrupted = false
					state.SetInterrupted(false)
					aiTextAccum = nil
					userTextAccum = nil
				} else {
					_ = browserWS.WriteJSON(map[string]string{
						"type": "turn_complete",
					})
					userSpeech := strings.TrimSpace(strings.Join(userTextAccum, ""))
					if userSpeech == "" {
						userSpeech = "（語音輸入）"
					}
					aiResponse := strings.TrimSpace(strings.Join(aiTextAccum, ""))
					if aiResponse != "" && pushFn != nil {
						pushFn(userSpeech, aiResponse)
					}
					aiTextAccum = nil
					userTextAccum = nil
				}
			}
		}

		// 4. ToolCall
		if tc, ok := raw["toolCall"].(map[string]any); ok && toolHandler != nil {
			if fcs, ok := tc["functionCalls"].([]any); ok && len(fcs) > 0 {
				var funcResponses []map[string]any
				for _, item := range fcs {
					fc, ok := item.(map[string]any)
					if !ok {
						continue
					}
					id, _ := fc["id"].(string)
					name, _ := fc["name"].(string)
					args, _ := fc["args"].(map[string]any)
					if args == nil {
						args = map[string]any{}
					}
					res := toolHandler(ctx, name, args)
					funcResponses = append(funcResponses, map[string]any{
						"id":       id,
						"name":     name,
						"response": res,
					})
				}
				_ = geminiWS.WriteJSON(map[string]any{
					"toolResponse": map[string]any{
						"functionResponses": funcResponses,
					},
				})
			}
		}
	}
}
