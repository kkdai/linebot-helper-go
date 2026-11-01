package line

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"testing"
)

func TestVerifySignatureAndParse(t *testing.T) {
	secret := "my-secret"
	body := []byte(`{"events":[{"type":"message","replyToken":"rt1","source":{"type":"user","userId":"U1"},"message":{"id":"m1","type":"text","text":"hello"}}]}`)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	if !VerifySignature(secret, body, sig) {
		t.Fatal("expected valid signature")
	}
	if VerifySignature(secret, body, "bad-sig") {
		t.Fatal("expected invalid signature to fail")
	}

	events, err := ParseWebhookEvents(secret, body, sig)
	if err != nil || len(events) != 1 {
		t.Fatalf("unexpected parse result: len=%d, err=%v", len(events), err)
	}
	if events[0].Message.Text != "hello" || events[0].Source.UserID != "U1" {
		t.Errorf("unexpected parsed event: %+v", events[0])
	}
}

func TestParseBookmarkCommand(t *testing.T) {
	cmd, arg, ok := ParseBookmarkCommand("/save https://example.com")
	if !ok || cmd != "save" || arg != "https://example.com" {
		t.Errorf("unexpected /save parse: %s %s %v", cmd, arg, ok)
	}

	cmd, arg, ok = ParseBookmarkCommand("/list")
	if !ok || cmd != "list" || arg != "" {
		t.Errorf("unexpected /list parse: %s %s %v", cmd, arg, ok)
	}

	_, _, ok = ParseBookmarkCommand("/clear")
	if ok {
		t.Error("expected /clear not to match bookmark command")
	}
}

func TestBuildSummaryBubble(t *testing.T) {
	bWithID := BuildSummaryBubble("標題", "摘要", "https://example.com", "doc123")
	footer := bWithID["footer"].(map[string]any)["contents"].([]any)
	if len(footer) != 3 {
		t.Errorf("expected 3 footer buttons when docID is set, got %d", len(footer))
	}

	bWithoutID := BuildSummaryBubble("標題", "摘要", "https://example.com", "")
	footerNoID := bWithoutID["footer"].(map[string]any)["contents"].([]any)
	if len(footerNoID) != 1 {
		t.Errorf("expected 1 footer button when docID is empty, got %d", len(footerNoID))
	}
}
