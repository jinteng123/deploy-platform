package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type driftRuntimeResolverStub struct {
	mu      sync.Mutex
	runtime KubernetesRuntime
	err     error
	calls   []int
}

func (s *driftRuntimeResolverStub) For(cluster Cluster) (KubernetesRuntime, error) {
	s.mu.Lock()
	s.calls = append(s.calls, cluster.ID)
	s.mu.Unlock()
	return s.runtime, s.err
}

func driftPollerFixture(t *testing.T) (*Store, *recordingK8s, *driftRuntimeResolverStub, *DriftPoller) {
	t.Helper()
	image := "registry.example/api:v1"
	actual := "registry.example/api:v2"
	store := NewStoreWithData(
		[]Cluster{{ID: 2, Name: "production", Enabled: true}, {ID: 3, Name: "disabled", Enabled: false}},
		[]App{
			{ID: 7, ClusterID: 2, AppName: "api", Namespace: "prod", DeploymentName: "api", ContainerName: "api"},
			{ID: 8, ClusterID: 2, AppName: "unmanaged", Namespace: "prod", DeploymentName: "unmanaged", ContainerName: "api"},
			{ID: 9, ClusterID: 3, AppName: "disabled", Namespace: "prod", DeploymentName: "disabled", ContainerName: "api"},
		},
		[]DeployLog{{ID: 11, ClusterID: 2, AppName: "api", Namespace: "prod", DeploymentName: "api", ContainerName: "api", Status: "success", ImageFull: &image}},
		nil, nil,
	)
	runtime := &recordingK8s{forcedImage: &actual}
	resolver := &driftRuntimeResolverStub{runtime: runtime}
	poller, err := NewDriftPoller(store, resolver, time.Minute)
	if err != nil {
		t.Fatalf("NewDriftPoller: %v", err)
	}
	return store, runtime, resolver, poller
}

func TestDriftPollerObservesManagedTargetsWithoutKubernetesWrites(t *testing.T) {
	store, runtime, resolver, poller := driftPollerFixture(t)
	if err := poller.ObserveOnce(context.Background()); err != nil {
		t.Fatalf("ObserveOnce: %v", err)
	}
	history, err := store.DeliveryDriftHistory(DeliveryDriftHistoryFilter{ClusterID: 2, Limit: 10})
	if err != nil || len(history) != 1 || history[0].AppID != 7 || history[0].Status != "drifted" || history[0].Source != DriftBackgroundSource || history[0].ObservedBy != driftPollerIdentity {
		t.Fatalf("background drift history=%+v err=%v", history, err)
	}
	resolver.mu.Lock()
	calls := append([]int(nil), resolver.calls...)
	resolver.mu.Unlock()
	if len(calls) != 1 || calls[0] != 2 {
		t.Fatalf("runtime calls=%v, want only enabled cluster", calls)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if len(runtime.setCalls) != 0 {
		t.Fatalf("background observer wrote the cluster: %v", runtime.setCalls)
	}
}

func TestDriftPollerMarksConnectionFailureUnknownWithoutLeakingCredentials(t *testing.T) {
	store, _, resolver, poller := driftPollerFixture(t)
	resolver.err = errors.New("private-credential-material")
	resolver.runtime = nil
	if err := poller.ObserveOnce(context.Background()); err == nil || strings.Contains(err.Error(), "private-credential-material") {
		t.Fatalf("connection failure error leaked sensitive detail: %v", err)
	}
	history, err := store.DeliveryDriftHistory(DeliveryDriftHistoryFilter{ClusterID: 2, Limit: 10})
	if err != nil || len(history) != 1 || history[0].Status != "unknown" || history[0].Source != DriftBackgroundSource || strings.Contains(history[0].Reason, "private-credential-material") {
		t.Fatalf("unknown drift history=%+v err=%v", history, err)
	}
}

func TestDriftPollerStartsImmediatelyAndStops(t *testing.T) {
	store, _, _, poller := driftPollerFixture(t)
	poller.Start()
	deadline := time.After(2 * time.Second)
	for {
		history, err := store.DeliveryDriftHistory(DeliveryDriftHistoryFilter{ClusterID: 2, Limit: 10})
		if err != nil {
			t.Fatalf("DeliveryDriftHistory: %v", err)
		}
		if len(history) == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("background poller did not record its first cycle")
		case <-time.After(10 * time.Millisecond):
		}
	}
	poller.Close()
	poller.Close()
}

func TestDriftPollIntervalValidation(t *testing.T) {
	for _, interval := range []time.Duration{-1, 30 * time.Second, 25 * time.Hour} {
		if err := ValidateDriftPollInterval(interval); err == nil {
			t.Fatalf("invalid interval %v accepted", interval)
		}
	}
	if err := ValidateDriftPollInterval(0); err != nil {
		t.Fatalf("disabled polling rejected: %v", err)
	}
	if err := ValidateDriftPollInterval(time.Minute); err != nil {
		t.Fatalf("minimum interval rejected: %v", err)
	}
}
