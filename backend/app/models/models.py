from sqlalchemy import (
    Column, Integer, BigInteger, String, Text, DateTime, Enum as SAEnum, func,
)
from app.db.session import Base


class Cluster(Base):
    __tablename__ = "clusters"

    id = Column(Integer, primary_key=True, autoincrement=True)
    name = Column(String(64), nullable=False)
    env_code = Column(String(16), unique=True, nullable=False)
    k8s_master_host = Column(String(128), nullable=False)
    k8s_master_port = Column(Integer, nullable=False, default=22)
    k8s_master_user = Column(String(64), nullable=False, default="root")
    k8s_master_key_path = Column(String(256), nullable=True)
    default_namespace = Column(String(64), nullable=False, default="default")
    description = Column(String(256), nullable=True)
    created_at = Column(DateTime, server_default=func.now())
    updated_at = Column(DateTime, server_default=func.now(), onupdate=func.now())


class App(Base):
    __tablename__ = "apps"

    id = Column(Integer, primary_key=True, autoincrement=True)
    cluster_id = Column(Integer, nullable=False, default=1)
    app_name = Column(String(128), nullable=False)
    match_keyword = Column(String(128), nullable=False, comment="文件名匹配关键词")
    deployment_name = Column(String(128), nullable=False)
    container_name = Column(String(128), nullable=False)
    namespace = Column(String(64), nullable=False, default="default")
    harbor_project = Column(String(128), nullable=False, default="library")
    description = Column(String(512), nullable=True)
    created_at = Column(DateTime, server_default=func.now())
    updated_at = Column(DateTime, server_default=func.now(), onupdate=func.now())


class DeployLog(Base):
    __tablename__ = "deploy_logs"

    id = Column(BigInteger, primary_key=True, autoincrement=True)
    cluster_id = Column(Integer, nullable=False, default=1)
    app_name = Column(String(128), nullable=False)
    deployment_name = Column(String(128), nullable=False)
    container_name = Column(String(128), nullable=False)
    namespace = Column(String(64), nullable=False)
    version = Column(String(64), nullable=False)
    file_name = Column(String(256), nullable=False)
    image_full = Column(String(512), nullable=True)
    deploy_type = Column(String(16), nullable=False, default="deploy", comment="deploy=发布, rollback=回滚")
    status = Column(SAEnum("pending", "running", "success", "failed"), default="pending")
    current_step = Column(String(64), nullable=True)
    step_detail = Column(Text, nullable=True)
    error_message = Column(Text, nullable=True)
    started_at = Column(DateTime, nullable=True)
    finished_at = Column(DateTime, nullable=True)
    created_at = Column(DateTime, server_default=func.now())
