package loader

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Default timeouts per loader method (matching loader/url.py).
const (
	DefaultLoaderTimeout = 30 * time.Second
	SingleFileTimeout    = 60 * time.Second
	HTTPXTimeout         = 20 * time.Second
	CloudScraperTimeout  = 30 * time.Second
	DocumentTimeout      = 60 * time.Second
)

// ContentResult matches the dictionary structure returned by tools/url_loader.py.
type ContentResult struct {
	Status         string `json:"status"`
	Content        string `json:"content,omitempty"`
	URL            string `json:"url,omitempty"`
	ContentType    string `json:"content_type,omitempty"`
	DocumentFormat string `json:"document_format,omitempty"`
	Method         string `json:"method,omitempty"`
	PageCount      int    `json:"page_count,omitempty"`
	ErrorMessage   string `json:"error_message,omitempty"`
}

// Loader orchestrates URL content loading with fallback chains and per-method timeouts.
type Loader struct {
	FirecrawlKey      string
	FirecrawlBaseURL  string
	SingleFilePath    string
	HTTPClient        *http.Client
	YouTubeSummarizer func(ctx context.Context, rawURL, mode string) (string, error)
	SingleFileRunner  func(ctx context.Context, rawURL, singleFilePath string) (string, error)
}

// NewLoader creates a new Loader instance.
func NewLoader(firecrawlKey, singleFilePath string) *Loader {
	if singleFilePath == "" {
		singleFilePath = "single-file"
	}
	return &Loader{
		FirecrawlKey:     firecrawlKey,
		FirecrawlBaseURL: DefaultFirecrawlBaseURL,
		SingleFilePath:   singleFilePath,
		HTTPClient:       &http.Client{Timeout: 30 * time.Second},
		SingleFileRunner: LoadHTMLWithSingleFile,
	}
}

type fallbackMethod struct {
	Name    string
	Timeout time.Duration
	Fn      func(ctx context.Context) (string, error)
}

func (l *Loader) tryFallbackChain(ctx context.Context, rawURL string, methods []fallbackMethod, errMsg string) (string, string, error) {
	for _, m := range methods {
		timeout := m.Timeout
		if timeout <= 0 {
			timeout = DefaultLoaderTimeout
		}
		slog.Info("Trying loader method", "method", m.Name, "url", rawURL)
		mCtx, cancel := context.WithTimeout(ctx, timeout)
		content, err := m.Fn(mCtx)
		cancel()
		if err == nil && strings.TrimSpace(content) != "" {
			return content, m.Name, nil
		}
		if mCtx.Err() == context.DeadlineExceeded {
			slog.Warn("Loader method timed out", "method", m.Name, "timeout", timeout, "url", rawURL)
		} else if err != nil {
			slog.Warn("Loader method failed", "method", m.Name, "url", rawURL, "error", err)
		}
	}
	slog.Error("All loader methods failed", "url", rawURL)
	return "", "", fmt.Errorf("%s", errMsg)
}

// LoadURL implements the fallback strategy from loader/url.py and returns extracted text or an error.
func (l *Loader) LoadURL(ctx context.Context, rawURL, youtubeMode string) (string, error) {
	if youtubeMode == "" {
		youtubeMode = "normal"
	}
	rawURL = ReplaceDomain(rawURL)

	if IsYouTubeURL(rawURL) {
		if l.YouTubeSummarizer == nil {
			return "", fmt.Errorf("YouTube summarizer not configured")
		}
		return l.YouTubeSummarizer(ctx, rawURL, youtubeMode)
	}

	httpMethod := fallbackMethod{
		Name:    "httpx",
		Timeout: HTTPXTimeout,
		Fn: func(c context.Context) (string, error) {
			return LoadHTMLWithHTTP(c, rawURL, true, l.HTTPClient)
		},
	}
	cloudscraperMethod := fallbackMethod{
		Name:    "cloudscraper",
		Timeout: CloudScraperTimeout,
		Fn: func(c context.Context) (string, error) {
			return LoadHTMLWithHTTP(c, rawURL, true, l.HTTPClient)
		},
	}
	sfRunner := l.SingleFileRunner
	if sfRunner == nil {
		sfRunner = LoadHTMLWithSingleFile
	}
	singleFileMethod := fallbackMethod{
		Name:    "singlefile",
		Timeout: SingleFileTimeout,
		Fn: func(c context.Context) (string, error) {
			return sfRunner(c, rawURL, l.SingleFilePath)
		},
	}

	if IsFirecrawlURL(rawURL) {
		if l.FirecrawlKey != "" {
			fcCtx, cancel := context.WithTimeout(ctx, DefaultLoaderTimeout)
			content, err := LoadHTMLWithFirecrawl(fcCtx, rawURL, l.FirecrawlKey, l.FirecrawlBaseURL, true, l.HTTPClient)
			cancel()
			if err == nil && strings.TrimSpace(content) != "" {
				return content, nil
			}
			slog.Warn("Firecrawl failed, falling back", "url", rawURL, "error", err)
		}

		if IsPTTURL(rawURL) {
			content, _, err := l.tryFallbackChain(ctx, rawURL, []fallbackMethod{
				cloudscraperMethod, httpMethod, singleFileMethod,
			}, "無法從 PTT 讀取內容，請稍後再試")
			return content, err
		}
		if strings.HasPrefix(rawURL, "https://openai.com") {
			content, _, err := l.tryFallbackChain(ctx, rawURL, []fallbackMethod{
				singleFileMethod, httpMethod,
			}, "無法從 OpenAI 讀取內容，請稍後再試")
			return content, err
		}
		if strings.Contains(rawURL, "medium.com") {
			content, _, err := l.tryFallbackChain(ctx, rawURL, []fallbackMethod{
				httpMethod, cloudscraperMethod, singleFileMethod,
			}, "無法從 Medium 讀取內容，請稍後再試")
			return content, err
		}
	}

	// Document links
	docFormat := DetectDocumentFormat(ctx, rawURL, l.HTTPClient)
	if docFormat != "" {
		docCtx, cancel := context.WithTimeout(ctx, DocumentTimeout)
		content, err := LoadDocument(docCtx, rawURL, docFormat, l.HTTPClient)
		cancel()
		if err == nil && strings.TrimSpace(content) != "" {
			return content, nil
		}
		return "", fmt.Errorf("無法讀取文件內容，請確認網址是否正確或稍後再試")
	}

	httpxDomains := []string{
		"https://ncode.syosetu.com",
		"https://pubmed.ncbi.nlm.nih.gov",
		"https://www.bnext.com.tw",
		"https://github.com",
		"https://www.twreporter.org",
		"https://telegra.ph",
		"https://www.jiqizhixin.com",
	}
	for _, d := range httpxDomains {
		if strings.HasPrefix(rawURL, d) {
			content, _, err := l.tryFallbackChain(ctx, rawURL, []fallbackMethod{
				httpMethod, singleFileMethod,
			}, "無法從網址讀取內容，請確認網址是否正確或稍後再試")
			return content, err
		}
	}

	if strings.HasPrefix(rawURL, "https://blog.tripplus.cc") {
		content, _, err := l.tryFallbackChain(ctx, rawURL, []fallbackMethod{
			cloudscraperMethod, singleFileMethod,
		}, "無法從網址讀取內容，請確認網址是否正確或稍後再試")
		return content, err
	}

	content, _, err := l.tryFallbackChain(ctx, rawURL, []fallbackMethod{
		singleFileMethod, httpMethod, cloudscraperMethod,
	}, "無法從網址讀取內容，請確認網址是否正確或稍後再試")
	return content, err
}

// LoadURLContent returns a structured ContentResult (matching tools/url_loader.py).
func (l *Loader) LoadURLContent(ctx context.Context, rawURL, youtubeMode string) ContentResult {
	if rawURL == "" {
		return ContentResult{Status: "error", ErrorMessage: "No URL provided"}
	}
	if youtubeMode == "" {
		youtubeMode = "normal"
	}
	rawURL = ReplaceDomain(rawURL)

	if IsYouTubeURL(rawURL) {
		if l.YouTubeSummarizer == nil {
			return ContentResult{Status: "error", ErrorMessage: "YouTube summarizer not configured", URL: rawURL}
		}
		summary, err := l.YouTubeSummarizer(ctx, rawURL, youtubeMode)
		if err != nil {
			return ContentResult{Status: "error", ErrorMessage: err.Error(), URL: rawURL}
		}
		return ContentResult{
			Status:      "success",
			Content:     summary,
			URL:         rawURL,
			ContentType: "youtube",
		}
	}

	docFormat := DetectDocumentFormat(ctx, rawURL, l.HTTPClient)
	if docFormat != "" {
		docCtx, cancel := context.WithTimeout(ctx, DocumentTimeout)
		content, err := LoadDocument(docCtx, rawURL, docFormat, l.HTTPClient)
		cancel()
		if err == nil && strings.TrimSpace(content) != "" {
			return ContentResult{
				Status:         "success",
				Content:        content,
				URL:            rawURL,
				ContentType:    "document",
				DocumentFormat: docFormat,
				Method:         "anydoc",
			}
		}
		return ContentResult{
			Status:       "error",
			ErrorMessage: "無法讀取文件內容，請確認網址是否正確或稍後再試",
			URL:          rawURL,
		}
	}

	content, err := l.LoadURL(ctx, rawURL, youtubeMode)
	if err != nil {
		return ContentResult{
			Status:       "error",
			ErrorMessage: err.Error(),
			URL:          rawURL,
		}
	}
	return ContentResult{
		Status:      "success",
		Content:     content,
		URL:         rawURL,
		ContentType: "html",
	}
}
