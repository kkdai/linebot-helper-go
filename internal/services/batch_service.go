package services

import (
	"bytes"
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kkdai/linebot-helper-go/internal/ai"
	"github.com/kkdai/linebot-helper-go/internal/firestore"
	"github.com/kkdai/linebot-helper-go/internal/gemini"
)

const (
	BatchJobsCollection = "batch_jobs"
	DefaultMappingFile  = "config/batch_jobs.json"
	DefaultJWKSURI      = "https://generativelanguage.googleapis.com/.well-known/jwks.json"
)

// BatchAnalysisItem represents one parsed restaurant analysis from a completed Batch API job.
type BatchAnalysisItem struct {
	Index    int    `json:"index"`
	Name     string `json:"name"`
	Analysis string `json:"analysis"`
}

// BatchSubmitResult represents the response from submitting a Gemini Batch API job.
type BatchSubmitResult struct {
	Status       string `json:"status"`
	BatchJobID   string `json:"batch_job_id,omitempty"`
	DisplayName  string `json:"display_name,omitempty"`
	State        string `json:"state,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
}

// BatchJobInfo represents metadata and status of a Gemini Batch API job.
type BatchJobInfo struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	State       string `json:"state"`
	DestFile    string `json:"destFile"`
}

// BatchService manages Gemini Batch API requests, job mappings, and result parsing.
type BatchService struct {
	APIKey      string
	BaseURL     string
	MappingFile string
	Store       firestore.KVStore
	HTTPClient  *http.Client

	mu sync.Mutex
}

// NewBatchService creates a new BatchService.
func NewBatchService(apiKey string, store firestore.KVStore, mappingFile string) *BatchService {
	if mappingFile == "" {
		mappingFile = DefaultMappingFile
	}
	bs := &BatchService{
		APIKey:      apiKey,
		BaseURL:     gemini.DefaultDeveloperBaseURL,
		MappingFile: mappingFile,
		Store:       store,
		HTTPClient:  &http.Client{Timeout: 60 * time.Second},
	}
	bs.ensureMappingFileExists()
	return bs
}

func (b *BatchService) ensureMappingFileExists() {
	if b.MappingFile == "" {
		return
	}
	dir := filepath.Dir(b.MappingFile)
	_ = os.MkdirAll(dir, 0o755)
	if _, err := os.Stat(b.MappingFile); os.IsNotExist(err) {
		_ = os.WriteFile(b.MappingFile, []byte("{}"), 0o644)
	}
}

// SaveJobMapping saves a batch job mapping (Firestore first, local JSON file as fallback).
func (b *BatchService) SaveJobMapping(batchJobID, userID string, metadata map[string]any) {
	doc := map[string]any{
		"user_id":    userID,
		"metadata":   metadata,
		"created_at": float64(time.Now().UnixNano()) / 1e9,
	}

	if b.Store != nil && b.Store.IsAvailable() {
		b.Store.Save(firestore.SanitizeKey(batchJobID), doc)
		slog.Info("Saved batch job mapping to Firestore", "batch_job_id", batchJobID, "user_id", userID)
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	b.ensureMappingFileExists()

	raw, err := os.ReadFile(b.MappingFile)
	if err != nil {
		slog.Error("Failed to read mapping file", "error", err)
		return
	}
	data := make(map[string]any)
	_ = json.Unmarshal(raw, &data)
	data[batchJobID] = doc

	out, err := json.MarshalIndent(data, "", "  ")
	if err == nil {
		_ = os.WriteFile(b.MappingFile, out, 0o644)
	}
}

// GetJobMapping retrieves a saved batch job mapping (Firestore first, local file as fallback).
func (b *BatchService) GetJobMapping(batchJobID string) map[string]any {
	if b.Store != nil && b.Store.IsAvailable() {
		if m := b.Store.Load(firestore.SanitizeKey(batchJobID)); m != nil {
			return m
		}
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	raw, err := os.ReadFile(b.MappingFile)
	if err != nil {
		return nil
	}
	data := make(map[string]map[string]any)
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil
	}
	return data[batchJobID]
}

// CleanOldJobs deletes job mappings older than maxAgeDays.
func (b *BatchService) CleanOldJobs(maxAgeDays int) {
	if maxAgeDays <= 0 {
		maxAgeDays = 7
	}
	cutoff := float64(time.Now().Unix()) - float64(maxAgeDays*86400)

	if b.Store != nil && b.Store.IsAvailable() {
		for k, doc := range b.Store.LoadAll() {
			ts, _ := toFloat64(doc["created_at"])
			if ts <= cutoff {
				b.Store.Delete(k)
			}
		}
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	raw, err := os.ReadFile(b.MappingFile)
	if err != nil {
		return
	}
	data := make(map[string]map[string]any)
	if err := json.Unmarshal(raw, &data); err != nil {
		return
	}
	cleaned := make(map[string]map[string]any)
	for k, v := range data {
		ts, _ := toFloat64(v["created_at"])
		if ts > cutoff {
			cleaned[k] = v
		}
	}
	if out, err := json.MarshalIndent(cleaned, "", "  "); err == nil {
		_ = os.WriteFile(b.MappingFile, out, 0o644)
	}
}

// GenerateRestaurantAnalysisJSONL builds the JSONL string for Gemini Batch API restaurant analysis.
func (b *BatchService) GenerateRestaurantAnalysisJSONL(userID string, restaurants []ai.RestaurantDetail) string {
	var lines []string
	for idx, rest := range restaurants {
		name := rest.Name
		if name == "" {
			name = "未知餐廳"
		}
		addr := rest.Address
		if addr == "" {
			addr = "未知地址"
		}
		rating := rest.Rating
		if rating == "" {
			rating = "未知評分"
		}
		reviews := rest.Reviews
		if len(reviews) > 15 {
			reviews = reviews[:15]
		}
		var revLines []string
		for _, r := range reviews {
			revLines = append(revLines, "- "+r)
		}
		reviewsText := strings.Join(revLines, "\n")

		prompt := fmt.Sprintf(`請幫我分析以下餐廳的最新用戶評論，並提供深度的美食分析：
餐廳名稱：%s
地址：%s
綜合評分：%s

用戶評論內容：
%s

請用繁體中文回答以下四點（請使用純文字，不要包含任何 markdown 標記如 **、## 或 - 符號）：
1. 餐廳特色與總體口味分析（2句話）
2. 網友高頻推薦的招牌特色菜色與點餐建議
3. 差評或需要注意的雷點/缺點提醒（如排隊太久、服務差、某道菜不好吃）
4. 綜合推薦指數（滿分 5 顆星，給出顆數與簡短理由）
`, name, addr, rating, reviewsText)

		payload := map[string]any{
			"key": fmt.Sprintf("%d:%s", idx, name),
			"request": map[string]any{
				"contents": []map[string]any{
					{
						"parts": []map[string]any{
							{"text": prompt},
						},
					},
				},
				"generation_config": map[string]any{
					"temperature":       0.5,
					"max_output_tokens": 1024,
				},
			},
		}
		lineBytes, err := json.Marshal(payload)
		if err == nil {
			lines = append(lines, string(lineBytes))
		}
	}
	return strings.Join(lines, "\n")
}

func (b *BatchService) uploadJSONL(ctx context.Context, displayName, jsonlContent string) (string, error) {
	base := strings.TrimRight(b.BaseURL, "/")
	uploadURL := fmt.Sprintf("%s/upload/v1beta/files?key=%s", base, b.APIKey)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, strings.NewReader(jsonlContent))
	if err != nil {
		return "", err
	}
	req.Header.Set("X-Goog-Upload-Protocol", "raw")
	req.Header.Set("X-Goog-Upload-Header-Content-Type", "application/jsonl")
	req.Header.Set("Content-Type", "application/jsonl")

	resp, err := b.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("file upload error (%d): %s", resp.StatusCode, string(bodyBytes))
	}

	var parsed struct {
		Name string `json:"name"`
		File struct {
			Name string `json:"name"`
		} `json:"file"`
	}
	if err := json.Unmarshal(bodyBytes, &parsed); err != nil {
		return "", err
	}
	if parsed.File.Name != "" {
		return parsed.File.Name, nil
	}
	if parsed.Name != "" {
		return parsed.Name, nil
	}
	return "", fmt.Errorf("file upload returned empty name")
}

// SubmitRestaurantBatchJob generates JSONL, uploads it to Gemini File API, and creates a Batch Job.
func (b *BatchService) SubmitRestaurantBatchJob(
	ctx context.Context,
	userID string,
	coordinates map[string]float64,
	restaurants []ai.RestaurantDetail,
	webhookDomain string,
) BatchSubmitResult {
	if len(restaurants) == 0 {
		return BatchSubmitResult{Status: "error", ErrorMessage: "沒有提供餐廳資料"}
	}

	jsonl := b.GenerateRestaurantAnalysisJSONL(userID, restaurants)
	nowTs := time.Now().Unix()

	fileName, err := b.uploadJSONL(ctx, fmt.Sprintf("restaurant_batch_%s_%d", userID, nowTs), jsonl)
	if err != nil {
		errMsg := err.Error()
		if len(errMsg) > 100 {
			errMsg = errMsg[:100]
		}
		return BatchSubmitResult{
			Status:       "error",
			ErrorMessage: fmt.Sprintf("建立批次分析工作失敗: %s", errMsg),
		}
	}

	displayName := fmt.Sprintf("FoodieAnalysis_%s_%d", userID, nowTs)
	batchConfig := map[string]any{
		"display_name": displayName,
		"input_config": map[string]any{
			"file_name": fileName,
		},
	}

	if webhookDomain != "" {
		webhookURI := fmt.Sprintf("https://%s/api/gemini-callback/dynamic", webhookDomain)
		batchConfig["webhook_config"] = map[string]any{
			"uris": []string{webhookURI},
			"user_metadata": map[string]any{
				"user_id": userID,
				"lat":     coordinates["latitude"],
				"lng":     coordinates["longitude"],
			},
		}
	}

	createBody, _ := json.Marshal(map[string]any{
		"batch": batchConfig,
	})
	base := strings.TrimRight(b.BaseURL, "/")
	createURL := fmt.Sprintf("%s/v1beta/models/gemini-3.1-flash-lite:batchGenerateContent?key=%s", base, b.APIKey)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, createURL, bytes.NewReader(createBody))
	if err != nil {
		return BatchSubmitResult{Status: "error", ErrorMessage: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.HTTPClient.Do(req)
	if err != nil {
		errMsg := err.Error()
		if len(errMsg) > 100 {
			errMsg = errMsg[:100]
		}
		return BatchSubmitResult{Status: "error", ErrorMessage: fmt.Sprintf("建立批次分析工作失敗: %s", errMsg)}
	}
	defer resp.Body.Close()
	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyStr := string(respBytes)
		if len(bodyStr) > 100 {
			bodyStr = bodyStr[:100]
		}
		return BatchSubmitResult{
			Status:       "error",
			ErrorMessage: fmt.Sprintf("建立批次分析工作失敗: %s", bodyStr),
		}
	}

	var jobResp map[string]any
	_ = json.Unmarshal(respBytes, &jobResp)
	jobName, _ := jobResp["name"].(string)
	state, _ := jobResp["state"].(string)
	if meta, ok := jobResp["metadata"].(map[string]any); ok {
		if st, ok := meta["state"].(string); ok && state == "" {
			state = st
		}
		if dn, ok := meta["displayName"].(string); ok && dn != "" {
			displayName = dn
		}
	}

	var restSummary []map[string]string
	for _, r := range restaurants {
		restSummary = append(restSummary, map[string]string{
			"name":    r.Name,
			"address": r.Address,
		})
	}
	b.SaveJobMapping(jobName, userID, map[string]any{
		"restaurants":   restSummary,
		"coordinates":   coordinates,
		"file_resource": fileName,
	})

	return BatchSubmitResult{
		Status:      "success",
		BatchJobID:  jobName,
		DisplayName: displayName,
		State:       state,
	}
}

// DownloadAndParseBatchResults downloads the output JSONL from Gemini File API and parses each restaurant's analysis.
func (b *BatchService) DownloadAndParseBatchResults(ctx context.Context, outputFileURI string) []BatchAnalysisItem {
	base := strings.TrimRight(b.BaseURL, "/")
	downloadURL := fmt.Sprintf("%s/v1beta/%s:download?alt=media&key=%s", base, strings.TrimLeft(outputFileURI, "/"), b.APIKey)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil
	}
	resp, err := b.HTTPClient.Do(req)
	if err != nil {
		slog.Error("Failed to download batch results", "uri", outputFileURI, "error", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		slog.Error("Batch download returned non-2xx", "status", resp.StatusCode, "uri", outputFileURI)
		return nil
	}

	contentBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}

	var items []BatchAnalysisItem
	for _, line := range strings.Split(strings.TrimSpace(string(contentBytes)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var obj struct {
			Key      string `json:"key"`
			Response struct {
				Candidates []struct {
					Content struct {
						Parts []struct {
							Text string `json:"text"`
						} `json:"parts"`
					} `json:"content"`
				} `json:"candidates"`
			} `json:"response"`
		}
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			continue
		}
		idx := 0
		restName := obj.Key
		if parts := strings.SplitN(obj.Key, ":", 2); len(parts) == 2 {
			if n, err := strconv.Atoi(parts[0]); err == nil {
				idx = n
			}
			restName = parts[1]
		}
		var sb strings.Builder
		if len(obj.Response.Candidates) > 0 {
			for _, p := range obj.Response.Candidates[0].Content.Parts {
				sb.WriteString(p.Text)
			}
		}
		analysis := sb.String()
		if analysis == "" {
			analysis = "（分析解析失敗）"
		}
		items = append(items, BatchAnalysisItem{
			Index:    idx,
			Name:     restName,
			Analysis: analysis,
		})
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].Index < items[j].Index
	})
	return items
}

func parseBatchJobMap(m map[string]any) BatchJobInfo {
	info := BatchJobInfo{}
	info.Name, _ = m["name"].(string)
	info.DisplayName, _ = m["displayName"].(string)
	if info.DisplayName == "" {
		info.DisplayName, _ = m["display_name"].(string)
	}
	info.State, _ = m["state"].(string)
	if dest, ok := m["dest"].(map[string]any); ok {
		info.DestFile, _ = dest["fileName"].(string)
		if info.DestFile == "" {
			info.DestFile, _ = dest["file_name"].(string)
		}
	}
	if meta, ok := m["metadata"].(map[string]any); ok {
		if info.DisplayName == "" {
			info.DisplayName, _ = meta["displayName"].(string)
		}
		if info.State == "" {
			info.State, _ = meta["state"].(string)
		}
		if outCfg, ok := meta["output"].(map[string]any); ok && info.DestFile == "" {
			info.DestFile, _ = outCfg["responsesFile"].(string)
		}
	}
	return info
}

// ListBatchJobs lists batch jobs from Gemini Developer API.
func (b *BatchService) ListBatchJobs(ctx context.Context) ([]BatchJobInfo, error) {
	if b.APIKey == "" {
		return nil, nil
	}
	base := strings.TrimRight(b.BaseURL, "/")
	url := fmt.Sprintf("%s/v1beta/batches?key=%s", base, b.APIKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("list batch jobs status %d", resp.StatusCode)
	}
	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	var list []BatchJobInfo
	for _, field := range []string{"batches", "operations"} {
		if arr, ok := raw[field].([]any); ok {
			for _, item := range arr {
				if m, ok := item.(map[string]any); ok {
					list = append(list, parseBatchJobMap(m))
				}
			}
		}
	}
	return list, nil
}

// GetBatchJob retrieves a single batch job by name.
func (b *BatchService) GetBatchJob(ctx context.Context, name string) (*BatchJobInfo, error) {
	base := strings.TrimRight(b.BaseURL, "/")
	url := fmt.Sprintf("%s/v1beta/%s?key=%s", base, strings.TrimLeft(name, "/"), b.APIKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("get batch job status %d", resp.StatusCode)
	}
	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	info := parseBatchJobMap(raw)
	return &info, nil
}

// DeleteBatchJob deletes a batch job by name.
func (b *BatchService) DeleteBatchJob(ctx context.Context, name string) error {
	base := strings.TrimRight(b.BaseURL, "/")
	url := fmt.Sprintf("%s/v1beta/%s?key=%s", base, strings.TrimLeft(name, "/"), b.APIKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	resp, err := b.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// VerifyStaticWebhook verifies a Standard Webhooks HMAC-SHA256 signature.
func VerifyStaticWebhook(payload string, headers http.Header, secret string) bool {
	webhookID := headers.Get("webhook-id")
	webhookTimestamp := headers.Get("webhook-timestamp")
	webhookSignature := headers.Get("webhook-signature")

	if webhookID == "" || webhookTimestamp == "" || webhookSignature == "" {
		return false
	}

	secretClean := strings.TrimPrefix(secret, "whsec_")
	keyBytes, err := base64.StdEncoding.DecodeString(secretClean)
	if err != nil {
		return false
	}

	toSign := fmt.Sprintf("%s.%s.%s", webhookID, webhookTimestamp, payload)
	mac := hmac.New(sha256.New, keyBytes)
	mac.Write([]byte(toSign))
	computedSig := mac.Sum(nil)

	for _, sigItem := range strings.Split(webhookSignature, " ") {
		parts := strings.SplitN(sigItem, ",", 2)
		if len(parts) != 2 || parts[0] != "v1" {
			continue
		}
		sigBytes, err := base64.StdEncoding.DecodeString(parts[1])
		if err != nil {
			continue
		}
		if hmac.Equal(computedSig, sigBytes) {
			return true
		}
	}
	return false
}

// VerifyDynamicWebhook verifies an RS256 JWT token against Google's JWKS endpoint.
func VerifyDynamicWebhook(ctx context.Context, token, jwksURI string, httpClient *http.Client) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid JWT format")
	}

	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("failed to decode JWT header: %w", err)
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return nil, fmt.Errorf("failed to parse JWT header: %w", err)
	}
	if header.Alg != "RS256" || header.Kid == "" {
		return nil, fmt.Errorf("unsupported alg %q or empty kid", header.Alg)
	}

	if jwksURI == "" {
		jwksURI = DefaultJWKSURI
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jwksURI, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var jwks struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		return nil, err
	}

	var pubKey *rsa.PublicKey
	for _, k := range jwks.Keys {
		if k.Kid == header.Kid && k.Kty == "RSA" {
			nBytes, err1 := base64.RawURLEncoding.DecodeString(k.N)
			eBytes, err2 := base64.RawURLEncoding.DecodeString(k.E)
			if err1 != nil || err2 != nil {
				continue
			}
			eInt := 0
			for _, b := range eBytes {
				eInt = (eInt << 8) | int(b)
			}
			pubKey = &rsa.PublicKey{
				N: new(big.Int).SetBytes(nBytes),
				E: eInt,
			}
			break
		}
	}
	if pubKey == nil {
		return nil, fmt.Errorf("JWK matching kid %q not found", header.Kid)
	}

	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("failed to decode JWT signature: %w", err)
	}
	signingInput := parts[0] + "." + parts[1]
	digest := sha256.Sum256([]byte(signingInput))

	if err := rsa.VerifyPKCS1v15(pubKey, crypto.SHA256, digest[:], sigBytes); err != nil {
		return nil, fmt.Errorf("RSA signature verification failed: %w", err)
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("failed to decode JWT payload: %w", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return nil, err
	}
	return claims, nil
}
