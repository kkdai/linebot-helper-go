package line

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	DefaultAPIBaseURL     = "https://api.line.me"
	DefaultDataAPIBaseURL = "https://api-data.line.me"
	MaxMessageLength      = 5000
)

// ErrInvalidSignature is returned when X-Line-Signature verification fails.
var ErrInvalidSignature = errors.New("invalid line webhook signature")

// VerifySignature validates the X-Line-Signature header using HMAC-SHA256.
func VerifySignature(channelSecret string, body []byte, signature string) bool {
	if channelSecret == "" || signature == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(channelSecret))
	mac.Write(body)
	expected := mac.Sum(nil)

	sigBytes, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return false
	}
	return hmac.Equal(expected, sigBytes)
}

// WebhookPayload represents the top-level JSON body sent by LINE Platform.
type WebhookPayload struct {
	Destination string         `json:"destination"`
	Events      []WebhookEvent `json:"events"`
}

// WebhookEvent represents a single event in a LINE webhook payload.
type WebhookEvent struct {
	Type       string         `json:"type"`
	ReplyToken string         `json:"replyToken,omitempty"`
	Timestamp  int64          `json:"timestamp,omitempty"`
	Source     EventSource    `json:"source"`
	Message    *EventMessage  `json:"message,omitempty"`
	Postback   *EventPostback `json:"postback,omitempty"`
}

// EventSource identifies the user, group, or room that triggered the event.
type EventSource struct {
	Type    string `json:"type"`
	UserID  string `json:"userId,omitempty"`
	GroupID string `json:"groupId,omitempty"`
	RoomID  string `json:"roomId,omitempty"`
}

// EventMessage holds message event details.
type EventMessage struct {
	ID        string  `json:"id"`
	Type      string  `json:"type"`
	Text      string  `json:"text,omitempty"`
	Latitude  float64 `json:"latitude,omitempty"`
	Longitude float64 `json:"longitude,omitempty"`
	Address   string  `json:"address,omitempty"`
}

// EventPostback holds postback event details.
type EventPostback struct {
	Data string `json:"data"`
}

// ParseWebhookEvents verifies the signature and parses the webhook events.
func ParseWebhookEvents(channelSecret string, body []byte, signature string) ([]WebhookEvent, error) {
	if !VerifySignature(channelSecret, body, signature) {
		return nil, ErrInvalidSignature
	}
	var payload WebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	return payload.Events, nil
}

// QuickReply represents LINE quick reply buttons.
type QuickReply struct {
	Items []QuickReplyItem `json:"items"`
}

// QuickReplyItem wraps a single quick reply action.
type QuickReplyItem struct {
	Type   string         `json:"type"`
	Action PostbackAction `json:"action"`
}

// PostbackAction represents a LINE postback action on a button.
type PostbackAction struct {
	Type        string `json:"type"`
	Label       string `json:"label"`
	Data        string `json:"data"`
	DisplayText string `json:"displayText,omitempty"`
}

// NewPostbackButton creates a QuickReplyItem with a postback action.
func NewPostbackButton(label, data, displayText string) QuickReplyItem {
	return QuickReplyItem{
		Type: "action",
		Action: PostbackAction{
			Type:        "postback",
			Label:       label,
			Data:        data,
			DisplayText: displayText,
		},
	}
}

// NewQuickReply constructs a QuickReply from items.
func NewQuickReply(items ...QuickReplyItem) *QuickReply {
	if len(items) == 0 {
		return nil
	}
	return &QuickReply{Items: items}
}

// NewTextMessage creates a LINE text message object.
func NewTextMessage(text string, qr *QuickReply) map[string]any {
	m := map[string]any{
		"type": "text",
		"text": text,
	}
	if qr != nil && len(qr.Items) > 0 {
		m["quickReply"] = qr
	}
	return m
}

// NewImageMessage creates a LINE image message object.
func NewImageMessage(originalURL, previewURL string) map[string]any {
	if previewURL == "" {
		previewURL = originalURL
	}
	return map[string]any{
		"type":               "image",
		"originalContentUrl": originalURL,
		"previewImageUrl":    previewURL,
	}
}

// NewAudioMessage creates a LINE audio message object.
func NewAudioMessage(originalURL string, durationMs int) map[string]any {
	if durationMs <= 0 {
		durationMs = 1000
	}
	return map[string]any{
		"type":               "audio",
		"originalContentUrl": originalURL,
		"duration":           durationMs,
	}
}

// NewFlexMessage creates a LINE Flex message object preserving custom actions like clipboard.
func NewFlexMessage(altText string, contents map[string]any, qr *QuickReply) map[string]any {
	m := map[string]any{
		"type":     "flex",
		"altText":  altText,
		"contents": contents,
	}
	if qr != nil && len(qr.Items) > 0 {
		m["quickReply"] = qr
	}
	return m
}

// Client wraps LINE Messaging API calls.
type Client struct {
	ChannelAccessToken string
	APIBaseURL         string
	DataAPIBaseURL     string
	HTTPClient         *http.Client
}

// NewClient creates a new LINE Messaging API client.
func NewClient(channelAccessToken string) *Client {
	return &Client{
		ChannelAccessToken: channelAccessToken,
		APIBaseURL:         DefaultAPIBaseURL,
		DataAPIBaseURL:     DefaultDataAPIBaseURL,
		HTTPClient:         &http.Client{Timeout: 60 * time.Second},
	}
}

// WithToken returns a shallow copy of the client using a different channel access token (e.g. for /hf).
func (c *Client) WithToken(token string) *Client {
	cp := *c
	cp.ChannelAccessToken = token
	return &cp
}

func (c *Client) postJSON(ctx context.Context, url string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.ChannelAccessToken)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("line api error (%d): %s", resp.StatusCode, string(b))
	}
	return nil
}

// ReplyMessages sends up to 5 messages using a replyToken.
func (c *Client) ReplyMessages(ctx context.Context, replyToken string, messages []map[string]any) error {
	if len(messages) == 0 || replyToken == "" {
		return nil
	}
	if len(messages) > 5 {
		slog.Warn("Too many reply messages, truncating to 5", "count", len(messages))
		messages = messages[:5]
	}
	url := strings.TrimRight(c.APIBaseURL, "/") + "/v2/bot/message/reply"
	return c.postJSON(ctx, url, map[string]any{
		"replyToken": replyToken,
		"messages":   messages,
	})
}

// PushMessages pushes messages to a user in batches of 5.
func (c *Client) PushMessages(ctx context.Context, to string, messages []map[string]any) error {
	if len(messages) == 0 || to == "" {
		return nil
	}
	url := strings.TrimRight(c.APIBaseURL, "/") + "/v2/bot/message/push"
	for i := 0; i < len(messages); i += 5 {
		end := i + 5
		if end > len(messages) {
			end = len(messages)
		}
		batch := messages[i:end]
		if err := c.postJSON(ctx, url, map[string]any{
			"to":       to,
			"messages": batch,
		}); err != nil {
			return err
		}
	}
	return nil
}

// SplitLongMessage splits a long text string into chunks under MaxMessageLength - 100 runes.
func SplitLongMessage(text string) []string {
	limit := MaxMessageLength - 100
	if utf8.RuneCountInString(text) <= limit {
		return []string{text}
	}
	paragraphs := strings.Split(text, "\n\n")
	var chunks []string
	current := ""

	for _, p := range paragraphs {
		cand := p
		if current != "" {
			cand = current + "\n\n" + p
		}
		if utf8.RuneCountInString(cand) > limit {
			if current != "" {
				chunks = append(chunks, strings.TrimSpace(current))
			}
			current = p
		} else {
			current = cand
		}
	}
	if current != "" {
		chunks = append(chunks, strings.TrimSpace(current))
	}
	if len(chunks) == 0 {
		runes := []rune(text)
		return []string{string(runes[:limit])}
	}
	return chunks
}

// ReplyText replies with text (splitting if necessary) and attaches quickReply to the last message.
func (c *Client) ReplyText(ctx context.Context, replyToken, text string, qr *QuickReply) error {
	chunks := SplitLongMessage(text)
	msgs := make([]map[string]any, 0, len(chunks))
	for i, ch := range chunks {
		var itemQR *QuickReply
		if i == len(chunks)-1 {
			itemQR = qr
		}
		msgs = append(msgs, NewTextMessage(ch, itemQR))
	}
	return c.ReplyMessages(ctx, replyToken, msgs)
}

// PushText pushes text (splitting if necessary) and attaches quickReply to the last message.
func (c *Client) PushText(ctx context.Context, to, text string, qr *QuickReply) error {
	chunks := SplitLongMessage(text)
	msgs := make([]map[string]any, 0, len(chunks))
	for i, ch := range chunks {
		var itemQR *QuickReply
		if i == len(chunks)-1 {
			itemQR = qr
		}
		msgs = append(msgs, NewTextMessage(ch, itemQR))
	}
	return c.PushMessages(ctx, to, msgs)
}

// GetMessageContent downloads binary content (image or audio) from LINE Data API.
func (c *Client) GetMessageContent(ctx context.Context, messageID string) ([]byte, error) {
	url := fmt.Sprintf("%s/v2/bot/message/%s/content", strings.TrimRight(c.DataAPIBaseURL, "/"), messageID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.ChannelAccessToken)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("line content api error (%d): %s", resp.StatusCode, string(b))
	}
	return io.ReadAll(resp.Body)
}

// ShowLoadingAnimation displays the 1:1 chat loading animation without consuming reply tokens.
func (c *Client) ShowLoadingAnimation(ctx context.Context, chatID string, seconds int) {
	if c.ChannelAccessToken == "" || chatID == "" {
		return
	}
	if seconds <= 0 {
		seconds = 60
	}
	url := strings.TrimRight(c.APIBaseURL, "/") + "/v2/bot/chat/loading/start"
	if err := c.postJSON(ctx, url, map[string]any{
		"chatId":         chatID,
		"loadingSeconds": seconds,
	}); err != nil {
		slog.Warn("show_loading_animation failed", "error", err)
	}
}

// FormatErrorMessage formats an error into a friendly Traditional Chinese message (matching LineService.format_error_message).
func FormatErrorMessage(err error, contextStr string) string {
	errStr := ""
	if err != nil {
		errStr = strings.ToLower(err.Error())
	}
	switch {
	case strings.Contains(errStr, "quota") || strings.Contains(errStr, "rate limit") || strings.Contains(errStr, "429"):
		return "⏳ 系統目前繁忙，請稍後再試。\n\n💡 建議等待 1-2 分鐘後重試。"
	case strings.Contains(errStr, "timeout") || strings.Contains(errStr, "deadline exceeded"):
		return "⏱️ 處理時間過長，請稍後再試。\n\n💡 如果問題持續，請嘗試簡化您的問題。"
	case strings.Contains(errStr, "not found") || strings.Contains(errStr, "404"):
		return "🔍 找不到相關資訊。\n\n💡 請嘗試用不同的方式描述您的問題。"
	case strings.Contains(errStr, "empty response"):
		return "⚠️ 無法生成回應，可能是內容被過濾。\n\n💡 請嘗試用不同的問法。"
	default:
		baseMsg := "❌ 處理時發生錯誤。"
		if contextStr != "" {
			baseMsg = fmt.Sprintf("❌ %s時發生錯誤。", contextStr)
		}
		return fmt.Sprintf("%s\n\n請稍後再試，或使用 /clear 清除對話記憶後重新開始。", baseMsg)
	}
}

// FriendlyErrorMessage formats URL loading / summarization errors (matching FriendlyErrorMessage.get_message).
func FriendlyErrorMessage(err error, url string) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "403"):
		return fmt.Sprintf("❌ 抱歉，無法存取這個網站（被拒絕存取）。\n%s\n\n可能原因：網站有防爬蟲保護，請稍後再試。", url)
	case strings.Contains(lower, "404"):
		return fmt.Sprintf("❌ 找不到這個頁面（404）。\n%s\n\n請確認網址是否正確。", url)
	case strings.Contains(lower, "429"):
		return fmt.Sprintf("❌ 請求過於頻繁（429）。\n%s\n\n請稍後再試（約 1-2 分鐘）。", url)
	case strings.Contains(lower, "500"):
		return fmt.Sprintf("❌ 網站伺服器錯誤（500）。\n%s\n\n這是對方網站的問題，請稍後再試。", url)
	case strings.Contains(lower, "502") || strings.Contains(lower, "503"):
		return fmt.Sprintf("❌ 網站暫時無法使用。\n%s\n\n請稍後再試。", url)
	case strings.Contains(lower, "timeout") || strings.Contains(lower, "deadline exceeded"):
		return fmt.Sprintf("⏱️ 讀取超時。\n%s\n\n網站回應太慢，已自動重試但仍失敗。請稍後再試。", url)
	default:
		if len(msg) > 200 {
			msg = msg[:200] + "..."
		}
		return fmt.Sprintf("❌ 處理時發生錯誤。\n\n技術訊息：%s\n\n已通知開發者，請稍後再試。", msg)
	}
}
