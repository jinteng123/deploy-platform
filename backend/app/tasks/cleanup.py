"""
定时清理任务：每天凌晨 2:00 删除 TRANSFER_DIR 下超过 N 天的文件
"""
import logging
import os
import time

from app.core.config import settings

logger = logging.getLogger(__name__)


def cleanup_old_files():
    transfer_dir = settings.TRANSFER_DIR
    max_age_seconds = settings.CLEANUP_MAX_AGE_DAYS * 86400
    now = time.time()

    if not os.path.isdir(transfer_dir):
        logger.warning("清理目录不存在, 跳过: %s", transfer_dir)
        return

    removed, failed = 0, 0
    for entry in os.scandir(transfer_dir):
        try:
            age = now - entry.stat().st_mtime
            if age > max_age_seconds:
                if entry.is_file() or entry.is_symlink():
                    os.remove(entry.path)
                    logger.info("已删除过期文件: %s (%.1f 天)", entry.name, age / 86400)
                    removed += 1
        except Exception as e:
            logger.error("删除失败 %s: %s", entry.path, e)
            failed += 1

    logger.info("清理完成: 删除 %d 个文件, 失败 %d 个", removed, failed)
