package services

import (
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/kkdai/linebot-helper-go/internal/firestore"
)

const ReportsCollection = "reports"

// ReportStore persists generated HTML research reports in Firestore.
type ReportStore struct {
	Store firestore.KVStore
}

// NewReportStore creates a new ReportStore.
func NewReportStore(store firestore.KVStore) *ReportStore {
	return &ReportStore{Store: store}
}

// Put saves an HTML report and returns its hex UUID report ID.
func (rs *ReportStore) Put(html string) string {
	reportID := strings.ReplaceAll(uuid.New().String(), "-", "")
	if rs.Store != nil {
		rs.Store.Save(reportID, map[string]any{
			"html":       html,
			"created_at": float64(time.Now().UnixNano()) / 1e9,
		})
	}
	return reportID
}

// Get retrieves the HTML report by reportID, or returns ("", false) if not found.
func (rs *ReportStore) Get(reportID string) (string, bool) {
	if rs.Store == nil {
		return "", false
	}
	doc := rs.Store.Load(reportID)
	if doc == nil {
		return "", false
	}
	html, ok := doc["html"].(string)
	return html, ok && html != ""
}
