package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"
)

const (
	minDriftPollInterval           = time.Minute
	maxDriftPollInterval           = 24 * time.Hour
	driftCycleTimeout              = 2 * time.Minute
	driftReadTimeout               = 10 * time.Second
	driftPollerIdentity            = "system:drift-poller"
	maxDriftTargetsPerClusterCycle = MaxConcurrentDeliveryDriftReads * 6
)

type DriftRuntimeResolver interface {
	For(Cluster) (KubernetesRuntime, error)
}

// DriftPoller is an opt-in, read-only observer. A single goroutine runs one
// bounded cycle at a time and waits a full interval after each cycle. It does
// not reconcile Kubernetes resources or emit notifications.
type DriftPoller struct {
	store         *Store
	resolver      DriftRuntimeResolver
	interval      time.Duration
	mu            sync.Mutex
	started       bool
	closed        bool
	cancel        context.CancelFunc
	done          chan struct{}
	lastClusterID int
	targetCursor  map[int]int
}

func ValidateDriftPollInterval(interval time.Duration) error {
	if interval == 0 {
		return nil
	}
	if interval < minDriftPollInterval || interval > maxDriftPollInterval {
		return NewValidationError("后台漂移观测间隔必须在 60 秒至 24 小时之间")
	}
	return nil
}

func NewDriftPoller(store *Store, resolver DriftRuntimeResolver, interval time.Duration) (*DriftPoller, error) {
	if interval == 0 {
		return nil, NewValidationError("后台漂移观测间隔为 0，未启用定时任务")
	}
	if err := ValidateDriftPollInterval(interval); err != nil {
		return nil, err
	}
	if store == nil || resolver == nil {
		return nil, NewValidationError("后台漂移观测缺少集群读取能力")
	}
	return &DriftPoller{
		store: store, resolver: resolver, interval: interval, done: make(chan struct{}), targetCursor: map[int]int{},
	}, nil
}

func (p *DriftPoller) Start() {
	p.mu.Lock()
	if p.started || p.closed {
		p.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.started = true
	p.mu.Unlock()
	go func() {
		defer close(p.done)
		for {
			cycleCtx, stop := context.WithTimeout(ctx, driftCycleTimeout)
			if err := p.ObserveOnce(cycleCtx); err != nil && ctx.Err() == nil {
				log.Printf("[drift-poller] 后台观测未完成: %v", err)
			}
			stop()
			timer := time.NewTimer(p.interval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
}

func (p *DriftPoller) Close() {
	p.mu.Lock()
	if p.closed {
		started := p.started
		p.mu.Unlock()
		if started {
			<-p.done
		}
		return
	}
	p.closed = true
	started := p.started
	cancel := p.cancel
	if !started {
		close(p.done)
	}
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if started {
		<-p.done
	}
}

// ObserveOnce records only targets that have a successful deployment baseline.
// Disabled clusters are skipped. An unreadable enabled cluster becomes unknown
// so a previous in-sync observation is not left looking current.
func (p *DriftPoller) ObserveOnce(ctx context.Context) error {
	var failures []error
	clusters := p.store.Clusters()
	sort.Slice(clusters, func(i, j int) bool { return clusters[i].ID < clusters[j].ID })
	p.mu.Lock()
	lastClusterID := p.lastClusterID
	p.mu.Unlock()
	if len(clusters) > 0 {
		start := sort.Search(len(clusters), func(i int) bool { return clusters[i].ID > lastClusterID })
		if start == len(clusters) {
			start = 0
		}
		clusters = append(append([]Cluster{}, clusters[start:]...), clusters[:start]...)
	}
	for _, cluster := range clusters {
		if err := ctx.Err(); err != nil {
			return err
		}
		p.mu.Lock()
		p.lastClusterID = cluster.ID
		p.mu.Unlock()
		if !cluster.Enabled {
			continue
		}
		apps := p.store.Apps(cluster.ID)
		managed := apps[:0]
		for _, app := range apps {
			baseline, ok := p.store.SuccessfulWorkloadLog(cluster.ID, app.Namespace, app.DeploymentName, app.ContainerName)
			if ok && baseline.ImageFull != nil && *baseline.ImageFull != "" {
				managed = append(managed, app)
			}
		}
		if len(managed) == 0 {
			continue
		}
		sort.Slice(managed, func(i, j int) bool { return managed[i].ID < managed[j].ID })
		p.mu.Lock()
		lastAppID := p.targetCursor[cluster.ID]
		p.mu.Unlock()
		start := sort.Search(len(managed), func(i int) bool { return managed[i].ID > lastAppID })
		if start == len(managed) {
			start = 0
		}
		batchSize := min(len(managed), maxDriftTargetsPerClusterCycle)
		batch := make([]App, batchSize)
		for i := range batch {
			batch[i] = managed[(start+i)%len(managed)]
		}
		nextAppID := batch[len(batch)-1].ID
		runtime, runtimeErr := p.resolver.For(cluster)
		if runtimeErr != nil || runtime == nil {
			failures = append(failures, fmt.Errorf("集群 %d 连接读取失败", cluster.ID))
		}
		observations := ObserveDeliveryDriftTargets(ctx, p.store, cluster.ID, batch, driftPollerIdentity, DriftBackgroundSource,
			func(ctx context.Context, app App) (string, error) {
				if runtimeErr != nil || runtime == nil {
					return "", fmt.Errorf("集群读取能力不可用")
				}
				readCtx, cancel := context.WithTimeout(ctx, driftReadTimeout)
				defer cancel()
				return runtime.CurrentImage(readCtx, app.Namespace, app.DeploymentName, app.ContainerName)
			})
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := p.store.ObserveDeliveryDrift(observations); err != nil {
			failures = append(failures, fmt.Errorf("集群 %d 漂移记录保存失败: %w", cluster.ID, err))
			continue
		}
		p.mu.Lock()
		p.targetCursor[cluster.ID] = nextAppID
		p.mu.Unlock()
	}
	return errors.Join(failures...)
}
