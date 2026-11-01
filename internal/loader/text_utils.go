package loader

import (
	"net/url"
	"regexp"
	"strings"
)

var (
	shortPattern    = regexp.MustCompile(`(?i)\[短\]|\(短\)|\[short\]|\(short\)`)
	detailedPattern = regexp.MustCompile(`(?i)\[詳\]|\(詳\)|\[detailed\]|\(detailed\)|\[detail\]|\(detail\)`)
	normalPattern   = regexp.MustCompile(`(?i)\[標準\]|\(標準\)|\[normal\]|\(normal\)`)
	urlRegex        = regexp.MustCompile(`https?://[^\s<>"{}|\\^\x60\[\]]+`)
)

// ParseSummaryMode extracts the summary mode indicator from user input and returns (cleanedText, mode).
func ParseSummaryMode(text string) (string, string) {
	if shortPattern.MatchString(text) {
		return strings.TrimSpace(shortPattern.ReplaceAllString(text, "")), "short"
	}
	if detailedPattern.MatchString(text) {
		return strings.TrimSpace(detailedPattern.ReplaceAllString(text, "")), "detailed"
	}
	if normalPattern.MatchString(text) {
		return strings.TrimSpace(normalPattern.ReplaceAllString(text, "")), "normal"
	}
	return text, "normal"
}

// FindURLs finds all HTTP/HTTPS URLs in a string.
func FindURLs(input string) []string {
	return urlRegex.FindAllString(input, -1)
}

// ExtractURLAndMode extracts URLs and summary mode from message text.
func ExtractURLAndMode(messageText string) ([]string, string) {
	cleaned, mode := ParseSummaryMode(messageText)
	urls := FindURLs(cleaned)
	return urls, mode
}

// GetModeDescription returns a Traditional Chinese description for a summary mode.
func GetModeDescription(mode string) string {
	switch mode {
	case "short":
		return "簡短摘要（1-3 個重點）"
	case "detailed":
		return "詳細摘要（完整分析）"
	default:
		return "標準摘要（平衡詳細度）"
	}
}

// ReplaceDomain replaces twitter.com / x.com with api.fxtwitter.com.
func ReplaceDomain(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	switch u.Host {
	case "twitter.com", "x.com", "www.twitter.com", "www.x.com":
		u.Host = "api.fxtwitter.com"
		return u.String()
	}
	return rawURL
}

// IsYouTubeURL checks if a URL points to YouTube.
func IsYouTubeURL(rawURL string) bool {
	return strings.HasPrefix(rawURL, "https://www.youtube.com") ||
		strings.HasPrefix(rawURL, "https://youtu.be") ||
		strings.HasPrefix(rawURL, "https://m.youtube.com") ||
		strings.HasPrefix(rawURL, "https://youtube.com")
}

// IsPTTURL checks if a URL points to PTT BBS.
func IsPTTURL(rawURL string) bool {
	return strings.HasPrefix(rawURL, "https://www.ptt.cc/bbs")
}

// IsFirecrawlURL checks if a URL should prioritize Firecrawl (PTT, Medium, OpenAI).
func IsFirecrawlURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	host := ""
	if err == nil {
		host = u.Hostname()
	}
	return strings.HasPrefix(rawURL, "https://www.ptt.cc/bbs") ||
		strings.HasPrefix(rawURL, "https://medium.com") ||
		strings.HasSuffix(host, "medium.com") ||
		strings.HasPrefix(rawURL, "https://openai.com")
}
