# linebot-helper-go

LINE Bot 智能助手（Go 1.26 改寫版），整合 Google Vertex AI、Gemini Developer API（Live API / TTS / Batch API）與 Cloud Firestore，支援 Cloud Run 無伺服器部署。

## 核心功能

- **多意圖 Orchestrator 與專職 Agents**：
  - `ChatAgent`：具備 Google Search Grounding 與 Firestore Session 記憶的連續對話問答
  - `ContentAgent`：網頁、YouTube 影片、多格式文件（PDF / DOCX / PPTX / XLSX / HTML / TXT）智慧摘要與 FB / LinkedIn / Threads 社群爆款文案 Carousel
  - `LocationAgent`：Google Maps Grounding 附近加油站、停車場、餐廳搜尋
  - `VisionAgent`：一般圖片辨識與 Agentic Vision（`gemini-3-flash-preview` + Code Execution 標註圖片）
  - `GitHubAgent`：`@g` 自動彙整近期 GitHub Issues
- **書籤與研究報告系統**：
  - `/save <網址>`、`/list`、`/search <關鍵字>` 與 Flex Message 書籤管理
  - 點擊「📄 詳細研究報告」自動以 Google Search Grounding 撰寫深度 Markdown 報告，渲染為永久保存於 Firestore 的 HTML 頁面（`/reports/{report_id}`）
- **語音訊息與 TTS 朗讀**：
  - 傳送 LINE 語音訊息自動顯示 Loading 動畫、Gemini 語音轉錄並透過 Orchestrator 回答，附帶「🔊 用語音聽」按鈕（`gemini-3.1-flash-tts-preview` + `ffmpeg` M4A 轉檔）
- **Gemini Batch API 美食深度分析**：
  - 結合 Maps Grounding 與 Google Search Grounding 蒐集餐廳評論，提交 Gemini Batch API 批次分析，支援 Static HMAC Webhook、Dynamic RS256 JWKS Webhook 與 60 秒背景輪詢
- **LIFF 即時雙向語音助手**：
  - `/liff/` 前端與 `/ws/voice/{session_id}` WebSocket 雙向串流（`gemini-3.1-flash-live-preview`），支援 Push-to-Talk、免持自動 VAD、Barge-in 打斷、Session Resumption、Context Window Compression 與 Live Tool Calling

## 專案結構

```text
.
├── main.go                          # 應用程式入口與 Graceful Shutdown
├── Dockerfile                       # Multi-stage Go 1.26 + Chromium/SingleFile + ffmpeg 映像檔
├── static/liff/                     # LIFF 即時語音助手前端資產
└── internal/
    ├── config/                      # 環境變數載入與驗證
    ├── gemini/                      # Vertex AI & Developer API REST + TTS 客戶端
    ├── firestore/                   # Cloud Firestore REST v1 KVStore 與記憶體測試替身
    ├── services/                    # SessionManager, BookmarkService, ReportStore, ReportPage, BatchService
    ├── line/                        # LINE Messaging API Client, Webhook 簽章驗證, Flex Message 組裝
    ├── loader/                      # URL Fallback Chain, SingleFile, Firecrawl, 多格式文件解析
    ├── ai/                          # 摘要、社群爆款文案、研究報告、YouTube、Vision、GitHub、Maps
    ├── agents/                      # Orchestrator 與 5 個專職 Agents
    ├── voice/                       # Gemini Live API BidiGenerateContent WebSocket Relay
    └── handlers/                    # HTTP 路由、非同步 Webhook 處理與 Postback 分派
```

## 測試與建置

```bash
go vet ./...
go test -v ./...
go build -o linebot-helper-go .
```

## 部署至 Cloud Run

```bash
gcloud run deploy linebot-helper-go \
  --source . \
  --project line-vertex \
  --region us-central1 \
  --allow-unauthenticated \
  --no-cpu-throttling
```
