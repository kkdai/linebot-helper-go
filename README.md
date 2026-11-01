# linebot-helper-go

LINE Bot 智能助手（Go 1.26 改寫版），整合 **Google ADK for Go (`google.golang.org/adk v1.9.0`)**、Google Vertex AI、Gemini Developer API（Live API / TTS / Batch API）與 Cloud Firestore，支援 Cloud Run 無伺服器部署。

- **Google ADK 版本**：`google.golang.org/adk v1.9.0`（搭配 `google.golang.org/genai v1.57.0`）

---

## 核心功能

- **多意圖 ADK Orchestrator 與專職 Agents（`google.golang.org/adk v1.9.0`）**：
  - 使用 `google.golang.org/adk/agent/llmagent` 與 `google.golang.org/adk/tool/functiontool` 建立 Root `orchestrator` Agent 及 5 個 Sub-Agents：
    1. `ChatAgent` (`chat_agent`)：具備 Google Search Grounding 與 Firestore Session 記憶的連續對話問答
    2. `ContentAgent` (`content_agent`)：網頁、YouTube 影片、多格式文件（PDF / DOCX / PPTX / XLSX / HTML / TXT）智慧摘要與 FB / LinkedIn / Threads 社群爆款文案 Carousel
    3. `LocationAgent` (`location_agent`)：Google Maps Grounding 附近加油站、停車場、餐廳搜尋
    4. `VisionAgent` (`vision_agent`)：一般圖片辨識與 Agentic Vision（`gemini-3-flash-preview` + Code Execution 標註圖片）
    5. `GitHubAgent` (`github_agent`)：`@g` 自動彙整近期 GitHub Issues
- **書籤與研究報告系統**：
  - `/save <網址>`、`/list`、`/search <關鍵字>` 與 Flex Message 書籤管理
  - 點擊「📄 詳細研究報告」自動以 Google Search Grounding 撰寫深度 Markdown 報告，渲染為永久保存於 Firestore 的 HTML 頁面（`/reports/{report_id}`）
- **語音訊息與 TTS 朗讀**：
  - 傳送 LINE 語音訊息自動顯示 Loading 動畫、Gemini 語音轉錄並透過 Orchestrator 回答，附帶「🔊 用語音聽」按鈕（`gemini-3.1-flash-tts-preview` + `ffmpeg` M4A 轉檔）
- **Gemini Batch API 美食深度分析**：
  - 結合 Maps Grounding 與 Google Search Grounding 蒐集餐廳評論，提交 Gemini Batch API 批次分析，支援 Static HMAC Webhook、Dynamic RS256 JWKS Webhook 與 60 秒背景輪詢
- **LIFF 即時雙向語音助手**：
  - `/liff/` 前端與 `/ws/voice/{session_id}` WebSocket 雙向串流（`gemini-3.1-flash-live-preview`），支援 Push-to-Talk、免持自動 VAD、Barge-in 打斷、Session Resumption、Context Window Compression 與 Live Tool Calling

---

## 從 Python 版切換

準備好從 `linebot-helper-python` 切換至 `linebot-helper-go` 時，請依序更新以下設定（新舊服務共用同一個 Firestore 資料庫 `chat_sessions`、`bookmarks`、`reports`、`batch_jobs`，切換過程不會遺失對話、書籤或報告資料）：

### 1. 把 LINE Webhook URL（以及 LIFF Endpoint）改指向新服務

#### (A) LINE Bot Webhook URL (`POST /`)
- **目標 Webhook URL**：
  ```text
  https://<YOUR_CLOUD_RUN_SERVICE_URL>/
  ```
- **方式一：透過 LINE Developers Console 修改**
  1. 前往 [LINE Developers Console](https://developers.line.biz/console/) → 選擇對應的 Provider 與 Messaging API Channel。
  2. 進入 **Messaging API** 分頁 → 找到 **Webhook settings**。
  3. 將 **Webhook URL** 改為您的 Cloud Run 服務網址：`https://<YOUR_CLOUD_RUN_SERVICE_URL>/`
  4. 點擊 **Update** 並按 **Verify** 確認回傳 `200 OK`，確保 **Use webhook** 為開啟狀態。
- **方式二：透過 LINE Messaging API 指令直接切換**
  ```bash
  curl -X PUT https://api.line.me/v2/bot/channel/webhook/endpoint \
    -H "Authorization: Bearer $ChannelAccessToken" \
    -H "Content-Type: application/json" \
    -d '{"endpoint":"https://<YOUR_CLOUD_RUN_SERVICE_URL>/"}'
  ```

#### (B) LINE LIFF Endpoint URL (`GET /liff/`)
- **目標 LIFF Endpoint URL**：
  ```text
  https://<YOUR_CLOUD_RUN_SERVICE_URL>/liff/
  ```
- **切換步驟**：
  1. 前往 [LINE Developers Console](https://developers.line.biz/console/) → 選擇同一個 Provider 下的 **LINE Login** Channel。
  2. 進入 **LIFF** 分頁 → 選擇對應的 LIFF App（對應環境變數 `LIFF_ID`）。
  3. 將 **Endpoint URL** 更新為：`https://<YOUR_CLOUD_RUN_SERVICE_URL>/liff/`

#### (C) （選用）Gemini Batch API 靜態 Webhook
- 新端點：`https://<YOUR_CLOUD_RUN_SERVICE_URL>/api/gemini-callback/static`
- （Dynamic Webhook `/api/gemini-callback/dynamic` 會自動根據進站請求 Host 動態帶入，無須手動調整）

---

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
    ├── agents/                      # Google ADK (v1.9.0) Orchestrator 與 5 個專職 Agents
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
  --project <YOUR_GCP_PROJECT_ID> \
  --region us-central1 \
  --cpu 1 \
  --memory 1Gi \
  --allow-unauthenticated \
  --no-cpu-throttling
```
