from urllib.parse import quote

from pydantic_settings import BaseSettings
from typing import Optional


class Settings(BaseSettings):
    ENV: str = "dev"
    APP_NAME: str = "Deploy Platform"
    APP_VERSION: str = "1.0.0"
    DEBUG: bool = True

    # MySQL
    DB_HOST: str = "127.0.0.1"
    DB_PORT: int = 3306
    DB_USER: str = "root"
    DB_PASSWORD: str = ""
    DB_NAME: str = "deploy_platform"

    # 镜像仓库 (院内阿里云 ACR / Harbor 均可)
    REGISTRY_HOST: str = "registry.example.com"
    REGISTRY_PROJECT: str = "Demo"
    REGISTRY_USER: str = "admin"
    REGISTRY_PASSWORD: str = ""

    # K8s Master SSH (prod: 127.0.0.1)
    K8S_MASTER_HOST: str = "127.0.0.1"
    K8S_MASTER_PORT: int = 22
    K8S_MASTER_USER: str = "root"
    K8S_MASTER_KEY_PATH: Optional[str] = None

    # 文件传输目录
    TRANSFER_DIR: str = "/data/transfer_space"

    # 文件清理
    CLEANUP_MAX_AGE_DAYS: int = 3

    @property
    def database_url(self) -> str:
        password = quote(self.DB_PASSWORD, safe="")
        return (
            f"mysql+pymysql://{self.DB_USER}:{password}"
            f"@{self.DB_HOST}:{self.DB_PORT}/{self.DB_NAME}"
            f"?charset=utf8mb4"
        )

    @property
    def is_dev(self) -> bool:
        return self.ENV == "dev"

    model_config = {"env_file": ".env", "env_file_encoding": "utf-8"}


settings = Settings()
