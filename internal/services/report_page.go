package services

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/kkdai/linebot-helper-go/internal/gemini"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

const pageCSS = `
  :root { color-scheme: light dark; }
  * { box-sizing: border-box; }
  body {
    margin: 0; padding: 0;
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI",
      "Noto Sans TC", "PingFang TC", "Microsoft JhengHei", sans-serif;
    line-height: 1.75; color: #1a1a1a; background: #f5f5f4;
  }
  .container { max-width: 720px; margin: 0 auto; padding: 24px 20px 48px; }
  .card { background: #ffffff; border-radius: 12px; padding: 28px 24px;
          box-shadow: 0 1px 4px rgba(0,0,0,.08); }
  h1 { font-size: 1.5rem; line-height: 1.4; margin: 0 0 4px; }
  h2 { font-size: 1.2rem; margin-top: 2em; padding-bottom: .3em;
       border-bottom: 2px solid #E67E22; }
  h3 { font-size: 1.05rem; margin-top: 1.6em; }
  a { color: #C0611A; word-break: break-all; }
  blockquote { margin: 1em 0; padding: .5em 1em; border-left: 4px solid #E67E22;
               background: #faf3ec; color: #555; }
  code { background: #f0f0ef; padding: .15em .4em; border-radius: 4px;
         font-size: .9em; }
  table { border-collapse: collapse; width: 100%; display: block;
          overflow-x: auto; }
  th, td { border: 1px solid #ddd; padding: 8px 10px; text-align: left; }
  .meta { color: #888; font-size: .85rem; margin-bottom: 24px; }
  .sources { margin-top: 32px; font-size: .9rem; }
  .sources li { margin-bottom: .5em; }
  @media (prefers-color-scheme: dark) {
    body { background: #191918; color: #e8e8e6; }
    .card { background: #242422; box-shadow: none; }
    blockquote { background: #2e2a24; color: #bbb; }
    code { background: #333; }
    th, td { border-color: #444; }
    a { color: #e89a55; }
  }
`

var mdRenderer = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
)

// RenderReportPage converts Markdown into a styled mobile-friendly HTML page.
func RenderReportPage(title, markdownText, url string, sources []gemini.GroundingSource) string {
	if strings.TrimSpace(title) == "" {
		title = "研究報告"
	}
	var bodyBuf bytes.Buffer
	_ = mdRenderer.Convert([]byte(markdownText), &bodyBuf)
	bodyHTML := bodyBuf.String()
	generatedAt := time.Now().Format("2006-01-02 15:04")

	sourcesHTML := ""
	if len(sources) > 0 {
		var items strings.Builder
		for _, s := range sources {
			if s.URI == "" {
				continue
			}
			label := s.Title
			if label == "" {
				label = s.URI
			}
			items.WriteString(fmt.Sprintf(`<li><a href="%s" target="_blank" rel="noopener">%s</a></li>`, s.URI, label))
		}
		if items.Len() > 0 {
			sourcesHTML = fmt.Sprintf(`<div class="sources"><h2>📚 參考來源</h2><ol>%s</ol></div>`, items.String())
		}
	}

	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-Hant">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>%s</title>
<style>%s</style>
</head>
<body>
<div class="container">
  <div class="card">
    <h1>🔬 %s</h1>
    <div class="meta">
      產生時間：%s<br>
      原文：<a href="%s" target="_blank" rel="noopener">%s</a>
    </div>
    %s
    %s
  </div>
</div>
</body>
</html>`, title, pageCSS, title, generatedAt, url, url, bodyHTML, sourcesHTML)
}

// RenderExpiredPage renders the 404 HTML page when a report is not found.
func RenderExpiredPage() string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-Hant">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>找不到報告</title>
<style>%s</style>
</head>
<body>
<div class="container">
  <div class="card">
    <h1>🔍 找不到這份報告</h1>
    <p>這個連結對應的研究報告不存在，可能是網址有誤。</p>
    <p>回到 LINE 重新點「📄 詳細研究報告」即可再產生一份。</p>
  </div>
</div>
</body>
</html>`, pageCSS)
}
