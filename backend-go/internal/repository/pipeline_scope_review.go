package repository

import (
	"database/sql"
	"deploy-platform/backend-go/internal/service"
	"errors"
	"fmt"
	"time"
)

func scanPipelineScopeReview(row rowScanner) (service.DeliveryPipelineScopeReview, error) {
	var review service.DeliveryPipelineScopeReview
	err := row.Scan(
		&review.ID, &review.BindingID, &review.ConnectionID, &review.PipelineID,
		&review.DefinitionHash, &review.WriteScope, &review.Reviewer, &review.ReviewedAt,
	)
	return review, err
}

func (m *MySQL) SaveDeliveryPipelineScopeReview(review service.DeliveryPipelineScopeReview) (service.DeliveryPipelineScopeReview, error) {
	tx, err := m.db.Begin()
	if err != nil {
		return review, err
	}
	defer tx.Rollback()

	var connectionID sql.NullInt64
	var pipelineID string
	err = tx.QueryRow(`SELECT deployment_pipeline_connection_id,deployment_pipeline_id
FROM delivery_bindings WHERE id=? FOR UPDATE`, review.BindingID).Scan(&connectionID, &pipelineID)
	if errors.Is(err, sql.ErrNoRows) {
		return review, service.ErrNotFound
	}
	if err != nil {
		return review, err
	}
	if !connectionID.Valid || connectionID.Int64 != review.ConnectionID || pipelineID != review.PipelineID {
		return review, fmt.Errorf("%w: 环境绑定的配置流水线已变化，请刷新后重新核对", service.ErrConflict)
	}
	if review.ReviewedAt.IsZero() {
		review.ReviewedAt = time.Now().UTC()
	}
	result, err := tx.Exec(`INSERT INTO delivery_pipeline_scope_reviews
(delivery_binding_id,connection_id,pipeline_id,definition_hash,write_scope,reviewer,reviewed_at)
VALUES(?,?,?,?,?,?,?)`, review.BindingID, review.ConnectionID, review.PipelineID, review.DefinitionHash,
		review.WriteScope, review.Reviewer, review.ReviewedAt)
	if err != nil {
		return review, wrapConflict(err)
	}
	review.ID, err = result.LastInsertId()
	if err != nil {
		return review, err
	}
	if err := tx.Commit(); err != nil {
		return review, err
	}
	return review, nil
}

func (m *MySQL) LatestDeliveryPipelineScopeReviews() ([]service.DeliveryPipelineScopeReview, error) {
	rows, err := m.db.Query(`SELECT r.id,r.delivery_binding_id,r.connection_id,r.pipeline_id,r.definition_hash,r.write_scope,r.reviewer,r.reviewed_at
FROM delivery_pipeline_scope_reviews r
JOIN (
  SELECT delivery_binding_id,MAX(id) AS latest_id
  FROM delivery_pipeline_scope_reviews
  GROUP BY delivery_binding_id
) latest ON latest.latest_id=r.id
ORDER BY r.delivery_binding_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	reviews := []service.DeliveryPipelineScopeReview{}
	for rows.Next() {
		review, err := scanPipelineScopeReview(rows)
		if err != nil {
			return nil, err
		}
		reviews = append(reviews, review)
	}
	return reviews, rows.Err()
}
