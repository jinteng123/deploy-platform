package service

import (
	"errors"
	"testing"
	"time"
)

func mkKey(t *testing.T, s *Store, name string, role string) (APIKey, string) {
	t.Helper()
	token, hash, prefix, err := NewAPIKeyToken()
	if err != nil {
		t.Fatal(err)
	}
	k, err := s.SaveAPIKey(APIKey{Name: name, KeyHash: hash, Prefix: prefix, Role: role, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	return k, token
}

func TestAPIKeyTokenFormat(t *testing.T) {
	token, hash, prefix, err := NewAPIKeyToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != 3+43 || token[:3] != "dp_" {
		t.Fatalf("token format %q", token)
	}
	if len(hash) != 64 || len(prefix) != 11 {
		t.Fatalf("hash=%q prefix=%q", hash, prefix)
	}
}

func TestAPIKeyLifecycle(t *testing.T) {
	s := NewStore()

	k, token := mkKey(t, s, "ci-bot", RoleOperator)
	if k.ID != 1 {
		t.Fatalf("first key id=%d", k.ID)
	}
	got, err := s.ResolveAPIKey(token)
	if err != nil || got.ID != k.ID || got.Name != "ci-bot" {
		t.Fatalf("resolve: %+v err=%v", got, err)
	}
	if _, err := s.ResolveAPIKey("dp_wrong"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bad token err=%v", err)
	}
	if _, err := s.ResolveAPIKey(""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty token err=%v", err)
	}

	// 同名冲突
	_, hash2, _, _ := NewAPIKeyToken()
	if _, err := s.SaveAPIKey(APIKey{Name: "ci-bot", KeyHash: hash2, Role: RoleViewer}); !errors.Is(err, ErrConflict) {
		t.Fatalf("dup name err=%v", err)
	}

	// 禁用 → resolve 失败；更新恢复
	k.Enabled = false
	if _, err := s.UpdateAPIKey(k); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveAPIKey(token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disabled resolve err=%v", err)
	}
	k.Enabled = true
	k.Role = RoleViewer
	s.UpdateAPIKey(k)
	got, _ = s.ResolveAPIKey(token)
	if got.Role != RoleViewer {
		t.Fatalf("role not updated: %s", got.Role)
	}

	// 过期
	past := time.Now().Add(-time.Hour)
	k.ExpiresAt = &past
	s.UpdateAPIKey(k)
	if _, err := s.ResolveAPIKey(token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired resolve err=%v", err)
	}
	k.ExpiresAt = nil
	s.UpdateAPIKey(k)

	// last_used_at
	s.TouchAPIKey(k.ID)
	got, _ = s.FindAPIKey(k.ID)
	if got.LastUsedAt == nil {
		t.Fatal("last_used_at not stamped")
	}

	// 删除
	if err := s.DeleteAPIKey(k.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveAPIKey(token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted resolve err=%v", err)
	}
	if len(s.APIKeys()) != 0 {
		t.Fatal("key still listed after delete")
	}
}

func TestLoadAPIKeysRestoresIDs(t *testing.T) {
	s := NewStore()
	s.LoadAPIKeys([]APIKey{{ID: 5, Name: "old", KeyHash: "h", Prefix: "p"}})
	k, _ := mkKey(t, s, "new", RoleViewer)
	if k.ID != 6 {
		t.Fatalf("id after load=%d", k.ID)
	}
}
