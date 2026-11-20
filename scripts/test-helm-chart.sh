#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
chart_dir="$repo_root/charts/deploy-platform"
helm_values=(
  --set mysql.host=mysql.example
  --set mysql.existingSecret.name=mysql-secret
  --set secrets.credentialEncryptionKey.secretName=app-secrets
  --set secrets.yunxiaoWebhookToken.secretName=app-secrets
  --set secrets.adminInitialPassword.secretName=app-secrets
)

helm lint "$chart_dir" "${helm_values[@]}"

rendered_chart="$(helm template deploy-platform "$chart_dir" "${helm_values[@]}" --set replicaCount=1)"
if ! rg -q '^  replicas: 1$' <<<"$rendered_chart"; then
  printf 'Expected the supported single replica Deployment to render.\n' >&2
  exit 1
fi

public_url_deployment="$(helm template deploy-platform "$chart_dir" "${helm_values[@]}" --set-string env.PLATFORM_PUBLIC_URL=https://deploy.example.com/platform --show-only templates/deployment.yaml)"
if ! rg -q 'name: PLATFORM_PUBLIC_URL' <<<"$public_url_deployment" || ! rg -q 'value: "https://deploy.example.com/platform"' <<<"$public_url_deployment"; then
  printf 'Deployment is missing the configured platform public URL for notification task links.\n' >&2
  exit 1
fi

preflight_job="$(helm template deploy-platform "$chart_dir" "${helm_values[@]}" --set replicaCount=1 --show-only templates/credential-preflight-job.yaml)"
for expected in \
  '^kind: Job$' \
  '"helm.sh/hook": pre-install,pre-upgrade' \
  '"helm.sh/hook-delete-policy": before-hook-creation,hook-succeeded' \
  'args: \["check-credentials"\]' \
  'name: CREDENTIAL_ENCRYPTION_KEY' \
  'name: ADMIN_INITIAL_PASSWORD' \
  'automountServiceAccountToken: false' \
  'app.kubernetes.io/component: credential-preflight'; do
  if ! rg -q "$expected" <<<"$preflight_job"; then
    printf 'Credential preflight Job is missing expected configuration: %s\n' "$expected" >&2
    exit 1
  fi
done

preflight_pod_labels="$(sed -n '/template:/,/containers:/p' <<<"$preflight_job")"
if ! rg -q 'app.kubernetes.io/name: deploy-platform' <<<"$preflight_pod_labels"; then
  printf 'Credential preflight pod should retain the app name label for policy selection.\n' >&2
  exit 1
fi
if rg -q 'app.kubernetes.io/instance:' <<<"$preflight_pod_labels"; then
  printf 'Credential preflight pod must not match the Deploy Platform Service selector.\n' >&2
  exit 1
fi

no_admin_job="$(helm template deploy-platform "$chart_dir" "${helm_values[@]}" --set-string secrets.adminInitialPassword.secretName= --show-only templates/credential-preflight-job.yaml)"
no_admin_deployment="$(helm template deploy-platform "$chart_dir" "${helm_values[@]}" --set-string secrets.adminInitialPassword.secretName= --show-only templates/deployment.yaml)"
if rg -q 'name: ADMIN_INITIAL_PASSWORD' <<<"$no_admin_job$no_admin_deployment"; then
  printf 'Chart rendered an initial-admin Secret reference after the Secret name was omitted.\n' >&2
  exit 1
fi

for replica_count in 0 2; do
  if error_output="$(helm template deploy-platform "$chart_dir" "${helm_values[@]}" --set "replicaCount=$replica_count" 2>&1)"; then
    printf 'Helm unexpectedly rendered replicaCount=%s.\n' "$replica_count" >&2
    exit 1
  fi
  if [[ "$error_output" != *'replicaCount must be 1'* ]]; then
    printf 'Helm rejected replicaCount=%s for an unexpected reason:\n%s\n' "$replica_count" "$error_output" >&2
    exit 1
  fi
done

printf 'Helm chart validation passed; omitted admin Secret stays optional for existing user databases, and replicaCount=0/2 are rejected.\n'
