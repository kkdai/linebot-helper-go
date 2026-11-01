package services

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/kkdai/linebot-helper-go/internal/firestore"
)

const SessionCollection = "chat_sessions"

// HistoryMessage represents a single turn in a user's conversation history.
type HistoryMessage struct {
	Role      string         `json:"role"`
	Content   string         `json:"content"`
	Timestamp time.Time      `json:"timestamp"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// SessionData holds conversation state and metadata for a single user.
type SessionData struct {
	UserID     string
	History    []HistoryMessage
	CreatedAt  time.Time
	LastActive time.Time
	Metadata   map[string]any
}

// SessionInfo is a snapshot of a user's session for display (/status).
type SessionInfo struct {
	UserID       string
	HistoryCount int
	CreatedAt    time.Time
	LastActive   time.Time
	IsExpired    bool
	AgeMinutes   float64
}

// SessionStats contains aggregate metrics for /stats.
type SessionStats struct {
	ActiveSessions          int
	TotalMessages           int
	OldestSessionAgeMinutes float64
	CleanupRuns             int
	SessionsCleaned         int
}

// SessionManager manages per-user conversation sessions with TTL and Firestore persistence.
type SessionManager struct {
	Timeout          time.Duration
	MaxHistoryLength int
	CleanupInterval  time.Duration
	Store            firestore.KVStore

	mu              sync.Mutex
	sessions        map[string]*SessionData
	cleanupRuns     int
	sessionsCleaned int
}

// NewSessionManager creates a new SessionManager.
func NewSessionManager(timeoutMinutes, maxHistoryLength, cleanupIntervalSeconds int, store firestore.KVStore) *SessionManager {
	if timeoutMinutes <= 0 {
		timeoutMinutes = 30
	}
	if maxHistoryLength <= 0 {
		maxHistoryLength = 20
	}
	if cleanupIntervalSeconds <= 0 {
		cleanupIntervalSeconds = 300
	}
	return &SessionManager{
		Timeout:          time.Duration(timeoutMinutes) * time.Minute,
		MaxHistoryLength: maxHistoryLength,
		CleanupInterval:  time.Duration(cleanupIntervalSeconds) * time.Second,
		Store:            store,
		sessions:         make(map[string]*SessionData),
	}
}

func (sm *SessionManager) isExpired(s *SessionData) bool {
	return time.Since(s.LastActive) >= sm.Timeout
}

func serializeHistory(history []HistoryMessage) []any {
	out := make([]any, 0, len(history))
	for _, msg := range history {
		m := map[string]any{
			"role":      msg.Role,
			"content":   msg.Content,
			"timestamp": float64(msg.Timestamp.UnixNano()) / 1e9,
		}
		if len(msg.Metadata) > 0 {
			m["metadata"] = msg.Metadata
		}
		out = append(out, m)
	}
	return out
}

func deserializeHistory(raw any) []HistoryMessage {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []HistoryMessage
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role, _ := m["role"].(string)
		content, _ := m["content"].(string)
		ts := time.Now()
		if tsFloat, ok := toFloat64(m["timestamp"]); ok && tsFloat > 0 {
			sec := int64(tsFloat)
			nsec := int64((tsFloat - float64(sec)) * 1e9)
			ts = time.Unix(sec, nsec)
		}
		meta, _ := m["metadata"].(map[string]any)
		out = append(out, HistoryMessage{
			Role:      role,
			Content:   content,
			Timestamp: ts,
			Metadata:  meta,
		})
	}
	return out
}

func toFloat64(v any) (float64, bool) {
	switch val := v.(type) {
	case float64:
		return val, true
	case float32:
		return float64(val), true
	case int:
		return float64(val), true
	case int64:
		return float64(val), true
	default:
		return 0, false
	}
}

func (sm *SessionManager) persistSession(s *SessionData) {
	if sm.Store == nil || !sm.Store.IsAvailable() {
		return
	}
	sm.Store.Save(s.UserID, map[string]any{
		"user_id":     s.UserID,
		"history":     serializeHistory(s.History),
		"created_at":  float64(s.CreatedAt.UnixNano()) / 1e9,
		"last_active": float64(s.LastActive.UnixNano()) / 1e9,
	})
}

func (sm *SessionManager) restoreSessionLocked(userID string) *SessionData {
	if sm.Store == nil || !sm.Store.IsAvailable() {
		return nil
	}
	stored := sm.Store.Load(userID)
	if stored == nil {
		return nil
	}
	lastActiveSec, _ := toFloat64(stored["last_active"])
	lastActive := time.Unix(int64(lastActiveSec), 0)
	if time.Since(lastActive) >= sm.Timeout {
		sm.Store.Delete(userID)
		return nil
	}
	createdSec, ok := toFloat64(stored["created_at"])
	createdAt := time.Now()
	if ok && createdSec > 0 {
		createdAt = time.Unix(int64(createdSec), 0)
	}
	history := deserializeHistory(stored["history"])
	s := &SessionData{
		UserID:     userID,
		History:    history,
		CreatedAt:  createdAt,
		LastActive: time.Now(),
		Metadata:   make(map[string]any),
	}
	sm.sessions[userID] = s
	sm.persistSession(s)
	slog.Info("Restored session from persistent store", "user_id", userID, "messages", len(history))
	return s
}

// GetSession returns the active session for userID or nil if none/expired.
func (sm *SessionManager) GetSession(userID string) *SessionData {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	s, ok := sm.sessions[userID]
	if ok && !sm.isExpired(s) {
		return s
	}
	return nil
}

// GetOrCreateSession returns an existing valid session, restores from Firestore, or creates a new one.
func (sm *SessionManager) GetOrCreateSession(userID string) *SessionData {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if s, ok := sm.sessions[userID]; ok {
		if !sm.isExpired(s) {
			s.LastActive = time.Now()
			return s
		}
		slog.Info("Session expired", "user_id", userID)
	} else {
		if restored := sm.restoreSessionLocked(userID); restored != nil {
			return restored
		}
	}

	now := time.Now()
	s := &SessionData{
		UserID:     userID,
		History:    []HistoryMessage{},
		CreatedAt:  now,
		LastActive: now,
		Metadata:   make(map[string]any),
	}
	sm.sessions[userID] = s
	sm.persistSession(s)
	return s
}

// SetMetadata sets a key-value pair on the user's session metadata.
func (sm *SessionManager) SetMetadata(userID, key string, val any) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	s, ok := sm.sessions[userID]
	if !ok || sm.isExpired(s) {
		return
	}
	if s.Metadata == nil {
		s.Metadata = make(map[string]any)
	}
	s.Metadata[key] = val
}

// GetMetadata retrieves a metadata value from the user's session.
func (sm *SessionManager) GetMetadata(userID, key string) (any, bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	s, ok := sm.sessions[userID]
	if !ok || sm.isExpired(s) || s.Metadata == nil {
		return nil, false
	}
	v, exists := s.Metadata[key]
	return v, exists
}

// AddToHistory appends a message to the user's conversation history.
func (sm *SessionManager) AddToHistory(userID, role, content string) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	s, ok := sm.sessions[userID]
	if !ok {
		return false
	}
	s.History = append(s.History, HistoryMessage{
		Role:      role,
		Content:   content,
		Timestamp: time.Now(),
	})
	if len(s.History) > sm.MaxHistoryLength {
		s.History = s.History[len(s.History)-sm.MaxHistoryLength:]
	}
	s.LastActive = time.Now()
	sm.persistSession(s)
	return true
}

// GetHistory returns a copy of the user's conversation history.
func (sm *SessionManager) GetHistory(userID string) []HistoryMessage {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	s, ok := sm.sessions[userID]
	if !ok || sm.isExpired(s) {
		return nil
	}
	cp := make([]HistoryMessage, len(s.History))
	copy(cp, s.History)
	return cp
}

// ClearSession removes the user's session from memory and persistent store.
func (sm *SessionManager) ClearSession(userID string) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.Store != nil {
		sm.Store.Delete(userID)
	}
	if _, ok := sm.sessions[userID]; ok {
		delete(sm.sessions, userID)
		return true
	}
	return false
}

// GetSessionInfo returns display information about a user's session.
func (sm *SessionManager) GetSessionInfo(userID string) *SessionInfo {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	s, ok := sm.sessions[userID]
	if !ok {
		return nil
	}
	return &SessionInfo{
		UserID:       s.UserID,
		HistoryCount: len(s.History),
		CreatedAt:    s.CreatedAt,
		LastActive:   s.LastActive,
		IsExpired:    sm.isExpired(s),
		AgeMinutes:   time.Since(s.CreatedAt).Minutes(),
	}
}

// CleanupExpiredSessions removes all expired sessions and returns the count removed.
func (sm *SessionManager) CleanupExpiredSessions() int {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	var expired []string
	for uid, s := range sm.sessions {
		if sm.isExpired(s) {
			expired = append(expired, uid)
		}
	}
	for _, uid := range expired {
		delete(sm.sessions, uid)
		if sm.Store != nil {
			sm.Store.Delete(uid)
		}
	}
	sm.sessionsCleaned += len(expired)
	sm.cleanupRuns++
	return len(expired)
}

// GetStats returns aggregate session statistics.
func (sm *SessionManager) GetStats() SessionStats {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	totalMsgs := 0
	var oldest time.Time
	for _, s := range sm.sessions {
		totalMsgs += len(s.History)
		if oldest.IsZero() || s.CreatedAt.Before(oldest) {
			oldest = s.CreatedAt
		}
	}
	oldestAge := 0.0
	if !oldest.IsZero() {
		oldestAge = time.Since(oldest).Minutes()
	}
	return SessionStats{
		ActiveSessions:          len(sm.sessions),
		TotalMessages:           totalMsgs,
		OldestSessionAgeMinutes: oldestAge,
		CleanupRuns:             sm.cleanupRuns,
		SessionsCleaned:         sm.sessionsCleaned,
	}
}

// StartCleanupTask runs periodic session cleanup in a background goroutine until ctx is cancelled.
func (sm *SessionManager) StartCleanupTask(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(sm.CleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cleaned := sm.CleanupExpiredSessions()
				if cleaned > 0 {
					slog.Info("Cleaned expired sessions", "count", cleaned)
				}
			}
		}
	}()
}
