"""
/api/v1/deploy    — 发布接口
/api/v1/deploy/logs — 发布日志查询
/api/v1/files     — 传输目录文件列表
/api/v1/apps      — 应用映射管理
/api/v1/clusters  — 集群/环境管理
/api/v1/k8s/*     — K8s 服务管理（Pod/日志/配置/事件）
/api/v1/rollback  — 回滚操作
"""
import json
import logging
import os
import re
from datetime import datetime
from typing import Optional

from fastapi import APIRouter, Depends, HTTPException, Query
from sqlalchemy.orm import Session

from app.core.config import settings
from app.db.session import get_db
from app.models.models import App, Cluster, DeployLog
from app.schemas.deploy import (
    AppCreate, AppOut, ClusterCreate, ClusterOut,
    DeployLogOut, DeployRequest, DeployResponse, FileInfo,
    RollbackRequest,
)
from app.services.deploy_service import run_deploy_pipeline, run_rollback_pipeline
from app.services.k8s_service import (
    get_pods, get_pod_logs, get_deployment_info, get_deployment_events,
    restart_pod,
)

logger = logging.getLogger(__name__)
router = APIRouter(prefix="/api/v1", tags=["Deploy"])


def _get_cluster(db: Session, cluster_id: int) -> Cluster:
    cluster = db.query(Cluster).filter(Cluster.id == cluster_id).first()
    if not cluster:
        raise HTTPException(status_code=404, detail=f"集群 ID={cluster_id} 不存在")
    return cluster


# ════════════════════ 集群/环境管理 ════════════════════

@router.get("/clusters", response_model=list[ClusterOut], summary="查询所有集群")
def list_clusters(db: Session = Depends(get_db)):
    return db.query(Cluster).order_by(Cluster.id).all()


@router.post("/clusters", response_model=ClusterOut, summary="新增集群")
def create_cluster(body: ClusterCreate, db: Session = Depends(get_db)):
    exists = db.query(Cluster).filter(Cluster.env_code == body.env_code).first()
    if exists:
        raise HTTPException(status_code=409, detail=f"环境代码 {body.env_code} 已存在")
    cluster = Cluster(**body.model_dump())
    db.add(cluster)
    db.commit()
    db.refresh(cluster)
    return cluster


@router.put("/clusters/{cluster_id}", response_model=ClusterOut, summary="更新集群")
def update_cluster(cluster_id: int, body: ClusterCreate, db: Session = Depends(get_db)):
    cluster = _get_cluster(db, cluster_id)
    for k, v in body.model_dump().items():
        setattr(cluster, k, v)
    db.commit()
    db.refresh(cluster)
    return cluster


@router.delete("/clusters/{cluster_id}", summary="删除集群")
def delete_cluster(cluster_id: int, db: Session = Depends(get_db)):
    cluster = _get_cluster(db, cluster_id)
    db.delete(cluster)
    db.commit()
    return {"message": f"集群 {cluster.name} 已删除"}


# ════════════════════ 发布 ════════════════════

@router.post("/deploy", response_model=DeployResponse, summary="触发发布流水线")
def trigger_deploy(req: DeployRequest, db: Session = Depends(get_db)):
    cluster = _get_cluster(db, req.cluster_id)
    try:
        log = run_deploy_pipeline(db, req, cluster)
    except Exception as e:
        logger.exception("发布流水线异常")
        raise HTTPException(status_code=500, detail=f"流水线内部错误: {e}")

    steps = None
    if log.step_detail:
        try:
            steps = json.loads(log.step_detail)
        except json.JSONDecodeError:
            steps = log.step_detail

    return DeployResponse(
        log_id=log.id,
        status=log.status,
        message="发布成功" if log.status == "success" else f"发布失败: {log.error_message}",
        steps={"pipeline": steps},
    )


@router.get("/deploy/logs", response_model=list[DeployLogOut], summary="查询发布日志")
def list_deploy_logs(
    cluster_id: Optional[int] = Query(None),
    app_name: Optional[str] = Query(None),
    status: Optional[str] = Query(None),
    limit: int = Query(20, ge=1, le=100),
    db: Session = Depends(get_db),
):
    query = db.query(DeployLog).order_by(DeployLog.id.desc())
    if cluster_id:
        query = query.filter(DeployLog.cluster_id == cluster_id)
    if app_name:
        query = query.filter(DeployLog.app_name == app_name)
    if status:
        query = query.filter(DeployLog.status == status)
    return query.limit(limit).all()


# ════════════════════ 文件列表 ════════════════════

def _parse_version_from_filename(filename: str) -> str:
    """从文件名中提取时间戳作为版本号，例如 20260707185356 → v20260707185356"""
    m = re.search(r"(\d{14})", filename)
    if m:
        return f"v{m.group(1)}"
    m = re.search(r"[vV]?([\d]+(?:\.[\d]+){1,3})", filename)
    if m:
        return f"v{m.group(1)}"
    return ""


@router.get("/files", response_model=list[FileInfo], summary="扫描传输目录文件列表")
def list_files(cluster_id: Optional[int] = Query(None), db: Session = Depends(get_db)):
    transfer_dir = settings.TRANSFER_DIR
    if not os.path.isdir(transfer_dir):
        return []

    app_query = db.query(App)
    if cluster_id:
        app_query = app_query.filter(App.cluster_id == cluster_id)
    apps = app_query.all()
    results = []

    for entry in sorted(os.scandir(transfer_dir), key=lambda e: e.stat().st_mtime, reverse=True):
        if not entry.is_file():
            continue
        stat = entry.stat()
        matched_app = None
        for app in apps:
            if app.match_keyword in entry.name:
                matched_app = app
                break

        results.append(FileInfo(
            file_name=entry.name,
            size_mb=round(stat.st_size / 1048576, 2),
            modified_at=datetime.fromtimestamp(stat.st_mtime).strftime("%Y-%m-%d %H:%M:%S"),
            matched_app=AppOut.model_validate(matched_app) if matched_app else None,
        ))

    return results


@router.get("/files/parse-version", summary="从文件名解析版本号")
def parse_version(file_name: str = Query(...)):
    return {"version": _parse_version_from_filename(file_name)}


# ════════════════════ 应用管理 ════════════════════

@router.get("/apps", response_model=list[AppOut], summary="查询所有应用映射")
def list_apps(cluster_id: Optional[int] = Query(None), db: Session = Depends(get_db)):
    query = db.query(App).order_by(App.id)
    if cluster_id:
        query = query.filter(App.cluster_id == cluster_id)
    return query.all()


@router.post("/apps", response_model=AppOut, summary="新增应用映射")
def create_app(body: AppCreate, db: Session = Depends(get_db)):
    exists = db.query(App).filter(
        App.app_name == body.app_name, App.cluster_id == body.cluster_id
    ).first()
    if exists:
        raise HTTPException(status_code=409, detail=f"应用 {body.app_name} 在该集群已存在")

    app = App(**body.model_dump())
    db.add(app)
    db.commit()
    db.refresh(app)
    return app


@router.put("/apps/{app_id}", response_model=AppOut, summary="更新应用映射")
def update_app(app_id: int, body: AppCreate, db: Session = Depends(get_db)):
    app = db.query(App).filter(App.id == app_id).first()
    if not app:
        raise HTTPException(status_code=404, detail="应用不存在")
    for k, v in body.model_dump().items():
        setattr(app, k, v)
    db.commit()
    db.refresh(app)
    return app


@router.delete("/apps/{app_id}", summary="删除应用映射")
def delete_app(app_id: int, db: Session = Depends(get_db)):
    app = db.query(App).filter(App.id == app_id).first()
    if not app:
        raise HTTPException(status_code=404, detail="应用不存在")
    db.delete(app)
    db.commit()
    return {"message": f"应用 {app.app_name} 已删除"}


# ════════════════════ 回滚 ════════════════════

@router.get("/rollback/history/{app_name}", summary="查询可回滚的历史版本")
def rollback_history(
    app_name: str,
    cluster_id: Optional[int] = Query(None),
    limit: int = Query(10, ge=1, le=50),
    db: Session = Depends(get_db),
):
    query = db.query(DeployLog).filter(
        DeployLog.app_name == app_name, DeployLog.status == "success"
    )
    if cluster_id:
        query = query.filter(DeployLog.cluster_id == cluster_id)
    records = query.order_by(DeployLog.id.desc()).limit(limit).all()
    return [
        {
            "id": r.id,
            "version": r.version,
            "image_full": r.image_full,
            "deploy_type": getattr(r, "deploy_type", "deploy"),
            "finished_at": r.finished_at.isoformat() if r.finished_at else None,
        }
        for r in records
    ]


@router.post("/rollback", response_model=DeployResponse, summary="执行回滚")
def trigger_rollback(req: RollbackRequest, db: Session = Depends(get_db)):
    cluster = _get_cluster(db, req.cluster_id)
    try:
        log = run_rollback_pipeline(db, req, cluster)
    except Exception as e:
        logger.exception("回滚流水线异常")
        raise HTTPException(status_code=500, detail=f"回滚内部错误: {e}")

    steps = None
    if log.step_detail:
        try:
            steps = json.loads(log.step_detail)
        except json.JSONDecodeError:
            steps = log.step_detail

    return DeployResponse(
        log_id=log.id,
        status=log.status,
        message="回滚成功" if log.status == "success" else f"回滚失败: {log.error_message}",
        steps={"pipeline": steps},
    )


# ════════════════════ K8s 服务管理 ════════════════════

@router.get("/k8s/pods", summary="获取 Pod 列表")
def k8s_pods(
    cluster_id: int = Query(...),
    namespace: Optional[str] = Query(None),
    db: Session = Depends(get_db),
):
    cluster = _get_cluster(db, cluster_id)
    ns = namespace or cluster.default_namespace
    try:
        return get_pods(ns, cluster)
    except Exception as e:
        raise HTTPException(status_code=500, detail=str(e))


@router.get("/k8s/pods/{pod_name}/logs", summary="获取 Pod 日志")
def k8s_pod_logs(
    pod_name: str,
    cluster_id: int = Query(...),
    namespace: Optional[str] = Query(None),
    tail: int = Query(200, ge=10, le=5000),
    db: Session = Depends(get_db),
):
    cluster = _get_cluster(db, cluster_id)
    ns = namespace or cluster.default_namespace
    try:
        return {"pod": pod_name, "logs": get_pod_logs(ns, pod_name, tail, cluster)}
    except Exception as e:
        raise HTTPException(status_code=500, detail=str(e))


@router.get("/k8s/deployments/{name}", summary="获取 Deployment 配置")
def k8s_deployment(
    name: str,
    cluster_id: int = Query(...),
    namespace: Optional[str] = Query(None),
    db: Session = Depends(get_db),
):
    cluster = _get_cluster(db, cluster_id)
    ns = namespace or cluster.default_namespace
    try:
        return get_deployment_info(ns, name, cluster)
    except Exception as e:
        raise HTTPException(status_code=500, detail=str(e))


@router.get("/k8s/deployments/{name}/events", summary="获取 Deployment 事件")
def k8s_deployment_events(
    name: str,
    cluster_id: int = Query(...),
    namespace: Optional[str] = Query(None),
    db: Session = Depends(get_db),
):
    cluster = _get_cluster(db, cluster_id)
    ns = namespace or cluster.default_namespace
    try:
        return {"deployment": name, "events": get_deployment_events(ns, name, cluster)}
    except Exception as e:
        raise HTTPException(status_code=500, detail=str(e))


@router.post("/k8s/pods/{pod_name}/restart", summary="重启 Pod（delete 后自动重建）")
def k8s_restart_pod(
    pod_name: str,
    cluster_id: int = Query(...),
    namespace: Optional[str] = Query(None),
    db: Session = Depends(get_db),
):
    cluster = _get_cluster(db, cluster_id)
    ns = namespace or cluster.default_namespace
    try:
        return restart_pod(ns, pod_name, cluster)
    except Exception as e:
        raise HTTPException(status_code=500, detail=str(e))
