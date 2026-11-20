"""
发布核心流水线 — 匹配院内真实发布命令：

  docker load < /data/transfer_space/demo-agent-core-api-20260707202447.tar.gz
  docker push registry.example.com/Demo/demo-agent-core-api:20260707202447
  kubectl set image deployment/dev-demo-002-demo-agent-core-api \
    demo-agent-core-api=registry.example.com/Demo/demo-agent-core-api:20260707202447 \
    -n dev-demo-002

流水线步骤：
  1. check_file    — 校验文件存在
  2. docker_load   — docker load < xxx.tar.gz (直接加载，无需先解压)
  3. docker_tag    — 打上仓库地址标签
  4. docker_push   — 推送到镜像仓库
  5. k8s_set_image — SSH 到 K8s Master 执行 kubectl set image

dev 环境下步骤 2-5 全部 Mock，仅步骤 1 真实校验文件。
"""
import json
import logging
import os
import re
import subprocess
from datetime import datetime
from typing import Tuple

import paramiko
from sqlalchemy.orm import Session

from app.core.config import settings
from app.models.models import DeployLog

logger = logging.getLogger(__name__)


class StepResult:
    def __init__(self, step_name: str, success: bool, stdout: str = "", stderr: str = ""):
        self.step_name = step_name
        self.success = success
        self.stdout = stdout
        self.stderr = stderr

    def to_dict(self) -> dict:
        return {
            "step": self.step_name,
            "success": self.success,
            "stdout": self.stdout[-2000:],
            "stderr": self.stderr[-2000:],
        }


def _run_local_cmd(cmd: str, cwd: str = "/tmp") -> Tuple[int, str, str]:
    try:
        proc = subprocess.run(
            cmd, shell=True, cwd=cwd,
            capture_output=True, text=True, timeout=600,
        )
        return proc.returncode, proc.stdout, proc.stderr
    except subprocess.TimeoutExpired:
        return -1, "", "命令执行超时(600s)"
    except Exception as e:
        return -1, "", str(e)


def _mock_cmd(cmd: str, cwd: str = "/tmp") -> Tuple[int, str, str]:
    logger.info("[MOCK] %s", cmd)
    if "docker load" in cmd:
        return 0, "Loaded image: mock-image:latest", ""
    return 0, f"[MOCK] 命令已记录: {cmd}", ""


def _ssh_exec(host: str, port: int, user: str, cmd: str,
              key_path: str = None) -> Tuple[int, str, str]:
    client = paramiko.SSHClient()
    client.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    try:
        connect_kwargs = {"hostname": host, "port": port, "username": user, "timeout": 10}
        if key_path:
            connect_kwargs["key_filename"] = key_path
        else:
            connect_kwargs["allow_agent"] = True
            connect_kwargs["look_for_keys"] = True
        client.connect(**connect_kwargs)
        _, stdout_ch, stderr_ch = client.exec_command(cmd, timeout=180)
        exit_code = stdout_ch.channel.recv_exit_status()
        stdout_text = stdout_ch.read().decode("utf-8", errors="replace")
        stderr_text = stderr_ch.read().decode("utf-8", errors="replace")
        return exit_code, stdout_text, stderr_text
    except Exception as e:
        return -1, "", f"SSH 连接/执行失败: {e}"
    finally:
        client.close()


def _update_log(db: Session, log: DeployLog, **kwargs):
    for k, v in kwargs.items():
        setattr(log, k, v)
    db.commit()
    db.refresh(log)


def _fail(db, log, steps_detail, step_name, stderr):
    _update_log(
        db, log,
        status="failed",
        step_detail=json.dumps(steps_detail, ensure_ascii=False),
        error_message=f"【{step_name}】{stderr}",
        finished_at=datetime.now(),
    )
    return log


def _parse_loaded_image(docker_load_stdout: str) -> str:
    """
    从 docker load 输出解析已加载的镜像名。
    典型输出: "Loaded image: demo-agent-core-api:20260707202447"
    """
    m = re.search(r"Loaded image:\s*(\S+)", docker_load_stdout)
    if m:
        return m.group(1)
    return ""


def run_deploy_pipeline(db: Session, req, cluster=None) -> DeployLog:
    """
    执行发布流水线，任何一步失败即中止，将 stderr 写入 deploy_logs。
    """
    registry = settings.REGISTRY_HOST
    registry_project = settings.REGISTRY_PROJECT
    image_name = req.container_name
    image_tag = req.version
    image_full = f"{registry}/{registry_project}/{image_name}:{image_tag}"

    log = DeployLog(
        cluster_id=req.cluster_id,
        app_name=req.deployment_name,
        deployment_name=req.deployment_name,
        container_name=req.container_name,
        namespace=req.namespace,
        version=req.version,
        file_name=req.file_name,
        image_full=image_full,
        status="running",
        started_at=datetime.now(),
    )
    db.add(log)
    db.commit()
    db.refresh(log)

    steps_detail: list[dict] = []
    run_cmd = _mock_cmd if settings.is_dev else _run_local_cmd

    # ══════════════════════════════════════════════
    # 步骤 1: 检查文件是否存在
    # ══════════════════════════════════════════════
    step_name = "check_file"
    _update_log(db, log, current_step=step_name)

    file_path = os.path.join(settings.TRANSFER_DIR, req.file_name)
    if not os.path.isfile(file_path):
        result = StepResult(step_name, False, stderr=f"文件不存在: {file_path}")
        steps_detail.append(result.to_dict())
        return _fail(db, log, steps_detail, step_name, result.stderr)

    file_size_mb = round(os.path.getsize(file_path) / 1048576, 2)
    result = StepResult(step_name, True, stdout=f"文件已确认: {file_path} ({file_size_mb} MB)")
    steps_detail.append(result.to_dict())

    # ══════════════════════════════════════════════
    # 步骤 2: docker load — 直接加载 .tar.gz
    #   docker load < /data/transfer_space/xxx.tar.gz
    # ══════════════════════════════════════════════
    step_name = "docker_load"
    _update_log(db, log, current_step=step_name)

    load_cmd = f"docker load < {file_path}"
    code, stdout, stderr = run_cmd(load_cmd)
    result = StepResult(step_name, code == 0, stdout, stderr)
    steps_detail.append(result.to_dict())
    if code != 0:
        return _fail(db, log, steps_detail, step_name, stderr)

    # 解析加载后的镜像名（用于 docker tag 源）
    loaded_image = _parse_loaded_image(stdout)
    if settings.is_dev:
        loaded_image = f"{image_name}:latest"
    elif not loaded_image:
        loaded_image = f"{image_name}:latest"
        logger.warning("无法解析 docker load 输出的镜像名，回退: %s", loaded_image)

    # ══════════════════════════════════════════════
    # 步骤 3: docker tag — 打仓库地址标签
    #   docker tag <loaded> registry/project/name:tag
    # ══════════════════════════════════════════════
    step_name = "docker_tag"
    _update_log(db, log, current_step=step_name)

    tag_cmd = f"docker tag {loaded_image} {image_full}"
    code, stdout, stderr = run_cmd(tag_cmd)
    result = StepResult(step_name, code == 0,
                        stdout or f"{loaded_image} → {image_full}", stderr)
    steps_detail.append(result.to_dict())
    if code != 0:
        return _fail(db, log, steps_detail, step_name, stderr)

    # ══════════════════════════════════════════════
    # 步骤 4: docker push — 推送到镜像仓库
    #   docker push registry/project/name:tag
    # ══════════════════════════════════════════════
    step_name = "docker_push"
    _update_log(db, log, current_step=step_name)

    if settings.is_dev:
        code, stdout, stderr = _mock_cmd(f"docker push {image_full}")
    else:
        # 登录镜像仓库（Harbor 或阿里云 ACR）
        login_cmd = (
            f"docker login {registry} "
            f"-u {settings.REGISTRY_USER} -p {settings.REGISTRY_PASSWORD}"
        )
        code, stdout, stderr = _run_local_cmd(login_cmd)
        if code != 0:
            result = StepResult(step_name, False, stdout, f"镜像仓库登录失败: {stderr}")
            steps_detail.append(result.to_dict())
            return _fail(db, log, steps_detail, step_name, result.stderr)

        # 推送
        code, stdout, stderr = _run_local_cmd(f"docker push {image_full}")

    result = StepResult(step_name, code == 0, stdout, stderr)
    steps_detail.append(result.to_dict())
    if code != 0:
        return _fail(db, log, steps_detail, step_name, stderr)

    # ══════════════════════════════════════════════
    # 步骤 5: SSH → K8s Master → kubectl set image
    # ══════════════════════════════════════════════
    step_name = "k8s_set_image"
    _update_log(db, log, current_step=step_name)

    kubectl_cmd = (
        f"kubectl set image deployment/{req.deployment_name} "
        f"{req.container_name}={image_full} "
        f"-n {req.namespace}"
    )

    if settings.is_dev:
        code, stdout, stderr = _mock_cmd(kubectl_cmd)
    else:
        code, stdout, stderr = _ssh_exec(
            host=cluster.k8s_master_host,
            port=cluster.k8s_master_port,
            user=cluster.k8s_master_user,
            cmd=kubectl_cmd,
            key_path=cluster.k8s_master_key_path,
        )

    result = StepResult(step_name, code == 0, stdout, stderr)
    steps_detail.append(result.to_dict())
    if code != 0:
        return _fail(db, log, steps_detail, step_name, stderr)

    # ══════════════════════════════════════════════
    # 全部成功 — 清理源文件
    # ══════════════════════════════════════════════
    try:
        os.remove(file_path)
        logger.info("已清理源文件: %s", file_path)
    except Exception as e:
        logger.warning("清理源文件失败: %s", e)

    _update_log(
        db, log,
        status="success",
        current_step="done",
        step_detail=json.dumps(steps_detail, ensure_ascii=False),
        finished_at=datetime.now(),
    )
    return log


def run_rollback_pipeline(db: Session, req, cluster=None) -> DeployLog:
    """
    回滚流水线 — 直接 kubectl set image 到历史版本。
    不涉及 docker load/push（镜像已在 Harbor 中）。

    步骤：
      1. query_current  — 查询当前 Deployment 正在运行的镜像
      2. k8s_set_image  — SSH 到 K8s Master 执行 kubectl set image
    """
    log = DeployLog(
        cluster_id=req.cluster_id,
        app_name=req.app_name,
        deployment_name=req.deployment_name,
        container_name=req.container_name,
        namespace=req.namespace,
        version=req.version,
        file_name="rollback",
        image_full=req.image_full,
        deploy_type="rollback",
        status="running",
        started_at=datetime.now(),
    )
    db.add(log)
    db.commit()
    db.refresh(log)

    steps_detail: list[dict] = []

    # ══════════════════════════════════════════════
    # 步骤 1: 查询当前运行的镜像版本（用于审计对比）
    # ══════════════════════════════════════════════
    step_name = "query_current"
    _update_log(db, log, current_step=step_name)

    query_cmd = (
        f"kubectl get deployment/{req.deployment_name} "
        f"-n {req.namespace} "
        f"-o jsonpath='{{.spec.template.spec.containers[0].image}}'"
    )

    if settings.is_dev:
        code, stdout, stderr = _mock_cmd(query_cmd)
        stdout = f"registry.example.com/Demo/{req.container_name}:mock-current"
    else:
        code, stdout, stderr = _ssh_exec(
            host=cluster.k8s_master_host,
            port=cluster.k8s_master_port,
            user=cluster.k8s_master_user,
            cmd=query_cmd,
            key_path=cluster.k8s_master_key_path,
        )

    current_image = stdout.strip().strip("'")
    result = StepResult(
        step_name, True,
        stdout=f"当前镜像: {current_image}\n回滚目标: {req.image_full}",
    )
    steps_detail.append(result.to_dict())

    # ══════════════════════════════════════════════
    # 步骤 2: kubectl set image — 回滚到目标版本
    # ══════════════════════════════════════════════
    step_name = "k8s_set_image"
    _update_log(db, log, current_step=step_name)

    kubectl_cmd = (
        f"kubectl set image deployment/{req.deployment_name} "
        f"{req.container_name}={req.image_full} "
        f"-n {req.namespace}"
    )

    if settings.is_dev:
        code, stdout, stderr = _mock_cmd(kubectl_cmd)
    else:
        code, stdout, stderr = _ssh_exec(
            host=cluster.k8s_master_host,
            port=cluster.k8s_master_port,
            user=cluster.k8s_master_user,
            cmd=kubectl_cmd,
            key_path=cluster.k8s_master_key_path,
        )

    result = StepResult(step_name, code == 0, stdout, stderr)
    steps_detail.append(result.to_dict())
    if code != 0:
        return _fail(db, log, steps_detail, step_name, stderr)

    _update_log(
        db, log,
        status="success",
        current_step="done",
        step_detail=json.dumps(steps_detail, ensure_ascii=False),
        finished_at=datetime.now(),
    )
    return log
