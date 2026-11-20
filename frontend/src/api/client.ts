import axios from 'axios'
import { useAuthStore } from '@/stores/auth-store'

export const api = axios.create({
  baseURL: '/api/v1',
  timeout: 30_000,
  headers: { 'Content-Type': 'application/json' },
})

// Any authenticated call answering 401 means the session cookie expired —
// mark the store anonymous so the route guard sends the user to sign-in.
// /auth/* is exempt: a failed login or anonymous /me probe IS the answer,
// not a session-expiry signal.
api.interceptors.response.use(undefined, (err) => {
  const url: string = err?.config?.url ?? ''
  if (err?.response?.status === 401 && !url.startsWith('/auth/')) {
    useAuthStore.getState().auth.markAnonymous()
  }
  return Promise.reject(err)
})

// ---- Auth (step 9): opaque HttpOnly session cookie, no token in JS ----

export type PublicUser = {
  id: number
  username: string
  display_name: string
  role: 'viewer' | 'operator' | 'admin'
  k8s_username: string
  k8s_groups: string[]
  enabled: boolean
}

export const authApi = {
  login: (username: string, password: string) =>
    api.post<{ user: PublicUser }>('/auth/login', { username, password }),
  me: () => api.get<{ user: PublicUser }>('/auth/me'),
  logout: () => api.post('/auth/logout'),
}

// Resolves the session once per page load: /auth/me answers whether the
// HttpOnly cookie is still valid. Components never read the cookie itself.
let sessionProbe: Promise<PublicUser | null> | null = null

export function ensureSession(): Promise<PublicUser | null> {
  const { status, user } = useAuthStore.getState().auth
  if (status === 'authenticated') return Promise.resolve(user)
  if (status === 'anonymous') return Promise.resolve(null)
  sessionProbe ??= authApi
    .me()
    .then((r) => {
      useAuthStore.getState().auth.setUser(r.data.user)
      return r.data.user
    })
    .catch(() => {
      useAuthStore.getState().auth.markAnonymous()
      return null
    })
    .finally(() => {
      sessionProbe = null
    })
  return sessionProbe
}
export type Cluster = {
  id: number
  name: string
  env_code: string
  environment: string
  environment_tier: 'non_production' | 'production'
  deploy_policy: 'automatic' | 'manual'
  connection_type: 'kubeconfig' | 'token' | 'legacy_ssh'
  api_server?: string | null
  credential_configured: boolean
  ca_cert?: string | null
  insecure_skip_tls_verify: boolean
  enabled: boolean
  k8s_master_host?: string
  k8s_master_port?: number
  k8s_master_user?: string
  default_namespace: string
  description?: string | null
  created_at: string
  updated_at: string
}
// credential is write-only (plaintext in, never returned); on update leave it
// absent/blank to keep the stored credential.
export type ClusterPayload = {
  name: string
  env_code: string
  environment: string
  environment_tier?: string
  deploy_policy?: string
  connection_type: string
  api_server?: string
  credential?: string
  ca_cert?: string
  insecure_skip_tls_verify?: boolean
  enabled?: boolean
  default_namespace?: string
  description?: string
  k8s_master_host?: string
  k8s_master_port?: number
  k8s_master_user?: string
  k8s_master_key_path?: string
}
export type ComponentBinding = {
  id: number
  cluster_id: number
  app_name: string
  source_kind: 'repository' | 'third_party' | 'unknown'
  source_repo_url: string
  source_path: string
  deployment_config_repo_url?: string
  deployment_config_path?: string
  image_repository: string
  deployment_name: string
  container_name: string
  namespace: string
  match_keyword: string
  harbor_project: string
  description?: string | null
  created_at: string
}
export type ComponentBindingPayload = Omit<
  ComponentBinding,
  'id' | 'created_at'
>
export type BindingPreview = {
  app: ComponentBinding
  current_image: string
  warnings: string[]
}
export type DeployLog = {
  release_id?: number
  rollback_target_id?: number
  id: number
  cluster_id: number
  app_name: string
  deployment_name: string
  container_name: string
  namespace: string
  version: string
  image_full?: string | null
  status: string
  deploy_type: string
  current_step?: string | null
  // step_detail is a JSON-encoded DeployStepResult[] — parse before use;
  // it may be empty or truncated while the deploy is in flight.
  step_detail?: string
  error_message?: string | null
  started_at?: string | null
  finished_at?: string | null
  environment_tier_snapshot?: string | null
  created_at: string
}
export type DeliveryExecutionAction = {
  id: number
  deploy_log_id: number
  action_key: string
  action_kind: string
  status: 'intent' | 'applied' | 'not_applied' | 'unknown'
  intent: { previous_image?: string; target_image?: string }
  observed?: { current_image?: string; readback?: string }
  outcome_code?: string
  created_at: string
  finished_at?: string | null
}
export type DeliveryRunEvent = {
  id: number
  run_id: number
  version: number
  event_key: string
  event_type: string
  stage: string
  status: string
  detail?: string
  payload?: unknown
  created_at: string
}
export type DeliveryRunView = {
  run: {
    id: number
    delivery_plan_id: number
    promotion_request_id: number
    deploy_log_id: number
    strategy: 'rolling' | 'blue_green' | 'gateway_canary'
    status: string
    current_stage: string
    version: number
    error_message?: string | null
    created_at: string
    updated_at: string
  }
  plan: DeliveryPlan
  deploy_log: DeployLog
  events: DeliveryRunEvent[]
  actions: DeliveryExecutionAction[]
}
export type DeliveryRunControlAction =
  | 'confirm_canary_stage'
  | 'confirm_canary_restore'
  | 'confirm_cutover'
  | 'confirm_live_verification'
  | 'restore_stable'
  | 'reconcile'
  | 'cleanup_candidate'
  | 'pause'
  | 'continue'
  | 'cancel'
// One pipeline step entry inside DeployLog.step_detail (JSON string).
export type DeployStepResult = {
  step: string
  success: boolean
  observation_status?: 'verified' | 'unknown'
  stdout: string
  stderr: string
}
export type RollbackRecord = {
  id: number
  version: string
  image_full?: string | null
  deploy_type: string
  finished_at?: string | null
}
export type RollbackPayload = { cluster_id: number; target_log_id: number }
export type VerificationEvidence = {
  digest: string
  status: 'passed' | 'failed'
  kind: 'ci' | 'smoke' | 'manual' | 'scan'
  report_url: string
  summary: string
  tool?: string
  tool_version?: string
  rules_version?: string
  valid_until: string
  recorded_at?: string
  recorded_by?: string
}
export type ReleaseMetadata = {
  binding?: ComponentBinding
  source_repositories?: string[]
  source_path?: string
  commit_timestamp?: string
  configuration_source?: {
    repository_url: string
    path: string
    revision: string
    recorded_by: string
    recorded_at: string
  }
  digest_source: string
  verification?: VerificationEvidence
  verification_history?: VerificationEvidence[]
  scan?: VerificationEvidence
  scan_history?: VerificationEvidence[]
}
export type ImageReleaseView = {
  metadata?: ReleaseMetadata
  deployments?: DeployLog[]
  promotion_blockers?: string[]
  id: number
  event_id: string
  source: string
  app_name: string
  image_full: string
  version: string
  pipeline_id?: string | null
  pipeline_name?: string | null
  build_number?: string | null
  branch?: string | null
  commit_id?: string | null
  image_digest?: string | null
  test_cluster_id: number
  test_deploy_log_id: number
  prod_cluster_id?: number | null
  prod_deploy_log_id?: number | null
  created_at: string
  updated_at: string
  test_status: string
  test_cluster_name?: string | null
  test_current_step?: string | null
  test_error?: string | null
  prod_status?: string | null
  prod_current_step?: string | null
  prod_error?: string | null
  can_promote: boolean
}
// PromotionRequest is the separation-of-duties record: a promote call files
// a pending request; a second operator approves or rejects it.
export interface PromotionRequest {
  delivery_plan_id?: number | null
  can_approve?: boolean
  template_hash?: string
  snapshot?: string
  baseline_log_id?: number
  expected_image?: string
  id: number
  release_id: number
  prod_cluster_id: number
  requester: string
  status: 'pending' | 'approved' | 'rejected' | 'cancelled' | 'expired'
  approver?: string | null
  comment?: string | null
  deploy_log_id?: number | null
  created_at: string
  decided_at?: string | null
  expires_at?: string | null
}
export type DeliveryPlan = {
  id: number
  release_id: number
  target_cluster_id: number
  target_app_id: number
  strategy: 'rolling' | 'blue_green' | 'gateway_canary'
  status: 'preview_only'
  snapshot_hash: string
  snapshot: {
    schema_version: number
    strategy: string
    evidence_policy?: {
      environment_id: number
      version: number
      required_verification_kinds: Array<'ci' | 'smoke' | 'manual'>
      max_evidence_age_minutes: number
      trusted_submitters: Partial<Record<'ci' | 'smoke' | 'manual', string[]>>
    }
    policy?: {
      binding_id: number
      version: number
      source: 'versioned_policy' | 'legacy_binding_draft'
      strategy: string
      service_name?: string
      candidate_retention_hours?: number
      canary_analysis?: CanaryAnalysisPolicy
      readiness: 'configurable' | 'integration_pending'
    }
    candidate_retention_hours?: number
    checks?: Array<{
      key: string
      status: 'passed' | 'review' | 'blocked' | 'unknown'
      title: string
      detail: string
    }>
    artifact: {
      release_id: number
      event_id: string
      source?: string
      source_kind?: 'repository' | 'third_party' | 'unknown' | string
      source_repository?: string
      source_path?: string
      deployment_config_repository?: string
      deployment_config_path?: string
      image_repository?: string
      image: string
      digest?: string | null
      digest_source?: string
      version: string
      pipeline_id?: string | null
      pipeline_name?: string | null
      branch?: string | null
      commit_id?: string | null
      build_number?: string | null
      configuration_source?: {
        repository_url: string
        path: string
        revision: string
        recorded_by: string
        recorded_at: string
      }
    }
    target: {
      cluster_id: number
      cluster_name: string
      app_id: number
      app_name: string
      test_app_id: number
      test_binding_id: number
      test_environment_id: number
      test_environment_name: string
      namespace: string
      deployment: string
      container: string
      service_name?: string
      service_uid?: string
      service_resource_version?: string
      service_selector?: Record<string, string>
      service_config_hash?: string
      stable_label_key?: string
      stable_label_already_present?: boolean
      candidate_label_key?: string
      candidate_label_value?: string
      candidate_removed_label_keys?: string[]
      replicas?: number
      environment_id: number
      environment_name: string
      environment_tier: string
      application_id: number
      application_name: string
      component_id: number
      component_name: string
      binding_id: number
      template_workload_ref?: DeliveryWorkloadReference
      active_workload_ref?: DeliveryWorkloadReference
      workload_uid: string
      resource_version: string
      current_image: string
      stable_image_digest?: string
      stable_release_id?: number
      stable_configuration_source?: {
        repository_url: string
        path: string
        revision: string
        recorded_by: string
        recorded_at: string
      }
      deployment_pipeline?: {
        connection_id: number
        pipeline_id: string
      }
      deployment_pipeline_scope_review?: {
        id: number
        binding_id: number
        connection_id: number
        pipeline_id: string
        definition_hash: string
        write_scope: 'configuration_only' | 'workload_or_image' | 'unknown'
        reviewer: string
        reviewed_at: string
      }
      workload_hash: string
      service_account_name?: string
      service_account_pull_secrets_included?: boolean
      configuration_references?: Array<{
        kind: 'ConfigMap' | 'Secret'
        name: string
        uid?: string
        resource_version?: string
      }>
    }
  }
  blockers: string[]
  requested_by: string
  created_at: string
}
// promote returns either an executed deploy (approval off) or a filed
// approval request (approval on) — distinguish on `request_id`.
export type PromoteResult =
  | {
      request_id: number
      release_id: number
      prod_cluster_id: number
      status: string
      expires_at?: string | null
    }
  | { release_id: number; prod_log_id: number; status: string }
export type DeliveryNotification = {
  id: string
  event: string
  summary?: string
  status: 'pending' | 'delivered' | 'dead'
  attempts: number
  last_error?: string
  created_at: string
  next_attempt_at: string
}
export type DeliveryNotificationPage = {
  items: DeliveryNotification[]
  next_cursor?: string
  has_more: boolean
}
export type DeliveryMetrics = {
  invalid_timestamp_samples?: number
  unknown_tier_samples: number
  app_name?: string
  cluster_id?: number
  window_days: number
  window_start: string
  as_of: string
  production_attempts: number
  successful_deployments: number
  execution_failures: number
  active_tasks: number
  pinned_deployments: number
  duration_samples: number
  approval_wait_samples: number
  pending_approvals: number
  execution_failure_rate: number | null
  execution_p95_seconds: number | null
  approval_wait_p95_seconds: number | null
  incident_recovery: {
    data_available: boolean
    incident_samples: number
    recovered_samples: number
    unrecovered_samples: number
    intervention_recovery_samples: number
    invalid_timestamp_samples: number
    causality_assessed_samples: number
    causality_pending_samples: number
    remediation_classified_samples: number
    remediation_pending_samples: number
    mean_recovery_seconds: number | null
    mean_intervention_to_recovery_seconds: number | null
    missing_data: string
  }
  dora: Record<
    string,
    { value: number | null; missing_data: string; sample_count?: number }
  >
}
export type DeliveryIncidentEvent = {
  id: number
  incident_id: number
  event_type:
    | 'incident_recorded'
    | 'intervention_started'
    | 'recovery_confirmed'
    | 'causality_assessed'
    | 'remediation_classified'
  deploy_log_id: number
  occurred_at: string
  recorded_at: string
  recorded_by: string
  detail: string
  causality_status?: 'caused_by_deployment' | 'not_caused_by_deployment'
  remediation_type?: DeliveryRemediationType
  remediation_deploy_log_id?: number
  remediation_plan_status?: DeliveryRemediationPlanStatus
}
export type DeliveryRemediationType =
  | 'rollback'
  | 'hotfix_deployment'
  | 'configuration_deployment'
  | 'restart_or_scale'
  | 'no_action'
  | 'unknown'
export type DeliveryRemediationPlanStatus =
  | 'planned'
  | 'unplanned'
  | 'not_applicable'
  | 'unknown'
export type DeliveryIncident = {
  id: number
  deploy_log_id: number
  release_id?: number
  cluster_id: number
  app_name: string
  summary: string
  status: 'open' | 'recovered'
  occurred_at: string
  recorded_at: string
  recorded_by: string
  version: number
  recovery_occurred_at?: string
  recovery_recorded_at?: string
  recovery_recorded_by?: string
  recovery_confirmation?: string
  intervention_started_at?: string
  intervention_recorded_at?: string
  intervention_recorded_by?: string
  causality_status:
    | 'unknown'
    | 'caused_by_deployment'
    | 'not_caused_by_deployment'
  causality_recorded_at?: string
  causality_recorded_by?: string
  causality_reason?: string
  remediation_type?: DeliveryRemediationType
  remediation_deploy_log_id?: number
  remediation_plan_status?: DeliveryRemediationPlanStatus
  remediation_recorded_at?: string
  remediation_recorded_by?: string
  remediation_reason?: string
  events: DeliveryIncidentEvent[]
}
export type DriftObservation = {
  app_id: number
  app_name: string
  cluster_id: number
  namespace: string
  workload: string
  container: string
  status: 'unknown' | 'in_sync' | 'drifted'
  expected_image: string | null
  actual_image: string | null
  baseline_log_id?: number | null
  drift_started_at?: string | null
  observed_at?: string
  last_observed_at?: string
  observation_count?: number
  observed_by?: string
  source?: string
  reason?: string
}
export type DriftEvent = {
  id: number
  app_id: number
  app_name: string
  cluster_id: number
  namespace: string
  workload: string
  container: string
  previous_status?: 'unknown' | 'in_sync' | 'drifted'
  status: 'unknown' | 'in_sync' | 'drifted'
  change_kind: 'initial' | 'status' | 'baseline' | 'image' | 'reason'
  expected_image: string | null
  actual_image: string | null
  baseline_log_id?: number | null
  reason?: string
  drift_started_at?: string | null
  drift_duration_seconds?: number | null
  observed_at: string
  observed_by: string
  source: string
}
export type DeliveryEnvironment = {
  id: number
  code: string
  name: string
  environment_tier: 'non_production' | 'production'
  evidence_policy_version?: number
  legacy_cluster_id?: number
  requires_review: boolean
  created_at: string
}
export type DeliveryEnvironmentEvidencePolicyVersion = {
  environment_id: number
  version: number
  required_verification_kinds: Array<'ci' | 'smoke' | 'manual'>
  max_evidence_age_minutes: number
  trusted_submitters: Partial<Record<'ci' | 'smoke' | 'manual', string[]>>
  created_by: string
  created_at: string
}
export type DeliveryEnvironmentEvidencePolicyHistory = {
  environment_id: number
  current_version: number
  versions: DeliveryEnvironmentEvidencePolicyVersion[]
}
export type DeliveryApplication = {
  id: number
  name: string
  legacy_app_id?: number
  requires_review: boolean
  created_at: string
}
export type DeliveryComponent = {
  id: number
  application_id: number
  name: string
  legacy_app_id?: number
  requires_review: boolean
  created_at: string
}
export type DeliveryWorkloadReference = {
  cluster_id: number
  namespace: string
  kind: string
  name: string
  uid?: string
  resource_version?: string
}
export type DeliveryBinding = {
  id: number
  app_id: number
  application_id: number
  component_id: number
  environment_id: number
  strategy_draft?: 'rolling' | 'blue_green' | 'gateway_canary'
  service_name?: string
  deployment_pipeline?: {
    connection_id: number
    pipeline_id: string
  } | null
  template_workload_ref: DeliveryWorkloadReference
  active_workload_ref: DeliveryWorkloadReference
  requires_review: boolean
  created_at: string
}
export type CanaryGatewayRouteRef = {
  connection_id: number
  route_id: string
  stable_backend_id: string
  candidate_backend_id: string
}
export type CanaryAnalysisPolicy = {
  gateway_route?: CanaryGatewayRouteRef
  min_requests: number
  max_observation_age_seconds: number
  max_candidate_error_rate: number
  max_error_rate_increase: number
  max_candidate_p95_latency_ms: number
  max_p95_latency_increase_ratio: number
  stages: Array<{
    candidate_weight_percent: number
    observation_duration_seconds: number
  }>
}
export type DeliveryPolicyVersion = {
  binding_id: number
  version: number
  strategy: 'rolling' | 'blue_green' | 'gateway_canary'
  service_name?: string
  candidate_retention_hours?: number
  canary_analysis?: CanaryAnalysisPolicy
  readiness: 'configurable' | 'integration_pending'
  created_by: string
  created_at: string
}
export type DeliveryPolicyHistory = {
  binding_id: number
  current_version: number
  versions: DeliveryPolicyVersion[]
}
export type DeliveryPreviewCheck = {
  key: string
  status: 'passed' | 'review' | 'blocked' | 'unknown'
  title: string
  detail: string
}
export type DeliveryCapabilityPreview = {
  cluster_id: number
  namespace: string
  deployment: string
  deployment_uid?: string
  resource_version?: string
  observed_at: string
  matching_services: string[]
  autoscalers: string[]
  checks: DeliveryPreviewCheck[]
}
export type DeliveryModel = {
  environments: DeliveryEnvironment[]
  applications: DeliveryApplication[]
  components: DeliveryComponent[]
  bindings: DeliveryBinding[]
  pipeline_scope_reviews?: DeliveryPipelineScopeReview[]
}
export type DeliveryPipelineScopeReview = {
  id: number
  binding_id: number
  connection_id: number
  pipeline_id: string
  definition_hash: string
  write_scope: 'configuration_only' | 'workload_or_image' | 'unknown'
  reviewer: string
  reviewed_at: string
}
export type DeliveryApprovalGrant = {
  user_id: number
  username: string
  application_id: number
  application_name: string
  environment_id: number
  environment_name: string
  granted_by: string
  created_at: string
}
export const platformApi = {
  users: () => api.get<PublicUser[]>('/users'),
  deliveryApprovalGrants: () =>
    api.get<DeliveryApprovalGrant[]>('/delivery/approval-grants'),
  replaceDeliveryApprovalGrants: (
    userId: number,
    scopes: Array<{ application_id: number; environment_id: number }>
  ) =>
    api.put<DeliveryApprovalGrant[]>(
      `/delivery/approval-grants/users/${userId}`,
      { scopes }
    ),
  deliveryNotificationAlerts: (cursor?: string) =>
    api.get<DeliveryNotificationPage>('/delivery/notifications/active', {
      params: { limit: 20, ...(cursor ? { cursor } : {}) },
    }),
  deliveryMetrics: (
    windowDays = 30,
    filters: { appName?: string; clusterId?: number } = {}
  ) =>
    api.get<DeliveryMetrics>('/delivery/metrics', {
      params: {
        window_days: windowDays,
        ...(filters.appName ? { app_name: filters.appName } : {}),
        ...(filters.clusterId ? { cluster_id: filters.clusterId } : {}),
      },
    }),
  deliveryIncidents: (limit = 50) =>
    api.get<DeliveryIncident[]>('/delivery/incidents', { params: { limit } }),
  createDeliveryIncident: (payload: {
    deploy_log_id: number
    occurred_at: string
    summary: string
    idempotency_key: string
  }) => api.post<DeliveryIncident>('/delivery/incidents', payload),
  confirmDeliveryIncidentRecovery: (
    id: number,
    payload: {
      expected_version: number
      occurred_at: string
      confirmation: string
      idempotency_key: string
    }
  ) =>
    api.post<DeliveryIncident>(`/delivery/incidents/${id}/recovery`, payload),
  startDeliveryIncidentIntervention: (
    id: number,
    payload: {
      expected_version: number
      occurred_at: string
      detail: string
      idempotency_key: string
    }
  ) =>
    api.post<DeliveryIncident>(
      `/delivery/incidents/${id}/intervention`,
      payload
    ),
  assessDeliveryIncidentCausality: (
    id: number,
    payload: {
      expected_version: number
      status: 'caused_by_deployment' | 'not_caused_by_deployment'
      reason: string
      idempotency_key: string
    }
  ) =>
    api.post<DeliveryIncident>(`/delivery/incidents/${id}/causality`, payload),
  recordDeliveryIncidentRemediation: (
    id: number,
    payload: {
      expected_version: number
      remediation_type: Exclude<DeliveryRemediationType, 'unknown'>
      plan_status: DeliveryRemediationPlanStatus
      remediation_deploy_log_id?: number
      reason: string
      idempotency_key: string
    }
  ) =>
    api.post<DeliveryIncident>(
      `/delivery/incidents/${id}/remediation`,
      payload
    ),
  deliveryDrift: (clusterId: number, appIDs?: number[]) =>
    api.get<DriftObservation[]>('/delivery/drift', {
      params: {
        cluster_id: clusterId,
        ...(appIDs?.length ? { app_ids: appIDs.join(',') } : {}),
      },
    }),
  deliveryDriftHistory: (clusterId: number, appIDs?: number[], limit = 25) =>
    api.get<DriftEvent[]>('/delivery/drift/history', {
      params: {
        cluster_id: clusterId,
        limit,
        ...(appIDs?.length ? { app_ids: appIDs.join(',') } : {}),
      },
    }),
  deliveryModel: () => api.get<DeliveryModel>('/delivery/model'),
  deliveryEnvironmentEvidencePolicy: (environmentId: number) =>
    api.get<DeliveryEnvironmentEvidencePolicyHistory>(
      `/environments/${environmentId}/evidence-policy`
    ),
  saveDeliveryEnvironmentEvidencePolicy: (
    environmentId: number,
    payload: {
      expected_version: number
      required_verification_kinds: Array<'ci' | 'smoke' | 'manual'>
      max_evidence_age_minutes: number
      trusted_submitters: Partial<Record<'ci' | 'smoke' | 'manual', string[]>>
    }
  ) =>
    api.put<DeliveryEnvironmentEvidencePolicyVersion>(
      `/environments/${environmentId}/evidence-policy`,
      payload
    ),
  deliveryPolicy: (appId: number) =>
    api.get<DeliveryPolicyHistory>(`/apps/${appId}/delivery-policy`),
  createDeliveryPolicyVersion: (
    appId: number,
    payload: {
      expected_version: number
      strategy: DeliveryPolicyVersion['strategy']
      service_name?: string
      candidate_retention_hours?: number
      canary_analysis?: CanaryAnalysisPolicy
    }
  ) =>
    api.put<DeliveryPolicyVersion>(`/apps/${appId}/delivery-policy`, payload),
  deliveryPreview: (payload: {
    cluster_id: number
    namespace: string
    deployment_name: string
  }) => api.post<DeliveryCapabilityPreview>('/delivery-preview', payload),
  createEnvironment: (payload: {
    name: string
    environment_tier: DeliveryEnvironment['environment_tier']
  }) => api.post<DeliveryEnvironment>('/environments', payload),
  confirmEnvironment: (id: number, name: string) =>
    api.put<DeliveryEnvironment>(`/environments/${id}`, { name }),
  createDeliveryApplication: (name: string) =>
    api.post<DeliveryApplication>('/applications', { name }),
  createDeliveryComponent: (applicationId: number, name: string) =>
    api.post<DeliveryComponent>(`/applications/${applicationId}/components`, {
      name,
    }),
  assignDeliveryBinding: (
    appId: number,
    payload: {
      application_id: number
      component_id: number
      environment_id: number
      strategy_draft?: 'rolling' | 'blue_green' | 'gateway_canary' | ''
      service_name?: string
      expected_policy_version?: number
      strategy?: DeliveryPolicyVersion['strategy']
      candidate_retention_hours?: number
      canary_analysis?: CanaryAnalysisPolicy
      deployment_pipeline?: {
        connection_id: number
        pipeline_id: string
      }
    }
  ) => api.put<DeliveryBinding>(`/apps/${appId}/delivery-binding`, payload),
  rebindDeliveryWorkload: (appId: number) =>
    api.post<DeliveryBinding>(
      `/apps/${appId}/delivery-binding/rebind-workload`
    ),
  health: () => axios.get('/health'),
  clusters: () => api.get<Cluster[]>('/clusters'),
  createCluster: (payload: ClusterPayload) =>
    api.post<Cluster>('/clusters', payload),
  updateCluster: (id: number, payload: ClusterPayload) =>
    api.put<Cluster>(`/clusters/${id}`, payload),
  deleteCluster: (id: number) => api.delete(`/clusters/${id}`),
  testClusterConnection: (payload: ClusterPayload) =>
    api.post<{ ok: boolean; version: string }>(
      '/clusters/test-connection',
      payload
    ),
  apps: (clusterId?: number) =>
    api.get<ComponentBinding[]>('/apps', { params: { cluster_id: clusterId } }),
  createApp: (payload: ComponentBindingPayload) =>
    api.post<ComponentBinding>('/apps', payload),
  updateApp: (id: number, payload: ComponentBindingPayload) =>
    api.put<ComponentBinding>(`/apps/${id}`, payload),
  previewApp: (payload: ComponentBindingPayload) =>
    api.post<BindingPreview>('/apps/preview', payload),
  logs: (params?: {
    cluster_id?: number
    app_name?: string
    status?: string
    limit?: number
  }) => api.get<DeployLog[]>('/deploy/logs', { params }),
  deployLog: (id: number) => api.get<DeployLog>(`/deploy/logs/${id}`),
  deployActions: (id: number) =>
    api.get<DeliveryExecutionAction[]>(`/deploy/logs/${id}/actions`),
  deliveryRun: (id: number) => api.get<DeliveryRunView>(`/delivery-runs/${id}`),
  deliveryRunByDeployLog: (id: number) =>
    api.get<DeliveryRunView>(`/delivery-runs/by-deploy-log/${id}`),
  deliveryRunAction: (
    id: number,
    payload: {
      action: DeliveryRunControlAction
      expected_version: number
      idempotency_key: string
      recovery_compatibility_acknowledged?: boolean
    }
  ) => api.post<DeliveryRunView>(`/delivery-runs/${id}/actions`, payload),
  reconcileDeployLog: (id: number) =>
    api.post<{ log_id: number; status: string; message: string }>(
      `/deploy/logs/${id}/reconcile`
    ),
  deploy: (payload: Record<string, unknown>) => api.post('/deploy', payload),
  pods: (params?: { cluster_id?: number; namespace?: string }) =>
    api.get('/k8s/pods', { params }),
  releases: (params?: { app_name?: string; limit?: number }) =>
    api.get<ImageReleaseView[]>('/releases', { params }),
  promotionTargets: (id: number) =>
    api.get<Cluster[]>(`/releases/${id}/promotion-targets`),
  release: (id: number) => api.get<ImageReleaseView>(`/releases/${id}`),
  recordVerification: (id: number, evidence: VerificationEvidence) =>
    api.post<ReleaseMetadata>(`/releases/${id}/verification`, evidence),
  recordScan: (id: number, evidence: VerificationEvidence) =>
    api.post<ReleaseMetadata>(`/releases/${id}/scan`, evidence),
  promoteRelease: (id: number, clusterId: number) =>
    api.post<PromoteResult>(`/releases/${id}/promote`, {
      cluster_id: clusterId,
    }),
  promotionRequests: (status?: string) =>
    api.get<PromotionRequest[]>('/promotion-requests', {
      params: status ? { status } : {},
    }),
  approvePromotionRequest: (id: number) =>
    api.post(`/promotion-requests/${id}/approve`),
  rejectPromotionRequest: (id: number, comment?: string) =>
    api.post(`/promotion-requests/${id}/reject`, { comment: comment ?? '' }),
  deliveryPlans: () => api.get<DeliveryPlan[]>('/delivery-plans'),
  deliveryPlan: (id: number) => api.get<DeliveryPlan>(`/delivery-plans/${id}`),
  createDeliveryPlan: (releaseId: number, clusterId: number) =>
    api.post<DeliveryPlan>('/delivery-plans', {
      release_id: releaseId,
      cluster_id: clusterId,
    }),
  requestDeliveryPlanApproval: (id: number, manualReviewAcknowledged = false) =>
    api.post<{
      request_id: number
      delivery_plan_id: number
      release_id: number
      prod_cluster_id: number
      status: string
      expires_at?: string | null
    }>(`/delivery-plans/${id}/request-approval`, {
      manual_review_acknowledged: manualReviewAcknowledged,
    }),
  // What the release's app currently runs on the target cluster — the
  // promote dialog's current-vs-candidate comparison. 404 when the cluster
  // has no app mapping, 502 when the cluster is unreachable.
  currentImage: (id: number, clusterId: number) =>
    api.get<{ image: string; app?: ComponentBinding }>(
      `/releases/${id}/current-image`,
      {
        params: { cluster_id: clusterId },
      }
    ),
  rollbackHistory: (appName: string, clusterId: number) =>
    api.get<RollbackRecord[]>(
      `/rollback/history/${encodeURIComponent(appName)}`,
      { params: { cluster_id: clusterId } }
    ),
  rollback: (payload: RollbackPayload) =>
    api.post<{ log_id: number; status: string; message: string }>(
      '/rollback',
      payload
    ),
  apiKeys: () => api.get<APIKey[]>('/api-keys'),
  createApiKey: (payload: {
    name: string
    role?: string
    expires_at?: string
  }) => api.post<APIKeyCreated>('/api-keys', payload),
  updateApiKey: (
    id: number,
    payload: {
      name?: string
      role?: string
      enabled?: boolean
      expires_at?: string
    }
  ) => api.put<APIKey>(`/api-keys/${id}`, payload),
  deleteApiKey: (id: number) => api.delete(`/api-keys/${id}`),
}

export type APIKey = {
  id: number
  name: string
  prefix: string
  role: 'viewer' | 'operator'
  enabled: boolean
  expires_at: string | null
  last_used_at: string | null
  created_at: string
  updated_at: string
}

// createApiKey response — `key` is the plaintext token, returned exactly once.
export type APIKeyCreated = APIKey & { key: string }

// ---- Generic Kubernetes resource API (step 3 backend contract) ----

export type K8sResourceType = {
  group: string
  version: string
  resource: string
  kind: string
  namespaced: boolean
  // Write capability flags (step 6): the detail page renders action
  // controls only for capabilities the server reports.
  scalable?: boolean
  imageable?: boolean
  restartable?: boolean
  deletable?: boolean
  editable?: boolean
  creatable?: boolean
  // data_editable enables the structured key/value editor (configmaps,
  // secrets) — a server-side merge so masked Secret values never
  // round-trip through the client.
  data_editable?: boolean
}

// Unstructured Kubernetes object as returned by /k8s/resources.
export type K8sObject = {
  apiVersion?: string
  kind?: string
  metadata?: {
    name?: string
    namespace?: string
    uid?: string
    creationTimestamp?: string
    labels?: Record<string, string>
    annotations?: Record<string, string>
    [key: string]: unknown
  }
  spec?: Record<string, unknown>
  status?: Record<string, unknown>
  [key: string]: unknown
}

export type K8sObjectList = {
  apiVersion: string
  kind: string
  metadata?: Record<string, unknown>
  items: K8sObject[]
}

export type K8sResourceQuery = {
  cluster_id: number
  group?: string
  version: string
  resource: string
  namespace?: string
  all_namespaces?: boolean
  label_selector?: string
  field_selector?: string
}

// Params every k8s resource endpoint shares; the write helpers build
// their paths from the same query shape as the detail GET.
export type K8sObjectParams = Omit<
  K8sResourceQuery,
  'all_namespaces' | 'label_selector' | 'field_selector'
>

export type K8sScaleBody = { replicas: number; resource_version?: string }
export type K8sImageBody = {
  container: string
  image: string
  resource_version?: string
}
export type K8sRestartBody = { resource_version?: string }
export type K8sPodParams = Pick<K8sObjectParams, 'cluster_id' | 'namespace'>

// Per-(cluster,user,namespace) RBAC verdict from GET /k8s/capabilities —
// the UI hides buttons the apiserver would deny. Keys are
// "group/version/resource" (empty group renders as "/v1/pods").
export type K8sResourceCaps = {
  list: boolean
  create: boolean
  update: boolean
  delete: boolean
}
export type K8sCapabilities = {
  resources: Record<string, K8sResourceCaps>
  exec: boolean
  logs: boolean
}

export const capsKey = (p: {
  group?: string
  version: string
  resource: string
}) => `${p.group ?? ''}/${p.version}/${p.resource}`

const enc = encodeURIComponent

export const k8sApi = {
  resourceTypes: () => api.get<K8sResourceType[]>('/k8s/resource-types'),
  resources: (params: K8sResourceQuery) =>
    api.get<K8sObjectList>('/k8s/resources', { params }),
  resource: (name: string, params: K8sObjectParams, revealSecret = false) =>
    api.get<K8sObject>(`/k8s/resources/${enc(name)}`, {
      params: { ...params, ...(revealSecret ? { reveal_secret: true } : {}) },
    }),
  overview: (clusterId: number) =>
    api.get<K8sClusterOverview>('/k8s/overview', {
      params: { cluster_id: clusterId },
    }),
  scale: (name: string, params: K8sObjectParams, body: K8sScaleBody) =>
    api.post<K8sObject>(`/k8s/resources/${enc(name)}/scale`, body, { params }),
  setImage: (name: string, params: K8sObjectParams, body: K8sImageBody) =>
    api.post<K8sObject>(`/k8s/resources/${enc(name)}/image`, body, { params }),
  restart: (name: string, params: K8sObjectParams, body: K8sRestartBody) =>
    api.post<K8sObject>(`/k8s/resources/${enc(name)}/restart`, body, {
      params,
    }),
  logs: (name: string, params: K8sPodParams, tail = 200) =>
    api.get<{ pod: string; logs: string }>(`/k8s/pods/${enc(name)}/logs`, {
      params: { ...params, tail },
    }),
  restartPod: (name: string, params: K8sPodParams) =>
    api.post<{ pod: string; message: string }>(
      `/k8s/pods/${enc(name)}/restart`,
      undefined,
      { params }
    ),
  remove: (name: string, params: K8sObjectParams, resourceVersion?: string) =>
    api.delete<{ detail: string }>(`/k8s/resources/${enc(name)}`, {
      params: { ...params, resource_version: resourceVersion || undefined },
    }),
  // Whole-manifest replace — the YAML editor's save path. The manifest
  // must carry metadata.resourceVersion (update precondition → 409).
  // dryRun runs the same validation server-side without persisting —
  // the "校验后 Apply" step of the edit flow.
  update: (
    name: string,
    params: K8sObjectParams,
    manifest: string,
    dryRun = false
  ) =>
    api.put<K8sObject>(
      `/k8s/resources/${enc(name)}`,
      { manifest },
      { params: { ...params, dry_run: dryRun || undefined } }
    ),
  // Manifest create — the create-dialog path. dryRun runs the same
  // server-side validation without persisting (校验后创建).
  createResource: (manifest: string, params: K8sObjectParams, dryRun = false) =>
    api.post<K8sObject>(
      '/k8s/resources',
      { manifest },
      { params: { ...params, dry_run: dryRun || undefined } }
    ),
  // Structured data-section edit (configmaps, secrets). The client
  // sends the diff — plaintext values to set (base64 encoded server-side),
  // keys to delete, and optional metadata/immutable/type changes.
  // resource_version is
  // required and stale values answer 409.
  updateData: (
    name: string,
    params: K8sObjectParams,
    body: {
      resource_version: string
      type?: string
      set?: Record<string, string>
      delete_keys?: string[]
      immutable?: boolean
      metadata?: {
        labels?: Record<string, string>
        annotations?: Record<string, string>
      }
    },
    dryRun = false
  ) =>
    api.post<K8sObject>(`/k8s/resources/${enc(name)}/data`, body, {
      params: { ...params, dry_run: dryRun || undefined },
    }),
  capabilities: (clusterId: number, namespace?: string) =>
    api.get<K8sCapabilities>('/k8s/capabilities', {
      params: { cluster_id: clusterId, namespace: namespace || undefined },
    }),
}

// ---- Cluster overview (step 5) ----

export type K8sUsage = { cpu_millis: number; mem_bytes: number }

export type K8sNodeOverview = {
  name: string
  ready: boolean
  roles: string[]
  kubelet_version: string
  os_image: string
  internal_ip: string
  cpu_capacity_millis: number
  mem_capacity_bytes: number
  usage?: K8sUsage
}

export type K8sEventSummary = {
  namespace: string
  reason: string
  message: string
  object: string
  count: number
  last_timestamp: string
}

export type K8sClusterOverview = {
  server_version: string
  nodes: { total: number; ready: number; items: K8sNodeOverview[] }
  namespaces: number
  workloads: {
    deployments: number
    stateful_sets: number
    daemon_sets: number
    jobs: number
    cron_jobs: number
    pods: number
    pods_running: number
  }
  warning_events: K8sEventSummary[]
  metrics: { available: boolean; message?: string }
  top_pods: { namespace: string; name: string; usage: K8sUsage }[]
}

// Component-scoped Runner integration. Machine credentials are never dp_ keys.
export type RunnerBinding = {
  id: number
  app_id: number
  pipeline_id: string
  private_cluster_id: string
  platform_url: string
  connection_id: number
  binding: ComponentBinding
  env_code: string
  token_prefix: string
  enabled: boolean
  expires_at: string
  last_check_at?: string
  last_applied_at?: string
  created_at: string
  updated_at: string
}
export type RunnerEntry = {
  authorization_expired?: boolean
  binding: RunnerBinding
  configuration_error?: string
  last_release_id?: number
  last_event_at?: string
  last_status?: string
  last_image?: string
  last_error?: string
}
export type RunnerConfig = {
  app_id: number
  pipeline_id: string
  private_cluster_id: string
  platform_url: string
  connection_id: number
  expires_at?: string
}
export type RunnerScripts = {
  script: string
  check_script: string
  verification_script: string
  configuration_variables: string[]
  task_yaml: string
  release_id_file: string
  secret_variable: string
  required_variables: string[]
  verification_required_variables: string[]
}
export type YunxiaoConnection = {
  id: number
  name: string
  edition: 'central' | 'region'
  domain: string
  organization_id: string
  created_at: string
}
export type PipelinePreview = {
  before: string
  after: string
  name: string
  fingerprint: string
  change_hash: string
  already_installed: boolean
  note: string
}
export type YunxiaoDeploymentPipelineInfo = {
  pipeline_id: string
  name: string
  type: string
  update_time: number
}
export type YunxiaoPipelineScopeFinding = {
  node_ref: string
  category: string
  summary: string
}
export type YunxiaoDeploymentPipelineScopeReport = {
  pipeline_id: string
  name: string
  type: string
  update_time: number
  definition_hash?: string
  analysis_status: string
  review_required: boolean
  findings: YunxiaoPipelineScopeFinding[]
  unresolved_nodes: string[]
  warning: string
}
export type YunxiaoPipelineSearchResult = {
  items: { pipeline_id: number; name: string }[]
  page: number
  per_page: number
  total: number
  total_pages: number
}
export const runnerApi = {
  bindings: () => api.get<RunnerEntry[]>('/runner/bindings'),
  configure: (body: RunnerConfig, id?: number) =>
    id
      ? api.put<{
          binding: RunnerBinding
          key: string
          scripts: RunnerScripts
        }>(`/runner/bindings/${id}/config`, body)
      : api.post<{
          binding: RunnerBinding
          key: string
          scripts: RunnerScripts
        }>('/runner/bindings', body),
  enabled: (id: number, enabled: boolean) =>
    api.put<RunnerBinding>(`/runner/bindings/${id}/enabled`, { enabled }),
  scripts: (id: number) =>
    api.get<RunnerScripts>(`/runner/bindings/${id}/scripts`),
  connections: () => api.get<YunxiaoConnection[]>('/runner/connections'),
  searchDeploymentPipelines: (body: {
    connection_id: number
    name: string
    page: number
  }) =>
    api.post<YunxiaoPipelineSearchResult>(
      '/runner/deployment-pipelines/search',
      body
    ),
  inspectDeploymentPipeline: (body: {
    connection_id: number
    pipeline_id: string
  }) =>
    api.post<YunxiaoDeploymentPipelineInfo>(
      '/runner/deployment-pipeline-check',
      body
    ),
  inspectDeploymentPipelineScope: (body: {
    connection_id: number
    pipeline_id: string
  }) =>
    api.post<YunxiaoDeploymentPipelineScopeReport>(
      '/runner/deployment-pipeline-scope-check',
      body
    ),
  reviewDeploymentPipelineScope: (body: {
    app_id: number
    connection_id: number
    pipeline_id: string
    definition_hash: string
    write_scope: DeliveryPipelineScopeReview['write_scope']
    acknowledged: true
  }) =>
    api.post<{
      review: DeliveryPipelineScopeReview
      execution_enabled: false
      message: string
    }>('/runner/deployment-pipeline-scope-review', body),
  createConnection: (
    body: Omit<YunxiaoConnection, 'id' | 'created_at'> & { token: string }
  ) => api.post<YunxiaoConnection>('/runner/connections', body),
  preview: (id: number) =>
    api.post<PipelinePreview>(`/runner/bindings/${id}/pipeline-preview`),
  apply: (
    id: number,
    body: Pick<PipelinePreview, 'fingerprint' | 'change_hash'>
  ) =>
    api.post<{ saved: boolean; changed: boolean }>(
      `/runner/bindings/${id}/pipeline-apply`,
      body
    ),
  backup: (id: number) =>
    api.get<string>(`/runner/bindings/${id}/pipeline-backup`, {
      responseType: 'text',
    }),
}
