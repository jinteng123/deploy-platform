import json
import logging
from typing import Tuple

from app.core.config import settings
from app.services.deploy_service import _ssh_exec

logger = logging.getLogger(__name__)


def _kubectl(cmd: str, cluster=None) -> Tuple[int, str, str]:
    if settings.is_dev:
        return _mock_kubectl(cmd)
    if not cluster:
        raise RuntimeError("未指定目标集群")
    return _ssh_exec(
        host=cluster.k8s_master_host,
        port=cluster.k8s_master_port,
        user=cluster.k8s_master_user,
        cmd=cmd,
        key_path=cluster.k8s_master_key_path,
    )


def _mock_kubectl(cmd: str) -> Tuple[int, str, str]:
    """Dev 模式返回与院内 13 个服务一致的模拟数据"""
    import re as _re
    logger.info("[MOCK] %s", cmd)

    _SERVICES = [
        ("demo-gateway",          "20260709175002", 8080, 0, "Running"),
        ("demo-hospital-api",     "20260702152220", 8080, 1, "Running"),
        ("demo-hospital-web",     "20260708110530", 80,   0, "Running"),
        ("demo-user-api",         "20260707185356", 8080, 0, "Running"),
        ("demo-user-web",         "20260707190012", 80,   0, "Running"),
        ("demo-agent-core-api",   "20260706140800", 8080, 0, "Running"),
        ("demo-ai-assistant-web", "20260705093000", 80,   0, "Running"),
        ("demo-angel-agent-web",  "20260704160000", 80,   0, "Running"),
        ("demo-data-asset-api",   "20260703120000", 8080, 2, "Running"),
        ("demo-data-asset-web",   "20260703121500", 80,   0, "Running"),
        ("demo-out-gateway",      "20260701080000", 8080, 0, "Running"),
        ("demo-platform-map-web", "20260630150000", 80,   0, "Running"),
        ("demo-proxy",            "20260709200000", 80,   0, "Running"),
    ]
    _HASH = "7b8c9"

    # ── get pods ──
    if "get pods" in cmd and "-o json" in cmd:
        ns_match = _re.search(r"-n\s+(\S+)", cmd)
        ns = ns_match.group(1) if ns_match else "dev-demo-002"
        items = []
        for svc, tag, port, restarts, phase in _SERVICES:
            deploy = f"{ns}-{svc}"
            img = f"registry.example.com/Demo/{svc}:{tag}"
            items.append({
                "metadata": {"name": f"{deploy}-{_HASH}", "labels": {"app": deploy}},
                "status": {
                    "phase": phase,
                    "containerStatuses": [{"ready": phase == "Running", "restartCount": restarts, "image": img}],
                },
                "spec": {"containers": [{"name": svc, "image": img}]},
            })
        return 0, json.dumps({"items": items}), ""

    # ── logs ──
    if "logs" in cmd:
        pod = cmd.split("logs")[-1].strip().split()[0] if "logs" in cmd else "unknown"
        return 0, "\n".join([
            f"2026-07-10 10:00:01 INFO  [main] Application starting... (pod: {pod})",
            "2026-07-10 10:00:02 INFO  [main] Connected to database",
            "2026-07-10 10:00:03 INFO  [main] Listening on port 8080",
            "2026-07-10 10:00:05 INFO  [main] Health check endpoint registered",
            "2026-07-10 10:00:10 INFO  [http] GET /health 200 2ms",
            "2026-07-10 10:01:00 INFO  [http] GET /api/v1/status 200 5ms",
            "2026-07-10 10:01:15 INFO  [http] POST /api/v1/data 201 45ms",
            "2026-07-10 10:02:30 WARN  [pool] Connection pool nearly full (8/10)",
            "2026-07-10 10:03:00 INFO  [http] GET /api/v1/users 200 12ms",
            "2026-07-10 10:05:00 INFO  [scheduler] Heartbeat OK",
            "[MOCK] 以上为模拟日志数据",
        ]), ""

    # ── get deployment ──
    if "get deployment" in cmd and "-o json" in cmd:
        m = _re.search(r"deployment/(\S+)", cmd)
        deploy_name = m.group(1) if m else "dev-demo-002-demo-gateway"
        ns_match = _re.search(r"-n\s+(\S+)", cmd)
        ns = ns_match.group(1) if ns_match else "dev-demo-002"
        svc_name = deploy_name.replace(f"{ns}-", "")
        svc_info = next((s for s in _SERVICES if s[0] == svc_name), _SERVICES[0])
        svc, tag, port, restarts, phase = svc_info
        img = f"registry.example.com/Demo/{svc}:{tag}"

        return 0, json.dumps({
            "metadata": {"name": deploy_name, "namespace": ns},
            "spec": {
                "replicas": 1,
                "strategy": {"type": "Recreate"},
                "template": {
                    "spec": {
                        "containers": [{
                            "name": svc,
                            "image": img,
                            "ports": [{"containerPort": port}],
                            "envFrom": [
                                {"configMapRef": {"name": f"{ns}-demo-common-config"}},
                                {"configMapRef": {"name": f"{ns}-{svc}-config"}},
                            ],
                            "env": [
                                {"name": "SPRING_PROFILES_ACTIVE", "value": "test"},
                                {"name": "SERVER_PORT", "value": str(port)},
                                {"name": "DB_PASSWORD", "valueFrom": {
                                    "secretKeyRef": {"name": f"{ns}-demo-db-secrets", "key": "mysql-root-password"}
                                }},
                            ],
                            "resources": {},
                        }],
                    },
                },
            },
            "status": {
                "replicas": 1, "readyReplicas": 1, "availableReplicas": 1,
                "conditions": [
                    {"type": "Available", "status": "True", "lastTransitionTime": "2026-07-09T10:00:00Z"},
                    {"type": "Progressing", "status": "True", "lastTransitionTime": "2026-07-09T10:00:00Z"},
                ],
            },
        }), ""

    # ── events ──
    if "get events" in cmd or "describe" in cmd:
        m = _re.search(r"involvedObject\.name=(\S+)", cmd)
        obj = m.group(1) if m else "dev-demo-002-demo-gateway"
        return 0, "\n".join([
            "LAST SEEN   TYPE     REASON             OBJECT                                      MESSAGE",
            f"5m          Normal   ScalingReplicaSet   deployment/{obj}   Scaled up replica set {obj}-{_HASH} to 1",
            f"5m          Normal   Scheduled           pod/{obj}-{_HASH}  Successfully assigned ns/{obj}-{_HASH} to worker-111",
            f"5m          Normal   Pulled              pod/{obj}-{_HASH}  Container image already present on machine",
            f"5m          Normal   Created             pod/{obj}-{_HASH}  Created container",
            f"5m          Normal   Started             pod/{obj}-{_HASH}  Started container",
            "[MOCK] 以上为模拟事件数据",
        ]), ""

    # ── delete pod ──
    if "delete pod" in cmd:
        parts = cmd.split()
        pod_idx = parts.index("pod") + 1
        pod_name = parts[pod_idx] if pod_idx < len(parts) else "unknown"
        return 0, f'pod "{pod_name}" deleted', ""

    return 0, f"[MOCK] {cmd}", ""


def get_pods(namespace: str, cluster=None) -> list[dict]:
    cmd = f"kubectl get pods -n {namespace} -o json"
    code, stdout, stderr = _kubectl(cmd, cluster)
    if code != 0:
        raise RuntimeError(f"获取 Pod 列表失败: {stderr}")

    data = json.loads(stdout)
    result = []
    for item in data.get("items", []):
        meta = item.get("metadata", {})
        status = item.get("status", {})
        containers = status.get("containerStatuses", [{}])
        cs = containers[0] if containers else {}
        spec_containers = item.get("spec", {}).get("containers", [{}])
        image = spec_containers[0].get("image", "") if spec_containers else ""

        ready_count = sum(1 for c in containers if c.get("ready"))
        total_count = len(containers) or len(spec_containers)

        result.append({
            "name": meta.get("name", ""),
            "status": status.get("phase", "Unknown"),
            "ready": f"{ready_count}/{total_count}",
            "restarts": cs.get("restartCount", 0),
            "image": image,
            "app_label": meta.get("labels", {}).get("app", ""),
        })
    return result


def get_pod_logs(namespace: str, pod_name: str, tail: int = 200, cluster=None) -> str:
    cmd = f"kubectl logs {pod_name} -n {namespace} --tail={tail}"
    code, stdout, stderr = _kubectl(cmd, cluster)
    if code != 0:
        raise RuntimeError(f"获取日志失败: {stderr}")
    return stdout


def get_deployment_info(namespace: str, deployment_name: str, cluster=None) -> dict:
    cmd = f"kubectl get deployment/{deployment_name} -n {namespace} -o json"
    code, stdout, stderr = _kubectl(cmd, cluster)
    if code != 0:
        raise RuntimeError(f"获取 Deployment 信息失败: {stderr}")

    data = json.loads(stdout)
    spec = data.get("spec", {})
    template_spec = spec.get("template", {}).get("spec", {})
    container = template_spec.get("containers", [{}])[0] if template_spec.get("containers") else {}
    status = data.get("status", {})

    env_from = container.get("envFrom", [])
    config_maps = [ef["configMapRef"]["name"] for ef in env_from if "configMapRef" in ef]

    return {
        "name": data.get("metadata", {}).get("name", ""),
        "namespace": data.get("metadata", {}).get("namespace", ""),
        "replicas": f"{status.get('readyReplicas', 0)}/{spec.get('replicas', 0)}",
        "image": container.get("image", ""),
        "strategy": spec.get("strategy", {}).get("type", ""),
        "env": container.get("env", []),
        "config_maps": config_maps,
        "ports": container.get("ports", []),
        "resources": container.get("resources", {}),
        "conditions": status.get("conditions", []),
    }


def restart_pod(namespace: str, pod_name: str, cluster=None) -> dict:
    cmd = f"kubectl delete pod {pod_name} -n {namespace}"
    code, stdout, stderr = _kubectl(cmd, cluster)
    if code != 0:
        raise RuntimeError(f"重启 Pod 失败: {stderr}")
    return {"pod": pod_name, "message": stdout.strip() or f'pod "{pod_name}" deleted'}


def get_deployment_events(namespace: str, deployment_name: str, cluster=None) -> str:
    cmd = (
        f"kubectl get events -n {namespace} "
        f"--field-selector involvedObject.name={deployment_name} "
        f"--sort-by='.lastTimestamp' 2>/dev/null; "
        f"kubectl get events -n {namespace} "
        f"--sort-by='.lastTimestamp' 2>/dev/null | head -30"
    )
    code, stdout, stderr = _kubectl(cmd, cluster)
    if code != 0:
        raise RuntimeError(f"获取事件失败: {stderr}")
    return stdout
