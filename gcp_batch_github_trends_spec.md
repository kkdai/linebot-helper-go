# GCP Batch 工作規範書：每週 GitHub Trending 自動追蹤與智慧研析系統

**專案名稱**：`weekly-github-trends-batch-collector`  
**文件版本**：v1.0.0  
**目標平台**：Google Cloud Platform (GCP) Batch / Cloud Scheduler / Secret Manager / Cloud Storage  
**執行週期**：每週六上午 08:00 (Asia/Taipei 時區，Cron: `0 8 * * 6`)  
**主要交付物**：結構化繁體中文 Markdown 週報、歷程 JSON 資料集、即時 Webhook 廣播  

---

## 1. 系統願景與架構概述

### 1.1 背景與遷移目標
本系統將原先在對話式排程環境中執行的「每週 GitHub Trending 前十名追蹤與研析任務」，重構成為企業級、雲原生且完全無伺服器（Serverless）的 **GCP Batch (Google Cloud Batch)** 容器化批次作業。

* **任務自主性**：不依賴任何長駐 VM 伺服器，按需啟動運算資源，執行完畢即刻釋放，達到極致成本效益。
* **高可靠性與冪等性**：具備自動重試機制、超時防護、歷程去重比對，以及完整結構化審計日誌。
* **多通道交付**：產出的研析結果自動儲存至 Google Cloud Storage (GCS) 永久歸檔，並可同時推播至企業通訊軟體（Slack、Discord、Teams、Webhook）或寄送 Email。

### 1.2 端到端系統架構

```
+-----------------------------------------------------------------------------------+
|                               Google Cloud Platform                               |
|                                                                                   |
|  [ Cloud Scheduler ]                                                              |
|         |                                                                         |
|         | (每週六 08:00 Asia/Taipei 觸發)                                         |
|         v                                                                         |
|  [ Cloud Workflows / Batch API ]                                                  |
|         |                                                                         |
|         | (建立批次任務 batch.jobs.create)                                        |
|         v                                                                         |
|  [ GCP Batch Job (Spot VM / Container: python:3.11-slim) ]                       |
|         |                                                                         |
|         +---> (讀取 API 金鑰) --------> [ Secret Manager ]                        |
|         |                                                                         |
|         +---> (抓取 Trending & Repo) -> [ GitHub Web & REST/GraphQL API ]         |
|         |                                                                         |
|         +---> (對比歷程與讀取歷史) ---> [ Cloud Storage (gs://.../archive) ]       |
|         |                                                                         |
|         +---> (深層語意研析與繁中總結) -> [ Google Gemini API / LLM ]               |
|         |                                                                         |
|         +---> (寫入 Markdown & JSON) -> [ Cloud Storage (gs://.../reports) ]       |
|         |                                                                         |
|         +---> (發送通知廣播) ---------> [ Webhook (Slack / Discord / Email) ]      |
|         |                                                                         |
|         +---> (結構化日誌輸出) -------> [ Cloud Logging & Cloud Monitoring ]       |
+-----------------------------------------------------------------------------------+
```

---

## 2. GCP Batch 工作規格定義 (Job Specification)

GCP Batch 工作採用容器化 Runnable 定義，執行時由 Batch 服務自動配置底層 Compute Engine 執行個體（推薦使用 Spot 執行個體以節省 60%~91% 運算成本）。

### 2.1 批次工作設定檔 (`job-spec.json`)

以下為提交至 GCP Batch API 的完整任務規格定義檔：

```json
{
  "name": "projects/${PROJECT_ID}/locations/${REGION}/jobs/github-trends-weekly-${DATE_TAG}",
  "priority": 50,
  "taskGroups": [
    {
      "taskSpec": {
        "runnables": [
          {
            "container": {
              "imageUri": "${REGION}-docker.pkg.dev/${PROJECT_ID}/batch-runners/github-trends-agent:latest",
              "commands": [
                "python",
                "-m",
                "src.main"
              ],
              "options": "--net=host"
            },
            "environment": {
              "variables": {
                "TARGET_LOCALE": "zh-TW",
                "TREND_TIME_RANGE": "weekly",
                "OUTPUT_BUCKET": "${GCS_OUTPUT_BUCKET}",
                "LOG_LEVEL": "INFO"
              },
              "secretVariables": {
                "GITHUB_TOKEN": "projects/${PROJECT_ID}/secrets/github-access-token/versions/latest",
                "GEMINI_API_KEY": "projects/${PROJECT_ID}/secrets/gemini-api-key/versions/latest",
                "NOTIFICATION_WEBHOOK_URL": "projects/${PROJECT_ID}/secrets/notification-webhook-url/versions/latest"
              }
            }
          }
        ],
        "computeResource": {
          "cpuMilli": 1000,
          "memoryMib": 2048
        },
        "maxRunDuration": "900s"
      },
      "taskCount": 1,
      "parallelism": 1,
      "maxRetryCount": 2
    }
  ],
  "allocationPolicy": {
    "instances": [
      {
        "policy": {
          "machineType": "e2-medium",
          "provisioningModel": "SPOT"
        }
      }
    ],
    "serviceAccount": {
      "email": "batch-trends-runner@${PROJECT_ID}.iam.gserviceaccount.com",
      "scopes": [
        "https://www.googleapis.com/auth/cloud-platform"
      ]
    }
  },
  "logsPolicy": {
    "destination": "CLOUD_LOGGING"
  }
}
```

### 2.2 運算與調度參數規範

| 參數項目 | 設定值 | 設計說明 |
| :--- | :--- | :--- |
| **機器類型 (Machine Type)** | `e2-medium` (1 vCPU, 4GB RAM) | 兼顧抓取解析與輕量推論呼叫的記憶體緩衝區 |
| **計費模式 (Provisioning)** | `SPOT` (優先搶佔式) | 成本極低，若遇搶佔由 Batch 自動重新排程至其他可用節點 |
| **超時時限 (maxRunDuration)** | `900s` (15 分鐘) | 防範網路爬蟲遭遇死鎖或下游 API 無響應造成資源浪費 |
| **重試上限 (maxRetryCount)** | `2` 次 | 針對偶發性網路超時或 GitHub 503 錯誤提供容錯機制 |
| **日誌儲存 (logsPolicy)** | `CLOUD_LOGGING` | 結構化輸出至 Google Cloud Logging，便於集中檢索與審計 |

---

## 3. 核心容器與處理管線架構 (Container & Processing Pipeline)

### 3.1 容器映像檔規格 (Dockerfile)

```dockerfile
# 使用輕量且具備安全性更新的官方 Python 基礎映像
FROM python:3.11-slim-bookworm

# 設置非互動模式與環境變數
ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    PIP_NO_CACHE_DIR=1

WORKDIR /app

# 安裝基本網路與 CA 憑證
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    && rm -rf /var/lib/apt/lists/*

# 安裝依賴庫
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# 複製應用程式原始碼
COPY src/ ./src/

# 建立非 root 安全使用者
RUN useradd -u 10001 -m batchuser
USER 10001

CMD ["python", "-m", "src.main"]
```

**`requirements.txt` 清單**：
```txt
beautifulsoup4==4.12.3
requests==2.32.3
google-cloud-storage==2.18.2
google-cloud-secret-manager==2.20.0
google-genai==0.1.1
pydantic==2.8.2
tenacity==8.5.0
```

---

### 3.2 處理管線五階段邏輯

```
[階段 1: Ingestion]  --> [階段 2: Delta Analysis] --> [階段 3: LLM Structuring] --> [階段 4: Compilation] --> [階段 5: Delivery]
抓取 GitHub Trending      比對 GCS 歷史 JSON              呼叫 Gemini 結構化分析        生成繁中 Markdown 週報      寫入 GCS & Webhook 推播
提取 Repo/Language/Stars   標記新進與持續霸榜專案         產出核心亮點與趨勢成因        嵌入來源可點擊超連結        記錄 Cloud Logging 結構化狀態
```

#### 階段 1：資料採集與元數據豐富化 (Ingestion & Enrichment)
* **目標**：抓取 `https://github.com/trending?since=weekly` 榜單前 10 名。
* **資料清洗**：
  * 解析 Repo 擁有者與名稱（例：`google/ax`）。
  * 提取主要程式語言、當週累計星標（Total Stars）與當日/當週激增星標（Stars this week）。
  * 透過 GitHub REST API (`/repos/{owner}/{repo}`) 擴充最新 Release 資訊、主要 Topics 標籤、授權條款（License）與近一週 Commit 頻率。

#### 階段 2：差異比對與去重機制 (Diff & Delta Analysis)
* **目標**：識別本週趨勢動能與持久度。
* **比對流程**：
  * 從 GCS 讀取前一週的存檔 `gs://${OUTPUT_BUCKET}/archive/weekly_latest.json`。
  * 計算狀態標記：
    * `NEW`：本週新上榜專案（首次突破進入前十名）。
    * `RISING`：排名上升或星標激增斜率顯著增加。
    * `CONTINUOUS`：持續維持在趨勢前列（如特定基礎架構或熱門官方工具）。

#### 階段 3：大語言模型結構化研析 (LLM Analysis & Insight Generation)
* **模型選擇**：Google Gemini 2.5 Flash / Pro (透過 `google-genai` SDK)。
* **輸入提示詞架構**：
  * 傳入結構化 JSON（包含專案名稱、README 摘要、Release Notes、近期討論熱度）。
  * 指定分析維度：專案定位領域、解決之傳統痛點、架構突破技術點、社群激增驅動因（如論文發表、大廠開源、重大資安事件、關鍵痛點解決）。
  * 強制語言與風格：符合繁體中文（台灣習慣用語）、客觀嚴謹、不使用誇大行銷詞彙。

#### 階段 4：繁體中文 Markdown 報告編譯 (Report Compilation)
* **排版規範**：
  * 包含頂部整體開源社群趨勢摘要（宏觀洞察）。
  * 緊接著結構化總覽比較表格（排名、專案、語言領域、星標與動態、核心亮點）。
  * 依序展開 10 個專案的深度解析章節（包含可點擊連結、程式語言、核心功能簡介、為何值得關注）。
  * 每個主張與資料點皆精確綁定實體來源 URL。

#### 階段 5：多通道儲存與通知廣播 (Multi-channel Delivery & Archival)
* **GCS 永久儲存**：
  * `gs://${OUTPUT_BUCKET}/reports/weekly_trends_YYYY-MM-DD.md` (當期 Markdown 報告)
  * `gs://${OUTPUT_BUCKET}/archive/trends_YYYY-MM-DD.json` (結構化原始數據)
  * `gs://${OUTPUT_BUCKET}/archive/weekly_latest.json` (提供下週對比的指標檔案)
* **推播廣播**：
  * 發送 Slack / Discord / Microsoft Teams 摘要卡片至 `NOTIFICATION_WEBHOOK_URL`。

---

### 3.3 參考 Python 核心實現骨架 (`src/main.py`)

```python
import os
import json
import logging
from datetime import datetime
import requests
from bs4 import BeautifulSoup
from google.cloud import storage
from google import genai
from pydantic import BaseModel, Field

logging.basicConfig(level=os.getenv("LOG_LEVEL", "INFO"), format="%(asctime)s [%(levelname)s] %(message)s")

class TrendItem(BaseModel):
    rank: int
    name: str
    url: str
    language: str
    total_stars: str
    stars_added: str
    description: str

def fetch_github_trending() -> list[TrendItem]:
    headers = {"User-Agent": "Mozilla/5.0 (GCP-Batch-Weekly-Crawler/1.0)"}
    resp = requests.get("https://github.com/trending?since=weekly", headers=headers, timeout=30)
    resp.raise_for_status()
    soup = BeautifulSoup(resp.text, "html.parser")
    rows = soup.find_all("article", class_="Box-row")
    
    items = []
    for idx, row in enumerate(rows[:10], start=1):
        h2 = row.find("h2")
        a_tag = h2.find("a")
        repo_name = "".join(a_tag.text.split())
        repo_url = f"https://github.com/{repo_name}"
        
        desc_p = row.find("p")
        desc = desc_p.text.strip() if desc_p else ""
        
        lang_span = row.find("span", itemprop="programmingLanguage")
        language = lang_span.text.strip() if lang_span else "General"
        
        stars_a = row.find("a", href=lambda h: h and h.endswith("/stargazers"))
        total_stars = stars_a.text.strip() if stars_a else "0"
        
        added_span = row.find("span", class_="d-inline-block float-sm-right")
        stars_added = added_span.text.strip() if added_span else ""
        
        items.append(TrendItem(
            rank=idx,
            name=repo_name,
            url=repo_url,
            language=language,
            total_stars=total_stars,
            stars_added=stars_added,
            description=desc
        ))
    return items

def analyze_with_llm(items: list[TrendItem], gemini_client: genai.Client) -> str:
    prompt = f"""
你是一位資深的軟體架構師與開源趨勢分析師。
請根據以下本週 GitHub Trending 前 10 名專案的資料，產出一份專業、客觀且結構嚴謹的繁體中文 Markdown 週報。

專案清單資料：
{json.dumps([i.model_dump() for i in items], ensure_ascii=False, indent=2)}

報告格式要求：
1. 頂部摘要：總結本週全球開源社群的 2~3 個核心技術趨勢。
2. 總覽表格：使用 Markdown 表格比較 10 個專案（欄位：排名、專案、主要語言/領域、星標趨勢、核心亮點）。
3. 詳細章節：為每個專案撰寫詳細分析，必須包含可點擊 Markdown 連結、專案簡介與「本週受到關注的原因」。
4. 語言風格：繁體中文，專業自然，避免過度誇大的行銷詞彙。
"""
    response = gemini_client.models.generate_content(
        model="gemini-2.5-flash",
        contents=prompt
    )
    return response.text

def main():
    logging.info("啟動每週 GitHub Trending 批次工作...")
    items = fetch_github_trending()
    logging.info(f"成功抓取 {len(items)} 個熱門專案")
    
    client = genai.Client(api_key=os.environ["GEMINI_API_KEY"])
    report_md = analyze_with_llm(items, client)
    
    # 寫入 Google Cloud Storage
    bucket_name = os.environ["OUTPUT_BUCKET"]
    date_str = datetime.now().strftime("%Y-%m-%d")
    storage_client = storage.Client()
    bucket = storage_client.bucket(bucket_name)
    
    report_blob = bucket.blob(f"reports/weekly_trends_{date_str}.md")
    report_blob.upload_from_string(report_md, content_type="text/markdown; charset=utf-8")
    
    json_blob = bucket.blob(f"archive/trends_{date_str}.json")
    json_blob.upload_from_string(json.dumps([i.model_dump() for i in items], ensure_ascii=False), content_type="application/json")
    
    latest_blob = bucket.blob("archive/weekly_latest.json")
    latest_blob.upload_from_string(json.dumps([i.model_dump() for i in items], ensure_ascii=False), content_type="application/json")
    logging.info(f"產物成功發布至 GCS: gs://{bucket_name}/reports/weekly_trends_{date_str}.md")
    
    # 觸發 Webhook 推播
    webhook_url = os.getenv("NOTIFICATION_WEBHOOK_URL")
    if webhook_url:
        payload = {
            "text": f"🚀 *本週 GitHub Trending Top 10 週報已產出 ({date_str})*\n報告已儲存至 Cloud Storage，並完成趨勢研析。"
        }
        requests.post(webhook_url, json=payload, timeout=10)
        logging.info("通知已成功發送至 Webhook")

if __name__ == "__main__":
    main()
```

---

## 4. 排程調度與自動化管線 (Scheduling & Orchestration)

為了實現無人值守的每週定時觸發，採用 **Cloud Scheduler -> Cloud Workflows (或直接呼叫 Batch API)** 架構。

### 4.1 Cloud Scheduler 設定指令

```bash
gcloud scheduler jobs create http weekly-github-trends-trigger \
    --location="asia-east1" \
    --schedule="0 8 * * 6" \
    --time-zone="Asia/Taipei" \
    --uri="https://batch.googleapis.com/v1/projects/${PROJECT_ID}/locations/asia-east1/jobs?job_id=github-trends-weekly-$(date +%Y%m%d%H%M)" \
    --http-method="POST" \
    --headers="Content-Type=application/json" \
    --oauth-service-account-email="batch-scheduler@${PROJECT_ID}.iam.gserviceaccount.com" \
    --message-body-from-file="job-spec.json"
```

---

## 5. 安全性、機密管理與 IAM 權限配置 (Security & IAM)

貫徹最小權限原則（Principle of Least Privilege），建立專用的服務帳戶並綁定精準的角色權限。

### 5.1 服務帳戶劃分與權限對照

```
+---------------------------------------------+------------------------------------------------------------+
| 服務帳戶名稱                                | 賦予 IAM 角色與職責說明                                    |
+---------------------------------------------+------------------------------------------------------------+
| batch-scheduler@...iam.gserviceaccount.com  | roles/batch.jobsEditor (負責於週六建立 Batch Job)          |
|                                             | roles/iam.serviceAccountUser (允許代理 Runner 執行)       |
+---------------------------------------------+------------------------------------------------------------+
| batch-trends-runner@...iam.gserviceaccount.com | roles/batch.agentReporter (回報 Batch Agent 心跳狀態)     |
|                                             | roles/storage.objectAdmin (讀寫報告至 gs://${BUCKET})      |
|                                             | roles/secretmanager.secretAccessor (讀取 API Key 與 Token) |
|                                             | roles/logging.logWriter (寫入 Cloud Logging 結構化日誌)   |
+---------------------------------------------+------------------------------------------------------------+
```

### 5.2 Secret Manager 機密建立指令

```bash
# 1. 建立 GitHub Token 機密
echo -n "${GITHUB_TOKEN}" | gcloud secrets create github-access-token --data-file=-

# 2. 建立 Gemini API Key 機密
echo -n "${GEMINI_API_KEY}" | gcloud secrets create gemini-api-key --data-file=-

# 3. 建立通知 Webhook 機密
echo -n "${WEBHOOK_URL}" | gcloud secrets create notification-webhook-url --data-file=-

# 4. 授予 Batch Runner 服務帳戶讀取權限
for SECRET in github-access-token gemini-api-key notification-webhook-url; do
    gcloud secrets add-iam-policy-binding ${SECRET} \
        --member="serviceAccount:batch-trends-runner@${PROJECT_ID}.iam.gserviceaccount.com" \
        --role="roles/secretmanager.secretAccessor"
done
```

---

## 6. 基礎設施即代碼 (Terraform 部署腳本)

提供完整的 `main.tf` 基礎設施範本，方便一鍵在 GCP 專案中完成資源建置：

```hcl
terraform {
  required_version = ">= 1.5.0"
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 5.30"
    }
  }
}

variable "project_id" {
  type        = string
  description = "GCP Project ID"
}

variable "region" {
  type        = string
  default     = "asia-east1"
  description = "Deployment Region (Taiwan)"
}

# 1. 存放產出報告與歷史 JSON 的 GCS Bucket
resource "google_storage_bucket" "trends_bucket" {
  name          = "${var.project_id}-github-trends-archive"
  location      = var.region
  force_destroy = false

  uniform_bucket_level_access = true

  versioning {
    enabled = true
  }

  lifecycle_rule {
    condition {
      age = 365
    }
    action {
      type = "SetStorageClass"
      storage_class = "COLDLINE"
    }
  }
}

# 2. Batch Runner 專用服務帳戶
resource "google_service_account" "batch_runner" {
  account_id   = "batch-trends-runner"
  display_name = "GCP Batch Runner Service Account for GitHub Trends"
}

# 3. 賦予 Storage 物件管理權限
resource "google_storage_bucket_iam_member" "storage_admin" {
  bucket = google_storage_bucket.trends_bucket.name
  role   = "roles/storage.objectAdmin"
  member = "serviceAccount:${google_service_account.batch_runner.email}"
}

# 4. 賦予 Batch 回報與 Logging 權限
resource "google_project_iam_member" "batch_agent_reporter" {
  project = var.project_id
  role    = "roles/batch.agentReporter"
  member  = "serviceAccount:${google_service_account.batch_runner.email}"
}

resource "google_project_iam_member" "log_writer" {
  project = var.project_id
  role    = "roles/logging.logWriter"
  member  = "serviceAccount:${google_service_account.batch_runner.email}"
}
```

---

## 7. 監控告警、SRE 維運與成本試算 (Observability & SRE)

### 7.1 Cloud Monitoring 失敗告警設定
建立日誌指標與警報條件：當 Batch 工作回傳 `FAILED` 狀態或任務超時中斷時，自動向維運人員發送 PagerDuty 或 Email 通知。

* **Log-based Metric Filter**:
  ```
  resource.type="batch.googleapis.com/Job"
  jsonPayload.jobStatus="FAILED"
  ```

### 7.2 每月營運成本預估 (Cost Estimation)

GCP Batch 本身**不收取額外的調度服務費用**，僅依實際消耗的 Compute Engine 與 GCS 資源計費：

| 資源項目 | 單次消耗量 | 每週執行 1 次 (每月 4 次) | 預估月費 (USD) |
| :--- | :--- | :--- | :--- |
| **Compute Engine (Spot e2-medium)** | 執行約 2~3 分鐘 (0.05 小時) | 0.2 小時 / 月 | < $0.01 |
| **Cloud Storage (標準儲存 + API 操作)** | < 10 MB 文本資料 | 每年 < 100 MB | < $0.01 |
| **Secret Manager (API 存取次數)** | 3 次 Secret 存取 / 執行 | 12 次 / 月 | 免費額度內 ($0.00) |
| **Cloud Scheduler** | 1 個排程工作 | 每月 4 次呼叫 | 免費額度內 ($0.00) |
| **Gemini 2.5 Flash API** | ~8k Input Tokens + ~3k Output | 4 次研析 / 月 | < $0.02 |
| **總計預估月費用** | - | - | **< $0.05 USD / 月** |

---

## 8. 驗收標準與上線檢核清單 (Acceptance Criteria & Checklist)

- [ ] **容器建置測試**：本地端執行 `docker build` 與 `docker run` 測試各模組相依性，確認無權限或缺少套件問題。
- [ ] **Secret Manager 存取驗證**：容器在不帶本機環境變數下，能正確自 GCP 讀取金鑰。
- [ ] **GCS 寫入授權測試**：確認生成的 Markdown 週報與 JSON 檔案能正確寫入指定目錄且編碼為 UTF-8。
- [ ] **Cloud Scheduler 乾跑測試 (Dry-run)**：手動點擊 Scheduler 觸發，驗證 GCP Batch 於 3 分鐘內自啟動至成功結束（狀態顯示 `SUCCEEDED`）。
- [ ] **產物品質檢驗**：確認產出的 Markdown 週報包含宏觀趨勢、比較表格與可點擊超連結，無幻覺或格式損壞。
