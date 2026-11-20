package service

import (
	"context"
	"strings"
	"sync"
	"time"
)

const MaxConcurrentDeliveryDriftReads = 8

type DeliveryDriftImageReader func(context.Context, App) (string, error)

// ObserveDeliveryDriftTargets performs bounded, ordered reads for both the
// user-initiated endpoint and the optional background observer. It only
// returns observations; callers decide whether to persist the whole batch.
func ObserveDeliveryDriftTargets(ctx context.Context, store *Store, clusterID int, apps []App, observedBy, source string, readImage DeliveryDriftImageReader) []DeliveryDriftObservation {
	observations := make([]DeliveryDriftObservation, len(apps))
	if len(apps) == 0 {
		return observations
	}
	workers := min(len(apps), MaxConcurrentDeliveryDriftReads)
	jobs := make(chan int)
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			for index := range jobs {
				app := apps[index]
				row := DeliveryDriftObservation{
					AppID: app.ID, AppName: app.AppName, ClusterID: clusterID, Namespace: app.Namespace,
					Workload: app.DeploymentName, Container: app.ContainerName, Status: "unknown",
					ObservedBy: observedBy, ObservedAt: time.Now().UTC(), Source: source,
				}
				baseline, ok := store.SuccessfulWorkloadLog(clusterID, app.Namespace, app.DeploymentName, app.ContainerName)
				if !ok || baseline.ImageFull == nil || strings.TrimSpace(*baseline.ImageFull) == "" {
					row.Reason = "缺少已确认的成功部署基线"
					observations[index] = row
					continue
				}
				row.ExpectedImage = baseline.ImageFull
				baselineLogID := baseline.ID
				row.BaselineLogID = &baselineLogID

				actual, err := readImage(ctx, app)
				row.ObservedAt = time.Now().UTC()
				if err != nil {
					row.Reason = "读取当前镜像失败；请检查集群连接和资源读取权限"
				} else if strings.TrimSpace(actual) == "" {
					row.Reason = "读取到的当前镜像为空，无法与发布基线比较"
				} else {
					row.ActualImage = &actual
					if actual == *baseline.ImageFull {
						row.Status = "in_sync"
					} else {
						row.Status = "drifted"
					}
				}
				observations[index] = row
			}
		}()
	}
	for index := range apps {
		jobs <- index
	}
	close(jobs)
	wait.Wait()
	return observations
}
