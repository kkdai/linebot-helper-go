package firestore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const DefaultFirestoreBaseURL = "https://firestore.googleapis.com/v1"

// KVStore defines the key-value persistence interface used by SessionManager,
// BookmarkService, ReportStore, and BatchService.
type KVStore interface {
	IsAvailable() bool
	Save(key string, doc map[string]any)
	Load(key string) map[string]any
	Delete(key string)
	LoadAll() map[string]map[string]any
}

// SanitizeKey replaces '/' with '__' because Firestore document IDs cannot contain '/'.
func SanitizeKey(key string) string {
	return strings.ReplaceAll(key, "/", "__")
}

// FirestoreKVStore implements KVStore backed by Google Cloud Firestore REST v1 API.
// If credentials are unavailable (e.g., local dev or CI), IsAvailable() returns false
// and all operations become safe no-ops.
type FirestoreKVStore struct {
	ProjectID      string
	CollectionName string
	BaseURL        string
	HTTPClient     *http.Client
	TokenSource    oauth2.TokenSource
	available      bool
}

// NewFirestoreKVStore creates a FirestoreKVStore for the given project and collection.
func NewFirestoreKVStore(projectID, collection string) *FirestoreKVStore {
	store := &FirestoreKVStore{
		ProjectID:      projectID,
		CollectionName: collection,
		BaseURL:        DefaultFirestoreBaseURL,
		HTTPClient:     &http.Client{Timeout: 15 * time.Second},
	}
	if projectID == "" {
		slog.Warn("Firestore unavailable (empty projectID)", "collection", collection)
		return store
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	creds, err := google.FindDefaultCredentials(ctx, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		slog.Warn("Firestore unavailable, persistence disabled", "collection", collection, "error", err)
		return store
	}
	if _, err := creds.TokenSource.Token(); err != nil {
		slog.Warn("Firestore token unavailable, persistence disabled", "collection", collection, "error", err)
		return store
	}
	store.TokenSource = creds.TokenSource
	store.available = true
	slog.Info("FirestoreKVStore ready", "collection", collection)
	return store
}

// NewFirestoreKVStoreWithTokenSource creates a FirestoreKVStore with an explicit TokenSource (for testing).
func NewFirestoreKVStoreWithTokenSource(projectID, collection, baseURL string, ts oauth2.TokenSource) *FirestoreKVStore {
	if baseURL == "" {
		baseURL = DefaultFirestoreBaseURL
	}
	return &FirestoreKVStore{
		ProjectID:      projectID,
		CollectionName: collection,
		BaseURL:        strings.TrimRight(baseURL, "/"),
		HTTPClient:     &http.Client{Timeout: 15 * time.Second},
		TokenSource:    ts,
		available:      ts != nil && projectID != "",
	}
}

func (s *FirestoreKVStore) IsAvailable() bool {
	return s != nil && s.available
}

func (s *FirestoreKVStore) collectionURL() string {
	return fmt.Sprintf("%s/projects/%s/databases/(default)/documents/%s",
		strings.TrimRight(s.BaseURL, "/"), s.ProjectID, s.CollectionName)
}

func (s *FirestoreKVStore) docURL(key string) string {
	return fmt.Sprintf("%s/%s", s.collectionURL(), SanitizeKey(key))
}

func (s *FirestoreKVStore) authHeader() (string, error) {
	if s.TokenSource == nil {
		return "", fmt.Errorf("no token source")
	}
	tok, err := s.TokenSource.Token()
	if err != nil {
		return "", err
	}
	return "Bearer " + tok.AccessToken, nil
}

func (s *FirestoreKVStore) Save(key string, doc map[string]any) {
	if !s.IsAvailable() {
		return
	}
	auth, err := s.authHeader()
	if err != nil {
		slog.Error("Firestore auth failed on save", "collection", s.CollectionName, "key", key, "error", err)
		return
	}
	payload := map[string]any{
		"fields": encodeMapFields(doc),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		slog.Error("Firestore marshal failed", "collection", s.CollectionName, "key", key, "error", err)
		return
	}
	req, err := http.NewRequest(http.MethodPatch, s.docURL(key), bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.HTTPClient.Do(req)
	if err != nil {
		slog.Error("Firestore save failed", "collection", s.CollectionName, "key", key, "error", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		slog.Error("Firestore save status error", "collection", s.CollectionName, "key", key, "status", resp.StatusCode, "body", string(b))
	}
}

func (s *FirestoreKVStore) Load(key string) map[string]any {
	if !s.IsAvailable() {
		return nil
	}
	auth, err := s.authHeader()
	if err != nil {
		slog.Error("Firestore auth failed on load", "collection", s.CollectionName, "key", key, "error", err)
		return nil
	}
	req, err := http.NewRequest(http.MethodGet, s.docURL(key), nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", auth)

	resp, err := s.HTTPClient.Do(req)
	if err != nil {
		slog.Error("Firestore load failed", "collection", s.CollectionName, "key", key, "error", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		slog.Error("Firestore load status error", "collection", s.CollectionName, "key", key, "status", resp.StatusCode, "body", string(b))
		return nil
	}
	var fsDoc firestoreDocument
	if err := json.NewDecoder(resp.Body).Decode(&fsDoc); err != nil {
		slog.Error("Firestore decode failed", "collection", s.CollectionName, "key", key, "error", err)
		return nil
	}
	return decodeMapFields(fsDoc.Fields)
}

func (s *FirestoreKVStore) Delete(key string) {
	if !s.IsAvailable() {
		return
	}
	auth, err := s.authHeader()
	if err != nil {
		return
	}
	req, err := http.NewRequest(http.MethodDelete, s.docURL(key), nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", auth)
	resp, err := s.HTTPClient.Do(req)
	if err != nil {
		slog.Error("Firestore delete failed", "collection", s.CollectionName, "key", key, "error", err)
		return
	}
	defer resp.Body.Close()
}

func (s *FirestoreKVStore) LoadAll() map[string]map[string]any {
	result := make(map[string]map[string]any)
	if !s.IsAvailable() {
		return result
	}
	auth, err := s.authHeader()
	if err != nil {
		return result
	}

	pageToken := ""
	for {
		u := s.collectionURL() + "?pageSize=300"
		if pageToken != "" {
			u += "&pageToken=" + pageToken
		}
		req, err := http.NewRequest(http.MethodGet, u, nil)
		if err != nil {
			break
		}
		req.Header.Set("Authorization", auth)
		resp, err := s.HTTPClient.Do(req)
		if err != nil {
			slog.Error("Firestore load_all failed", "collection", s.CollectionName, "error", err)
			break
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			resp.Body.Close()
			break
		}
		var listResp struct {
			Documents     []firestoreDocument `json:"documents"`
			NextPageToken string              `json:"nextPageToken"`
		}
		err = json.NewDecoder(resp.Body).Decode(&listResp)
		resp.Body.Close()
		if err != nil {
			break
		}
		for _, doc := range listResp.Documents {
			parts := strings.Split(doc.Name, "/")
			docID := parts[len(parts)-1]
			result[docID] = decodeMapFields(doc.Fields)
		}
		if listResp.NextPageToken == "" {
			break
		}
		pageToken = listResp.NextPageToken
	}
	return result
}

type firestoreDocument struct {
	Name   string                    `json:"name"`
	Fields map[string]firestoreValue `json:"fields"`
}

type firestoreValue struct {
	NullValue    *string               `json:"nullValue,omitempty"`
	BooleanValue *bool                 `json:"booleanValue,omitempty"`
	IntegerValue *string               `json:"integerValue,omitempty"`
	DoubleValue  *float64              `json:"doubleValue,omitempty"`
	StringValue  *string               `json:"stringValue,omitempty"`
	ArrayValue   *firestoreArrayValue  `json:"arrayValue,omitempty"`
	MapValue     *firestoreMapValue    `json:"mapValue,omitempty"`
}

type firestoreArrayValue struct {
	Values []firestoreValue `json:"values,omitempty"`
}

type firestoreMapValue struct {
	Fields map[string]firestoreValue `json:"fields,omitempty"`
}

func encodeMapFields(m map[string]any) map[string]firestoreValue {
	out := make(map[string]firestoreValue, len(m))
	for k, v := range m {
		out[k] = encodeValue(v)
	}
	return out
}

func encodeValue(v any) firestoreValue {
	if v == nil {
		nullStr := "NULL_VALUE"
		return firestoreValue{NullValue: &nullStr}
	}
	switch val := v.(type) {
	case string:
		return firestoreValue{StringValue: &val}
	case bool:
		return firestoreValue{BooleanValue: &val}
	case int:
		s := strconv.Itoa(val)
		return firestoreValue{IntegerValue: &s}
	case int64:
		s := strconv.FormatInt(val, 10)
		return firestoreValue{IntegerValue: &s}
	case float64:
		return firestoreValue{DoubleValue: &val}
	case float32:
		f := float64(val)
		return firestoreValue{DoubleValue: &f}
	case []any:
		arr := make([]firestoreValue, 0, len(val))
		for _, item := range val {
			arr = append(arr, encodeValue(item))
		}
		return firestoreValue{ArrayValue: &firestoreArrayValue{Values: arr}}
	case []string:
		arr := make([]firestoreValue, 0, len(val))
		for _, item := range val {
			arr = append(arr, encodeValue(item))
		}
		return firestoreValue{ArrayValue: &firestoreArrayValue{Values: arr}}
	case []map[string]any:
		arr := make([]firestoreValue, 0, len(val))
		for _, item := range val {
			arr = append(arr, encodeValue(item))
		}
		return firestoreValue{ArrayValue: &firestoreArrayValue{Values: arr}}
	case map[string]any:
		return firestoreValue{MapValue: &firestoreMapValue{Fields: encodeMapFields(val)}}
	default:
		// Fallback via JSON round-trip
		b, err := json.Marshal(v)
		if err == nil {
			var generic any
			if json.Unmarshal(b, &generic) == nil {
				return encodeValue(generic)
			}
		}
		s := fmt.Sprintf("%v", v)
		return firestoreValue{StringValue: &s}
	}
}

func decodeMapFields(fields map[string]firestoreValue) map[string]any {
	out := make(map[string]any, len(fields))
	for k, v := range fields {
		out[k] = decodeValue(v)
	}
	return out
}

func decodeValue(v firestoreValue) any {
	if v.StringValue != nil {
		return *v.StringValue
	}
	if v.BooleanValue != nil {
		return *v.BooleanValue
	}
	if v.DoubleValue != nil {
		return *v.DoubleValue
	}
	if v.IntegerValue != nil {
		if i, err := strconv.ParseInt(*v.IntegerValue, 10, 64); err == nil {
			return float64(i)
		}
		return float64(0)
	}
	if v.ArrayValue != nil {
		out := make([]any, 0, len(v.ArrayValue.Values))
		for _, item := range v.ArrayValue.Values {
			out = append(out, decodeValue(item))
		}
		return out
	}
	if v.MapValue != nil {
		return decodeMapFields(v.MapValue.Fields)
	}
	return nil
}

// MemoryKVStore is a thread-safe in-memory KVStore for unit tests and local fallback.
type MemoryKVStore struct {
	Available bool
	mu        sync.RWMutex
	data      map[string]map[string]any
}

func NewMemoryKVStore(available bool) *MemoryKVStore {
	return &MemoryKVStore{
		Available: available,
		data:      make(map[string]map[string]any),
	}
}

func (m *MemoryKVStore) IsAvailable() bool {
	return m != nil && m.Available
}

func (m *MemoryKVStore) Save(key string, doc map[string]any) {
	if !m.IsAvailable() {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b, _ := json.Marshal(doc)
	var cloned map[string]any
	_ = json.Unmarshal(b, &cloned)
	m.data[SanitizeKey(key)] = cloned
}

func (m *MemoryKVStore) Load(key string) map[string]any {
	if !m.IsAvailable() {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	doc, ok := m.data[SanitizeKey(key)]
	if !ok {
		return nil
	}
	b, _ := json.Marshal(doc)
	var cloned map[string]any
	_ = json.Unmarshal(b, &cloned)
	return cloned
}

func (m *MemoryKVStore) Delete(key string) {
	if !m.IsAvailable() {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, SanitizeKey(key))
}

func (m *MemoryKVStore) LoadAll() map[string]map[string]any {
	out := make(map[string]map[string]any)
	if !m.IsAvailable() {
		return out
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for k, v := range m.data {
		b, _ := json.Marshal(v)
		var cloned map[string]any
		_ = json.Unmarshal(b, &cloned)
		out[k] = cloned
	}
	return out
}
