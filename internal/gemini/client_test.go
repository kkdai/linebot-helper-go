package gemini

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/oauth2"
)

type staticTokenSource struct{}

func (staticTokenSource) Token() (*oauth2.Token, error) {
	return &oauth2.Token{AccessToken: "test-token"}, nil
}

func TestGenerateContent_GroundingAndThoughtFilter(t *testing.T) {
	imgBytes := []byte("fake-png-data")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("unexpected Authorization header: %s", r.Header.Get("Authorization"))
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		resp := map[string]any{
			"candidates": []any{
				map[string]any{
					"content": map[string]any{
						"role": "model",
						"parts": []any{
							map[string]any{"text": "internal thinking", "thought": true},
							map[string]any{"text": "Hello from Gemini"},
							map[string]any{
								"codeExecutionResult": map[string]any{
									"outcome": "OUTCOME_OK",
									"output":  "42",
								},
							},
							map[string]any{
								"inlineData": map[string]any{
									"mimeType": "image/png",
									"data":     base64.StdEncoding.EncodeToString(imgBytes),
								},
							},
						},
					},
					"groundingMetadata": map[string]any{
						"groundingChunks": []any{
							map[string]any{
								"web": map[string]any{
									"title": "Example Site",
									"uri":   "https://example.com",
								},
							},
						},
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := NewClient("test-proj", "global", "dev-key")
	client.VertexBaseURL = srv.URL
	client.TokenSource = staticTokenSource{}

	res, err := client.GenerateContent(context.Background(), GenerateOptions{
		Model:               "gemini-3-flash-preview",
		EnableGoogleSearch:  true,
		EnableCodeExecution: true,
		Contents: []Content{
			{Role: "user", Parts: []Part{NewTextPart("test")}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectedText := "Hello from Gemini\n[Code Output]: 42"
	if res.Text != expectedText {
		t.Errorf("expected text %q, got %q", expectedText, res.Text)
	}
	if len(res.Sources) != 1 || res.Sources[0].URI != "https://example.com" {
		t.Errorf("unexpected sources: %+v", res.Sources)
	}
	if len(res.Images) != 1 || string(res.Images[0]) != string(imgBytes) {
		t.Errorf("unexpected images: %+v", res.Images)
	}
}

func TestParseAudioMimeType(t *testing.T) {
	bits, rate := ParseAudioMimeType("audio/L16;codec=pcm;rate=24000")
	if bits != 16 || rate != 24000 {
		t.Errorf("expected 16/24000, got %d/%d", bits, rate)
	}

	bits, rate = ParseAudioMimeType("audio/L24; rate=48000")
	if bits != 24 || rate != 48000 {
		t.Errorf("expected 24/48000, got %d/%d", bits, rate)
	}
}

func TestTextToSpeech_MockConverter(t *testing.T) {
	// 1 second of 24kHz 16-bit mono PCM = 48000 bytes
	fakePCM := make([]byte, 48000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"candidates": []any{
				map[string]any{
					"content": map[string]any{
						"role": "model",
						"parts": []any{
							map[string]any{
								"inlineData": map[string]any{
									"mimeType": "audio/L16;codec=pcm;rate=24000",
									"data":     base64.StdEncoding.EncodeToString(fakePCM),
								},
							},
						},
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := NewClient("test-proj", "global", "dev-key")
	client.DeveloperBaseURL = srv.URL
	client.PCMToM4AConvert = func(pcmBytes []byte, sampleRate int) ([]byte, error) {
		if sampleRate != 24000 {
			t.Errorf("expected sampleRate 24000, got %d", sampleRate)
		}
		return []byte("fake-m4a"), nil
	}

	m4a, durationMs, err := client.TextToSpeech(context.Background(), "測試語音")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(m4a) != "fake-m4a" {
		t.Errorf("unexpected m4a: %s", string(m4a))
	}
	if durationMs != 1000 {
		t.Errorf("expected durationMs 1000, got %d", durationMs)
	}
}
