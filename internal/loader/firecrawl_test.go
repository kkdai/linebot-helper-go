package loader

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLoadHTMLWithFirecrawlV2(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/scrape" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("unexpected auth header %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if f, _ := body["formats"].([]any); len(f) != 1 || f[0] != "markdown" {
			t.Errorf("unexpected formats %v", body["formats"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":{"markdown":"# hello","metadata":{"statusCode":200}}}`))
	}))
	defer srv.Close()

	got, err := LoadHTMLWithFirecrawl(context.Background(), "https://example.com", "test-key", srv.URL, true, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if got != "# hello" {
		t.Errorf("got %q", got)
	}
}

func TestLoadHTMLWithFirecrawlV2Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":false,"error":"blocked"}`))
	}))
	defer srv.Close()

	if _, err := LoadHTMLWithFirecrawl(context.Background(), "https://example.com", "k", srv.URL, true, srv.Client()); err == nil {
		t.Fatal("expected error")
	}
}
