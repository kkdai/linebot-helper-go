package services

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kkdai/linebot-helper-go/internal/ai"
	"github.com/kkdai/linebot-helper-go/internal/firestore"
)

func TestBatchServiceSubmitAndParse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/upload/v1beta/files"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"file": map[string]any{"name": "files/f-123"},
			})
		case strings.Contains(r.URL.Path, ":batchGenerateContent"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name":  "batches/b-999",
				"state": "JOB_STATE_PENDING",
			})
		case strings.Contains(r.URL.Path, ":download"):
			line1 := `{"key":"1:餐廳B","response":{"candidates":[{"content":{"parts":[{"text":"B 分析結果"}]}}]}}`
			line0 := `{"key":"0:餐廳A","response":{"candidates":[{"content":{"parts":[{"text":"A 分析結果"}]}}]}}`
			_, _ = w.Write([]byte(line1 + "\n" + line0 + "\n"))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/v1beta/batches"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"batches": []map[string]any{
					{
						"name":        "batches/b-999",
						"displayName": "FoodieAnalysis_u1_1000",
						"state":       "JOB_STATE_SUCCEEDED",
						"dest":        map[string]any{"fileName": "files/out-999"},
					},
				},
			})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	tmpFile := filepath.Join(t.TempDir(), "batch_jobs.json")
	store := firestore.NewMemoryKVStore(true)
	bs := NewBatchService("test-key", store, tmpFile)
	bs.BaseURL = srv.URL

	restaurants := []ai.RestaurantDetail{
		{Name: "餐廳A", Address: "地址A", Rating: "4.5", Reviews: []string{"好吃"}},
		{Name: "餐廳B", Address: "地址B", Rating: "4.2", Reviews: []string{"普通"}},
	}

	res := bs.SubmitRestaurantBatchJob(context.Background(), "u1", map[string]float64{"latitude": 25.0, "longitude": 121.5}, restaurants, "example.run.app")
	if res.Status != "success" || res.BatchJobID != "batches/b-999" {
		t.Fatalf("unexpected submit result: %+v", res)
	}

	mapping := bs.GetJobMapping("batches/b-999")
	if mapping == nil || mapping["user_id"] != "u1" {
		t.Fatalf("expected job mapping for batches/b-999, got %+v", mapping)
	}

	items := bs.DownloadAndParseBatchResults(context.Background(), "files/out-999")
	if len(items) != 2 || items[0].Name != "餐廳A" || items[1].Name != "餐廳B" {
		t.Fatalf("unexpected parsed batch results: %+v", items)
	}

	jobs, err := bs.ListBatchJobs(context.Background())
	if err != nil || len(jobs) != 1 || jobs[0].DestFile != "files/out-999" {
		t.Fatalf("unexpected ListBatchJobs: %+v, err=%v", jobs, err)
	}
}

func TestVerifyStaticWebhook(t *testing.T) {
	rawKey := []byte("0123456789abcdef0123456789abcdef")
	secret := "whsec_" + base64.StdEncoding.EncodeToString(rawKey)
	payload := `{"type":"batch.succeeded","data":{"id":"batches/123"}}`
	msgID := "msg_1"
	ts := "1700000000"

	mac := hmac.New(sha256.New, rawKey)
	mac.Write([]byte(msgID + "." + ts + "." + payload))
	sig := "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))

	headers := http.Header{}
	headers.Set("Webhook-Id", msgID)
	headers.Set("Webhook-Timestamp", ts)
	headers.Set("Webhook-Signature", sig)

	if !VerifyStaticWebhook(payload, headers, secret) {
		t.Errorf("expected VerifyStaticWebhook to succeed")
	}

	headers.Set("Webhook-Signature", "v1,aW52YWxpZA==")
	if VerifyStaticWebhook(payload, headers, secret) {
		t.Errorf("expected VerifyStaticWebhook to fail with invalid signature")
	}
}

func TestVerifyDynamicWebhook(t *testing.T) {
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate RSA key: %v", err)
	}
	pubKey := &privKey.PublicKey
	kid := "test-kid-1"

	jwksSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nStr := base64.RawURLEncoding.EncodeToString(pubKey.N.Bytes())
		eBytes := big.NewInt(int64(pubKey.E)).Bytes()
		eStr := base64.RawURLEncoding.EncodeToString(eBytes)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]any{
				{
					"kid": kid,
					"kty": "RSA",
					"alg": "RS256",
					"n":   nStr,
					"e":   eStr,
				},
			},
		})
	}))
	defer jwksSrv.Close()

	headerJSON, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": kid})
	payloadJSON, _ := json.Marshal(map[string]any{
		"type": "batch.succeeded",
		"data": map[string]any{
			"id":              "batches/777",
			"output_file_uri": "files/out-777",
		},
	})
	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." + base64.RawURLEncoding.EncodeToString(payloadJSON)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, privKey, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("failed to sign JWT: %v", err)
	}
	jwtToken := signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)

	claims, err := VerifyDynamicWebhook(context.Background(), jwtToken, jwksSrv.URL, jwksSrv.Client())
	if err != nil {
		t.Fatalf("VerifyDynamicWebhook failed: %v", err)
	}
	if claims["type"] != "batch.succeeded" {
		t.Errorf("unexpected claims: %+v", claims)
	}
}
