package service

import (
	"time"
	"unicode/utf8"
)

type Cluster struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	EnvCode     string `json:"env_code"`
	Environment string `json:"environment"`
	// EnvironmentTier: non_production | production
	EnvironmentTier string `json:"environment_tier"`
	// DeployPolicy: automatic | manual
	DeployPolicy string `json:"deploy_policy"`
	// ConnectionType: kubeconfig | token | legacy_ssh
	ConnectionType       string  `json:"connection_type"`
	APIServer            *string `json:"api_server"`
	CredentialCiphertext *string `json:"-"`
	// PlainCredential carries a not-yet-saved credential for the
	// test-connection endpoint; it is never serialized or persisted.
	PlainCredential       string  `json:"-"`
	CACert                *string `json:"ca_cert"`
	InsecureSkipTLSVerify bool    `json:"insecure_skip_tls_verify"`
	Enabled               bool    `json:"enabled"`
	// legacy SSH fields, kept for connection_type=legacy_ssh rows
	K8sMasterHost    string    `json:"k8s_master_host"`
	K8sMasterPort    int       `json:"k8s_master_port"`
	K8sMasterUser    string    `json:"k8s_master_user"`
	K8sMasterKeyPath *string   `json:"k8s_master_key_path"`
	DefaultNamespace string    `json:"default_namespace"`
	Description      *string   `json:"description"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type App struct {
	SourceKind              string    `json:"source_kind"`
	SourceRepoURL           string    `json:"source_repo_url"`
	SourcePath              string    `json:"source_path"`
	DeploymentConfigRepoURL string    `json:"deployment_config_repo_url,omitempty"`
	DeploymentConfigPath    string    `json:"deployment_config_path,omitempty"`
	ImageRepository         string    `json:"image_repository"`
	ID                      int       `json:"id"`
	ClusterID               int       `json:"cluster_id"`
	AppName                 string    `json:"app_name"`
	MatchKeyword            string    `json:"match_keyword"`
	DeploymentName          string    `json:"deployment_name"`
	ContainerName           string    `json:"container_name"`
	Namespace               string    `json:"namespace"`
	HarborProject           string    `json:"harbor_project"`
	Description             *string   `json:"description"`
	CreatedAt               time.Time `json:"created_at"`
}

type DeployLog struct {
	EnvironmentTierSnapshot *string    `json:"environment_tier_snapshot"`
	WorkerClaimID           string     `json:"-"`
	ReleaseID               int64      `json:"release_id,omitempty"`
	RollbackTargetID        int64      `json:"rollback_target_id,omitempty"`
	ID                      int64      `json:"id"`
	ClusterID               int        `json:"cluster_id"`
	AppName                 string     `json:"app_name"`
	DeploymentName          string     `json:"deployment_name"`
	ContainerName           string     `json:"container_name"`
	Namespace               string     `json:"namespace"`
	Version                 string     `json:"version"`
	FileName                string     `json:"file_name"`
	ImageFull               *string    `json:"image_full"`
	DeployType              string     `json:"deploy_type"`
	Status                  string     `json:"status"`
	CurrentStep             *string    `json:"current_step"`
	StepDetail              string     `json:"step_detail,omitempty"`
	ErrorMessage            *string    `json:"error_message"`
	StartedAt               *time.Time `json:"started_at"`
	FinishedAt              *time.Time `json:"finished_at"`
	CreatedAt               time.Time  `json:"created_at"`
}

// ImageRelease mirrors the image_releases table: one row per webhook event,
// tracking the automatic test deploy and the gated production promotion.
type ImageRelease struct {
	RunnerAuthorizationHash string          `json:"-"`
	Metadata                ReleaseMetadata `json:"metadata"`
	ID                      int64           `json:"id"`
	EventKey                string          `json:"event_key"`
	EventID                 string          `json:"event_id"`
	Source                  string          `json:"source"`
	PipelineID              *string         `json:"pipeline_id"`
	PipelineName            *string         `json:"pipeline_name"`
	BuildNumber             *string         `json:"build_number"`
	Branch                  *string         `json:"branch"`
	CommitID                *string         `json:"commit_id"`
	AppName                 string          `json:"app_name"`
	ImageFull               string          `json:"image_full"`
	ImageDigest             *string         `json:"image_digest"`
	Version                 string          `json:"version"`
	TestClusterID           int             `json:"test_cluster_id"`
	TestDeployLogID         int64           `json:"test_deploy_log_id"`
	ProdClusterID           *int            `json:"prod_cluster_id"`
	ProdDeployLogID         *int64          `json:"prod_deploy_log_id"`
	CreatedAt               time.Time       `json:"created_at"`
	UpdatedAt               time.Time       `json:"updated_at"`
}

// ImageEvent is the normalized form of either webhook payload shape.
// TargetCluster is the env_code of the single cluster the build deploys to.
type ImageEvent struct {
	SourceRepositories []string
	SourcePath         string
	EventID            string
	Source             string
	TargetCluster      string
	PipelineID         *string
	PipelineName       *string
	BuildNumber        *string
	Branch             *string
	CommitID           *string
	CommitTimestamp    *time.Time
	ImageDigest        *string
	AppName            string
	ImageFull          string
}

// PromotionRequest is the approval record gating a production promotion.
// One open (pending) request per (release, prod cluster); decided rows keep
// the linked deploy log for audit.
type PromotionRequest struct {
	DeliveryPlanID *int64     `json:"delivery_plan_id,omitempty"`
	CanApprove     *bool      `json:"can_approve,omitempty"`
	TemplateHash   string     `json:"template_hash"`
	ExpectedImage  string     `json:"expected_image"`
	Snapshot       string     `json:"snapshot"`
	BaselineLogID  int64      `json:"baseline_log_id"`
	ID             int64      `json:"id"`
	ReleaseID      int64      `json:"release_id"`
	ProdClusterID  int        `json:"prod_cluster_id"`
	Requester      string     `json:"requester"`
	Status         string     `json:"status"` // pending|approved|rejected|cancelled|expired
	Approver       *string    `json:"approver"`
	Comment        *string    `json:"comment"`
	DeployLogID    *int64     `json:"deploy_log_id"`
	CreatedAt      time.Time  `json:"created_at"`
	DecidedAt      *time.Time `json:"decided_at"`
	ExpiresAt      *time.Time `json:"expires_at"`
}

// ImageReleaseView joins a release with its test/prod deploy log state.
type ImageReleaseView struct {
	Deployments       []DeployLog `json:"deployments"`
	PromotionBlockers []string    `json:"promotion_blockers"`
	ImageRelease
	TestStatus      string  `json:"test_status"`
	TestClusterName string  `json:"test_cluster_name"`
	TestCurrentStep *string `json:"test_current_step"`
	TestError       *string `json:"test_error"`
	ProdStatus      *string `json:"prod_status"`
	ProdCurrentStep *string `json:"prod_current_step"`
	ProdError       *string `json:"prod_error"`
	CanPromote      bool    `json:"can_promote"`
}

// StepResult is one pipeline step entry stored in deploy_logs.step_detail.
type StepResult struct {
	Step              string `json:"step"`
	Success           bool   `json:"success"`
	ObservationStatus string `json:"observation_status,omitempty"`
	Stdout            string `json:"stdout"`
	Stderr            string `json:"stderr"`
}

func NewStepResult(step string, success bool, stdout, stderr string) StepResult {
	return StepResult{
		Step:    step,
		Success: success,
		Stdout:  tailBytes(stdout, 2000),
		Stderr:  tailBytes(stderr, 2000),
	}
}

// tailBytes keeps the last n bytes without cutting a UTF-8 sequence.
func tailBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[1:]
	}
	return s
}

func strPtr(s string) *string { return &s }

// ReleaseMetadata is persisted with the immutable artifact. Evidence is kept
// separate from Kubernetes rollout success.
type ReleaseMetadata struct {
	Binding             *App                         `json:"binding,omitempty"`
	SourceRepositories  []string                     `json:"source_repositories,omitempty"`
	SourcePath          string                       `json:"source_path,omitempty"`
	CommitTimestamp     *time.Time                   `json:"commit_timestamp,omitempty"`
	ConfigurationSource *ConfigurationSourceRevision `json:"configuration_source,omitempty"`
	DigestSource        string                       `json:"digest_source"`
	Verification        *VerificationEvidence        `json:"verification,omitempty"`
	VerificationHistory []VerificationEvidence       `json:"verification_history,omitempty"`
	Scan                *VerificationEvidence        `json:"scan,omitempty"`
	ScanHistory         []VerificationEvidence       `json:"scan_history,omitempty"`
}

// ConfigurationSourceRevision is a Runner-reported Git revision used to
// produce deployment configuration for this image. It records provenance;
// it does not prove checkout contents or make that configuration restorable.
type ConfigurationSourceRevision struct {
	Repository string    `json:"repository_url"`
	Path       string    `json:"path"`
	Revision   string    `json:"revision"`
	RecordedBy string    `json:"recorded_by"`
	RecordedAt time.Time `json:"recorded_at"`
}

type VerificationEvidence struct {
	Digest       string    `json:"digest"`
	Status       string    `json:"status"`
	Kind         string    `json:"kind"`
	Tool         string    `json:"tool,omitempty"`
	ToolVersion  string    `json:"tool_version,omitempty"`
	RulesVersion string    `json:"rules_version,omitempty"`
	ReportURL    string    `json:"report_url"`
	Summary      string    `json:"summary"`
	RecordedBy   string    `json:"recorded_by"`
	RecordedAt   time.Time `json:"recorded_at"`
	ValidUntil   time.Time `json:"valid_until"`
}
