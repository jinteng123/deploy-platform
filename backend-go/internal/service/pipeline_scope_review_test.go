package service

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDeliveryPlanPipelineScopeOnlyPinsMatchingReviewWithoutGrantingExecution(t *testing.T) {
	binding := DeliveryBinding{ID: 41, DeploymentPipeline: &YunxiaoDeploymentPipelineBinding{ConnectionID: 9, PipelineID: "123456"}}
	latest := DeliveryPipelineScopeReview{
		ID: 3, BindingID: 41, ConnectionID: 9, PipelineID: "654321",
		DefinitionHash: strings.Repeat("a", 64), WriteScope: PipelineScopeConfigurationOnly,
		Reviewer: "operator", ReviewedAt: time.Now().UTC(),
	}
	older := latest
	older.ID = 2
	older.PipelineID = "123456"
	if review := matchingDeploymentPipelineScopeReview(DeliveryModel{PipelineScopeReviews: []DeliveryPipelineScopeReview{older, latest}}, binding); review != nil {
		t.Fatalf("review for a different pipeline was accepted: %+v", review)
	}
	if check := deploymentPipelineScopeCheck(binding.DeploymentPipeline, nil); check.Status != "unknown" || !strings.Contains(check.Detail, "保持关闭") {
		t.Fatalf("unreviewed pipeline was treated as ready: %+v", check)
	}
	latest.PipelineID = "123456"
	latest.WriteScope = PipelineScopeWorkloadOrImage
	review := matchingDeploymentPipelineScopeReview(DeliveryModel{PipelineScopeReviews: []DeliveryPipelineScopeReview{latest}}, binding)
	if review == nil || deploymentPipelineScopeCheck(binding.DeploymentPipeline, review).Status != "unknown" {
		t.Fatalf("workload-writing pipeline was treated as a config-only recovery path: %+v", review)
	}
	latest.WriteScope = PipelineScopeConfigurationOnly
	review = matchingDeploymentPipelineScopeReview(DeliveryModel{PipelineScopeReviews: []DeliveryPipelineScopeReview{latest}}, binding)
	check := deploymentPipelineScopeCheck(binding.DeploymentPipeline, review)
	if check.Status != "review" || !strings.Contains(check.Detail, "不能据此自动触发") {
		t.Fatalf("human config-only review was treated as execution proof: %+v", check)
	}
	latest.DefinitionHash = "not-a-sha256"
	if malformed := matchingDeploymentPipelineScopeReview(DeliveryModel{PipelineScopeReviews: []DeliveryPipelineScopeReview{latest}}, binding); malformed != nil {
		t.Fatalf("malformed persisted review was accepted: %+v", malformed)
	}
}

func TestRecordDeliveryPipelineScopeReviewIsBoundToExactPipelineAndAuditable(t *testing.T) {
	store := NewStore()
	store.LoadDeliveryModel(DeliveryModel{Bindings: []DeliveryBinding{{
		ID: 41, AppID: 2,
		DeploymentPipeline: &YunxiaoDeploymentPipelineBinding{ConnectionID: 9, PipelineID: "123456"},
	}}})
	hash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	first, err := store.RecordDeliveryPipelineScopeReview(DeliveryPipelineScopeReview{
		BindingID: 41, ConnectionID: 9, PipelineID: "123456", DefinitionHash: hash,
		WriteScope: PipelineScopeConfigurationOnly, Reviewer: "operator-a",
	})
	if err != nil || first.ID != 1 || first.ReviewedAt.IsZero() {
		t.Fatalf("first scope review=%+v err=%v", first, err)
	}
	second, err := store.RecordDeliveryPipelineScopeReview(DeliveryPipelineScopeReview{
		BindingID: 41, ConnectionID: 9, PipelineID: "123456", DefinitionHash: hash,
		WriteScope: PipelineScopeUnknown, Reviewer: "operator-b", ReviewedAt: time.Now().UTC(),
	})
	if err != nil || second.ID != 2 {
		t.Fatalf("second scope review=%+v err=%v", second, err)
	}
	model := store.DeliveryModel()
	if len(model.PipelineScopeReviews) != 1 || model.PipelineScopeReviews[0].ID != second.ID || model.PipelineScopeReviews[0].WriteScope != PipelineScopeUnknown {
		t.Fatalf("delivery model should expose only the latest review per binding: %+v", model.PipelineScopeReviews)
	}
	model.PipelineScopeReviews[0].WriteScope = PipelineScopeWorkloadOrImage
	if store.DeliveryModel().PipelineScopeReviews[0].WriteScope != PipelineScopeUnknown {
		t.Fatal("review model leaked mutable state")
	}

	store.LoadDeliveryModel(DeliveryModel{Bindings: []DeliveryBinding{{
		ID: 41, AppID: 2,
		DeploymentPipeline: &YunxiaoDeploymentPipelineBinding{ConnectionID: 9, PipelineID: "654321"},
	}}})
	if _, err := store.RecordDeliveryPipelineScopeReview(DeliveryPipelineScopeReview{
		BindingID: 41, ConnectionID: 9, PipelineID: "123456", DefinitionHash: hash,
		WriteScope: PipelineScopeUnknown, Reviewer: "operator-a",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("review of a no-longer-bound pipeline must conflict: %v", err)
	}
}

func TestRecordDeliveryPipelineScopeReviewRejectsInvalidClaims(t *testing.T) {
	store := NewStore()
	store.LoadDeliveryModel(DeliveryModel{Bindings: []DeliveryBinding{{
		ID: 41, AppID: 2,
		DeploymentPipeline: &YunxiaoDeploymentPipelineBinding{ConnectionID: 9, PipelineID: "123456"},
	}}})
	base := DeliveryPipelineScopeReview{
		BindingID: 41, ConnectionID: 9, PipelineID: "123456",
		DefinitionHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		WriteScope:     PipelineScopeUnknown, Reviewer: "operator-a",
	}
	for name, update := range map[string]func(*DeliveryPipelineScopeReview){
		"invalid hash":     func(row *DeliveryPipelineScopeReview) { row.DefinitionHash = "abc" },
		"invalid decision": func(row *DeliveryPipelineScopeReview) { row.WriteScope = "read_only" },
		"missing reviewer": func(row *DeliveryPipelineScopeReview) { row.Reviewer = " " },
	} {
		t.Run(name, func(t *testing.T) {
			row := base
			update(&row)
			if _, err := store.RecordDeliveryPipelineScopeReview(row); !errors.Is(err, ErrValidation) {
				t.Fatalf("invalid review accepted: %v", err)
			}
		})
	}
}
