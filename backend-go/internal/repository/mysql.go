package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"deploy-platform/backend-go/internal/service"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

type MySQL struct{ db *sql.DB }

func Open(dsn string) (*MySQL, error) {
	settings, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, err
	}
	// Bound both startup and execution-state writes when the DB is unreachable
	// or accepts a connection without answering. Explicit non-zero DSN values win.
	// The default Go decoder uses UTC. Match SQL NOW/default DATETIME writes
	// to it, while preserving an explicitly configured DSN time zone/location.
	if settings.Loc == time.UTC && settings.Params["time_zone"] == "" {
		if settings.Params == nil {
			settings.Params = map[string]string{}
		}
		settings.Params["time_zone"] = "'+00:00'"
	}
	if settings.Timeout == 0 {
		settings.Timeout = 5 * time.Second
	}
	if settings.ReadTimeout == 0 {
		settings.ReadTimeout = 15 * time.Second
	}
	if settings.WriteTimeout == 0 {
		settings.WriteTimeout = 15 * time.Second
	}
	db, err := sql.Open("mysql", settings.FormatDSN())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(3 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return &MySQL{db: db}, nil
}

func (m *MySQL) Close() error { return m.db.Close() }

// Ping reports whether the persistent store is currently reachable. It is
// used by readiness probes so a database outage drains traffic without
// causing the process liveness probe to restart the application.
func (m *MySQL) Ping(ctx context.Context) error { return m.db.PingContext(ctx) }

func (m *MySQL) Clusters() ([]service.Cluster, error) {
	rows, err := m.db.Query(`SELECT id,name,env_code,environment,environment_tier,deploy_policy,connection_type,api_server,credential_ciphertext,ca_cert,insecure_skip_tls_verify,enabled,k8s_master_host,k8s_master_port,k8s_master_user,k8s_master_key_path,default_namespace,description,created_at,updated_at FROM clusters ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []service.Cluster
	for rows.Next() {
		var v service.Cluster
		var apiServer, cred, caCert, keyPath, desc sql.NullString
		if err := rows.Scan(&v.ID, &v.Name, &v.EnvCode, &v.Environment, &v.EnvironmentTier, &v.DeployPolicy, &v.ConnectionType,
			&apiServer, &cred, &caCert, &v.InsecureSkipTLSVerify, &v.Enabled,
			&v.K8sMasterHost, &v.K8sMasterPort, &v.K8sMasterUser, &keyPath, &v.DefaultNamespace, &desc,
			&v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		v.APIServer = nullStr(apiServer)
		v.CredentialCiphertext = nullStr(cred)
		v.CACert = nullStr(caCert)
		v.K8sMasterKeyPath = nullStr(keyPath)
		v.Description = nullStr(desc)
		result = append(result, v)
	}
	return result, rows.Err()
}

func (m *MySQL) Apps(clusterID int) ([]service.App, error) {
	query := `SELECT id,cluster_id,app_name,match_keyword,deployment_name,container_name,namespace,harbor_project,description,created_at,source_repo_url,source_path,image_repository,source_kind,deployment_config_repo_url,deployment_config_path FROM apps`
	args := []any{}
	if clusterID > 0 {
		query += ` WHERE cluster_id=?`
		args = append(args, clusterID)
	}
	query += ` ORDER BY id`
	rows, err := m.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []service.App
	for rows.Next() {
		var v service.App
		var desc sql.NullString
		if err := rows.Scan(&v.ID, &v.ClusterID, &v.AppName, &v.MatchKeyword, &v.DeploymentName, &v.ContainerName, &v.Namespace, &v.HarborProject, &desc, &v.CreatedAt, &v.SourceRepoURL, &v.SourcePath, &v.ImageRepository, &v.SourceKind, &v.DeploymentConfigRepoURL, &v.DeploymentConfigPath); err != nil {
			return nil, err
		}
		v.Description = nullStr(desc)
		result = append(result, v)
	}
	return result, rows.Err()
}

func (m *MySQL) DeliveryModel() (service.DeliveryModel, error) {
	model := service.DeliveryModel{
		Environments: []service.DeliveryEnvironment{},
		Applications: []service.DeliveryApplication{},
		Components:   []service.DeliveryComponent{},
		Bindings:     []service.DeliveryBinding{},
	}
	rows, err := m.db.Query(`SELECT id,code,name,environment_tier,legacy_cluster_id,requires_review,created_at FROM delivery_environments ORDER BY name,id`)
	if err != nil {
		return model, err
	}
	for rows.Next() {
		var v service.DeliveryEnvironment
		var clusterID sql.NullInt64
		if err := rows.Scan(&v.ID, &v.Code, &v.Name, &v.Tier, &clusterID, &v.RequiresReview, &v.CreatedAt); err != nil {
			rows.Close()
			return model, err
		}
		if clusterID.Valid {
			id := int(clusterID.Int64)
			v.LegacyClusterID = &id
		}
		model.Environments = append(model.Environments, v)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return model, err
	}
	rows.Close()
	rows, err = m.db.Query(`SELECT id,name,legacy_app_id,requires_review,created_at FROM delivery_applications ORDER BY name,id`)
	if err != nil {
		return model, err
	}
	for rows.Next() {
		var v service.DeliveryApplication
		var legacyID sql.NullInt64
		if err := rows.Scan(&v.ID, &v.Name, &legacyID, &v.RequiresReview, &v.CreatedAt); err != nil {
			rows.Close()
			return model, err
		}
		if legacyID.Valid {
			id := int(legacyID.Int64)
			v.LegacyAppID = &id
		}
		model.Applications = append(model.Applications, v)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return model, err
	}
	rows.Close()
	rows, err = m.db.Query(`SELECT id,application_id,name,legacy_app_id,requires_review,created_at FROM delivery_components ORDER BY name,id`)
	if err != nil {
		return model, err
	}
	for rows.Next() {
		var v service.DeliveryComponent
		var legacyID sql.NullInt64
		if err := rows.Scan(&v.ID, &v.ApplicationID, &v.Name, &legacyID, &v.RequiresReview, &v.CreatedAt); err != nil {
			rows.Close()
			return model, err
		}
		if legacyID.Valid {
			id := int(legacyID.Int64)
			v.LegacyAppID = &id
		}
		model.Components = append(model.Components, v)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return model, err
	}
	rows.Close()
	rows, err = m.db.Query(`SELECT ` + deliveryBindingColumns + ` FROM delivery_bindings ORDER BY id`)
	if err != nil {
		return model, err
	}
	for rows.Next() {
		v, err := scanDeliveryBinding(rows)
		if err != nil {
			return model, err
		}
		model.Bindings = append(model.Bindings, v)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return model, err
	}
	if err := rows.Close(); err != nil {
		return model, err
	}
	model.PipelineScopeReviews, err = m.LatestDeliveryPipelineScopeReviews()
	if err != nil {
		return model, err
	}
	return model, nil
}

const deliveryPolicyColumns = `binding_id,version,strategy,service_name,candidate_retention_hours,canary_analysis_json,created_by,created_at`

func scanDeliveryPolicy(row rowScanner) (service.DeliveryPolicyVersion, error) {
	var policy service.DeliveryPolicyVersion
	var canaryJSON []byte
	err := row.Scan(&policy.BindingID, &policy.Version, &policy.Strategy, &policy.ServiceName, &policy.CandidateRetentionHours, &canaryJSON, &policy.CreatedBy, &policy.CreatedAt)
	if err != nil {
		return service.DeliveryPolicyVersion{}, err
	}
	if len(canaryJSON) > 0 {
		var canary service.CanaryAnalysisPolicy
		if err := json.Unmarshal(canaryJSON, &canary); err != nil {
			return service.DeliveryPolicyVersion{}, fmt.Errorf("解析金丝雀策略配置失败: %w", err)
		}
		policy.CanaryAnalysis = &canary
	}
	return policy, nil
}

func deliveryPolicyCanaryJSON(policy service.DeliveryPolicyVersion) (any, error) {
	if policy.CanaryAnalysis == nil {
		return nil, nil
	}
	return json.Marshal(policy.CanaryAnalysis)
}

func (m *MySQL) DeliveryApprovalGrants() ([]service.DeliveryApprovalGrant, error) {
	rows, err := m.db.Query(`SELECT g.user_id,u.username,g.application_id,a.name,g.environment_id,e.name,g.granted_by,g.created_at
		FROM delivery_approval_grants g
		JOIN users u ON u.id=g.user_id
		JOIN delivery_applications a ON a.id=g.application_id
		JOIN delivery_environments e ON e.id=g.environment_id
		ORDER BY u.username,a.name,e.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]service.DeliveryApprovalGrant, 0)
	for rows.Next() {
		var grant service.DeliveryApprovalGrant
		if err := rows.Scan(&grant.UserID, &grant.Username, &grant.ApplicationID, &grant.ApplicationName,
			&grant.EnvironmentID, &grant.EnvironmentName, &grant.GrantedBy, &grant.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, grant)
	}
	return result, rows.Err()
}

func (m *MySQL) ReplaceDeliveryApprovalGrants(userID int64, grants []service.DeliveryApprovalGrant, actor string) error {
	tx, err := m.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM delivery_approval_grants WHERE user_id=?`, userID); err != nil {
		return err
	}
	for _, grant := range grants {
		if _, err := tx.Exec(`INSERT INTO delivery_approval_grants (user_id,application_id,environment_id,granted_by,created_at) VALUES (?,?,?,?,?)`,
			userID, grant.ApplicationID, grant.EnvironmentID, actor, grant.CreatedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (m *MySQL) DeliveryPolicyVersions() ([]service.DeliveryPolicyVersion, error) {
	rows, err := m.db.Query(`SELECT ` + deliveryPolicyColumns + ` FROM delivery_policy_versions ORDER BY binding_id,version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]service.DeliveryPolicyVersion, 0)
	for rows.Next() {
		row, err := scanDeliveryPolicy(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (m *MySQL) SaveDeliveryPolicyVersion(expectedVersion int64, policy service.DeliveryPolicyVersion) (service.DeliveryPolicyVersion, bool, error) {
	tx, err := m.db.Begin()
	if err != nil {
		return service.DeliveryPolicyVersion{}, false, err
	}
	defer tx.Rollback()
	var requiresReview bool
	if err := tx.QueryRow(`SELECT requires_review FROM delivery_bindings WHERE id=? FOR UPDATE`, policy.BindingID).Scan(&requiresReview); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return service.DeliveryPolicyVersion{}, false, service.ErrNotFound
		}
		return service.DeliveryPolicyVersion{}, false, err
	}
	if requiresReview {
		return service.DeliveryPolicyVersion{}, false, service.NewValidationError("请先完成组件与环境绑定核对，再配置发布策略")
	}
	var current service.DeliveryPolicyVersion
	current, err = scanDeliveryPolicy(tx.QueryRow(`SELECT `+deliveryPolicyColumns+`
FROM delivery_policy_versions WHERE binding_id=? ORDER BY version DESC LIMIT 1 FOR UPDATE`, policy.BindingID))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return service.DeliveryPolicyVersion{}, false, err
	}
	if current.Version != expectedVersion {
		return service.DeliveryPolicyVersion{}, false, fmt.Errorf("%w: 策略已更新到 v%d，请刷新后重试", service.ErrConflict, current.Version)
	}
	if current.Version > 0 && sameRepositoryDeliveryPolicy(current, policy) {
		if err := tx.Commit(); err != nil {
			return service.DeliveryPolicyVersion{}, false, err
		}
		return current, false, nil
	}
	policy.Version = current.Version + 1
	canaryJSON, err := deliveryPolicyCanaryJSON(policy)
	if err != nil {
		return service.DeliveryPolicyVersion{}, false, err
	}
	_, err = tx.Exec(`INSERT INTO delivery_policy_versions(binding_id,version,strategy,service_name,candidate_retention_hours,canary_analysis_json,created_by,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		policy.BindingID, policy.Version, policy.Strategy, policy.ServiceName, policy.CandidateRetentionHours, canaryJSON, policy.CreatedBy, policy.CreatedAt)
	if err != nil {
		return service.DeliveryPolicyVersion{}, false, wrapConflict(err)
	}
	if err := tx.Commit(); err != nil {
		return service.DeliveryPolicyVersion{}, false, err
	}
	return policy, true, nil
}

func sameRepositoryDeliveryPolicy(a, b service.DeliveryPolicyVersion) bool {
	return a.Strategy == b.Strategy && a.ServiceName == b.ServiceName && a.CandidateRetentionHours == b.CandidateRetentionHours && reflect.DeepEqual(a.CanaryAnalysis, b.CanaryAnalysis)
}

const deliveryEnvironmentEvidencePolicyColumns = `environment_id,version,required_verification_kinds,max_evidence_age_minutes,trusted_submitters,created_by,created_at`

func scanDeliveryEnvironmentEvidencePolicy(row rowScanner) (service.DeliveryEnvironmentEvidencePolicyVersion, error) {
	var policy service.DeliveryEnvironmentEvidencePolicyVersion
	var kindsJSON, submittersJSON []byte
	if err := row.Scan(&policy.EnvironmentID, &policy.Version, &kindsJSON, &policy.MaxEvidenceAgeMinutes, &submittersJSON, &policy.CreatedBy, &policy.CreatedAt); err != nil {
		return service.DeliveryEnvironmentEvidencePolicyVersion{}, err
	}
	if err := json.Unmarshal(kindsJSON, &policy.RequiredVerificationKinds); err != nil {
		return service.DeliveryEnvironmentEvidencePolicyVersion{}, fmt.Errorf("解析环境必需验证类型失败: %w", err)
	}
	if err := json.Unmarshal(submittersJSON, &policy.TrustedSubmitters); err != nil {
		return service.DeliveryEnvironmentEvidencePolicyVersion{}, fmt.Errorf("解析环境可信提交身份失败: %w", err)
	}
	return policy, nil
}

func (m *MySQL) DeliveryEnvironmentEvidencePolicyVersions() ([]service.DeliveryEnvironmentEvidencePolicyVersion, error) {
	rows, err := m.db.Query(`SELECT ` + deliveryEnvironmentEvidencePolicyColumns + ` FROM delivery_environment_evidence_policy_versions ORDER BY environment_id,version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]service.DeliveryEnvironmentEvidencePolicyVersion, 0)
	for rows.Next() {
		policy, err := scanDeliveryEnvironmentEvidencePolicy(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, policy)
	}
	return result, rows.Err()
}

func (m *MySQL) SaveDeliveryEnvironmentEvidencePolicyVersion(expectedVersion int64, policy service.DeliveryEnvironmentEvidencePolicyVersion) (service.DeliveryEnvironmentEvidencePolicyVersion, bool, error) {
	tx, err := m.db.Begin()
	if err != nil {
		return service.DeliveryEnvironmentEvidencePolicyVersion{}, false, err
	}
	defer tx.Rollback()
	var tier string
	var requiresReview bool
	if err := tx.QueryRow(`SELECT environment_tier,requires_review FROM delivery_environments WHERE id=? FOR UPDATE`, policy.EnvironmentID).Scan(&tier, &requiresReview); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return service.DeliveryEnvironmentEvidencePolicyVersion{}, false, service.ErrNotFound
		}
		return service.DeliveryEnvironmentEvidencePolicyVersion{}, false, err
	}
	if tier != "production" {
		return service.DeliveryEnvironmentEvidencePolicyVersion{}, false, service.NewValidationError("仅生产环境需要配置晋级证据策略")
	}
	if requiresReview {
		return service.DeliveryEnvironmentEvidencePolicyVersion{}, false, service.NewValidationError("请先核对并确认逻辑环境")
	}
	var current service.DeliveryEnvironmentEvidencePolicyVersion
	current, err = scanDeliveryEnvironmentEvidencePolicy(tx.QueryRow(`SELECT `+deliveryEnvironmentEvidencePolicyColumns+`
FROM delivery_environment_evidence_policy_versions WHERE environment_id=? ORDER BY version DESC LIMIT 1 FOR UPDATE`, policy.EnvironmentID))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return service.DeliveryEnvironmentEvidencePolicyVersion{}, false, err
	}
	if current.Version != expectedVersion {
		return service.DeliveryEnvironmentEvidencePolicyVersion{}, false, fmt.Errorf("%w: 环境证据策略已更新到 v%d，请刷新后重试", service.ErrConflict, current.Version)
	}
	if current.Version > 0 && currentEvidencePolicyMatches(current, policy) {
		if err := tx.Commit(); err != nil {
			return service.DeliveryEnvironmentEvidencePolicyVersion{}, false, err
		}
		return current, false, nil
	}
	policy.Version = current.Version + 1
	kindsJSON, err := json.Marshal(policy.RequiredVerificationKinds)
	if err != nil {
		return service.DeliveryEnvironmentEvidencePolicyVersion{}, false, err
	}
	submittersJSON, err := json.Marshal(policy.TrustedSubmitters)
	if err != nil {
		return service.DeliveryEnvironmentEvidencePolicyVersion{}, false, err
	}
	_, err = tx.Exec(`INSERT INTO delivery_environment_evidence_policy_versions(environment_id,version,required_verification_kinds,max_evidence_age_minutes,trusted_submitters,created_by,created_at) VALUES(?,?,?,?,?,?,?)`,
		policy.EnvironmentID, policy.Version, kindsJSON, policy.MaxEvidenceAgeMinutes, submittersJSON, policy.CreatedBy, policy.CreatedAt)
	if err != nil {
		return service.DeliveryEnvironmentEvidencePolicyVersion{}, false, wrapConflict(err)
	}
	if err := tx.Commit(); err != nil {
		return service.DeliveryEnvironmentEvidencePolicyVersion{}, false, err
	}
	return policy, true, nil
}

func currentEvidencePolicyMatches(a, b service.DeliveryEnvironmentEvidencePolicyVersion) bool {
	return a.MaxEvidenceAgeMinutes == b.MaxEvidenceAgeMinutes &&
		reflect.DeepEqual(a.RequiredVerificationKinds, b.RequiredVerificationKinds) &&
		reflect.DeepEqual(a.TrustedSubmitters, b.TrustedSubmitters)
}

const deliveryBindingColumns = `id,app_id,application_id,component_id,environment_id,strategy_draft,service_name,deployment_pipeline_connection_id,deployment_pipeline_id,requires_review,created_at,
template_workload_cluster_id,template_workload_namespace,template_workload_kind,template_workload_name,template_workload_uid,template_workload_resource_version,
active_workload_cluster_id,active_workload_namespace,active_workload_kind,active_workload_name,active_workload_uid,active_workload_resource_version`

func scanDeliveryBinding(row rowScanner) (service.DeliveryBinding, error) {
	var binding service.DeliveryBinding
	var strategyDraft, deploymentPipelineID sql.NullString
	var deploymentPipelineConnectionID sql.NullInt64
	err := row.Scan(
		&binding.ID, &binding.AppID, &binding.ApplicationID, &binding.ComponentID, &binding.EnvironmentID,
		&strategyDraft, &binding.ServiceName, &deploymentPipelineConnectionID, &deploymentPipelineID, &binding.RequiresReview, &binding.CreatedAt,
		&binding.TemplateWorkloadRef.ClusterID, &binding.TemplateWorkloadRef.Namespace, &binding.TemplateWorkloadRef.Kind,
		&binding.TemplateWorkloadRef.Name, &binding.TemplateWorkloadRef.UID, &binding.TemplateWorkloadRef.ResourceVersion,
		&binding.ActiveWorkloadRef.ClusterID, &binding.ActiveWorkloadRef.Namespace, &binding.ActiveWorkloadRef.Kind,
		&binding.ActiveWorkloadRef.Name, &binding.ActiveWorkloadRef.UID, &binding.ActiveWorkloadRef.ResourceVersion,
	)
	if strategyDraft.Valid {
		binding.StrategyDraft = strategyDraft.String
	}
	if deploymentPipelineConnectionID.Valid && deploymentPipelineID.Valid && deploymentPipelineID.String != "" {
		binding.DeploymentPipeline = &service.YunxiaoDeploymentPipelineBinding{
			ConnectionID: deploymentPipelineConnectionID.Int64,
			PipelineID:   deploymentPipelineID.String,
		}
	}
	return binding, err
}

func (m *MySQL) SaveDeliveryEnvironment(v service.DeliveryEnvironment) (service.DeliveryEnvironment, error) {
	res, err := m.db.Exec(`INSERT INTO delivery_environments(code,name,environment_tier,requires_review) VALUES(?,?,?,0)`, v.Code, v.Name, v.Tier)
	if err != nil {
		return v, wrapConflict(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return v, err
	}
	v.ID = id
	v.RequiresReview = false
	v.CreatedAt = time.Now()
	return v, nil
}

func (m *MySQL) ConfirmDeliveryEnvironment(v service.DeliveryEnvironment) error {
	res, err := m.db.Exec(`UPDATE delivery_environments SET name=?,requires_review=0 WHERE id=?`, v.Name, v.ID)
	if err != nil {
		return wrapConflict(err)
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		var exists int
		if err := m.db.QueryRow(`SELECT 1 FROM delivery_environments WHERE id=?`, v.ID).Scan(&exists); err != nil {
			return err
		}
	}
	return nil
}

func (m *MySQL) SaveDeliveryApplication(v service.DeliveryApplication) (service.DeliveryApplication, error) {
	res, err := m.db.Exec(`INSERT INTO delivery_applications(name,requires_review) VALUES(?,0)`, v.Name)
	if err != nil {
		return v, wrapConflict(err)
	}
	v.ID, err = res.LastInsertId()
	if err != nil {
		return v, err
	}
	v.CreatedAt = time.Now()
	return v, nil
}

func (m *MySQL) SaveDeliveryComponent(v service.DeliveryComponent) (service.DeliveryComponent, error) {
	res, err := m.db.Exec(`INSERT INTO delivery_components(application_id,name,requires_review) VALUES(?,?,0)`, v.ApplicationID, v.Name)
	if err != nil {
		return v, wrapConflict(err)
	}
	v.ID, err = res.LastInsertId()
	if err != nil {
		return v, err
	}
	v.CreatedAt = time.Now()
	return v, nil
}

func (m *MySQL) AssignDeliveryBinding(v service.DeliveryBinding) (service.DeliveryBinding, error) {
	tx, err := m.db.Begin()
	if err != nil {
		return v, err
	}
	defer tx.Rollback()
	v, err = assignDeliveryBindingTx(tx, v)
	if err != nil {
		return v, err
	}
	if err := tx.Commit(); err != nil {
		return v, err
	}
	return v, nil
}

func (m *MySQL) AssignDeliveryBindingWithPolicy(v service.DeliveryBinding, policy service.DeliveryPolicyVersion, expectedVersion int64) (service.DeliveryBinding, service.DeliveryPolicyVersion, bool, error) {
	tx, err := m.db.Begin()
	if err != nil {
		return v, service.DeliveryPolicyVersion{}, false, err
	}
	defer tx.Rollback()
	v, err = assignDeliveryBindingTx(tx, v)
	if err != nil {
		return v, service.DeliveryPolicyVersion{}, false, err
	}
	policy.BindingID = v.ID
	var current service.DeliveryPolicyVersion
	current, err = scanDeliveryPolicy(tx.QueryRow(`SELECT `+deliveryPolicyColumns+`
FROM delivery_policy_versions WHERE binding_id=? ORDER BY version DESC LIMIT 1 FOR UPDATE`, v.ID))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return v, service.DeliveryPolicyVersion{}, false, err
	}
	if current.Version != expectedVersion {
		return v, service.DeliveryPolicyVersion{}, false, fmt.Errorf("%w: 策略已更新到 v%d，请刷新后重试", service.ErrConflict, current.Version)
	}
	if current.Version > 0 && sameRepositoryDeliveryPolicy(current, policy) {
		if err := tx.Commit(); err != nil {
			return v, service.DeliveryPolicyVersion{}, false, err
		}
		return v, current, false, nil
	}
	policy.Version = current.Version + 1
	if policy.CreatedAt.IsZero() {
		policy.CreatedAt = time.Now()
	}
	canaryJSON, err := deliveryPolicyCanaryJSON(policy)
	if err != nil {
		return v, service.DeliveryPolicyVersion{}, false, err
	}
	_, err = tx.Exec(`INSERT INTO delivery_policy_versions(binding_id,version,strategy,service_name,candidate_retention_hours,canary_analysis_json,created_by,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		policy.BindingID, policy.Version, policy.Strategy, policy.ServiceName, policy.CandidateRetentionHours, canaryJSON, policy.CreatedBy, policy.CreatedAt)
	if err != nil {
		return v, service.DeliveryPolicyVersion{}, false, wrapConflict(err)
	}
	if err := tx.Commit(); err != nil {
		return v, service.DeliveryPolicyVersion{}, false, err
	}
	return v, policy, true, nil
}

func assignDeliveryBindingTx(tx *sql.Tx, v service.DeliveryBinding) (service.DeliveryBinding, error) {
	var err error
	var clusterTier, environmentTier string
	var environmentNeedsReview bool
	var sourceKind, sourceRepoURL, sourcePath, imageRepository, deploymentConfigRepoURL, deploymentConfigPath string
	err = tx.QueryRow(`SELECT c.environment_tier,e.environment_tier,e.requires_review,a.source_kind,a.source_repo_url,a.source_path,a.image_repository,a.deployment_config_repo_url,a.deployment_config_path
FROM apps a JOIN clusters c ON c.id=a.cluster_id
JOIN delivery_environments e ON e.id=? WHERE a.id=? FOR UPDATE`, v.EnvironmentID, v.AppID).
		Scan(&clusterTier, &environmentTier, &environmentNeedsReview, &sourceKind, &sourceRepoURL, &sourcePath, &imageRepository, &deploymentConfigRepoURL, &deploymentConfigPath)
	if err != nil {
		return v, err
	}
	if environmentNeedsReview {
		return v, service.NewValidationError("请先核对并确认环境名称")
	}
	if clusterTier != environmentTier {
		return v, service.NewValidationError("环境类型与目标集群不一致；请核对集群环境")
	}
	var componentApplicationID int64
	if err := tx.QueryRow(`SELECT application_id FROM delivery_components WHERE id=? FOR UPDATE`, v.ComponentID).Scan(&componentApplicationID); err != nil {
		return v, err
	}
	if componentApplicationID != v.ApplicationID {
		return v, service.NewValidationError("请选择同一业务应用下的有效组件")
	}
	var mismatchedBindings int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM delivery_bindings b JOIN apps a ON a.id=b.app_id
		WHERE b.component_id=? AND b.app_id<>? AND
(a.source_kind<>? OR a.source_repo_url<>? OR a.source_path<>? OR a.image_repository<>? OR a.deployment_config_repo_url<>? OR a.deployment_config_path<>?)`,
		v.ComponentID, v.AppID, sourceKind, sourceRepoURL, sourcePath, imageRepository, deploymentConfigRepoURL, deploymentConfigPath).Scan(&mismatchedBindings); err != nil {
		return v, err
	}
	if mismatchedBindings != 0 {
		return v, service.NewValidationError("此组件已绑定不同的代码来源或镜像仓库；请分别建立发布组件")
	}
	var deploymentPipelineConnectionID any
	var deploymentPipelineID string
	if v.DeploymentPipeline != nil {
		deploymentPipelineConnectionID = v.DeploymentPipeline.ConnectionID
		deploymentPipelineID = v.DeploymentPipeline.PipelineID
	}
	_, err = tx.Exec(`INSERT INTO delivery_bindings(app_id,application_id,component_id,environment_id,strategy_draft,service_name,deployment_pipeline_connection_id,deployment_pipeline_id,requires_review,
template_workload_cluster_id,template_workload_namespace,template_workload_kind,template_workload_name,template_workload_uid,template_workload_resource_version,
active_workload_cluster_id,active_workload_namespace,active_workload_kind,active_workload_name,active_workload_uid,active_workload_resource_version)
VALUES(?,?,?,?,?,?,?,?,0,?,?,?,?,?,?, ?,?,?,?,?,?)
ON DUPLICATE KEY UPDATE application_id=VALUES(application_id),component_id=VALUES(component_id),environment_id=VALUES(environment_id),strategy_draft=VALUES(strategy_draft),service_name=VALUES(service_name),deployment_pipeline_connection_id=VALUES(deployment_pipeline_connection_id),deployment_pipeline_id=VALUES(deployment_pipeline_id),requires_review=0`,
		v.AppID, v.ApplicationID, v.ComponentID, v.EnvironmentID, emptyToNil(v.StrategyDraft), v.ServiceName, deploymentPipelineConnectionID, deploymentPipelineID,
		v.TemplateWorkloadRef.ClusterID, v.TemplateWorkloadRef.Namespace, v.TemplateWorkloadRef.Kind, v.TemplateWorkloadRef.Name, v.TemplateWorkloadRef.UID, v.TemplateWorkloadRef.ResourceVersion,
		v.ActiveWorkloadRef.ClusterID, v.ActiveWorkloadRef.Namespace, v.ActiveWorkloadRef.Kind, v.ActiveWorkloadRef.Name, v.ActiveWorkloadRef.UID, v.ActiveWorkloadRef.ResourceVersion)
	if err != nil {
		return v, wrapConflict(err)
	}
	if _, err = tx.Exec(`UPDATE delivery_applications SET requires_review=0 WHERE id=?`, v.ApplicationID); err != nil {
		return v, err
	}
	if _, err = tx.Exec(`UPDATE delivery_components SET requires_review=0 WHERE id=?`, v.ComponentID); err != nil {
		return v, err
	}
	v, err = scanDeliveryBinding(tx.QueryRow(`SELECT `+deliveryBindingColumns+` FROM delivery_bindings WHERE app_id=?`, v.AppID))
	if err != nil {
		return v, err
	}
	return v, nil
}

func (m *MySQL) PinDeliveryBindingWorkloadRefs(bindingID int64, expectedTemplate, expectedActive, template, active service.DeliveryWorkloadReference) (service.DeliveryBinding, error) {
	tx, err := m.db.Begin()
	if err != nil {
		return service.DeliveryBinding{}, err
	}
	defer tx.Rollback()
	var currentTemplate, currentActive service.DeliveryWorkloadReference
	err = tx.QueryRow(`SELECT template_workload_cluster_id,template_workload_namespace,template_workload_kind,template_workload_name,template_workload_uid,template_workload_resource_version,
active_workload_cluster_id,active_workload_namespace,active_workload_kind,active_workload_name,active_workload_uid,active_workload_resource_version
FROM delivery_bindings WHERE id=? FOR UPDATE`, bindingID).Scan(
		&currentTemplate.ClusterID, &currentTemplate.Namespace, &currentTemplate.Kind, &currentTemplate.Name, &currentTemplate.UID, &currentTemplate.ResourceVersion,
		&currentActive.ClusterID, &currentActive.Namespace, &currentActive.Kind, &currentActive.Name, &currentActive.UID, &currentActive.ResourceVersion,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return service.DeliveryBinding{}, service.ErrNotFound
	}
	if err != nil {
		return service.DeliveryBinding{}, err
	}
	if currentTemplate != expectedTemplate || currentActive != expectedActive {
		return service.DeliveryBinding{}, fmt.Errorf("%w: 绑定资源身份已变化，请刷新后重试", service.ErrConflict)
	}
	if active.UID == "" || active.Kind != "Deployment" || active.ClusterID <= 0 || active.Namespace == "" || active.Name == "" {
		return service.DeliveryBinding{}, service.NewValidationError("活动 Deployment 身份不完整，无法固定资源 UID")
	}
	if _, err := tx.Exec(`UPDATE delivery_bindings SET
template_workload_cluster_id=?,template_workload_namespace=?,template_workload_kind=?,template_workload_name=?,template_workload_uid=?,template_workload_resource_version=?,
active_workload_cluster_id=?,active_workload_namespace=?,active_workload_kind=?,active_workload_name=?,active_workload_uid=?,active_workload_resource_version=? WHERE id=?`,
		template.ClusterID, template.Namespace, template.Kind, template.Name, template.UID, template.ResourceVersion,
		active.ClusterID, active.Namespace, active.Kind, active.Name, active.UID, active.ResourceVersion, bindingID); err != nil {
		return service.DeliveryBinding{}, wrapConflict(err)
	}
	binding, err := scanDeliveryBinding(tx.QueryRow(`SELECT `+deliveryBindingColumns+` FROM delivery_bindings WHERE id=?`, bindingID))
	if err != nil {
		return service.DeliveryBinding{}, err
	}
	if err := tx.Commit(); err != nil {
		return service.DeliveryBinding{}, err
	}
	return binding, nil
}

func (m *MySQL) RebindDeliveryBindingActiveWorkloadRef(bindingID int64, expectedTemplate, expectedActive, template, active service.DeliveryWorkloadReference) (service.DeliveryBinding, error) {
	tx, err := m.db.Begin()
	if err != nil {
		return service.DeliveryBinding{}, err
	}
	defer tx.Rollback()
	var currentTemplate, currentActive service.DeliveryWorkloadReference
	err = tx.QueryRow(`SELECT template_workload_cluster_id,template_workload_namespace,template_workload_kind,template_workload_name,template_workload_uid,template_workload_resource_version,
active_workload_cluster_id,active_workload_namespace,active_workload_kind,active_workload_name,active_workload_uid,active_workload_resource_version
FROM delivery_bindings WHERE id=? FOR UPDATE`, bindingID).Scan(
		&currentTemplate.ClusterID, &currentTemplate.Namespace, &currentTemplate.Kind, &currentTemplate.Name, &currentTemplate.UID, &currentTemplate.ResourceVersion,
		&currentActive.ClusterID, &currentActive.Namespace, &currentActive.Kind, &currentActive.Name, &currentActive.UID, &currentActive.ResourceVersion,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return service.DeliveryBinding{}, service.ErrNotFound
	}
	if err != nil {
		return service.DeliveryBinding{}, err
	}
	if currentTemplate != expectedTemplate || currentActive != expectedActive {
		return service.DeliveryBinding{}, fmt.Errorf("%w: 资源绑定已变化，请刷新后重试", service.ErrConflict)
	}
	if active.ClusterID <= 0 || active.Namespace == "" || active.Kind != "Deployment" || active.Name == "" || active.UID == "" {
		return service.DeliveryBinding{}, service.NewValidationError("活动 Deployment 身份不完整，无法重新绑定")
	}
	var appClusterID int
	var appNamespace, appDeployment string
	if err := tx.QueryRow(`SELECT a.cluster_id,a.namespace,a.deployment_name FROM apps a JOIN delivery_bindings b ON b.app_id=a.id WHERE b.id=? FOR UPDATE`, bindingID).
		Scan(&appClusterID, &appNamespace, &appDeployment); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return service.DeliveryBinding{}, service.ErrNotFound
		}
		return service.DeliveryBinding{}, err
	}
	if appClusterID != active.ClusterID || appNamespace != active.Namespace || appDeployment != active.Name {
		return service.DeliveryBinding{}, fmt.Errorf("%w: 应用当前 Deployment 已变化，请刷新后重新核对", service.ErrConflict)
	}
	checkedLocks := map[string]bool{}
	for _, ref := range []service.DeliveryWorkloadReference{currentActive, active} {
		if ref.ClusterID <= 0 || ref.Namespace == "" || ref.Name == "" {
			continue
		}
		lock := service.ResourceLock{ClusterID: ref.ClusterID, Namespace: ref.Namespace, Kind: "deployment", Name: ref.Name}
		key := resourceLockKey(lock)
		if checkedLocks[key] {
			continue
		}
		checkedLocks[key] = true
		var owner int64
		err := tx.QueryRow(`SELECT deploy_log_id FROM delivery_resource_locks WHERE resource_key=? FOR UPDATE`, key).Scan(&owner)
		if err == nil {
			return service.DeliveryBinding{}, fmt.Errorf("%w: Deployment 正由发布任务 %d 占用，不能重新绑定", service.ErrConflict, owner)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return service.DeliveryBinding{}, err
		}
	}
	if _, err := tx.Exec(`UPDATE delivery_bindings SET
template_workload_cluster_id=?,template_workload_namespace=?,template_workload_kind=?,template_workload_name=?,template_workload_uid=?,template_workload_resource_version=?,
active_workload_cluster_id=?,active_workload_namespace=?,active_workload_kind=?,active_workload_name=?,active_workload_uid=?,active_workload_resource_version=? WHERE id=?`,
		template.ClusterID, template.Namespace, template.Kind, template.Name, template.UID, template.ResourceVersion,
		active.ClusterID, active.Namespace, active.Kind, active.Name, active.UID, active.ResourceVersion, bindingID); err != nil {
		return service.DeliveryBinding{}, wrapConflict(err)
	}
	binding, err := scanDeliveryBinding(tx.QueryRow(`SELECT `+deliveryBindingColumns+` FROM delivery_bindings WHERE id=?`, bindingID))
	if err != nil {
		return service.DeliveryBinding{}, err
	}
	if err := tx.Commit(); err != nil {
		return service.DeliveryBinding{}, err
	}
	return binding, nil
}

const deployLogColumns = `id,cluster_id,app_name,deployment_name,container_name,namespace,version,file_name,image_full,deploy_type,status,current_step,step_detail,error_message,started_at,finished_at,created_at,release_id,rollback_target_id,environment_tier_snapshot,worker_claim_id`

func scanDeployLog(row rowScanner) (service.DeployLog, error) {
	var v service.DeployLog
	var imageFull, step, stepDetail, errMsg, tierSnapshot, workerClaimID sql.NullString
	var started, finished sql.NullTime
	if err := row.Scan(&v.ID, &v.ClusterID, &v.AppName, &v.DeploymentName, &v.ContainerName, &v.Namespace, &v.Version, &v.FileName, &imageFull, &v.DeployType, &v.Status, &step, &stepDetail, &errMsg, &started, &finished, &v.CreatedAt, &v.ReleaseID, &v.RollbackTargetID, &tierSnapshot, &workerClaimID); err != nil {
		return v, err
	}
	v.ImageFull = nullStr(imageFull)
	v.CurrentStep = nullStr(step)
	v.StepDetail = stepDetail.String
	v.ErrorMessage = nullStr(errMsg)
	v.StartedAt = nullTime(started)
	v.FinishedAt = nullTime(finished)
	v.EnvironmentTierSnapshot = nullStr(tierSnapshot)
	v.WorkerClaimID = workerClaimID.String
	return v, nil
}
func (m *MySQL) LogByID(id int64) (service.DeployLog, error) {
	return scanDeployLog(m.db.QueryRow(`SELECT `+deployLogColumns+` FROM deploy_logs WHERE id=?`, id))
}

// PendingDeployLogIDsAfter walks the durable queue in bounded keyset pages.
// The status/id index avoids loading the full deployment history for the
// dispatcher's periodic recovery scan.
func (m *MySQL) PendingDeployLogIDsAfter(afterID int64, limit int) ([]int64, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := m.db.Query(`SELECT id FROM deploy_logs WHERE status='pending' AND id>? ORDER BY id ASC LIMIT ?`, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]int64, 0, limit)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

func (m *MySQL) Logs() ([]service.DeployLog, error) {
	rows, err := m.db.Query(`SELECT ` + deployLogColumns + ` FROM deploy_logs ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []service.DeployLog{}
	for rows.Next() {
		v, err := scanDeployLog(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, rows.Err()
}

func (m *MySQL) SaveCluster(v service.Cluster) (service.Cluster, error) {
	res, err := m.db.Exec(`INSERT INTO clusters(name,env_code,environment,environment_tier,deploy_policy,connection_type,api_server,credential_ciphertext,ca_cert,insecure_skip_tls_verify,enabled,k8s_master_host,k8s_master_port,k8s_master_user,k8s_master_key_path,default_namespace,description) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		v.Name, v.EnvCode, v.Environment, v.EnvironmentTier, v.DeployPolicy, v.ConnectionType, v.APIServer, v.CredentialCiphertext, v.CACert, v.InsecureSkipTLSVerify, v.Enabled, v.K8sMasterHost, v.K8sMasterPort, v.K8sMasterUser, v.K8sMasterKeyPath, v.DefaultNamespace, v.Description)
	if err != nil {
		return v, wrapConflict(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return v, err
	}
	v.ID = int(id)
	v.CreatedAt = time.Now()
	v.UpdatedAt = v.CreatedAt
	return v, nil
}

func (m *MySQL) UpdateCluster(v service.Cluster) error {
	_, err := m.db.Exec(`UPDATE clusters SET name=?,env_code=?,environment=?,environment_tier=?,deploy_policy=?,connection_type=?,api_server=?,credential_ciphertext=?,ca_cert=?,insecure_skip_tls_verify=?,enabled=?,k8s_master_host=?,k8s_master_port=?,k8s_master_user=?,k8s_master_key_path=?,default_namespace=?,description=? WHERE id=?`,
		v.Name, v.EnvCode, v.Environment, v.EnvironmentTier, v.DeployPolicy, v.ConnectionType, v.APIServer, v.CredentialCiphertext, v.CACert, v.InsecureSkipTLSVerify, v.Enabled, v.K8sMasterHost, v.K8sMasterPort, v.K8sMasterUser, v.K8sMasterKeyPath, v.DefaultNamespace, v.Description, v.ID)
	return wrapConflict(err)
}

func (m *MySQL) DeleteCluster(id int) error {
	_, err := m.db.Exec(`DELETE FROM clusters WHERE id=?`, id)
	return err
}

func (m *MySQL) SaveApp(v service.App) (service.App, error) {
	res, err := m.db.Exec(`INSERT INTO apps(cluster_id,app_name,match_keyword,deployment_name,container_name,namespace,harbor_project,description,source_repo_url,source_path,image_repository,source_kind,deployment_config_repo_url,deployment_config_path) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		v.ClusterID, v.AppName, v.MatchKeyword, v.DeploymentName, v.ContainerName, v.Namespace, v.HarborProject, v.Description, v.SourceRepoURL, v.SourcePath, v.ImageRepository, v.SourceKind, v.DeploymentConfigRepoURL, v.DeploymentConfigPath)
	if err != nil {
		return v, wrapConflict(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return v, err
	}
	v.ID = int(id)
	v.CreatedAt = time.Now()
	return v, nil
}

func (m *MySQL) UpdateApp(v service.App) error {
	_, err := m.db.Exec(`UPDATE apps SET cluster_id=?,app_name=?,match_keyword=?,deployment_name=?,container_name=?,namespace=?,harbor_project=?,description=?,source_repo_url=?,source_path=?,image_repository=?,source_kind=?,deployment_config_repo_url=?,deployment_config_path=? WHERE id=?`,
		v.ClusterID, v.AppName, v.MatchKeyword, v.DeploymentName, v.ContainerName, v.Namespace, v.HarborProject, v.Description, v.SourceRepoURL, v.SourcePath, v.ImageRepository, v.SourceKind, v.DeploymentConfigRepoURL, v.DeploymentConfigPath, v.ID)
	return wrapConflict(err)
}

func (m *MySQL) DeleteApp(id int) error {
	_, err := m.db.Exec(`DELETE FROM apps WHERE id=?`, id)
	return err
}

func (m *MySQL) SaveLog(v service.DeployLog) (service.DeployLog, error) {
	res, err := m.db.Exec(`INSERT INTO deploy_logs(cluster_id,app_name,deployment_name,container_name,namespace,version,file_name,image_full,deploy_type,status,current_step,step_detail,error_message,started_at,finished_at,release_id,rollback_target_id,environment_tier_snapshot) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		v.ClusterID, v.AppName, v.DeploymentName, v.ContainerName, v.Namespace, v.Version, v.FileName, v.ImageFull, v.DeployType, v.Status, v.CurrentStep, emptyToNil(v.StepDetail), v.ErrorMessage, v.StartedAt, v.FinishedAt, v.ReleaseID, v.RollbackTargetID, v.EnvironmentTierSnapshot)
	if err != nil {
		return v, wrapConflict(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return v, err
	}
	v.ID = id
	v.CreatedAt = time.Now()
	return v, nil
}

// CreateDeployLog stores the task and its resource ownership in one
// transaction. The unique resource key is the cross-process mutex used by
// legacy rolling tasks and future multi-resource delivery runs alike.
func (m *MySQL) CreateDeployLog(v service.DeployLog, requestedLocks []service.ResourceLock) (service.DeployLog, error) {
	locks, err := service.NormalizeResourceLocks(requestedLocks)
	if err != nil {
		return v, err
	}
	if len(locks) == 0 {
		return v, service.NewValidationError("发布任务至少需要锁定一个 Kubernetes 资源")
	}
	for _, lock := range locks {
		if lock.ClusterID != v.ClusterID {
			return v, service.NewValidationError("资源锁必须属于发布任务的目标集群")
		}
	}
	tx, err := m.db.Begin()
	if err != nil {
		return v, err
	}
	defer tx.Rollback()
	if err := insertDeployLog(tx, &v); err != nil {
		return v, wrapConflict(err)
	}
	if err := acquireResourceLocks(tx, v.ID, locks); err != nil {
		return v, err
	}
	if err := tx.Commit(); err != nil {
		return v, err
	}
	v.CreatedAt = time.Now()
	return v, nil
}

func resourceLockKey(lock service.ResourceLock) string {
	sum := sha256.Sum256([]byte(lock.Identity()))
	return hex.EncodeToString(sum[:])
}

func acquireResourceLocks(tx *sql.Tx, logID int64, locks []service.ResourceLock) error {
	for _, lock := range locks {
		_, err := tx.Exec(`INSERT INTO delivery_resource_locks(resource_key,cluster_id,namespace,resource_kind,resource_name,deploy_log_id) VALUES(?,?,?,?,?,?)`,
			resourceLockKey(lock), lock.ClusterID, lock.Namespace, lock.Kind, lock.Name, logID)
		if err != nil {
			return wrapConflict(err)
		}
	}
	return nil
}

func (m *MySQL) ResourceLockOwner(lock service.ResourceLock) (int64, bool, error) {
	if err := lock.Validate(); err != nil {
		return 0, false, err
	}
	var logID int64
	err := m.db.QueryRow(`SELECT deploy_log_id FROM delivery_resource_locks WHERE resource_key=? AND (expires_at IS NULL OR expires_at > UTC_TIMESTAMP())`, resourceLockKey(lock)).Scan(&logID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return logID, true, nil
}

// ReserveResourceLockLease uses the same unique resource keys as active
// delivery runs, but expires the reservation if the API process exits before
// it can release it.
func (m *MySQL) ReserveResourceLockLease(logID int64, requested []service.ResourceLock, expiresAt time.Time) error {
	if logID <= 0 || !expiresAt.After(time.Now()) {
		return service.NewValidationError("资源锁租约参数无效")
	}
	locks, err := service.NormalizeResourceLocks(requested)
	if err != nil {
		return err
	}
	if len(locks) == 0 {
		return service.NewValidationError("资源锁租约不能为空")
	}
	tx, err := m.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM delivery_resource_locks WHERE expires_at IS NOT NULL AND expires_at <= UTC_TIMESTAMP()`); err != nil {
		return err
	}
	for _, lock := range locks {
		_, err := tx.Exec(`INSERT INTO delivery_resource_locks(resource_key,cluster_id,namespace,resource_kind,resource_name,deploy_log_id,expires_at) VALUES(?,?,?,?,?,?,?)`,
			resourceLockKey(lock), lock.ClusterID, lock.Namespace, lock.Kind, lock.Name, logID, expiresAt.UTC())
		if err != nil {
			return wrapConflict(err)
		}
	}
	return tx.Commit()
}

func (m *MySQL) ReleaseResourceLockLease(logID int64, locks []service.ResourceLock) error {
	if logID <= 0 {
		return service.NewValidationError("资源锁租约归属无效")
	}
	normalized, err := service.NormalizeResourceLocks(locks)
	if err != nil {
		return err
	}
	tx, err := m.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, lock := range normalized {
		if _, err := tx.Exec(`DELETE FROM delivery_resource_locks WHERE resource_key=? AND deploy_log_id=? AND expires_at IS NOT NULL`, resourceLockKey(lock), logID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (m *MySQL) UpdateLogExecution(v service.DeployLog) error {
	tx, err := m.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	query := `UPDATE deploy_logs SET status=?,current_step=?,image_full=?,step_detail=?,error_message=?,started_at=?,finished_at=? WHERE id=?`
	if v.Status == "success" || v.Status == "failed" || v.Status == "interrupted" {
		query = `UPDATE deploy_logs SET status=?,current_step=?,image_full=?,step_detail=?,error_message=?,started_at=?,finished_at=?,worker_claim_id=NULL WHERE id=?`
	}
	if _, err := tx.Exec(query,
		v.Status, v.CurrentStep, v.ImageFull, emptyToNil(v.StepDetail), v.ErrorMessage, v.StartedAt, v.FinishedAt, v.ID); err != nil {
		return wrapConflict(err)
	}
	if v.Status == "success" || v.Status == "failed" || v.Status == "interrupted" {
		if _, err := tx.Exec(`DELETE FROM delivery_resource_locks WHERE deploy_log_id=?`, v.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UpdateLogExecutionWithClaim is the worker-only write path. The conditional
// update is a fencing check: a stale process cannot write after another claim
// has replaced its token.
func (m *MySQL) UpdateLogExecutionWithClaim(v service.DeployLog, claimID string) error {
	if claimID == "" {
		return fmt.Errorf("worker claim is required")
	}
	tx, err := m.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	query := `UPDATE deploy_logs SET status=?,current_step=?,image_full=?,step_detail=?,error_message=?,started_at=?,finished_at=?,worker_claim_id=CASE WHEN ? IN ('success','failed','interrupted') THEN NULL ELSE worker_claim_id END WHERE id=? AND worker_claim_id=?`
	result, err := tx.Exec(query, v.Status, v.CurrentStep, v.ImageFull, emptyToNil(v.StepDetail), v.ErrorMessage, v.StartedAt, v.FinishedAt, v.Status, v.ID, claimID)
	if err != nil {
		return wrapConflict(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 && v.Status != "success" && v.Status != "failed" && v.Status != "interrupted" {
		var current sql.NullString
		if err := tx.QueryRow(`SELECT worker_claim_id FROM deploy_logs WHERE id=?`, v.ID).Scan(&current); err != nil || !current.Valid || current.String != claimID {
			return fmt.Errorf("%w: Worker 认领已失效，拒绝写入发布任务", service.ErrConflict)
		}
	} else if changed == 0 {
		return fmt.Errorf("%w: Worker 认领已失效，拒绝写入发布任务", service.ErrConflict)
	}
	if v.Status == "success" || v.Status == "failed" || v.Status == "interrupted" {
		if _, err := tx.Exec(`DELETE FROM delivery_resource_locks WHERE deploy_log_id=?`, v.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ReopenInterruptedDeployLog reacquires the Deployment lock and changes an
// interrupted task back to pending in one transaction. A competing release
// cannot enter between reconciliation and worker resumption.
func (m *MySQL) ReopenInterruptedDeployLog(id int64) (service.DeployLog, error) {
	tx, err := m.db.Begin()
	if err != nil {
		return service.DeployLog{}, err
	}
	defer tx.Rollback()
	current, err := scanDeployLog(tx.QueryRow(`SELECT `+deployLogColumns+` FROM deploy_logs WHERE id=? FOR UPDATE`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return service.DeployLog{}, service.ErrNotFound
	}
	if err != nil {
		return service.DeployLog{}, err
	}
	if current.Status != "interrupted" {
		return service.DeployLog{}, fmt.Errorf("%w: 只有中断的发布任务可以核对恢复", service.ErrConflict)
	}
	locks, err := service.NormalizeResourceLocks([]service.ResourceLock{{ClusterID: current.ClusterID, Namespace: current.Namespace, Kind: "deployment", Name: current.DeploymentName}})
	if err != nil {
		return service.DeployLog{}, err
	}
	if err := acquireResourceLocks(tx, id, locks); err != nil {
		return service.DeployLog{}, err
	}
	result, err := tx.Exec(`UPDATE deploy_logs SET status='pending',current_step=NULL,step_detail='[]',error_message=NULL,started_at=NULL,finished_at=NULL,worker_claim_id=NULL WHERE id=? AND status='interrupted'`, id)
	if err != nil {
		return service.DeployLog{}, wrapConflict(err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return service.DeployLog{}, err
	} else if affected != 1 {
		return service.DeployLog{}, fmt.Errorf("%w: 发布任务状态已改变", service.ErrConflict)
	}
	current, err = scanDeployLog(tx.QueryRow(`SELECT `+deployLogColumns+` FROM deploy_logs WHERE id=?`, id))
	if err != nil {
		return service.DeployLog{}, err
	}
	if err := tx.Commit(); err != nil {
		return service.DeployLog{}, err
	}
	return current, nil
}

// wrapConflict maps MySQL duplicate-key errors (1062) to service.ErrConflict.
func wrapConflict(err error) error {
	if err == nil {
		return nil
	}
	var me *mysql.MySQLError
	if errors.As(err, &me) && me.Number == 1062 {
		return fmt.Errorf("%w: %s", service.ErrConflict, me.Message)
	}
	return err
}

func isDup(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == 1062
}

type rowScanner interface{ Scan(dest ...any) error }

const imageReleaseColumns = `id,event_key,event_id,source,pipeline_id,pipeline_name,build_number,branch,commit_id,app_name,image_full,image_digest,version,test_cluster_id,test_deploy_log_id,prod_cluster_id,prod_deploy_log_id,created_at,updated_at,metadata`

func scanImageRelease(s rowScanner) (service.ImageRelease, error) {
	var r service.ImageRelease
	var pID, pName, bNum, branch, commit, digest sql.NullString
	var prodCluster, prodLog sql.NullInt64
	var metadata sql.NullString
	err := s.Scan(&r.ID, &r.EventKey, &r.EventID, &r.Source, &pID, &pName, &bNum, &branch, &commit,
		&r.AppName, &r.ImageFull, &digest, &r.Version, &r.TestClusterID, &r.TestDeployLogID,
		&prodCluster, &prodLog, &r.CreatedAt, &r.UpdatedAt, &metadata)
	if err != nil {
		return r, err
	}
	if metadata.Valid {
		if err := json.Unmarshal([]byte(metadata.String), &r.Metadata); err != nil {
			return r, fmt.Errorf("release metadata: %w", err)
		}
	}
	r.PipelineID = nullStr(pID)
	r.PipelineName = nullStr(pName)
	r.BuildNumber = nullStr(bNum)
	r.Branch = nullStr(branch)
	r.CommitID = nullStr(commit)
	r.ImageDigest = nullStr(digest)
	if prodCluster.Valid {
		v := int(prodCluster.Int64)
		r.ProdClusterID = &v
	}
	if prodLog.Valid {
		r.ProdDeployLogID = &prodLog.Int64
	}
	return r, nil
}

func (m *MySQL) ImageReleases() ([]service.ImageRelease, error) {
	rows, err := m.db.Query(`SELECT ` + imageReleaseColumns + ` FROM image_releases ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []service.ImageRelease
	for rows.Next() {
		r, err := scanImageRelease(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

const deliveryPlanColumns = `id,release_id,target_cluster_id,target_app_id,strategy,status,snapshot_hash,snapshot_json,blockers_json,requested_by,created_at`

func scanDeliveryPlan(row rowScanner) (service.DeliveryPlan, error) {
	var plan service.DeliveryPlan
	var snapshotJSON, blockersJSON []byte
	if err := row.Scan(&plan.ID, &plan.ReleaseID, &plan.TargetClusterID, &plan.TargetAppID, &plan.Strategy, &plan.Status, &plan.SnapshotHash, &snapshotJSON, &blockersJSON, &plan.RequestedBy, &plan.CreatedAt); err != nil {
		return plan, err
	}
	if err := json.Unmarshal(snapshotJSON, &plan.Snapshot); err != nil {
		return plan, fmt.Errorf("decode delivery plan snapshot: %w", err)
	}
	if err := json.Unmarshal(blockersJSON, &plan.Blockers); err != nil {
		return plan, fmt.Errorf("decode delivery plan blockers: %w", err)
	}
	if plan.Blockers == nil {
		plan.Blockers = []string{}
	}
	return plan, nil
}

func (m *MySQL) DeliveryPlans() ([]service.DeliveryPlan, error) {
	rows, err := m.db.Query(`SELECT ` + deliveryPlanColumns + ` FROM delivery_plans ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	plans := []service.DeliveryPlan{}
	for rows.Next() {
		plan, err := scanDeliveryPlan(rows)
		if err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	return plans, rows.Err()
}

func (m *MySQL) SaveDeliveryPlan(plan service.DeliveryPlan) (service.DeliveryPlan, bool, error) {
	snapshotJSON, err := json.Marshal(plan.Snapshot)
	if err != nil {
		return plan, false, err
	}
	blockersJSON, err := json.Marshal(plan.Blockers)
	if err != nil {
		return plan, false, err
	}
	res, err := m.db.Exec(`INSERT IGNORE INTO delivery_plans(release_id,target_cluster_id,target_app_id,strategy,status,snapshot_hash,snapshot_json,blockers_json,requested_by,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		plan.ReleaseID, plan.TargetClusterID, plan.TargetAppID, plan.Strategy, plan.Status, plan.SnapshotHash, snapshotJSON, blockersJSON, plan.RequestedBy, plan.CreatedAt)
	if err != nil {
		return plan, false, wrapConflict(err)
	}
	created, err := res.RowsAffected()
	if err != nil {
		return plan, false, err
	}
	if created == 0 {
		existing, err := scanDeliveryPlan(m.db.QueryRow(`SELECT `+deliveryPlanColumns+` FROM delivery_plans WHERE snapshot_hash=?`, plan.SnapshotHash))
		if err != nil {
			return plan, false, err
		}
		return existing, false, nil
	}
	id, err := res.LastInsertId()
	if err != nil {
		return plan, false, err
	}
	plan.ID = id
	return plan, true, nil
}

func (m *MySQL) FindDeliveryPlan(id int64) (service.DeliveryPlan, error) {
	return scanDeliveryPlan(m.db.QueryRow(`SELECT `+deliveryPlanColumns+` FROM delivery_plans WHERE id=?`, id))
}

func (m *MySQL) imageReleaseByEventKey(key string) (service.ImageRelease, error) {
	return scanImageRelease(m.db.QueryRow(`SELECT `+imageReleaseColumns+` FROM image_releases WHERE event_key=?`, key))
}

// insertDeployLog runs the shared deploy_logs INSERT inside tx and fills in
// the generated id.
func insertDeployLog(tx *sql.Tx, v *service.DeployLog) error {
	res, err := tx.Exec(`INSERT INTO deploy_logs(cluster_id,app_name,deployment_name,container_name,namespace,version,file_name,image_full,deploy_type,status,current_step,step_detail,error_message,started_at,finished_at,release_id,rollback_target_id,environment_tier_snapshot) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		v.ClusterID, v.AppName, v.DeploymentName, v.ContainerName, v.Namespace, v.Version, v.FileName, v.ImageFull, v.DeployType, v.Status, v.CurrentStep, emptyToNil(v.StepDetail), v.ErrorMessage, v.StartedAt, v.FinishedAt, v.ReleaseID, v.RollbackTargetID, v.EnvironmentTierSnapshot)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	v.ID = id
	return nil
}

func (m *MySQL) CreateImageRelease(r service.ImageRelease, l service.DeployLog) (service.ImageRelease, service.DeployLog, bool, error) {
	tx, err := m.db.Begin()
	if err != nil {
		return r, l, false, err
	}
	rollback := func(err error) (service.ImageRelease, service.DeployLog, bool, error) {
		tx.Rollback()
		return r, l, false, err
	}
	if err := insertDeployLog(tx, &l); err != nil {
		return rollback(err)
	}
	locks, err := service.NormalizeResourceLocks([]service.ResourceLock{{ClusterID: l.ClusterID, Namespace: l.Namespace, Kind: "deployment", Name: l.DeploymentName}})
	if err != nil {
		return rollback(err)
	}
	if err := acquireResourceLocks(tx, l.ID, locks); err != nil {
		return rollback(err)
	}
	res, err := tx.Exec(`INSERT INTO image_releases(event_key,event_id,source,pipeline_id,pipeline_name,build_number,branch,commit_id,app_name,image_full,image_digest,version,test_cluster_id,test_deploy_log_id,metadata) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.EventKey, r.EventID, r.Source, r.PipelineID, r.PipelineName, r.BuildNumber, r.Branch, r.CommitID, r.AppName, r.ImageFull, r.ImageDigest, r.Version, r.TestClusterID, l.ID, releaseMetadataJSON(r.Metadata))
	if err != nil {
		if isDup(err) {
			tx.Rollback()
			existing, qerr := m.imageReleaseByEventKey(r.EventKey)
			if qerr != nil {
				return r, l, false, qerr
			}
			return existing, service.DeployLog{}, false, nil
		}
		return rollback(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return rollback(err)
	}
	r.ID = id
	l.ReleaseID = id
	if _, err := tx.Exec(`UPDATE deploy_logs SET release_id=? WHERE id=?`, id, l.ID); err != nil {
		return rollback(err)
	}
	r.TestDeployLogID = l.ID
	if err := tx.Commit(); err != nil {
		return r, l, false, err
	}
	return r, l, true, nil
}

func (m *MySQL) PromoteImageRelease(id int64, prodClusterID int, l service.DeployLog) (service.ImageRelease, service.DeployLog, error) {
	tx, err := m.db.Begin()
	if err != nil {
		return service.ImageRelease{}, l, err
	}
	defer tx.Rollback()
	r, l, err := promoteInTx(tx, id, prodClusterID, l)
	if err != nil {
		return r, l, err
	}
	return r, l, tx.Commit()
}

func promoteInTx(tx *sql.Tx, id int64, clusterID int, l service.DeployLog, requestedLocks ...service.ResourceLock) (service.ImageRelease, service.DeployLog, error) {
	r, err := scanImageRelease(tx.QueryRow(`SELECT `+imageReleaseColumns+` FROM image_releases WHERE id=? FOR UPDATE`, id))
	if errors.Is(err, sql.ErrNoRows) {
		err = service.ErrNotFound
	}
	if err != nil {
		return r, l, err
	}
	var logID int64
	var status string
	err = tx.QueryRow(`SELECT id,status FROM deploy_logs WHERE release_id=? AND cluster_id=? ORDER BY id DESC LIMIT 1 FOR UPDATE`, id, clusterID).Scan(&logID, &status)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return r, l, err
	}
	if err == nil && (status == "pending" || status == "running" || status == "success") {
		return r, l, fmt.Errorf("%w: 该目标已有发布任务 %d", service.ErrConflict, logID)
	}
	l.ReleaseID = id
	if err := insertDeployLog(tx, &l); err != nil {
		return r, l, err
	}
	locks, err := service.NormalizeResourceLocks(append(requestedLocks, service.ResourceLock{ClusterID: l.ClusterID, Namespace: l.Namespace, Kind: "deployment", Name: l.DeploymentName}))
	if err != nil {
		return r, l, err
	}
	for _, lock := range locks {
		if lock.ClusterID != l.ClusterID {
			return r, l, service.NewValidationError("审批资源锁必须属于发布任务的目标集群")
		}
	}
	if err := acquireResourceLocks(tx, l.ID, locks); err != nil {
		return r, l, err
	}
	if _, err := tx.Exec(`UPDATE image_releases SET prod_cluster_id=?,prod_deploy_log_id=? WHERE id=?`, clusterID, l.ID, id); err != nil {
		return r, l, err
	}
	r.ProdClusterID = &clusterID
	r.ProdDeployLogID = &l.ID
	return r, l, nil
}

func (m *MySQL) ApprovePromotion(req service.PromotionRequest, l service.DeployLog) (service.PromotionRequest, service.ImageRelease, service.DeployLog, error) {
	return m.ApprovePromotionWithLocks(req, l, []service.ResourceLock{{ClusterID: l.ClusterID, Namespace: l.Namespace, Kind: "deployment", Name: l.DeploymentName}})
}

// ApprovePromotionWithLocks atomically records the approval, creates the
// execution task, and reserves strategy resources in the same transaction.
func (m *MySQL) ApprovePromotionWithLocks(req service.PromotionRequest, l service.DeployLog, locks []service.ResourceLock) (service.PromotionRequest, service.ImageRelease, service.DeployLog, error) {
	savedReq, savedRelease, savedLog, _, err := m.approvePromotionWithLocksAndRun(req, l, locks, nil)
	return savedReq, savedRelease, savedLog, err
}

func (m *MySQL) ApprovePromotionWithRun(req service.PromotionRequest, l service.DeployLog, locks []service.ResourceLock, run service.DeliveryRun) (service.PromotionRequest, service.ImageRelease, service.DeployLog, service.DeliveryRun, error) {
	return m.approvePromotionWithLocksAndRun(req, l, locks, &run)
}

func (m *MySQL) approvePromotionWithLocksAndRun(req service.PromotionRequest, l service.DeployLog, locks []service.ResourceLock, run *service.DeliveryRun) (service.PromotionRequest, service.ImageRelease, service.DeployLog, service.DeliveryRun, error) {
	tx, err := m.db.Begin()
	if err != nil {
		return req, service.ImageRelease{}, l, service.DeliveryRun{}, err
	}
	defer tx.Rollback()
	current, err := scanPromotionRequest(tx.QueryRow(`SELECT `+promotionRequestColumns+` FROM promotion_requests WHERE id=? FOR UPDATE`, req.ID))
	if err != nil {
		return req, service.ImageRelease{}, l, service.DeliveryRun{}, err
	}
	if current.Status != "pending" || current.ExpiresAt == nil || !time.Now().Before(*current.ExpiresAt) || current.Snapshot != req.Snapshot || current.TemplateHash != req.TemplateHash || !sameOptionalInt64(current.DeliveryPlanID, req.DeliveryPlanID) {
		return req, service.ImageRelease{}, l, service.DeliveryRun{}, fmt.Errorf("%w: 审批状态或快照已改变", service.ErrConflict)
	}
	var baseline int64
	err = tx.QueryRow(`SELECT id FROM deploy_logs WHERE cluster_id=? AND namespace=? AND deployment_name=? AND status NOT IN ('pending','running') ORDER BY id DESC LIMIT 1`, l.ClusterID, l.Namespace, l.DeploymentName).Scan(&baseline)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return req, service.ImageRelease{}, l, service.DeliveryRun{}, err
	}
	if baseline != current.BaselineLogID {
		return req, service.ImageRelease{}, l, service.DeliveryRun{}, fmt.Errorf("%w: 生产基线已改变", service.ErrConflict)
	}
	r, l, err := promoteInTx(tx, req.ReleaseID, req.ProdClusterID, l, locks...)
	if err != nil {
		return req, r, l, service.DeliveryRun{}, err
	}
	req.DeployLogID = &l.ID
	if _, err := tx.Exec(`UPDATE promotion_requests SET status='approved',approver=?,deploy_log_id=?,decided_at=? WHERE id=? AND status='pending'`, req.Approver, l.ID, req.DecidedAt, req.ID); err != nil {
		return req, r, l, service.DeliveryRun{}, err
	}
	if run != nil {
		run.DeployLogID = l.ID
		if err := insertDeliveryRun(tx, run); err != nil {
			return req, r, l, service.DeliveryRun{}, err
		}
		event := service.DeliveryRunEvent{
			RunID: run.ID, Version: run.Version, EventKey: "run-created", EventType: "queued",
			Stage: "queued", Status: "queued", Detail: "生产审批通过，发布运行已排队。", CreatedAt: run.CreatedAt,
		}
		if err := insertDeliveryRunEvent(tx, event); err != nil {
			return req, r, l, service.DeliveryRun{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return req, r, l, service.DeliveryRun{}, err
	}
	if run == nil {
		return req, r, l, service.DeliveryRun{}, nil
	}
	return req, r, l, *run, nil
}

func sameOptionalInt64(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func releaseMetadataJSON(metadata service.ReleaseMetadata) string {
	b, _ := json.Marshal(metadata)
	return string(b)
}
func (m *MySQL) UpdateReleaseMetadata(id int64, metadata service.ReleaseMetadata) error {
	_, err := m.db.Exec(`UPDATE image_releases SET metadata=? WHERE id=?`, releaseMetadataJSON(metadata), id)
	return err
}
func (m *MySQL) ClaimLog(id int64) (bool, error) {
	res, err := m.db.Exec(`UPDATE deploy_logs SET status='running',started_at=NOW() WHERE id=? AND status='pending'`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ClaimLogWithOwnership atomically claims only a pending row. Repeating the
// same claim ID is idempotent, while another worker's running claim is left
// untouched and reported as not acquired.
func (m *MySQL) ClaimLogWithOwnership(id int64, claimID string) (bool, error) {
	if id <= 0 || claimID == "" {
		return false, fmt.Errorf("invalid worker claim")
	}
	tx, err := m.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var status string
	var currentClaim sql.NullString
	if err := tx.QueryRow(`SELECT status,worker_claim_id FROM deploy_logs WHERE id=? FOR UPDATE`, id).Scan(&status, &currentClaim); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, service.ErrNotFound
		}
		return false, err
	}
	if status == "running" && currentClaim.Valid && currentClaim.String == claimID {
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return true, nil
	}
	if status != "pending" {
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	result, err := tx.Exec(`UPDATE deploy_logs SET status='running',started_at=NOW(),worker_claim_id=? WHERE id=? AND status='pending'`, claimID, id)
	if err != nil {
		return false, wrapConflict(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return changed == 1, nil
}

func (m *MySQL) ReleaseLogOwnership(id int64, claimID string) error {
	if id <= 0 || claimID == "" {
		return fmt.Errorf("invalid worker claim")
	}
	_, err := m.db.Exec(`UPDATE deploy_logs SET worker_claim_id=NULL WHERE id=? AND worker_claim_id=?`, id, claimID)
	return err
}

func nullStr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	s := v.String
	return &s
}

func nullTime(v sql.NullTime) *time.Time {
	if !v.Valid {
		return nil
	}
	t := v.Time
	return &t
}

func emptyToNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ---- users & audit (migration 003) ----

func scanUser(s rowScanner) (service.User, error) {
	var u service.User
	var groups string
	err := s.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Role, &u.PasswordHash,
		&u.K8sUsername, &groups, &u.Enabled, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return u, err
	}
	for _, g := range strings.Split(groups, ",") {
		if g = strings.TrimSpace(g); g != "" {
			u.K8sGroups = append(u.K8sGroups, g)
		}
	}
	return u, nil
}

const userColumns = `id,username,display_name,role,password_hash,k8s_username,k8s_groups,enabled,created_at,updated_at`

func (m *MySQL) Users() ([]service.User, error) {
	rows, err := m.db.Query(`SELECT ` + userColumns + ` FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []service.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, u)
	}
	return result, rows.Err()
}

func (m *MySQL) UserByID(id int64) (service.User, error) {
	user, err := scanUser(m.db.QueryRow(`SELECT `+userColumns+` FROM users WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return service.User{}, service.ErrNotFound
	}
	return user, err
}

func (m *MySQL) UserByUsername(username string) (service.User, error) {
	user, err := scanUser(m.db.QueryRow(`SELECT `+userColumns+` FROM users WHERE username=?`, username))
	if errors.Is(err, sql.ErrNoRows) {
		return service.User{}, service.ErrNotFound
	}
	return user, err
}

func (m *MySQL) SaveUser(v service.User) (service.User, error) {
	res, err := m.db.Exec(`INSERT INTO users(username,display_name,role,password_hash,k8s_username,k8s_groups,enabled) VALUES(?,?,?,?,?,?,?)`,
		v.Username, v.DisplayName, v.Role, v.PasswordHash, v.K8sUsername, strings.Join(v.K8sGroups, ","), v.Enabled)
	if err != nil {
		return v, wrapConflict(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return v, err
	}
	v.ID = id
	v.CreatedAt = time.Now()
	v.UpdatedAt = v.CreatedAt
	return v, nil
}

func (m *MySQL) UpdateUser(v service.User) error {
	_, err := m.db.Exec(`UPDATE users SET display_name=?,role=?,password_hash=?,k8s_username=?,k8s_groups=?,enabled=? WHERE id=?`,
		v.DisplayName, v.Role, v.PasswordHash, v.K8sUsername, strings.Join(v.K8sGroups, ","), v.Enabled, v.ID)
	return wrapConflict(err)
}

func (m *MySQL) SaveSession(v service.Session) error {
	_, err := m.db.Exec(`INSERT INTO user_sessions(token_hash,user_id,created_at,expires_at) VALUES(?,?,?,?)`,
		v.TokenHash, v.UserID, v.CreatedAt, v.ExpiresAt)
	return wrapConflict(err)
}

func (m *MySQL) SessionByTokenHash(tokenHash string) (service.Session, error) {
	var v service.Session
	err := m.db.QueryRow(`SELECT token_hash,user_id,created_at,expires_at FROM user_sessions WHERE token_hash=?`, tokenHash).
		Scan(&v.TokenHash, &v.UserID, &v.CreatedAt, &v.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return service.Session{}, service.ErrNotFound
	}
	return v, err
}

func (m *MySQL) DeleteSession(tokenHash string) error {
	_, err := m.db.Exec(`DELETE FROM user_sessions WHERE token_hash=?`, tokenHash)
	return err
}

func (m *MySQL) CheckSessionSchema() error {
	rows, err := m.db.Query(`SELECT token_hash,user_id,created_at,expires_at FROM user_sessions LIMIT 0`)
	if err != nil {
		return err
	}
	return rows.Close()
}

// SaveAudit appends one audit row. Audit writes are best-effort at the DB
// edge only — callers decide whether a failed audit blocks the operation.
func (m *MySQL) SaveAudit(v service.AuditEntry) error {
	_, err := m.db.Exec(`INSERT INTO audit_logs(username,action,cluster_id,namespace,gvr,name,detail,result) VALUES(?,?,?,?,?,?,?,?)`,
		v.Username, v.Action, v.ClusterID, v.Namespace, v.GVR, v.Name, v.Detail, v.Result)
	return err
}

// Audits returns recent audit rows ordered by id desc, capped by limit.
func (m *MySQL) Audits(limit int) ([]service.AuditEntry, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := m.db.Query(`SELECT id,username,action,cluster_id,namespace,gvr,name,detail,result,created_at FROM audit_logs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []service.AuditEntry
	for rows.Next() {
		var v service.AuditEntry
		if err := rows.Scan(&v.ID, &v.Username, &v.Action, &v.ClusterID, &v.Namespace, &v.GVR, &v.Name, &v.Detail, &v.Result, &v.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, rows.Err()
}

// ---- api_keys: external OpenAPI credentials (hash-only storage) ----

func scanAPIKey(rows rowScanner) (service.APIKey, error) {
	var k service.APIKey
	err := rows.Scan(&k.ID, &k.Name, &k.KeyHash, &k.Prefix, &k.Role,
		&k.Enabled, &k.ExpiresAt, &k.LastUsedAt, &k.CreatedAt, &k.UpdatedAt)
	return k, err
}

func (m *MySQL) APIKeys() ([]service.APIKey, error) {
	rows, err := m.db.Query(`SELECT id,name,key_hash,prefix,role,enabled,expires_at,last_used_at,created_at,updated_at FROM api_keys ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []service.APIKey
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, k)
	}
	return result, rows.Err()
}

func (m *MySQL) APIKeyByID(id int64) (service.APIKey, error) {
	key, err := scanAPIKey(m.db.QueryRow(`SELECT id,name,key_hash,prefix,role,enabled,expires_at,last_used_at,created_at,updated_at FROM api_keys WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return service.APIKey{}, service.ErrNotFound
	}
	return key, err
}

func (m *MySQL) APIKeyByHash(hash string) (service.APIKey, error) {
	key, err := scanAPIKey(m.db.QueryRow(`SELECT id,name,key_hash,prefix,role,enabled,expires_at,last_used_at,created_at,updated_at FROM api_keys WHERE key_hash=?`, hash))
	if errors.Is(err, sql.ErrNoRows) {
		return service.APIKey{}, service.ErrNotFound
	}
	return key, err
}

func (m *MySQL) SaveAPIKey(v service.APIKey) (service.APIKey, error) {
	res, err := m.db.Exec(`INSERT INTO api_keys(name,key_hash,prefix,role,enabled,expires_at) VALUES(?,?,?,?,?,?)`,
		v.Name, v.KeyHash, v.Prefix, v.Role, v.Enabled, v.ExpiresAt)
	if err != nil {
		return v, wrapConflict(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return v, err
	}
	v.ID = id
	v.CreatedAt = time.Now()
	v.UpdatedAt = v.CreatedAt
	return v, nil
}

func (m *MySQL) UpdateAPIKey(v service.APIKey) error {
	_, err := m.db.Exec(`UPDATE api_keys SET name=?,role=?,enabled=?,expires_at=? WHERE id=?`,
		v.Name, v.Role, v.Enabled, v.ExpiresAt, v.ID)
	return wrapConflict(err)
}

func (m *MySQL) DeleteAPIKey(id int64) error {
	_, err := m.db.Exec(`DELETE FROM api_keys WHERE id=?`, id)
	return err
}

func (m *MySQL) TouchAPIKey(id int64, at time.Time) error {
	_, err := m.db.Exec(`UPDATE api_keys SET last_used_at=? WHERE id=?`, at, id)
	return err
}

// ---- promotion_requests (晋级审批) ----

const promotionRequestColumns = `id,release_id,prod_cluster_id,requester,status,approver,comment,deploy_log_id,created_at,decided_at,expires_at,snapshot,baseline_log_id,expected_image,template_hash,delivery_plan_id`

func scanPromotionRequest(s rowScanner) (service.PromotionRequest, error) {
	var p service.PromotionRequest
	var approver, comment sql.NullString
	var logID, planID sql.NullInt64
	var decided, expires sql.NullTime
	err := s.Scan(&p.ID, &p.ReleaseID, &p.ProdClusterID, &p.Requester, &p.Status,
		&approver, &comment, &logID, &p.CreatedAt, &decided, &expires, &p.Snapshot, &p.BaselineLogID, &p.ExpectedImage, &p.TemplateHash, &planID)
	if err != nil {
		return p, err
	}
	p.Approver = nullStr(approver)
	p.Comment = nullStr(comment)
	if logID.Valid {
		p.DeployLogID = &logID.Int64
	}
	if planID.Valid {
		p.DeliveryPlanID = &planID.Int64
	}
	if decided.Valid {
		t := decided.Time
		p.DecidedAt = &t
	}
	if expires.Valid {
		t := expires.Time
		p.ExpiresAt = &t
	}
	return p, nil
}

func (m *MySQL) PromotionRequests() ([]service.PromotionRequest, error) {
	rows, err := m.db.Query(`SELECT ` + promotionRequestColumns + ` FROM promotion_requests ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []service.PromotionRequest
	for rows.Next() {
		p, err := scanPromotionRequest(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

func (m *MySQL) SavePromotionRequest(v service.PromotionRequest) (service.PromotionRequest, error) {
	res, err := m.db.Exec(`INSERT INTO promotion_requests(release_id,delivery_plan_id,prod_cluster_id,requester,status,expires_at,snapshot,baseline_log_id,expected_image,template_hash) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		v.ReleaseID, v.DeliveryPlanID, v.ProdClusterID, v.Requester, "pending", v.ExpiresAt, v.Snapshot, v.BaselineLogID, v.ExpectedImage, v.TemplateHash)
	if err != nil {
		return v, wrapConflict(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return v, err
	}
	v.ID = id
	v.CreatedAt = time.Now()
	v.Status = "pending"
	return v, nil
}

func (m *MySQL) UpdatePromotionRequest(v service.PromotionRequest) error {
	res, err := m.db.Exec(`UPDATE promotion_requests SET status=?,approver=?,comment=?,deploy_log_id=?,decided_at=? WHERE id=? AND status='pending'`,
		v.Status, v.Approver, v.Comment, v.DeployLogID, v.DecidedAt, v.ID)
	if err != nil {
		return wrapConflict(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("%w: 审批已决定", service.ErrConflict)
	}
	return nil
}
