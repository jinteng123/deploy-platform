package service

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

// APIKey grants external systems scoped access to the /openapi/v1 surface.
// Only a SHA-256 hash plus a display prefix are stored — the plaintext token
// is returned once at creation and never persisted, so a leaked DB dump
// cannot mint working keys.
type APIKey struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	KeyHash    string     `json:"-"`
	Prefix     string     `json:"prefix"` // "dp_" + first 8 token chars — lookup hint only
	Role       string     `json:"role"`   // viewer（只读）| operator（可触发发布/晋级/回滚）
	Enabled    bool       `json:"enabled"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// APIKeyRepository is the authoritative source for credential decisions and
// admin reads when the Store is backed by MySQL.
type APIKeyRepository interface {
	APIKeys() ([]APIKey, error)
	APIKeyByID(int64) (APIKey, error)
	APIKeyByHash(string) (APIKey, error)
}

const apiKeyTokenPrefix = "dp_"

// NewAPIKeyToken mints a `dp_` + 256-bit token. Returns the plaintext
// (shown to the operator exactly once), the hash to persist, and the
// display prefix.
func NewAPIKeyToken() (token, hash, prefix string, err error) {
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return "", "", "", err
	}
	token = apiKeyTokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	return token, HashAPIKey(token), token[:len(apiKeyTokenPrefix)+8], nil
}

// HashAPIKey is the lookup fingerprint — hex SHA-256, same scheme as
// session tokens.
func HashAPIKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// apiKeyState holds API keys inside the Store. Mirrors authState: memory is
// authoritative, every mutation also persists when a database is attached.
type apiKeyState struct {
	mu     sync.RWMutex
	nextID int64
	keys   []APIKey
	// lastPersist throttles last_used_at writes so every request doesn't
	// become a row update under automation traffic.
	lastPersist map[int64]time.Time
}

// LoadAPIKeys hydrates keys from persistence at startup.
func (s *Store) LoadAPIKeys(keys []APIKey) {
	s.apiKeys.mu.Lock()
	defer s.apiKeys.mu.Unlock()
	s.apiKeys.keys = append([]APIKey(nil), keys...)
	s.apiKeys.nextID = 1
	for _, k := range keys {
		if k.ID >= s.apiKeys.nextID {
			s.apiKeys.nextID = k.ID + 1
		}
	}
}

// APIKeys returns a snapshot for the admin list surface. The slice is
// never nil — an empty list must marshal as [] so clients can range over
// the body without a null check.
func (s *Store) APIKeys() []APIKey {
	s.apiKeys.mu.RLock()
	defer s.apiKeys.mu.RUnlock()
	return append([]APIKey{}, s.apiKeys.keys...)
}

func (s *Store) ListAPIKeys() ([]APIKey, error) {
	if repository, ok := s.persistence.(APIKeyRepository); ok {
		keys, err := repository.APIKeys()
		if err != nil {
			return nil, fmt.Errorf("读取 API 密钥失败: %w", err)
		}
		s.apiKeys.mu.Lock()
		s.apiKeys.keys = append([]APIKey(nil), keys...)
		s.apiKeys.mu.Unlock()
		return keys, nil
	}
	return s.APIKeys(), nil
}

// FindAPIKey locates one key row by ID.
func (s *Store) FindAPIKey(id int64) (APIKey, error) {
	if repository, ok := s.persistence.(APIKeyRepository); ok {
		key, err := repository.APIKeyByID(id)
		if err != nil {
			return APIKey{}, err
		}
		s.apiKeys.mu.Lock()
		defer s.apiKeys.mu.Unlock()
		for i := range s.apiKeys.keys {
			if s.apiKeys.keys[i].ID == key.ID {
				s.apiKeys.keys[i] = key
				return key, nil
			}
		}
		s.apiKeys.keys = append(s.apiKeys.keys, key)
		return key, nil
	}
	s.apiKeys.mu.RLock()
	defer s.apiKeys.mu.RUnlock()
	for _, k := range s.apiKeys.keys {
		if k.ID == id {
			return k, nil
		}
	}
	return APIKey{}, fmt.Errorf("%w: API 密钥 %d", ErrNotFound, id)
}

// SaveAPIKey validates and stores a new key (name unique per platform).
func (s *Store) SaveAPIKey(k APIKey) (APIKey, error) {
	s.apiKeys.mu.Lock()
	defer s.apiKeys.mu.Unlock()
	// nextID is only hydrated by LoadAPIKeys — memory-mode stores start at
	// zero and must not mint ID 0 (pathInt64 rejects it upstream).
	if s.apiKeys.nextID == 0 {
		s.apiKeys.nextID = 1
	}
	if _, persistent := s.persistence.(APIKeyRepository); !persistent {
		for _, x := range s.apiKeys.keys {
			if x.Name == k.Name {
				return k, newConflict("API 密钥名称 %s 已存在", k.Name)
			}
		}
	}
	if s.persistence != nil {
		saved, err := s.persistence.SaveAPIKey(k)
		if err != nil {
			return k, err
		}
		k = saved
	}
	if k.ID == 0 {
		k.ID = s.apiKeys.nextID
	}
	if k.ID >= s.apiKeys.nextID {
		s.apiKeys.nextID = k.ID + 1
	}
	now := time.Now()
	if k.CreatedAt.IsZero() {
		k.CreatedAt = now
	}
	k.UpdatedAt = now
	s.apiKeys.keys = append(s.apiKeys.keys, k)
	return k, nil
}

// UpdateAPIKey persists name/role/enabled/expiry edits.
func (s *Store) UpdateAPIKey(k APIKey) (APIKey, error) {
	s.apiKeys.mu.Lock()
	defer s.apiKeys.mu.Unlock()
	var current APIKey
	if repository, ok := s.persistence.(APIKeyRepository); ok {
		var err error
		current, err = repository.APIKeyByID(k.ID)
		if err != nil {
			return k, err
		}
	} else {
		for _, row := range s.apiKeys.keys {
			if row.ID == k.ID {
				current = row
				break
			}
		}
		if current.ID == 0 {
			return k, fmt.Errorf("%w: API 密钥 %d", ErrNotFound, k.ID)
		}
		if current.Name != k.Name {
			for _, row := range s.apiKeys.keys {
				if row.ID != k.ID && row.Name == k.Name {
					return k, newConflict("API 密钥名称 %s 已存在", k.Name)
				}
			}
		}
	}
	k.KeyHash = current.KeyHash // hash never changes after creation
	k.Prefix = current.Prefix
	if s.persistence != nil {
		if err := s.persistence.UpdateAPIKey(k); err != nil {
			return k, err
		}
	}
	k.UpdatedAt = time.Now()
	for i := range s.apiKeys.keys {
		if s.apiKeys.keys[i].ID == k.ID {
			s.apiKeys.keys[i] = k
			return k, nil
		}
	}
	s.apiKeys.keys = append(s.apiKeys.keys, k)
	return k, nil
}

// DeleteAPIKey removes the key entirely — the credential stops working
// immediately; audit entries keep the name.
func (s *Store) DeleteAPIKey(id int64) error {
	s.apiKeys.mu.Lock()
	defer s.apiKeys.mu.Unlock()
	if repository, ok := s.persistence.(APIKeyRepository); ok {
		if _, err := repository.APIKeyByID(id); err != nil {
			return err
		}
	} else {
		found := false
		for _, key := range s.apiKeys.keys {
			if key.ID == id {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: API 密钥 %d", ErrNotFound, id)
		}
	}
	if s.persistence != nil {
		if err := s.persistence.DeleteAPIKey(id); err != nil {
			return err
		}
	}
	for i := range s.apiKeys.keys {
		if s.apiKeys.keys[i].ID == id {
			s.apiKeys.keys = append(s.apiKeys.keys[:i], s.apiKeys.keys[i+1:]...)
			break
		}
	}
	delete(s.apiKeys.lastPersist, id)
	return nil
}

// ResolveAPIKey authenticates an openapi bearer token. Returns the key only
// when it exists, is enabled and unexpired — all failure modes collapse into
// one error so probes learn nothing about which keys exist.
func (s *Store) ResolveAPIKey(token string) (APIKey, error) {
	if token == "" {
		return APIKey{}, fmt.Errorf("%w: 空 token", ErrNotFound)
	}
	hash := HashAPIKey(token)
	if repository, ok := s.persistence.(APIKeyRepository); ok {
		key, err := repository.APIKeyByHash(hash)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return APIKey{}, fmt.Errorf("%w: 无效 API 密钥", ErrNotFound)
			}
			return APIKey{}, fmt.Errorf("验证 API 密钥时读取存储失败: %w", err)
		}
		if subtle.ConstantTimeCompare([]byte(key.KeyHash), []byte(hash)) != 1 || !key.Enabled || key.ExpiresAt != nil && time.Now().After(*key.ExpiresAt) {
			return APIKey{}, fmt.Errorf("%w: 无效 API 密钥", ErrNotFound)
		}
		return key, nil
	}
	s.apiKeys.mu.RLock()
	var found *APIKey
	for i := range s.apiKeys.keys {
		k := &s.apiKeys.keys[i]
		if subtle.ConstantTimeCompare([]byte(k.KeyHash), []byte(hash)) == 1 {
			found = k
			break
		}
	}
	s.apiKeys.mu.RUnlock()
	if found == nil {
		return APIKey{}, fmt.Errorf("%w: 无效 API 密钥", ErrNotFound)
	}
	if !found.Enabled {
		return APIKey{}, fmt.Errorf("%w: API 密钥已禁用", ErrNotFound)
	}
	if found.ExpiresAt != nil && time.Now().After(*found.ExpiresAt) {
		return APIKey{}, fmt.Errorf("%w: API 密钥已过期", ErrNotFound)
	}
	return *found, nil
}

// TouchAPIKey stamps last_used_at. Memory updates every call; persistence
// writes at most once per key per minute so automation traffic doesn't turn
// into a write storm.
func (s *Store) TouchAPIKey(id int64) {
	s.apiKeys.mu.Lock()
	defer s.apiKeys.mu.Unlock()
	now := time.Now()
	if s.apiKeys.lastPersist == nil {
		s.apiKeys.lastPersist = map[int64]time.Time{}
	}
	if s.persistence != nil {
		if last, ok := s.apiKeys.lastPersist[id]; ok && now.Sub(last) < time.Minute {
			return
		}
		if err := s.persistence.TouchAPIKey(id, now); err != nil {
			return
		}
		s.apiKeys.lastPersist[id] = now
	}
	for i := range s.apiKeys.keys {
		if s.apiKeys.keys[i].ID != id {
			continue
		}
		s.apiKeys.keys[i].LastUsedAt = &now
		return
	}
}
