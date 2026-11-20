package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Platform roles, ordered by privilege. The K8s RBAC layer is a separate
// axis: a platform role decides whether a route is reachable at all,
// while the user's K8s identity (impersonation) decides what the
// apiserver will let them do.
const (
	RoleViewer   = "viewer"
	RoleOperator = "operator"
	RoleAdmin    = "admin"
)

// RoleRank orders roles for minimum-role gates; unknown roles rank below
// viewer so a corrupt row cannot silently elevate.
func RoleRank(role string) int {
	switch role {
	case RoleAdmin:
		return 3
	case RoleOperator:
		return 2
	case RoleViewer:
		return 1
	default:
		return 0
	}
}

var validRoles = map[string]bool{RoleViewer: true, RoleOperator: true, RoleAdmin: true}

// User is a platform account. K8sUsername/K8sGroups name the identity the
// user's Kubernetes calls are impersonated as; an empty K8sUsername means
// the call runs as the cluster credential itself (no impersonation).
type User struct {
	ID           int64     `json:"id"`
	Username     string    `json:"username"`
	DisplayName  string    `json:"display_name"`
	Role         string    `json:"role"`
	PasswordHash string    `json:"-"`
	K8sUsername  string    `json:"k8s_username"`
	K8sGroups    []string  `json:"k8s_groups"`
	Enabled      bool      `json:"enabled"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// PublicUser is the API shape — never carries the password hash.
type PublicUser struct {
	ID          int64    `json:"id"`
	Username    string   `json:"username"`
	DisplayName string   `json:"display_name"`
	Role        string   `json:"role"`
	K8sUsername string   `json:"k8s_username"`
	K8sGroups   []string `json:"k8s_groups"`
	Enabled     bool     `json:"enabled"`
}

func (u User) Public() PublicUser {
	return PublicUser{
		ID: u.ID, Username: u.Username, DisplayName: u.DisplayName,
		Role: u.Role, K8sUsername: u.K8sUsername, K8sGroups: u.K8sGroups,
		Enabled: u.Enabled,
	}
}

var ErrBadCredentials = errors.New("用户名或密码错误")

// HashPassword wraps bcrypt so call sites never pick their own cost.
func HashPassword(plain string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	return string(h), err
}

func checkPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

// SessionTTL bounds an authenticated session; expiry is absolute, not
// sliding — long-running automation re-authenticates.
const SessionTTL = 24 * time.Hour

// Session is an opaque login token. Only the SHA-256 hash is stored, so
// a leaked store dump cannot mint live sessions.
type Session struct {
	TokenHash string
	UserID    int64
	ExpiresAt time.Time
	CreatedAt time.Time
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// SessionPersistence stores only a token hash. Durable implementations make
// authenticated sessions survive process restarts without ever persisting the
// browser's bearer token.
type SessionPersistence interface {
	SaveSession(Session) error
	SessionByTokenHash(string) (Session, error)
	DeleteSession(string) error
}

// UserRepository exposes authoritative user reads for request-time identity
// and permission checks. The Store's user slice remains a derived cache.
type UserRepository interface {
	Users() ([]User, error)
	UserByID(int64) (User, error)
	UserByUsername(string) (User, error)
}

// authState caches sessions only for in-memory stores and tests. Production
// persistence reads the durable row so logout and expiry are visible after a
// process restart.
type authState struct {
	mu       sync.RWMutex
	nextUser int64
	users    []User
	sessions map[string]Session
}

// SeedUsers installs the initial accounts when the store has none.
// Called once at startup; idempotent for existing deployments.
func (s *Store) SeedUsers(users []User) {
	s.auth.mu.Lock()
	defer s.auth.mu.Unlock()
	if len(s.auth.users) > 0 {
		return
	}
	for _, u := range users {
		if u.ID == 0 {
			u.ID = s.auth.nextUser
		}
		if u.ID >= s.auth.nextUser {
			s.auth.nextUser = u.ID + 1
		}
		now := time.Now()
		if u.CreatedAt.IsZero() {
			u.CreatedAt = now
		}
		if u.UpdatedAt.IsZero() {
			u.UpdatedAt = u.CreatedAt
		}
		s.auth.users = append(s.auth.users, u)
	}
}

// LoadUsers replaces the in-memory user list (startup hydration from
// persistence). Unlike SeedUsers it always applies.
func (s *Store) LoadUsers(users []User) {
	s.auth.mu.Lock()
	defer s.auth.mu.Unlock()
	s.auth.users = append([]User(nil), users...)
	s.auth.nextUser = 1
	for _, u := range s.auth.users {
		if u.ID >= s.auth.nextUser {
			s.auth.nextUser = u.ID + 1
		}
	}
}

func (s *Store) Users() []User {
	s.auth.mu.RLock()
	defer s.auth.mu.RUnlock()
	// Non-nil so an empty list serializes as [] rather than null.
	return append([]User{}, s.auth.users...)
}

func (s *Store) ListUsers() ([]User, error) {
	if repository, ok := s.persistence.(UserRepository); ok {
		users, err := repository.Users()
		if err != nil {
			return nil, fmt.Errorf("读取用户列表失败: %w", err)
		}
		s.LoadUsers(users)
		return users, nil
	}
	return s.Users(), nil
}

func (s *Store) FindUserByUsername(username string) (User, error) {
	if repository, ok := s.persistence.(UserRepository); ok {
		user, err := repository.UserByUsername(username)
		if err != nil {
			return User{}, err
		}
		s.cacheUser(user)
		return user, nil
	}
	s.auth.mu.RLock()
	defer s.auth.mu.RUnlock()
	for _, u := range s.auth.users {
		if u.Username == username {
			return u, nil
		}
	}
	return User{}, fmt.Errorf("%w: 用户 %s", ErrNotFound, username)
}

func (s *Store) FindUser(id int64) (User, error) {
	if repository, ok := s.persistence.(UserRepository); ok {
		user, err := repository.UserByID(id)
		if err != nil {
			return User{}, err
		}
		s.cacheUser(user)
		return user, nil
	}
	s.auth.mu.RLock()
	defer s.auth.mu.RUnlock()
	for _, u := range s.auth.users {
		if u.ID == id {
			return u, nil
		}
	}
	return User{}, fmt.Errorf("%w: 用户 ID=%d", ErrNotFound, id)
}

func (s *Store) cacheUser(user User) {
	s.auth.mu.Lock()
	defer s.auth.mu.Unlock()
	for i := range s.auth.users {
		if s.auth.users[i].ID == user.ID {
			s.auth.users[i] = user
			return
		}
	}
	s.auth.users = append(s.auth.users, user)
	if user.ID >= s.auth.nextUser {
		s.auth.nextUser = user.ID + 1
	}
}

// SaveUser creates a user. Password must arrive pre-hashed (HashPassword)
// so the plaintext never travels through the store layer.
func (s *Store) SaveUser(u User) (User, error) {
	s.auth.mu.Lock()
	defer s.auth.mu.Unlock()
	if u.Username == "" {
		return u, NewValidationError("用户名不能为空")
	}
	if !validRoles[u.Role] {
		return u, NewValidationError("非法角色 %q", u.Role)
	}
	for _, ex := range s.auth.users {
		if ex.Username == u.Username {
			return u, newConflict("用户名 %s 已存在", u.Username)
		}
	}
	if s.persistence != nil {
		saved, err := s.persistence.SaveUser(u)
		if err != nil {
			return u, err
		}
		u = saved
	}
	if u.ID == 0 {
		u.ID = s.auth.nextUser
	}
	now := time.Now()
	if u.CreatedAt.IsZero() {
		u.CreatedAt = now
	}
	u.UpdatedAt = now
	s.auth.users = append(s.auth.users, u)
	if u.ID >= s.auth.nextUser {
		s.auth.nextUser = u.ID + 1
	}
	return u, nil
}

// UpdateUser applies role/identity/enabled/password changes by id.
func (s *Store) UpdateUser(u User) (User, error) {
	s.auth.mu.Lock()
	defer s.auth.mu.Unlock()
	idx := -1
	for i := range s.auth.users {
		if s.auth.users[i].ID == u.ID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return u, ErrNotFound
	}
	if !validRoles[u.Role] {
		return u, NewValidationError("非法角色 %q", u.Role)
	}
	cur := s.auth.users[idx]
	u.Username = cur.Username // username is the identity — not mutable
	if u.PasswordHash == "" {
		u.PasswordHash = cur.PasswordHash // empty keeps the old password
	}
	u.CreatedAt = cur.CreatedAt
	u.UpdatedAt = time.Now()
	if s.persistence != nil {
		if err := s.persistence.UpdateUser(u); err != nil {
			return u, err
		}
	}
	s.auth.users[idx] = u
	return u, nil
}

// Authenticate verifies credentials and returns the user.
func (s *Store) Authenticate(username, password string) (User, error) {
	u, err := s.FindUserByUsername(username)
	if errors.Is(err, ErrNotFound) {
		return User{}, ErrBadCredentials
	}
	if err != nil {
		return User{}, fmt.Errorf("验证登录用户时读取存储失败: %w", err)
	}
	if !u.Enabled || !checkPassword(u.PasswordHash, password) {
		return User{}, ErrBadCredentials
	}
	return u, nil
}

// CreateSession mints an opaque token for the user and records its hash.
func (s *Store) CreateSession(userID int64) (token string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token = hex.EncodeToString(raw)
	s.auth.mu.Lock()
	defer s.auth.mu.Unlock()
	if s.auth.sessions == nil {
		s.auth.sessions = map[string]Session{}
	}
	now := time.Now()
	session := Session{
		TokenHash: hashToken(token),
		UserID:    userID,
		CreatedAt: now,
		ExpiresAt: now.Add(SessionTTL),
	}
	if persistence, ok := s.persistence.(SessionPersistence); ok {
		if err := persistence.SaveSession(session); err != nil {
			return "", fmt.Errorf("持久化登录会话失败: %w", err)
		}
	} else {
		s.auth.sessions[session.TokenHash] = session
	}
	return token, nil
}

// SeedSession registers a caller-chosen token — the deterministic twin
// of CreateSession used by tests and by pre-provisioned API tokens.
func (s *Store) SeedSession(token string, userID int64, ttl time.Duration) {
	s.auth.mu.Lock()
	defer s.auth.mu.Unlock()
	if s.auth.sessions == nil {
		s.auth.sessions = map[string]Session{}
	}
	now := time.Now()
	s.auth.sessions[hashToken(token)] = Session{
		TokenHash: hashToken(token),
		UserID:    userID,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
	}
}

// SessionUser resolves a token to its live user — expired tokens and
// disabled users both fail closed.
func (s *Store) SessionUser(token string) (User, error) {
	h := hashToken(token)
	var sess Session
	if persistence, ok := s.persistence.(SessionPersistence); ok {
		var err error
		sess, err = persistence.SessionByTokenHash(h)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return User{}, ErrBadCredentials
			}
			return User{}, fmt.Errorf("读取登录会话失败: %w", err)
		}
	} else {
		s.auth.mu.RLock()
		var found bool
		sess, found = s.auth.sessions[h]
		s.auth.mu.RUnlock()
		if !found {
			return User{}, ErrBadCredentials
		}
	}
	if time.Now().After(sess.ExpiresAt) {
		_ = s.DeleteSession(token)
		return User{}, ErrBadCredentials
	}
	u, err := s.FindUser(sess.UserID)
	if errors.Is(err, ErrNotFound) || err == nil && !u.Enabled {
		return User{}, ErrBadCredentials
	}
	if err != nil {
		return User{}, fmt.Errorf("读取登录用户失败: %w", err)
	}
	return u, nil
}

func (s *Store) DeleteSession(token string) error {
	s.auth.mu.Lock()
	defer s.auth.mu.Unlock()
	h := hashToken(token)
	delete(s.auth.sessions, h)
	if persistence, ok := s.persistence.(SessionPersistence); ok {
		return persistence.DeleteSession(h)
	}
	return nil
}
