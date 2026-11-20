#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
context="${KUBE_CONTEXT:-kind-kbdev}"
image="${DEPLOY_PLATFORM_IMAGE:?Set DEPLOY_PLATFORM_IMAGE to a locally built deploy-platform image}"
mysql_image="${MYSQL_IMAGE:-mysql:8.4}"
chart_dir="$repo_root/charts/deploy-platform"
namespace="dp-preflight-$(date +%Y%m%d%H%M%S)-$$"
release=preflight
job="$release-deploy-platform-credential-check"
bootstrap_release=preflight-no-admin
bootstrap_job="$bootstrap_release-deploy-platform-credential-check"
db_password="e2e-db-$(date +%s)-$$"
credential_key=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=
temp_dir="$(mktemp -d "${TMPDIR:-/tmp}/deploy-platform-preflight.XXXXXX")"
namespace_created=false

cleanup() {
  status=$?
  trap - EXIT
  if [[ "$namespace_created" == true ]]; then
    helm --kube-context "$context" uninstall "$release" --namespace "$namespace" --wait=false >/dev/null 2>&1 || true
    helm --kube-context "$context" uninstall "$bootstrap_release" --namespace "$namespace" --wait=false >/dev/null 2>&1 || true
    kubectl --context "$context" delete namespace "$namespace" --wait=true --timeout=120s >/dev/null 2>&1 || true
  fi
  rm -rf "$temp_dir"
  exit "$status"
}
trap cleanup EXIT

for command in docker helm kind kubectl; do
  command -v "$command" >/dev/null 2>&1 || {
    printf 'Required command not found: %s\n' "$command" >&2
    exit 1
  }
done

if [[ -n "${KIND_CLUSTER_NAME:-}" ]]; then
  kind_cluster="$KIND_CLUSTER_NAME"
elif [[ "$context" == kind-* ]]; then
  kind_cluster="${context#kind-}"
else
  printf 'Set KIND_CLUSTER_NAME when KUBE_CONTEXT is not a Kind context.\n' >&2
  exit 1
fi

kubectl --context "$context" get nodes >/dev/null
image_arch="$(docker image inspect "$image" --format '{{.Architecture}}')"
mysql_arch="$(docker image inspect "$mysql_image" --format '{{.Architecture}}')"
node_arches="$(kubectl --context "$context" get nodes -o jsonpath='{range .items[*]}{.status.nodeInfo.architecture}{"\n"}{end}' | sort -u)"
if [[ "$node_arches" != "$image_arch" || "$node_arches" != "$mysql_arch" ]]; then
  printf 'Image and Kind node architectures must match (app=%s mysql=%s nodes=%s).\n' \
    "$image_arch" "$mysql_arch" "${node_arches//$'\n'/,}" >&2
  exit 1
fi

kind_image_ref() {
  case "$1" in
    */*)
      first_component="${1%%/*}"
      if [[ "$first_component" == *.* || "$first_component" == *:* || "$first_component" == localhost ]]; then
        printf '%s' "$1"
      else
        printf 'docker.io/%s' "$1"
      fi
      ;;
    *) printf 'docker.io/library/%s' "$1" ;;
  esac
}

kind_nodes="$(kind get nodes --name "$kind_cluster")"
for item in "$image" "$mysql_image"; do
  expected_ref="$(kind_image_ref "$item")"
  image_missing=false
  while IFS= read -r node; do
    if ! docker exec "$node" ctr --namespace=k8s.io images ls 2>/dev/null | awk -v ref="$expected_ref" '$1 == ref { found=1 } END { exit !found }'; then
      image_missing=true
      break
    fi
  done <<<"$kind_nodes"
  if [[ "$image_missing" == false ]]; then
    printf 'Kind image already loaded: %s\n' "$expected_ref"
    continue
  fi
  archive="$temp_dir/$(printf '%s' "$item" | tr '/:' '__').tar"
  arch="$(docker image inspect "$item" --format '{{.Architecture}}')"
  docker image save --platform "linux/$arch" -o "$archive" "$item"
  kind load image-archive "$archive" --name "$kind_cluster"
done

kubectl --context "$context" create namespace "$namespace"
namespace_created=true
kubectl --context "$context" -n "$namespace" create secret generic mysql-secret --from-literal=password="$db_password"
kubectl --context "$context" -n "$namespace" create secret generic app-secrets \
  --from-literal=credential-encryption-key="$credential_key" \
  --from-literal=admin-initial-password=e2e-admin-password

kubectl --context "$context" apply -n "$namespace" -f - <<YAML
apiVersion: apps/v1
kind: Deployment
metadata:
  name: mysql
spec:
  replicas: 1
  selector:
    matchLabels:
      app: preflight-mysql
  template:
    metadata:
      labels:
        app: preflight-mysql
    spec:
      containers:
        - name: mysql
          image: $mysql_image
          imagePullPolicy: IfNotPresent
          ports:
            - name: mysql
              containerPort: 3306
          env:
            - name: MYSQL_ROOT_PASSWORD
              value: e2e-root-pass
            - name: MYSQL_DATABASE
              value: deploy_platform
            - name: MYSQL_USER
              value: deploy
            - name: MYSQL_PASSWORD
              value: $db_password
          readinessProbe:
            tcpSocket:
              port: mysql
            initialDelaySeconds: 3
            periodSeconds: 2
          volumeMounts:
            - name: data
              mountPath: /var/lib/mysql
      volumes:
        - name: data
          emptyDir:
            medium: Memory
            sizeLimit: 1Gi
---
apiVersion: v1
kind: Service
metadata:
  name: mysql
spec:
  selector:
    app: preflight-mysql
  ports:
    - name: mysql
      port: 3306
      targetPort: mysql
YAML

if ! kubectl --context "$context" -n "$namespace" wait --for=condition=available deployment/mysql --timeout=90s; then
  for pod in $(kubectl --context "$context" -n "$namespace" get pods -l app=preflight-mysql -o jsonpath='{.items[*].metadata.name}'); do
    kubectl --context "$context" -n "$namespace" logs "$pod" >&2 || true
    kubectl --context "$context" -n "$namespace" describe pod "$pod" >&2 || true
  done
  exit 1
fi
mysql_pod="$(kubectl --context "$context" -n "$namespace" get pods -l app=preflight-mysql -o jsonpath='{.items[0].metadata.name}')"
for attempt in {1..60}; do
  if kubectl --context "$context" -n "$namespace" exec "$mysql_pod" -- \
    mysqladmin ping -h 127.0.0.1 -uroot -pe2e-root-pass --silent >/dev/null 2>&1; then
    break
  fi
  if [[ "$attempt" == 60 ]]; then
    printf 'MySQL did not become ready for schema initialization.\n' >&2
    exit 1
  fi
  sleep 2
done

if ! MYSQL_CMD="kubectl --context $context exec -i -n $namespace $mysql_pod -- mysql -udeploy -p$db_password" \
  "$repo_root/scripts/apply-schema.sh" deploy_platform >"$temp_dir/apply-schema.log" 2>&1; then
  cat "$temp_dir/apply-schema.log" >&2
  exit 1
fi

image_repository="${image%:*}"
image_tag="${image##*:}"
helm_values=(
  --set image.repository="$image_repository"
  --set image.tag="$image_tag"
  --set image.pullPolicy=IfNotPresent
  --set mysql.host=mysql
  --set mysql.existingSecret.name=mysql-secret
  --set secrets.credentialEncryptionKey.secretName=app-secrets
  --set rbac.create=false
)
if helm --kube-context "$context" upgrade --install "$bootstrap_release" "$chart_dir" \
  --namespace "$namespace" "${helm_values[@]}" --wait --timeout 3m \
  >"$temp_dir/missing-admin-upgrade.log" 2>&1; then
  cat "$temp_dir/missing-admin-upgrade.log" >&2
  printf 'Fresh-database install unexpectedly passed without ADMIN_INITIAL_PASSWORD.\n' >&2
  exit 1
fi
kubectl --context "$context" -n "$namespace" wait --for=condition=failed "job/$bootstrap_job" --timeout=60s
bootstrap_log="$(kubectl --context "$context" -n "$namespace" logs "job/$bootstrap_job")"
if [[ "$bootstrap_log" != *'ADMIN_INITIAL_PASSWORD'* ]]; then
  printf 'Missing-admin preflight did not explain the required setting:\n%s\n' "$bootstrap_log" >&2
  exit 1
fi
if kubectl --context "$context" -n "$namespace" get deployment "$bootstrap_release-deploy-platform" -o name --ignore-not-found | rg -q .; then
  printf 'Missing-admin preflight created a serving Deployment.\n' >&2
  exit 1
fi

helm_values+=(--set secrets.adminInitialPassword.secretName=app-secrets)
helm --kube-context "$context" upgrade --install "$release" "$chart_dir" \
  --namespace "$namespace" "${helm_values[@]}" --wait --timeout 3m
kubectl --context "$context" -n "$namespace" rollout status deployment/preflight-deploy-platform --timeout=120s

kubectl --context "$context" -n "$namespace" exec "$mysql_pod" -- \
  mysql -udeploy -p"$db_password" -D deploy_platform -e \
  "INSERT INTO clusters(name,env_code,environment,environment_tier,deploy_policy,connection_type,api_server,credential_ciphertext,enabled,k8s_master_host,k8s_master_port,default_namespace) VALUES('credential preflight failure test','hookfail','test','non_production','manual','token','https://kubernetes.example.test','v1:AAAA',1,'',0,'default')"

if helm --kube-context "$context" upgrade "$release" "$chart_dir" --namespace "$namespace" \
  "${helm_values[@]}" --set service.port=81 --wait --timeout 3m >"$temp_dir/failed-upgrade.log" 2>&1; then
  cat "$temp_dir/failed-upgrade.log" >&2
  printf 'Helm upgrade unexpectedly passed with an unreadable stored credential.\n' >&2
  exit 1
fi

kubectl --context "$context" -n "$namespace" wait --for=condition=failed "job/$job" --timeout=60s
failure_log="$(kubectl --context "$context" -n "$namespace" logs "job/$job")"
if [[ "$failure_log" != *'credential preflight failure test'* ]]; then
  printf 'Failed preflight Job did not log the affected cluster:\n%s\n' "$failure_log" >&2
  exit 1
fi
service_port="$(kubectl --context "$context" -n "$namespace" get service preflight-deploy-platform -o jsonpath='{.spec.ports[0].port}')"
ready_replicas="$(kubectl --context "$context" -n "$namespace" get deployment preflight-deploy-platform -o jsonpath='{.status.readyReplicas}')"
if [[ "$service_port" != 80 || "$ready_replicas" != 1 ]]; then
  printf 'Failed preflight changed the serving release: service_port=%s ready_replicas=%s\n' \
    "$service_port" "$ready_replicas" >&2
  exit 1
fi

kubectl --context "$context" -n "$namespace" exec "$mysql_pod" -- \
  mysql -udeploy -p"$db_password" -D deploy_platform -e "DELETE FROM clusters WHERE env_code='hookfail'"
helm --kube-context "$context" upgrade "$release" "$chart_dir" --namespace "$namespace" \
  "${helm_values[@]}" --set service.port=81 --wait --timeout 3m
service_port="$(kubectl --context "$context" -n "$namespace" get service preflight-deploy-platform -o jsonpath='{.spec.ports[0].port}')"
if [[ "$service_port" != 81 ]]; then
  printf 'Upgrade did not proceed after a successful preflight: service_port=%s\n' "$service_port" >&2
  exit 1
fi
if kubectl --context "$context" -n "$namespace" get job "$job" -o name --ignore-not-found | rg -q .; then
  printf 'Successful preflight Job should be deleted by its hook policy.\n' >&2
  exit 1
fi

printf 'Helm credential preflight passed: missing initial password blocked a fresh install, bad credentials blocked upgrade, old Service stayed live, and repaired data allowed upgrade.\n'
