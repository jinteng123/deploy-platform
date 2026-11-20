package service

import (
	"sync"
	"time"
)

// AuditEntry records one sensitive operation. Detail must already be
// sanitized by the caller — this layer never sees Secret data, tokens,
// kubeconfigs or full manifests by contract, only bounded metadata.
type AuditEntry struct {
	ID        int64     `json:"id"`
	Username  string    `json:"username"`
	Action    string    `json:"action"`
	ClusterID int       `json:"cluster_id"`
	Namespace string    `json:"namespace"`
	GVR       string    `json:"gvr"`
	Name      string    `json:"name"`
	Detail    string    `json:"detail"`
	Result    string    `json:"result"` // success | failed | denied | conflict
	CreatedAt time.Time `json:"created_at"`
}

type AuditRepository interface {
	Audits(limit int) ([]AuditEntry, error)
}

const (
	AuditSuccess  = "success"
	AuditFailed   = "failed"
	AuditDenied   = "denied"
	AuditConflict = "conflict"
)

// auditCap bounds the in-memory ring; the DB keeps full history.
const auditCap = 2000

type auditState struct {
	mu      sync.RWMutex
	nextID  int64
	entries []AuditEntry
}

// AddAudit appends an entry to the memory ring and persists it. A
// persistence error is returned but the memory copy is still recorded —
// an audit that only exists in RAM is better than none.
func (s *Store) AddAudit(e AuditEntry) error {
	s.audit.mu.Lock()
	defer s.audit.mu.Unlock()
	var perr error
	if s.persistence != nil {
		perr = s.persistence.SaveAudit(e)
	}
	if e.ID == 0 {
		e.ID = s.audit.nextID
		s.audit.nextID++
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now()
	}
	s.audit.entries = append(s.audit.entries, e)
	if len(s.audit.entries) > auditCap {
		s.audit.entries = s.audit.entries[len(s.audit.entries)-auditCap:]
	}
	return perr
}

// Audits returns recent entries, newest first.
func (s *Store) Audits(limit int) []AuditEntry {
	s.audit.mu.RLock()
	defer s.audit.mu.RUnlock()
	if limit <= 0 || limit > len(s.audit.entries) {
		limit = len(s.audit.entries)
	}
	out := make([]AuditEntry, 0, limit)
	for i := len(s.audit.entries) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, s.audit.entries[i])
	}
	return out
}

func (s *Store) RecentAudits(limit int) ([]AuditEntry, error) {
	if repository, ok := s.persistence.(AuditRepository); ok {
		entries, err := repository.Audits(limit)
		if err != nil {
			return nil, err
		}
		return append([]AuditEntry{}, entries...), nil
	}
	return s.Audits(limit), nil
}

// LoadAudits hydrates the ring from persistence at startup.
func (s *Store) LoadAudits(entries []AuditEntry) {
	s.audit.mu.Lock()
	defer s.audit.mu.Unlock()
	s.audit.entries = append([]AuditEntry(nil), entries...)
	s.audit.nextID = 1
	for _, e := range entries {
		if e.ID >= s.audit.nextID {
			s.audit.nextID = e.ID + 1
		}
	}
}
