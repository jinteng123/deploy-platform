package httpapi

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"deploy-platform/backend-go/internal/service"
)

func TestObserveDeliveryDriftTargetsBoundsConcurrentReadsAndPreservesOrder(t *testing.T) {
	const targetCount = 24
	apps := make([]service.App, 0, targetCount)
	logs := make([]service.DeployLog, 0, targetCount)
	for i := 1; i <= targetCount; i++ {
		appName := fmt.Sprintf("app-%02d", i)
		apps = append(apps, service.App{
			ID: i, ClusterID: 5, AppName: appName, Namespace: "production",
			DeploymentName: appName, ContainerName: "api",
		})
		image := "registry.example/" + appName + ":v1"
		logs = append(logs, service.DeployLog{
			ID: int64(i), ClusterID: 5, AppName: appName, Namespace: "production",
			DeploymentName: appName, ContainerName: "api", Status: "success", ImageFull: &image,
		})
	}
	store := service.NewStoreWithData(nil, apps, logs, nil, nil)
	started := make(chan struct{}, targetCount)
	release := make(chan struct{})
	var active, maximum atomic.Int32
	read := func(ctx context.Context, app service.App) (string, error) {
		current := active.Add(1)
		defer active.Add(-1)
		for previous := maximum.Load(); current > previous; previous = maximum.Load() {
			if maximum.CompareAndSwap(previous, current) {
				break
			}
		}
		started <- struct{}{}
		select {
		case <-release:
			return "registry.example/" + app.AppName + ":v1", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	result := make(chan []service.DeliveryDriftObservation, 1)
	go func() {
		result <- observeDeliveryDriftTargets(context.Background(), store, 5, apps, "operator", read)
	}()

	for i := 0; i < maxConcurrentDeliveryDriftReads; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("bounded worker pool did not start all available readers")
		}
	}
	select {
	case <-started:
		t.Fatal("drift observation exceeded its concurrent read limit")
	case <-time.After(25 * time.Millisecond):
	}
	if got := maximum.Load(); got != maxConcurrentDeliveryDriftReads {
		t.Fatalf("maximum concurrent reads=%d, want %d", got, maxConcurrentDeliveryDriftReads)
	}
	close(release)
	observations := <-result
	if len(observations) != targetCount {
		t.Fatalf("observation count=%d, want %d", len(observations), targetCount)
	}
	for i, observation := range observations {
		wantName := apps[i].AppName
		if observation.AppID != apps[i].ID || observation.AppName != wantName || observation.Status != "in_sync" {
			t.Fatalf("observation[%d]=%+v, want ordered in_sync row for %s", i, observation, wantName)
		}
	}
}

func TestObserveDeliveryDriftTargetsTreatsEmptyImageAsUnknown(t *testing.T) {
	image := "registry.example/api:v1"
	app := service.App{ID: 1, ClusterID: 5, AppName: "api", Namespace: "production", DeploymentName: "api", ContainerName: "api"}
	store := service.NewStoreWithData(nil, []service.App{app}, []service.DeployLog{{
		ID: 1, ClusterID: 5, AppName: "api", Namespace: "production", DeploymentName: "api", ContainerName: "api",
		Status: "success", ImageFull: &image,
	}}, nil, nil)
	observations := observeDeliveryDriftTargets(context.Background(), store, 5, []service.App{app}, "operator", func(context.Context, service.App) (string, error) {
		return "", nil
	})
	if len(observations) != 1 || observations[0].Status != "unknown" || observations[0].ActualImage != nil || observations[0].Reason == "" {
		t.Fatalf("empty current image observation=%+v, want explicit unknown", observations)
	}
}
