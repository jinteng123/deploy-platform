package service

import (
	"context"
	"testing"
	"time"

	"deploy-platform/backend-go/internal/config"
)

// encCluster builds a token-connection cluster whose ciphertext decrypts
// under cipher — directClient resolves it offline (no dial on NewFromToken).
func encCluster(t *testing.T, cipher *CredentialCipher, version time.Time) Cluster {
	t.Helper()
	apiServer := "https://k8s:6443"
	ct, err := cipher.Encrypt("tok-" + version.Format("150405"))
	if err != nil {
		t.Fatal(err)
	}
	return Cluster{
		ID: 7, Name: "cached", Enabled: true, ConnectionType: "token",
		APIServer: &apiServer, CredentialCiphertext: &ct, UpdatedAt: version,
	}
}

func newDirectK8s(t *testing.T) (*K8sService, *CredentialCipher) {
	t.Helper()
	cipher, err := NewCredentialCipher(testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	return NewK8sService(config.Config{}, &MockRunner{}, cipher), cipher
}

// TestClientCacheKeying freezes the lifecycle contract: same
// (cluster,version,identity) → shared client; a credential update bumps
// UpdatedAt → new client; a different impersonation identity → new client.
func TestClientCacheKeying(t *testing.T) {
	k8s, cipher := newDirectK8s(t)
	v1 := time.Now()
	cluster := encCluster(t, cipher, v1)

	c1, err := k8s.ForUser(cluster, nil)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := k8s.ForUser(cluster, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c1 != c2 {
		t.Fatal("same (cluster,version,identity) must share the cached client")
	}

	// Credential rotation bumps UpdatedAt → a different cache key.
	clusterUpdated := cluster
	clusterUpdated.UpdatedAt = v1.Add(time.Minute)
	c3, err := k8s.ForUser(clusterUpdated, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c3 == c1 {
		t.Fatal("credential version bump must not reuse the stale client")
	}

	// A different impersonation identity gets its own client.
	u := &User{K8sUsername: "dev-one"}
	c4, err := k8s.ForUser(clusterUpdated, u)
	if err != nil {
		t.Fatal(err)
	}
	if c4 == c3 || c4 == c1 {
		t.Fatal("distinct identities must not share clients")
	}
	if len(k8s.clients) != 3 {
		t.Fatalf("cache size = %d, want 3", len(k8s.clients))
	}
}

// TestPlainCredentialBypassesCache — the transient test-connection path
// carries plaintext and must never touch the cache.
func TestPlainCredentialBypassesCache(t *testing.T) {
	k8s, _ := newDirectK8s(t)
	apiServer := "https://k8s:6443"
	cluster := Cluster{
		ID: 8, Name: "probe", Enabled: true, ConnectionType: "token",
		APIServer: &apiServer, PlainCredential: "tok", UpdatedAt: time.Now(),
	}
	if _, err := k8s.ForUser(cluster, nil); err != nil {
		t.Fatal(err)
	}
	if len(k8s.clients) != 0 {
		t.Fatal("plaintext credential path must not populate the cache")
	}
}

// TestInvalidateCluster drops cached clients and cancels tracked streams
// for exactly that cluster.
func TestInvalidateCluster(t *testing.T) {
	k8s, cipher := newDirectK8s(t)
	cluster := encCluster(t, cipher, time.Now())
	if _, err := k8s.ForUser(cluster, nil); err != nil {
		t.Fatal(err)
	}
	if len(k8s.clients) != 1 {
		t.Fatal("expected one cached client")
	}

	ctx, cancel := context.WithCancel(context.Background())
	untrack := k8s.TrackStream(cluster.ID, cancel)
	otherCtx, otherCancel := context.WithCancel(context.Background())
	k8s.TrackStream(99, otherCancel)

	k8s.InvalidateCluster(cluster.ID)

	if len(k8s.clients) != 0 {
		t.Fatal("invalidate must drop the cluster's clients")
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("invalidate must cancel tracked streams")
	}
	select {
	case <-otherCtx.Done():
		t.Fatal("other cluster's streams must not be cancelled")
	default:
	}
	untrack()
	otherCancel()

	// Next resolve rebuilds from the (now rotated) credential.
	if _, err := k8s.ForUser(cluster, nil); err != nil {
		t.Fatal(err)
	}
	if len(k8s.clients) != 1 {
		t.Fatal("rebuild must repopulate the cache")
	}
}

// TestUntrackStopsCancellation — a session that ended normally must not be
// cancelled by a later invalidation.
func TestUntrackStopsCancellation(t *testing.T) {
	k8s, _ := newDirectK8s(t)
	ctx, cancel := context.WithCancel(context.Background())
	untrack := k8s.TrackStream(7, cancel)
	untrack()
	k8s.InvalidateCluster(7)
	select {
	case <-ctx.Done():
		t.Fatal("untracked session must not be cancelled")
	default:
	}
	cancel()
}
