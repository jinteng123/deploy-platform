package service

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// ResourceLock identifies one Kubernetes resource that a delivery operation
// owns while it is active. A run may hold several locks (for example a
// candidate Deployment, a Service, and a gateway route).
type ResourceLock struct {
	ClusterID int
	Namespace string
	Kind      string
	Name      string
}

func (r ResourceLock) Identity() string {
	return fmt.Sprintf("%d\x00%s\x00%s\x00%s", r.ClusterID, r.Namespace, strings.ToLower(r.Kind), r.Name)
}

func (r ResourceLock) Validate() error {
	if r.ClusterID <= 0 || strings.TrimSpace(r.Namespace) == "" || strings.TrimSpace(r.Kind) == "" || strings.TrimSpace(r.Name) == "" {
		return NewValidationError("资源锁必须包含有效的集群、namespace、kind 和名称")
	}
	return nil
}

func NormalizeResourceLocks(locks []ResourceLock) ([]ResourceLock, error) {
	unique := make(map[string]ResourceLock, len(locks))
	for _, lock := range locks {
		lock.Namespace = strings.TrimSpace(lock.Namespace)
		lock.Kind = strings.ToLower(strings.TrimSpace(lock.Kind))
		lock.Name = strings.TrimSpace(lock.Name)
		if err := lock.Validate(); err != nil {
			return nil, err
		}
		unique[lock.Identity()] = lock
	}
	result := make([]ResourceLock, 0, len(unique))
	for _, lock := range unique {
		result = append(result, lock)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Identity() < result[j].Identity() })
	return result, nil
}

func workloadResourceLock(l DeployLog) ResourceLock {
	return ResourceLock{ClusterID: l.ClusterID, Namespace: l.Namespace, Kind: "deployment", Name: l.DeploymentName}
}

// DeployTaskPersistence atomically creates a deploy log and acquires every
// resource lock. It is optional to keep lightweight in-memory test stores and
// older adapters source-compatible; production MySQL implements it.
type DeployTaskPersistence interface {
	CreateDeployLog(DeployLog, []ResourceLock) (DeployLog, error)
}

// ResourceLockReader lets existing Kubernetes write routes consult the same
// database ownership index used when delivery tasks are created.
type ResourceLockReader interface {
	ResourceLockOwner(ResourceLock) (int64, bool, error)
}

// ResourceLockLeasePersistence temporarily reserves a set of resources for a
// bounded operator action. Unlike an active delivery lock, a lease expires if
// the process dies before it can release the reservation.
type ResourceLockLeasePersistence interface {
	ReserveResourceLockLease(int64, []ResourceLock, time.Time) error
	ReleaseResourceLockLease(int64, []ResourceLock) error
}

// LockedPromotionPersistence atomically approves a request, creates its task,
// and reserves every resource required by the strategy. Production MySQL uses
// this extension; the legacy Persistence method remains for existing adapters.
type LockedPromotionPersistence interface {
	ApprovePromotionWithLocks(PromotionRequest, DeployLog, []ResourceLock) (PromotionRequest, ImageRelease, DeployLog, error)
}
