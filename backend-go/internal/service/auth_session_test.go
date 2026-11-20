package service

import (
	"errors"
	"strings"
	"testing"
	"time"
)

type testSessionRepository struct {
	Persistence
	sessions map[string]Session
	users    []User
	keys     []APIKey
	audits   []AuditEntry
	err      error
}

func (r *testSessionRepository) SaveSession(session Session) error {
	if r.err != nil {
		return r.err
	}
	if r.sessions == nil {
		r.sessions = map[string]Session{}
	}
	r.sessions[session.TokenHash] = session
	return nil
}

func (r *testSessionRepository) SessionByTokenHash(hash string) (Session, error) {
	if r.err != nil {
		return Session{}, r.err
	}
	session, ok := r.sessions[hash]
	if !ok {
		return Session{}, ErrNotFound
	}
	return session, nil
}

func (r *testSessionRepository) DeleteSession(hash string) error {
	if r.err != nil {
		return r.err
	}
	delete(r.sessions, hash)
	return nil
}

func (r *testSessionRepository) Users() ([]User, error) {
	if r.err != nil {
		return nil, r.err
	}
	return append([]User(nil), r.users...), nil
}

func (r *testSessionRepository) UserByID(id int64) (User, error) {
	if r.err != nil {
		return User{}, r.err
	}
	for _, user := range r.users {
		if user.ID == id {
			return user, nil
		}
	}
	return User{}, ErrNotFound
}

func (r *testSessionRepository) UserByUsername(username string) (User, error) {
	if r.err != nil {
		return User{}, r.err
	}
	for _, user := range r.users {
		if user.Username == username {
			return user, nil
		}
	}
	return User{}, ErrNotFound
}

func (r *testSessionRepository) APIKeys() ([]APIKey, error) {
	if r.err != nil {
		return nil, r.err
	}
	return append([]APIKey(nil), r.keys...), nil
}

func (r *testSessionRepository) APIKeyByID(id int64) (APIKey, error) {
	if r.err != nil {
		return APIKey{}, r.err
	}
	for _, key := range r.keys {
		if key.ID == id {
			return key, nil
		}
	}
	return APIKey{}, ErrNotFound
}

func (r *testSessionRepository) APIKeyByHash(hash string) (APIKey, error) {
	if r.err != nil {
		return APIKey{}, r.err
	}
	for _, key := range r.keys {
		if key.KeyHash == hash {
			return key, nil
		}
	}
	return APIKey{}, ErrNotFound
}

func (r *testSessionRepository) Audits(limit int) ([]AuditEntry, error) {
	if r.err != nil {
		return nil, r.err
	}
	rows := append([]AuditEntry(nil), r.audits...)
	if limit > 0 && len(rows) > limit {
		rows = rows[len(rows)-limit:]
	}
	result := make([]AuditEntry, len(rows))
	for i := range rows {
		result[len(rows)-1-i] = rows[i]
	}
	return result, nil
}

func sessionTestStore(repository Persistence) *Store {
	store := NewStore()
	store.persistence = repository
	user := User{ID: 12, Username: "operator", Role: RoleOperator, Enabled: true}
	store.auth.users = []User{user}
	if repo, ok := repository.(*testSessionRepository); ok && len(repo.users) == 0 {
		repo.users = []User{user}
	}
	return store
}

func TestPersistentSessionSurvivesStoreRestartAndLogoutRevokesIt(t *testing.T) {
	repository := &testSessionRepository{}
	first := sessionTestStore(repository)
	token, err := first.CreateSession(12)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if len(token) != 64 {
		t.Fatalf("token length=%d, want 64 hex chars", len(token))
	}
	hash := hashToken(token)
	if _, ok := repository.sessions[hash]; !ok {
		t.Fatal("session hash was not persisted")
	}
	for key := range repository.sessions {
		if key == token {
			t.Fatal("plaintext session token was persisted")
		}
	}

	// A new Store represents a fresh process with the same persistent DB.
	restarted := sessionTestStore(repository)
	user, err := restarted.SessionUser(token)
	if err != nil || user.ID != 12 {
		t.Fatalf("session after restart user=%+v err=%v", user, err)
	}
	if err := first.DeleteSession(token); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if _, err := restarted.SessionUser(token); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("session remained valid after logout from another Store: %v", err)
	}
}

func TestPersistentSessionExpiryAndStorageErrorsFailClosed(t *testing.T) {
	repository := &testSessionRepository{sessions: map[string]Session{
		hashToken("expired"): {TokenHash: hashToken("expired"), UserID: 12, ExpiresAt: time.Now().Add(-time.Minute)},
	}}
	store := sessionTestStore(repository)
	if _, err := store.SessionUser("expired"); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("expired session error=%v", err)
	}
	if _, ok := repository.sessions[hashToken("expired")]; ok {
		t.Fatal("expired session row was not removed")
	}

	repository.err = errors.New("database unavailable")
	if token, err := store.CreateSession(12); err == nil || token != "" {
		t.Fatalf("session creation should fail closed: token=%q err=%v", token, err)
	}
	if _, err := store.SessionUser(strings.Repeat("a", 64)); err == nil || errors.Is(err, ErrBadCredentials) {
		t.Fatalf("storage outage should not be mistaken for invalid credentials: %v", err)
	}
	if err := store.DeleteSession("token"); err == nil {
		t.Fatal("logout should report a persistent revocation error")
	}
}

func TestRequestTimeUserAndAPIKeyReadsUseSharedRepository(t *testing.T) {
	user := User{ID: 12, Username: "operator", Role: RoleViewer, Enabled: false}
	apiKey := APIKey{ID: 7, Name: "ci", KeyHash: HashAPIKey("dp_shared-token"), Role: RoleOperator, Enabled: true}
	repository := &testSessionRepository{users: []User{user}, keys: []APIKey{apiKey}}
	store := sessionTestStore(repository)
	store.auth.users[0].Role = RoleAdmin
	store.auth.users[0].Enabled = true
	store.apiKeys.keys = []APIKey{{ID: 7, Name: "ci", KeyHash: apiKey.KeyHash, Role: RoleViewer, Enabled: false}}

	resolvedUser, err := store.FindUser(12)
	if err != nil || resolvedUser.Role != RoleViewer || resolvedUser.Enabled {
		t.Fatalf("FindUser ignored shared update: user=%+v err=%v", resolvedUser, err)
	}
	listed, err := store.ListUsers()
	if err != nil || len(listed) != 1 || listed[0].Enabled {
		t.Fatalf("ListUsers ignored repository state: users=%+v err=%v", listed, err)
	}
	resolvedKey, err := store.ResolveAPIKey("dp_shared-token")
	if err != nil || resolvedKey.Role != RoleOperator || !resolvedKey.Enabled {
		t.Fatalf("ResolveAPIKey ignored shared update: key=%+v err=%v", resolvedKey, err)
	}
	keys, err := store.ListAPIKeys()
	if err != nil || len(keys) != 1 || keys[0].Role != RoleOperator {
		t.Fatalf("ListAPIKeys ignored repository state: keys=%+v err=%v", keys, err)
	}

	repository.err = errors.New("database unavailable")
	if _, err := store.FindUser(12); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("user repository outage was hidden: %v", err)
	}
	if _, err := store.ResolveAPIKey("dp_shared-token"); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("API key repository outage was hidden as an invalid key: %v", err)
	}
}

func TestRecentAuditsReadsSharedRepository(t *testing.T) {
	repository := &testSessionRepository{audits: []AuditEntry{
		{ID: 1, Action: "create", CreatedAt: time.Unix(1, 0)},
		{ID: 2, Action: "delete", CreatedAt: time.Unix(2, 0)},
	}}
	store := sessionTestStore(repository)
	store.LoadAudits([]AuditEntry{{ID: 0, Action: "stale-local"}})
	entries, err := store.RecentAudits(1)
	if err != nil || len(entries) != 1 || entries[0].ID != 2 {
		t.Fatalf("RecentAudits should read shared newest entry: entries=%+v err=%v", entries, err)
	}
	repository.err = errors.New("database unavailable")
	if _, err := store.RecentAudits(10); err == nil {
		t.Fatal("audit repository outage should be visible")
	}
}
