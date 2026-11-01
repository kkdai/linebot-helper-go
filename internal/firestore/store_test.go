package firestore

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/oauth2"
)

type fakeTS struct{}

func (fakeTS) Token() (*oauth2.Token, error) {
	return &oauth2.Token{AccessToken: "fake-token"}, nil
}

func TestSanitizeKey(t *testing.T) {
	if got := SanitizeKey("batches/abc/123"); got != "batches__abc__123" {
		t.Errorf("expected batches__abc__123, got %s", got)
	}
}

func TestFirestoreKVStore_CRUD(t *testing.T) {
	docs := make(map[string]firestoreDocument)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Path, "/")
		last := parts[len(parts)-1]

		switch r.Method {
		case http.MethodPatch:
			var doc firestoreDocument
			_ = json.NewDecoder(r.Body).Decode(&doc)
			doc.Name = r.URL.Path
			docs[last] = doc
			_ = json.NewEncoder(w).Encode(doc)
		case http.MethodGet:
			if last == "test_col" {
				var list []firestoreDocument
				for _, d := range docs {
					list = append(list, d)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"documents": list})
				return
			}
			d, ok := docs[last]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(d)
		case http.MethodDelete:
			delete(docs, last)
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	store := NewFirestoreKVStoreWithTokenSource("test-proj", "test_col", srv.URL, fakeTS{})
	if !store.IsAvailable() {
		t.Fatal("expected store to be available")
	}

	store.Save("batches/job1", map[string]any{
		"user_id":    "U123",
		"saved":      true,
		"created_at": 1700000000.5,
		"tags":       []any{"a", "b"},
		"nested":     map[string]any{"k": "v"},
	})

	loaded := store.Load("batches/job1")
	if loaded == nil {
		t.Fatal("expected non-nil loaded doc")
	}
	if loaded["user_id"] != "U123" || loaded["saved"] != true {
		t.Errorf("unexpected loaded doc: %+v", loaded)
	}

	all := store.LoadAll()
	if len(all) != 1 || all["batches__job1"]["user_id"] != "U123" {
		t.Errorf("unexpected LoadAll: %+v", all)
	}

	store.Delete("batches/job1")
	if store.Load("batches/job1") != nil {
		t.Error("expected nil after delete")
	}
}
