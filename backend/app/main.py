"""
FastAPI 应用工厂
"""
import logging
import os
from contextlib import asynccontextmanager
from pathlib import Path

from fastapi import FastAPI, Request
from fastapi.middleware.cors import CORSMiddleware
from fastapi.responses import FileResponse, HTMLResponse
from fastapi.staticfiles import StaticFiles

from app.core.config import settings
from app.api.v1.deploy import router as deploy_router
from app.tasks.scheduler import start_scheduler, stop_scheduler

# dist 目录：与 backend/ 同级的 frontend/dist
DIST_DIR = Path(__file__).resolve().parent.parent.parent / "frontend" / "dist"

logging.basicConfig(
    level=logging.DEBUG if settings.DEBUG else logging.INFO,
    format="%(asctime)s [%(levelname)s] %(name)s: %(message)s",
)
logger = logging.getLogger(__name__)


@asynccontextmanager
async def lifespan(app: FastAPI):
    # ── 启动时 ──
    logger.info("ENV=%s | DB=%s:%s/%s", settings.ENV, settings.DB_HOST, settings.DB_PORT, settings.DB_NAME)
    logger.info("Registry=%s | K8s Master=%s", settings.REGISTRY_HOST, settings.K8S_MASTER_HOST)
    start_scheduler()
    logger.info("APScheduler 已启动 — 凌晨 2:00 自动清理")
    yield
    # ── 关闭时 ──
    stop_scheduler()
    logger.info("APScheduler 已关闭")


def create_app() -> FastAPI:
    app = FastAPI(
        title=settings.APP_NAME,
        version=settings.APP_VERSION,
        lifespan=lifespan,
    )

    app.add_middleware(
        CORSMiddleware,
        allow_origins=["*"],
        allow_credentials=True,
        allow_methods=["*"],
        allow_headers=["*"],
    )

    app.include_router(deploy_router)

    @app.get("/health", tags=["Health"])
    def health_check():
        return {
            "status": "ok",
            "env": settings.ENV,
            "version": settings.APP_VERSION,
        }

    # ── 挂载前端 dist 静态资源 ──
    if DIST_DIR.is_dir():
        # /assets/* 静态文件（js/css/图片）
        assets_dir = DIST_DIR / "assets"
        if assets_dir.is_dir():
            app.mount("/assets", StaticFiles(directory=str(assets_dir)), name="static-assets")
        logger.info("前端 dist 已挂载: %s", DIST_DIR)

        # SPA fallback: 所有非 /api、/health、/docs 的路径都返回 index.html
        @app.get("/{full_path:path}", include_in_schema=False)
        async def spa_fallback(request: Request, full_path: str):
            # 如果是对具体静态文件的请求 (favicon 等)
            file = DIST_DIR / full_path
            if full_path and file.is_file():
                return FileResponse(str(file))
            return FileResponse(str(DIST_DIR / "index.html"))
    else:
        logger.warning("前端 dist 目录不存在: %s — 仅提供 API 服务", DIST_DIR)

    return app
