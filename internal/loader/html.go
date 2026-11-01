package loader

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/google/uuid"
	"golang.org/x/net/html"
)

const DefaultFirecrawlBaseURL = "https://api.firecrawl.dev"

var base64ImageRegex = regexp.MustCompile(`!\[.*?\]\(data:image\/.*?;base64,.*?\)`)

// RemoveBase64Images removes inline base64 images from markdown text.
func RemoveBase64Images(md string) string {
	return base64ImageRegex.ReplaceAllString(md, "")
}

// ParseHTML converts HTML into Markdown (with base64 images removed) or plain text.
func ParseHTML(rawHTML string, toMarkdown bool) string {
	if toMarkdown {
		md, err := htmltomarkdown.ConvertString(rawHTML)
		if err == nil {
			return strings.TrimSpace(RemoveBase64Images(md))
		}
	}
	doc, err := html.Parse(strings.NewReader(rawHTML))
	if err != nil {
		return strings.TrimSpace(rawHTML)
	}
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style" || n.Data == "noscript") {
			return
		}
		if n.Type == html.TextNode {
			t := strings.TrimSpace(n.Data)
			if t != "" {
				if sb.Len() > 0 {
					sb.WriteByte(' ')
				}
				sb.WriteString(t)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return sb.String()
}

// LoadHTMLWithHTTP fetches HTML via standard HTTP with browser headers and PTT over18 cookie.
func LoadHTMLWithHTTP(ctx context.Context, rawURL string, toMarkdown bool, client *http.Client) (string, error) {
	if client == nil {
		client = &http.Client{Timeout: 25 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept-Language", "zh-TW,zh;q=0.9,ja;q=0.8,en-US;q=0.7,en;q=0.6")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36")
	req.Header.Set("Cookie", "over18=1")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d from %s", resp.StatusCode, rawURL)
	}
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	parsed := ParseHTML(string(bodyBytes), toMarkdown)
	if strings.TrimSpace(parsed) == "" {
		return "", fmt.Errorf("empty HTML content from %s", rawURL)
	}
	return parsed, nil
}

// LoadHTMLWithFirecrawl scrapes a URL using the Firecrawl v1 API.
func LoadHTMLWithFirecrawl(ctx context.Context, rawURL, apiKey, firecrawlBaseURL string, toMarkdown bool, client *http.Client) (string, error) {
	if apiKey == "" {
		return "", fmt.Errorf("firecrawl_key environment variable not set")
	}
	if firecrawlBaseURL == "" {
		firecrawlBaseURL = DefaultFirecrawlBaseURL
	}
	if client == nil {
		client = &http.Client{Timeout: 35 * time.Second}
	}

	formats := []string{"html"}
	if toMarkdown {
		formats = []string{"markdown"}
	}
	headers := map[string]string{
		"User-Agent":      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36",
		"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"Accept-Language": "en-US,en;q=0.9",
	}
	params := map[string]any{
		"url":                rawURL,
		"formats":            formats,
		"onlyMainContent":    true,
		"removeBase64Images": true,
		"blockAds":           true,
		"timeout":            30000,
		"headers":            headers,
	}

	parsedURL, _ := url.Parse(rawURL)
	if strings.HasPrefix(rawURL, "https://www.ptt.cc/bbs") {
		headers["Cookie"] = "over18=1"
		params["actions"] = []map[string]any{
			{"type": "wait", "selector": ".bbs-screen"},
		}
	} else if parsedURL != nil && strings.HasSuffix(parsedURL.Hostname(), "medium.com") {
		headers["Cookie"] = "uid=lo_5f5a79a81615; sid=1:zKvtbbPVwGuLiOQjwgkt"
	} else if strings.HasPrefix(rawURL, "https://openai.com") {
		headers["Cookie"] = "cookieConsent=true; OptanonAlertBoxClosed=true"
	}

	body, err := json.Marshal(params)
	if err != nil {
		return "", err
	}
	endpoint := strings.TrimRight(firecrawlBaseURL, "/") + "/v1/scrape"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("firecrawl error (%d): %s", resp.StatusCode, string(b))
	}

	var result struct {
		Markdown string `json:"markdown"`
		HTML     string `json:"html"`
		Data     *struct {
			Markdown string `json:"markdown"`
			HTML     string `json:"html"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	md := result.Markdown
	rawHTML := result.HTML
	if result.Data != nil {
		if md == "" {
			md = result.Data.Markdown
		}
		if rawHTML == "" {
			rawHTML = result.Data.HTML
		}
	}

	if toMarkdown && md != "" {
		lower := strings.ToLower(md)
		if (strings.Contains(lower, "enable javascript") || strings.Contains(lower, "enable cookies")) &&
			strings.HasPrefix(rawURL, "https://openai.com") {
			return "", fmt.Errorf("javascript/cookie challenge detected on openai.com")
		}
		return md, nil
	}
	if rawHTML != "" {
		return ParseHTML(rawHTML, toMarkdown), nil
	}
	return "", fmt.Errorf("firecrawl did not return expected content")
}

// LoadHTMLWithSingleFile downloads a page via single-file CLI (Headless Chromium) and converts it to Markdown.
func LoadHTMLWithSingleFile(ctx context.Context, rawURL, singleFilePath string) (string, error) {
	if singleFilePath == "" {
		singleFilePath = "single-file"
	}
	tmpFile := filepath.Join(os.TempDir(), fmt.Sprintf("sf-%s.html", uuid.New().String()))
	defer os.Remove(tmpFile)

	chromiumPath := "/usr/bin/chromium"
	args := []string{
		"--browser-executable-path", chromiumPath,
		"--filename-conflict-action", "overwrite",
		rawURL,
		tmpFile,
	}

	cmd := exec.CommandContext(ctx, singleFilePath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		slog.Warn("SingleFile execution failed", "url", rawURL, "error", err, "stderr", stderr.String())
		return "", fmt.Errorf("singlefile failed: %w", err)
	}

	data, err := os.ReadFile(tmpFile)
	if err != nil || len(data) == 0 {
		return "", fmt.Errorf("singlefile output missing for %s", rawURL)
	}
	text := ParseHTML(string(data), false)
	md := RemoveBase64Images(text)
	if strings.TrimSpace(md) == "" {
		return "", fmt.Errorf("singlefile extracted empty text for %s", rawURL)
	}
	return md, nil
}
