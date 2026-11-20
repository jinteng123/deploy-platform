package httpapi

import (
	"context"

	"deploy-platform/backend-go/internal/service"
)

const maxConcurrentDeliveryDriftReads = service.MaxConcurrentDeliveryDriftReads

type deliveryDriftImageReader = service.DeliveryDriftImageReader

// Keep the HTTP endpoint and background observer on the same read semantics.
func observeDeliveryDriftTargets(
	ctx context.Context,
	store *service.Store,
	clusterID int,
	apps []service.App,
	observedBy string,
	readImage deliveryDriftImageReader,
) []service.DeliveryDriftObservation {
	return service.ObserveDeliveryDriftTargets(ctx, store, clusterID, apps, observedBy, service.DriftObservationSource, readImage)
}
