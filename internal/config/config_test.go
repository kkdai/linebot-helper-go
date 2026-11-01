package config

import (
	"testing"
)

func TestLoadFromEnv_MissingRequired(t *testing.T) {
	t.Setenv("ChannelSecret", "")
	t.Setenv("ChannelAccessToken", "")
	t.Setenv("GOOGLE_CLOUD_PROJECT", "")
	t.Setenv("LINE_USER_ID", "")
	t.Setenv("ChannelAccessTokenHF", "")

	_, err := LoadFromEnv()
	if err == nil {
		t.Fatal("expected error when required env vars are missing")
	}
}

func TestLoadFromEnv_SuccessAndDefaults(t *testing.T) {
	t.Setenv("ChannelSecret", "sec")
	t.Setenv("ChannelAccessToken", "tok")
	t.Setenv("GOOGLE_CLOUD_PROJECT", "proj")
	t.Setenv("LINE_USER_ID", "uid")
	t.Setenv("ChannelAccessTokenHF", "hftok")
	t.Setenv("GOOGLE_CLOUD_LOCATION", "")
	t.Setenv("PORT", "")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.GoogleCloudLocation != "global" {
		t.Errorf("expected location=global, got %s", cfg.GoogleCloudLocation)
	}
	if cfg.Port != "8080" {
		t.Errorf("expected port=8080, got %s", cfg.Port)
	}
	if cfg.ChatModel != "gemini-3.1-flash-lite" {
		t.Errorf("expected ChatModel=gemini-3.1-flash-lite, got %s", cfg.ChatModel)
	}
	if cfg.SessionTimeoutMinutes != 30 {
		t.Errorf("expected SessionTimeoutMinutes=30, got %d", cfg.SessionTimeoutMinutes)
	}
	if !cfg.EnableGrounding || !cfg.EnableMapsGrounding {
		t.Errorf("expected grounding flags to default to true")
	}
}
