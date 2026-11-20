package service

import (
	"context"
	"fmt"
	"time"

	"deploy-platform/backend-go/internal/kube"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RebindDeliveryBindingWorkloadRef reads the app's configured Deployment and
// explicitly adopts its current UID as the active reference. It never writes
// Kubernetes resources and refuses to run while either old or new Deployment
// is owned by a delivery task.
func (s *CDService) RebindDeliveryBindingWorkloadRef(ctx context.Context, appID int) (DeliveryBinding, error) {
	if s == nil || s.store == nil || s.worker == nil || s.worker.pipeline == nil || s.worker.pipeline.k8s == nil {
		return DeliveryBinding{}, NewValidationError("发布运行时不可用，无法重新读取 Deployment")
	}
	if appID <= 0 {
		return DeliveryBinding{}, NewValidationError("应用 ID 无效")
	}
	app, err := s.store.FindApp(appID)
	if err != nil {
		return DeliveryBinding{}, err
	}
	binding, err := s.store.DeliveryBindingForApp(app.ID)
	if err != nil {
		return DeliveryBinding{}, NewValidationError("请先完成组件与环境绑定")
	}
	cluster, err := s.store.FindCluster(app.ClusterID)
	if err != nil {
		return DeliveryBinding{}, err
	}
	if app.Namespace == "" || app.DeploymentName == "" {
		return DeliveryBinding{}, NewValidationError("应用绑定缺少 namespace 或 Deployment 名称")
	}
	checked := map[string]bool{}
	for _, ref := range []DeliveryWorkloadReference{
		binding.ActiveWorkloadRef,
		{ClusterID: app.ClusterID, Namespace: app.Namespace, Kind: "Deployment", Name: app.DeploymentName},
	} {
		if ref.Name == "" || ref.Namespace == "" || ref.ClusterID <= 0 {
			continue
		}
		lock := ResourceLock{ClusterID: ref.ClusterID, Namespace: ref.Namespace, Kind: "deployment", Name: ref.Name}
		if checked[lock.Identity()] {
			continue
		}
		checked[lock.Identity()] = true
		if err := s.store.CheckResourceLock(lock); err != nil {
			return DeliveryBinding{}, err
		}
	}
	runtime, err := s.worker.pipeline.k8s.For(cluster)
	if err != nil {
		return DeliveryBinding{}, err
	}
	def, err := kube.LookupResource("apps", "v1", "deployments")
	if err != nil {
		return DeliveryBinding{}, err
	}
	readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	object, err := runtime.GetResource(readCtx, def, app.Namespace, app.DeploymentName, metav1.GetOptions{})
	if err != nil {
		return DeliveryBinding{}, fmt.Errorf("读取当前 Deployment 身份失败: %w", err)
	}
	if object == nil || object.GetName() != app.DeploymentName || object.GetNamespace() != app.Namespace || object.GetUID() == "" {
		return DeliveryBinding{}, NewValidationError("当前 Deployment 不存在或缺少可确认的 UID")
	}
	observed := DeliveryWorkloadReference{
		ClusterID: app.ClusterID, Namespace: app.Namespace, Kind: "Deployment", Name: app.DeploymentName,
		UID: string(object.GetUID()), ResourceVersion: object.GetResourceVersion(),
	}
	return s.store.RebindDeliveryBindingWorkloadRef(binding.ID, observed)
}
