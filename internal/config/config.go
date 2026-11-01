package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config holds all application and agent configurations loaded from environment variables.
type Config struct {
	// Server
	Port string

	// LINE Bot
	ChannelSecret        string
	ChannelAccessToken   string
	ChannelAccessTokenHF string
	LineUserID           string

	// Google Cloud / Vertex AI
	GoogleCloudProject  string
	GoogleCloudLocation string

	// Google AI Studio / Developer API (used for Live API, TTS, Batch API)
	GoogleAIAPIKey string

	// Optional integrations
	FirecrawlKey         string
	GitHubToken          string
	SingleFilePath       string
	LiffID               string
	WebhookSigningSecret string
	WebhookDomain        string

	// Agent / Model settings
	ChatModel              string
	OrchestratorModel      string
	FastModel              string
	VoiceModel             string
	SessionTimeoutMinutes  int
	MaxHistoryLength       int
	MaxOutputTokens        int
	Temperature            float32
	EnableGrounding        bool
	EnableMapsGrounding    bool
}

// LoadFromEnv loads configuration from environment variables and validates required fields.
func LoadFromEnv() (*Config, error) {
	channelSecret := os.Getenv("ChannelSecret")
	if channelSecret == "" {
		return nil, fmt.Errorf("Specify ChannelSecret as environment variable.")
	}
	channelAccessToken := os.Getenv("ChannelAccessToken")
	if channelAccessToken == "" {
		return nil, fmt.Errorf("Specify ChannelAccessToken as environment variable.")
	}
	project := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if project == "" {
		return nil, fmt.Errorf("Specify GOOGLE_CLOUD_PROJECT as environment variable for Vertex AI.")
	}
	lineUserID := os.Getenv("LINE_USER_ID")
	if lineUserID == "" {
		return nil, fmt.Errorf("Specify LINE_USER_ID as environment variable.")
	}
	channelAccessTokenHF := os.Getenv("ChannelAccessTokenHF")
	if channelAccessTokenHF == "" {
		return nil, fmt.Errorf("Specify HuggingFace ChannelAccessToken as environment variable.")
	}

	apiKey := os.Getenv("GOOGLE_AI_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("GEMINI_API_KEY")
	}

	return &Config{
		Port:                  envOrDefault("PORT", "8080"),
		ChannelSecret:         channelSecret,
		ChannelAccessToken:    channelAccessToken,
		ChannelAccessTokenHF:  channelAccessTokenHF,
		LineUserID:            lineUserID,
		GoogleCloudProject:    project,
		GoogleCloudLocation:   envOrDefault("GOOGLE_CLOUD_LOCATION", "global"),
		GoogleAIAPIKey:        apiKey,
		FirecrawlKey:          os.Getenv("firecrawl_key"),
		GitHubToken:           os.Getenv("GITHUB_TOKEN"),
		SingleFilePath:        envOrDefault("SINGLEFILE_PATH", "single-file"),
		LiffID:                os.Getenv("LIFF_ID"),
		WebhookSigningSecret:  os.Getenv("WEBHOOK_SIGNING_SECRET"),
		WebhookDomain:         os.Getenv("WEBHOOK_DOMAIN"),
		ChatModel:             envOrDefault("CHAT_MODEL", "gemini-3.1-flash-lite"),
		OrchestratorModel:     envOrDefault("ORCHESTRATOR_MODEL", "gemini-2.5-pro"),
		FastModel:             envOrDefault("FAST_MODEL", "gemini-3.1-flash-lite"),
		VoiceModel:            envOrDefault("VOICE_MODEL", "gemini-3.1-flash-live-preview"),
		SessionTimeoutMinutes: envIntOrDefault("SESSION_TIMEOUT_MINUTES", 30),
		MaxHistoryLength:      envIntOrDefault("MAX_HISTORY_LENGTH", 20),
		MaxOutputTokens:       envIntOrDefault("MAX_OUTPUT_TOKENS", 2048),
		Temperature:           envFloatOrDefault("AGENT_TEMPERATURE", 0.7),
		EnableGrounding:       envBoolOrDefault("ENABLE_GROUNDING", true),
		EnableMapsGrounding:   envBoolOrDefault("ENABLE_MAPS_GROUNDING", true),
	}, nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envIntOrDefault(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envFloatOrDefault(key string, def float32) float32 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 32); err == nil {
			return float32(f)
		}
	}
	return def
}

func envBoolOrDefault(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		return strings.EqualFold(v, "true") || v == "1"
	}
	return def
}
