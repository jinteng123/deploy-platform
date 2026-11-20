from pydantic import BaseModel, Field
from typing import Optional
from datetime import datetime


# ── 集群/环境 ──

class ClusterCreate(BaseModel):
    name: str = Field(..., description="环境名称", examples=["生产环境"])
    env_code: str = Field(..., description="环境代码", examples=["prod"])
    k8s_master_host: str = Field(..., description="K8s Master SSH 地址")
    k8s_master_port: int = Field(default=22)
    k8s_master_user: str = Field(default="root")
    k8s_master_key_path: Optional[str] = None
    default_namespace: str = Field(default="default")
    description: Optional[str] = None


class ClusterOut(BaseModel):
    id: int
    name: str
    env_code: str
    k8s_master_host: str
    k8s_master_port: int
    k8s_master_user: str
    k8s_master_key_path: Optional[str]
    default_namespace: str
    description: Optional[str]
    created_at: datetime

    model_config = {"from_attributes": True}


# ── 发布 ──

class DeployRequest(BaseModel):
    cluster_id: int = Field(..., description="目标集群 ID")
    file_name: str = Field(..., description="传输目录中的文件名", examples=["myapp-v1.2.tar.gz"])
    deployment_name: str = Field(..., description="K8s Deployment 名称", examples=["myapp-deployment"])
    container_name: str = Field(..., description="K8s 容器名称", examples=["myapp"])
    version: str = Field(..., description="镜像版本号", examples=["v1.2.0"])
    namespace: str = Field(default="default", description="K8s 命名空间")


class DeployResponse(BaseModel):
    log_id: int
    status: str
    message: str
    steps: Optional[dict] = None


class DeployLogOut(BaseModel):
    id: int
    cluster_id: int = 1
    app_name: str
    deployment_name: str
    container_name: str
    namespace: str
    version: str
    image_full: Optional[str] = None
    deploy_type: str = "deploy"
    status: str
    current_step: Optional[str]
    error_message: Optional[str]
    started_at: Optional[datetime]
    finished_at: Optional[datetime]
    created_at: datetime

    model_config = {"from_attributes": True}


# ── 应用管理 ──

class AppCreate(BaseModel):
    cluster_id: int = Field(..., description="所属集群 ID")
    app_name: str = Field(..., description="应用名称（唯一标识）", examples=["angel-agent-web"])
    match_keyword: str = Field(..., description="文件名匹配关键词", examples=["agent-web"])
    deployment_name: str = Field(..., description="K8s Deployment 名称", examples=["angel-agent-web"])
    container_name: str = Field(..., description="K8s 容器名称", examples=["angel-agent-web"])
    namespace: str = Field(default="default")
    harbor_project: str = Field(default="library")
    description: Optional[str] = None


class AppOut(BaseModel):
    id: int
    cluster_id: int = 1
    app_name: str
    match_keyword: str
    deployment_name: str
    container_name: str
    namespace: str
    harbor_project: str
    description: Optional[str]
    created_at: datetime

    model_config = {"from_attributes": True}


# ── 文件列表 ──

class FileInfo(BaseModel):
    file_name: str
    size_mb: float
    modified_at: str
    matched_app: Optional[AppOut] = None


# ── 回滚 ──

class RollbackRequest(BaseModel):
    cluster_id: int = Field(..., description="目标集群 ID")
    app_name: str = Field(..., description="应用名称")
    deployment_name: str = Field(..., description="K8s Deployment 名称")
    container_name: str = Field(..., description="K8s 容器名称")
    namespace: str = Field(..., description="K8s 命名空间")
    image_full: str = Field(..., description="回滚目标的完整镜像地址")
    version: str = Field(..., description="回滚目标版本号")
