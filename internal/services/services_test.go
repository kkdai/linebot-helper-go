package services

import (
	"strings"
	"testing"

	"github.com/kkdai/linebot-helper-go/internal/firestore"
	"github.com/kkdai/linebot-helper-go/internal/gemini"
)

func TestSessionManager_PersistAndRestore(t *testing.T) {
	store := firestore.NewMemoryKVStore(true)
	sm1 := NewSessionManager(30, 20, 300, store)

	sm1.GetOrCreateSession("U1")
	sm1.AddToHistory("U1", "user", "hello")
	sm1.AddToHistory("U1", "assistant", "hi there")

	// Simulate Cloud Run instance restart with a new SessionManager sharing the same store
	sm2 := NewSessionManager(30, 20, 300, store)
	s := sm2.GetOrCreateSession("U1")
	if len(s.History) != 2 {
		t.Fatalf("expected 2 restored messages, got %d", len(s.History))
	}
	if s.History[0].Content != "hello" || s.History[1].Content != "hi there" {
		t.Errorf("unexpected restored history: %+v", s.History)
	}

	if !sm2.ClearSession("U1") {
		t.Error("expected ClearSession to return true")
	}
	if store.Load("U1") != nil {
		t.Error("expected store entry to be deleted after ClearSession")
	}
}

func TestBookmarkService_Flow(t *testing.T) {
	store := firestore.NewMemoryKVStore(true)
	bs := NewBookmarkService(store)

	candID := bs.SaveCandidate("U1", "https://example.com/a", "文章 A", "摘要 A")
	if len(candID) != 20 {
		t.Errorf("expected 20-char doc_id, got %s", candID)
	}
	if len(bs.ListSaved("U1", 10)) != 0 {
		t.Error("candidate should not appear in ListSaved before confirmation")
	}

	if bs.ConfirmSave("U2", candID) != nil {
		t.Error("wrong user should not be able to confirm save")
	}
	confirmed := bs.ConfirmSave("U1", candID)
	if confirmed == nil {
		t.Fatal("expected ConfirmSave to succeed for owner")
	}

	saved := bs.ListSaved("U1", 10)
	if len(saved) != 1 || saved[0]["title"] != "文章 A" {
		t.Errorf("unexpected ListSaved: %+v", saved)
	}

	hits := bs.Search("U1", "摘要", 10)
	if len(hits) != 1 {
		t.Errorf("expected 1 search hit, got %d", len(hits))
	}

	bs.RecordReport(candID, "rep123")
	doc := bs.GetBookmark("U1", candID)
	if doc["report_id"] != "rep123" {
		t.Errorf("expected report_id rep123, got %v", doc["report_id"])
	}

	if !bs.Delete("U1", candID) {
		t.Error("expected Delete to return true")
	}
	if len(bs.ListSaved("U1", 10)) != 0 {
		t.Error("expected 0 bookmarks after delete")
	}
}

func TestReportStoreAndPage(t *testing.T) {
	store := firestore.NewMemoryKVStore(true)
	rs := NewReportStore(store)

	html := RenderReportPage("測試標題", "## 執行摘要\n\n內容段落", "https://example.com", []gemini.GroundingSource{
		{Title: "Source 1", URI: "https://source.example.com"},
	})
	if !strings.Contains(html, "<h2>執行摘要</h2>") || !strings.Contains(html, "https://source.example.com") {
		t.Errorf("unexpected rendered HTML: %s", html)
	}

	id := rs.Put(html)
	got, ok := rs.Get(id)
	if !ok || got != html {
		t.Errorf("unexpected report from store: ok=%v", ok)
	}

	expired := RenderExpiredPage()
	if !strings.Contains(expired, "找不到這份報告") {
		t.Errorf("unexpected expired page: %s", expired)
	}
}
