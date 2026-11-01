package services

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/kkdai/linebot-helper-go/internal/firestore"
)

const (
	BookmarksCollection = "bookmarks"
	DocIDLength         = 20
	StaleCandidateDays  = 7
)

// BookmarkService manages read-later bookmarks backed by Firestore.
type BookmarkService struct {
	Store firestore.KVStore
}

// NewBookmarkService creates a new BookmarkService.
func NewBookmarkService(store firestore.KVStore) *BookmarkService {
	return &BookmarkService{Store: store}
}

// Available reports whether the underlying persistent store is available.
func (bs *BookmarkService) Available() bool {
	return bs != nil && bs.Store != nil && bs.Store.IsAvailable()
}

// MakeDocID generates a deterministic 20-char hex ID from sha1(user_id + ":" + url).
func MakeDocID(userID, url string) string {
	h := sha1.Sum([]byte(fmt.Sprintf("%s:%s", userID, url)))
	return hex.EncodeToString(h[:])[:DocIDLength]
}

func (bs *BookmarkService) write(userID, url, title, summary string, saved bool, source string) string {
	docID := MakeDocID(userID, url)
	bs.Store.Save(docID, map[string]any{
		"user_id":    userID,
		"url":        url,
		"title":      title,
		"summary":    summary,
		"created_at": float64(time.Now().UnixNano()) / 1e9,
		"saved":      saved,
		"source":     source,
	})
	return docID
}

// SaveDirect saves a bookmark immediately with saved=true (/save command).
func (bs *BookmarkService) SaveDirect(userID, url, title, summary string) string {
	return bs.write(userID, url, title, summary, true, "command")
}

// SaveCandidate pre-writes a bookmark candidate with saved=false when generating social posts.
func (bs *BookmarkService) SaveCandidate(userID, url, title, summary string) string {
	bs.CleanupStaleCandidates(userID, StaleCandidateDays)
	return bs.write(userID, url, title, summary, false, "button")
}

// ConfirmSave flips a candidate to saved=true after verifying ownership.
func (bs *BookmarkService) ConfirmSave(userID, docID string) map[string]any {
	doc := bs.Store.Load(docID)
	if doc == nil {
		return nil
	}
	if uid, _ := doc["user_id"].(string); uid != userID {
		return nil
	}
	doc["saved"] = true
	bs.Store.Save(docID, doc)
	return doc
}

func (bs *BookmarkService) userBookmarks(userID string, savedOnly bool) []map[string]any {
	var items []map[string]any
	for docID, doc := range bs.Store.LoadAll() {
		if uid, _ := doc["user_id"].(string); uid != userID {
			continue
		}
		saved, _ := doc["saved"].(bool)
		if savedOnly && !saved {
			continue
		}
		doc["doc_id"] = docID
		items = append(items, doc)
	}
	sort.Slice(items, func(i, j int) bool {
		ci, _ := toFloat64(items[i]["created_at"])
		cj, _ := toFloat64(items[j]["created_at"])
		return ci > cj
	})
	return items
}

// ListSaved returns up to limit saved bookmarks for userID sorted newest first.
func (bs *BookmarkService) ListSaved(userID string, limit int) []map[string]any {
	if limit <= 0 {
		limit = 10
	}
	items := bs.userBookmarks(userID, true)
	if len(items) > limit {
		return items[:limit]
	}
	return items
}

// Search performs case-insensitive substring search across title and summary.
func (bs *BookmarkService) Search(userID, keyword string, limit int) []map[string]any {
	if limit <= 0 {
		limit = 10
	}
	needle := strings.ToLower(strings.TrimSpace(keyword))
	if needle == "" {
		return nil
	}
	var out []map[string]any
	for _, doc := range bs.userBookmarks(userID, true) {
		title, _ := doc["title"].(string)
		summary, _ := doc["summary"].(string)
		if strings.Contains(strings.ToLower(title), needle) || strings.Contains(strings.ToLower(summary), needle) {
			out = append(out, doc)
			if len(out) >= limit {
				break
			}
		}
	}
	return out
}

// GetBookmark loads a single bookmark (including candidate) and verifies ownership.
func (bs *BookmarkService) GetBookmark(userID, docID string) map[string]any {
	doc := bs.Store.Load(docID)
	if doc == nil {
		return nil
	}
	if uid, _ := doc["user_id"].(string); uid != userID {
		return nil
	}
	doc["doc_id"] = docID
	return doc
}

// RecordReport records the latest research report ID and timestamp on the bookmark doc.
func (bs *BookmarkService) RecordReport(docID, reportID string) {
	doc := bs.Store.Load(docID)
	if doc == nil {
		return
	}
	doc["report_id"] = reportID
	doc["report_generated_at"] = float64(time.Now().UnixNano()) / 1e9
	bs.Store.Save(docID, doc)
}

// Delete removes a bookmark after verifying ownership.
func (bs *BookmarkService) Delete(userID, docID string) bool {
	doc := bs.Store.Load(docID)
	if doc == nil {
		return false
	}
	if uid, _ := doc["user_id"].(string); uid != userID {
		return false
	}
	bs.Store.Delete(docID)
	return true
}

// CleanupStaleCandidates deletes unconfirmed candidates older than maxAgeDays.
func (bs *BookmarkService) CleanupStaleCandidates(userID string, maxAgeDays int) int {
	if maxAgeDays <= 0 {
		maxAgeDays = StaleCandidateDays
	}
	cutoff := float64(time.Now().Unix()) - float64(maxAgeDays*86400)
	removed := 0
	for _, doc := range bs.userBookmarks(userID, false) {
		saved, _ := doc["saved"].(bool)
		createdAt, _ := toFloat64(doc["created_at"])
		if !saved && createdAt <= cutoff {
			if docID, ok := doc["doc_id"].(string); ok {
				bs.Store.Delete(docID)
				removed++
			}
		}
	}
	if removed > 0 {
		slog.Info("Cleaned stale bookmark candidates", "user_id", userID, "removed", removed)
	}
	return removed
}
