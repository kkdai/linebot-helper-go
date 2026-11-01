package loader

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestExtractURLAndMode(t *testing.T) {
	urls, mode := ExtractURLAndMode("請幫我看 https://example.com/article [詳]")
	if mode != "detailed" || len(urls) != 1 || urls[0] != "https://example.com/article" {
		t.Errorf("unexpected result: urls=%v, mode=%s", urls, mode)
	}

	urls, mode = ExtractURLAndMode("https://x.com/test/status/1 (short)")
	if mode != "short" || len(urls) != 1 {
		t.Errorf("unexpected result: urls=%v, mode=%s", urls, mode)
	}
	if ReplaceDomain(urls[0]) != "https://api.fxtwitter.com/test/status/1" {
		t.Errorf("unexpected domain replacement: %s", ReplaceDomain(urls[0]))
	}
}

func TestParseHTMLAndRemoveBase64(t *testing.T) {
	raw := `<html><body><h1>Hello</h1><p>World</p><img src="data:image/png;base64,iVBORw0KGgo=" alt="img"/></body></html>`
	md := ParseHTML(raw, true)
	if !strings.Contains(md, "Hello") || strings.Contains(md, "data:image/png;base64") {
		t.Errorf("unexpected markdown output: %s", md)
	}
}

func TestDocumentDetectionAndConversion(t *testing.T) {
	if fmt := DetectDocumentFormat(context.Background(), "https://example.com/report.docx", nil); fmt != "docx" {
		t.Errorf("expected docx, got %s", fmt)
	}

	// Build a minimal in-memory .docx (ZIP with word/document.xml)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, _ := zw.Create("word/document.xml")
	_, _ = f.Write([]byte(`<w:document><w:body><w:p><w:r><w:t>Hello Docx World</w:t></w:r></w:p></w:body></w:document>`))
	_ = zw.Close()

	text, err := ConvertDocumentBytes(buf.Bytes(), "docx")
	if err != nil || !strings.Contains(text, "Hello Docx World") {
		t.Errorf("unexpected docx text=%q, err=%v", text, err)
	}
}

func TestLoaderFallbackChainOnTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `<html><body><p>Fallback HTTP Content</p></body></html>`)
	}))
	defer srv.Close()

	l := NewLoader("", "single-file")
	// Simulate SingleFile failing/timing out so fallback moves to HTTP
	l.SingleFileRunner = func(ctx context.Context, rawURL, singleFilePath string) (string, error) {
		return "", fmt.Errorf("simulated singlefile failure")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	content, err := l.LoadURL(ctx, srv.URL, "normal")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(content, "Fallback HTTP Content") {
		t.Errorf("unexpected content: %s", content)
	}
}
