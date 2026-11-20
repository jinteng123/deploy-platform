package service

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	PipelineScopeConfigurationOnly = "configuration_only"
	PipelineScopeWorkloadOrImage   = "workload_or_image"
	PipelineScopeUnknown           = "unknown"
)

var pipelineScopeHashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// DeliveryPipelineScopeReview is an operator's statement about one exact
// Yunxiao pipeline definition. It is an audit record, never an execution grant.
type DeliveryPipelineScopeReview struct {
	ID             int64     `json:"id"`
	BindingID      int64     `json:"binding_id"`
	ConnectionID   int64     `json:"connection_id"`
	PipelineID     string    `json:"pipeline_id"`
	DefinitionHash string    `json:"definition_hash"`
	WriteScope     string    `json:"write_scope"`
	Reviewer       string    `json:"reviewer"`
	ReviewedAt     time.Time `json:"reviewed_at"`
}

// DeliveryPipelineScopeReviewPersistence appends a human review after checking
// that the environment binding still points at the reviewed pipeline.
type DeliveryPipelineScopeReviewPersistence interface {
	SaveDeliveryPipelineScopeReview(DeliveryPipelineScopeReview) (DeliveryPipelineScopeReview, error)
}

func (s *Store) RecordDeliveryPipelineScopeReview(review DeliveryPipelineScopeReview) (DeliveryPipelineScopeReview, error) {
	review.PipelineID = strings.TrimSpace(review.PipelineID)
	review.Reviewer = strings.TrimSpace(review.Reviewer)
	if review.BindingID <= 0 || review.ConnectionID <= 0 || !runnerPipelineID.MatchString(review.PipelineID) ||
		!pipelineScopeHashPattern.MatchString(review.DefinitionHash) || review.Reviewer == "" || len([]rune(review.Reviewer)) > 120 {
		return DeliveryPipelineScopeReview{}, NewValidationError("流水线范围核对记录无效")
	}
	if review.WriteScope != PipelineScopeConfigurationOnly && review.WriteScope != PipelineScopeWorkloadOrImage && review.WriteScope != PipelineScopeUnknown {
		return DeliveryPipelineScopeReview{}, NewValidationError("请选择有效的流水线写入范围结论")
	}
	if review.ReviewedAt.IsZero() {
		review.ReviewedAt = time.Now().UTC()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	var binding *DeliveryBinding
	for i := range s.delivery.Bindings {
		if s.delivery.Bindings[i].ID == review.BindingID {
			binding = &s.delivery.Bindings[i]
			break
		}
	}
	if binding == nil {
		return DeliveryPipelineScopeReview{}, ErrNotFound
	}
	if binding.DeploymentPipeline == nil || binding.DeploymentPipeline.ConnectionID != review.ConnectionID || binding.DeploymentPipeline.PipelineID != review.PipelineID {
		return DeliveryPipelineScopeReview{}, newConflict("环境绑定的配置流水线已变化，请刷新后重新核对")
	}
	if s.persistence != nil {
		repo, ok := s.persistence.(DeliveryPipelineScopeReviewPersistence)
		if !ok {
			return DeliveryPipelineScopeReview{}, fmt.Errorf("持久化存储不支持配置流水线核对记录")
		}
		saved, err := repo.SaveDeliveryPipelineScopeReview(review)
		if err != nil {
			return DeliveryPipelineScopeReview{}, err
		}
		review = saved
	} else {
		review.ID = s.nextDeliveryPipelineScopeReview
		s.nextDeliveryPipelineScopeReview++
	}
	s.pipelineScopeReviews = append(s.pipelineScopeReviews, review)
	return review, nil
}

func latestPipelineScopeReviews(rows []DeliveryPipelineScopeReview) []DeliveryPipelineScopeReview {
	latest := make(map[int64]DeliveryPipelineScopeReview, len(rows))
	for _, row := range rows {
		current, ok := latest[row.BindingID]
		if !ok || row.ID > current.ID {
			latest[row.BindingID] = row
		}
	}
	result := make([]DeliveryPipelineScopeReview, 0, len(latest))
	for _, row := range latest {
		result = append(result, row)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].BindingID < result[j].BindingID })
	return result
}
